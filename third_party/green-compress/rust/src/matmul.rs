use crate::q8::{dequantize_row_q8, q8_row_spin};
use crate::repair::apply_low_rank_repair_matmul;
use crate::simd::{accumulate_q8_block, saxpy_accumulate};
use crate::types::{
    FusedWeightCache, Matrix, OutlierRowCache, Q8Matrix, Repair, SparseRowCache, SubspaceAdapter,
};
use rayon::prelude::*;

/// Small batch GEMV: parallelize over weight rows (k) with per-thread partial sums.
fn parallel_over_k(batch_rows: u32, weight_rows: u32) -> bool {
    batch_rows <= (weight_rows / 8).max(1)
}

fn reduce_partials(partials: &[Vec<f32>]) -> Vec<f32> {
    if partials.is_empty() {
        return Vec::new();
    }
    let len = partials[0].len();
    let mut out = vec![0.0f32; len];
    for partial in partials {
        for (o, v) in out.iter_mut().zip(partial.iter()) {
            *o += *v;
        }
    }
    out
}

fn k_parallel_chunk_count(k_rows: usize) -> usize {
    rayon::current_num_threads().max(1).min(k_rows.max(1))
}

pub fn fused_cache_runtime_bytes(cache: &FusedWeightCache) -> u64 {
    (cache.weights.len() + cache.row_spin.len()) as u64 * 4
}

pub fn build_fused_weight_cache(
    q8: &Q8Matrix,
    outlier_row_cache: Option<&OutlierRowCache>,
    sparse_row_cache: Option<&SparseRowCache>,
) -> FusedWeightCache {
    let cols = q8.cols as usize;
    let mut weights = vec![0.0f32; q8.rows as usize * cols];
    weights
        .par_chunks_mut(cols)
        .enumerate()
        .for_each(|(k, w_row_slice)| {
            let k = k as u32;
            dequantize_row_q8(q8, k, w_row_slice);
            if let Some(outliers) = outlier_row_cache {
                if (k as usize) < outliers.by_row.len() {
                    for &(col, val) in &outliers.by_row[k as usize] {
                        w_row_slice[col as usize] = val;
                    }
                }
            }
            if let Some(sparse) = sparse_row_cache {
                if (k as usize) < sparse.by_row.len() {
                    let spin = q8_row_spin(q8, k);
                    for &(col, val) in &sparse.by_row[k as usize] {
                        w_row_slice[col as usize] += val * spin;
                    }
                }
            }
        });

    FusedWeightCache {
        rows: q8.rows,
        cols: q8.cols,
        weights,
        row_spin: q8.row_spin.clone(),
    }
}

pub fn matmul_prepacked(
    activations: &Matrix,
    cache: &FusedWeightCache,
    repair: Option<&Repair>,
    output_bias: Option<&[f32]>,
    subspace: Option<&SubspaceAdapter>,
) -> Matrix {
    assert_eq!(activations.cols, cache.rows, "activation/fused-cache dimension mismatch");
    let batch = activations.rows as usize;
    let cols = cache.cols as usize;
    let k_rows = cache.rows as usize;
    let out_size = batch * cols;

    let out_data = if parallel_over_k(activations.rows, cache.rows) {
        let n_chunks = k_parallel_chunk_count(k_rows);
        let chunk = (k_rows + n_chunks - 1) / n_chunks;
        let partials: Vec<Vec<f32>> = (0..n_chunks)
            .into_par_iter()
            .map(|ci| {
                let k0 = ci * chunk;
                let k1 = (k0 + chunk).min(k_rows);
                let mut partial = vec![0.0f32; out_size];
                for k in k0..k1 {
                    let spin = if k < cache.row_spin.len() {
                        cache.row_spin[k]
                    } else {
                        1.0
                    };
                    let w_base = k * cols;
                    let w_row = &cache.weights[w_base..w_base + cols];
                    for b in 0..batch {
                        let x = activations.at(b as u32, k as u32) * spin;
                        if x == 0.0 {
                            continue;
                        }
                        let out_row = &mut partial[b * cols..(b + 1) * cols];
                        saxpy_accumulate(x, w_row, out_row);
                    }
                }
                partial
            })
            .collect();
        reduce_partials(&partials)
    } else {
        let mut out_data = vec![0.0f32; out_size];
        out_data
            .par_chunks_mut(cols)
            .enumerate()
            .for_each(|(b, out_row)| {
                let b = b as u32;
                for k in 0..cache.rows {
                    let spin = if (k as usize) < cache.row_spin.len() {
                        cache.row_spin[k as usize]
                    } else {
                        1.0
                    };
                    let x = activations.at(b, k) * spin;
                    if x == 0.0 {
                        continue;
                    }
                    let w_base = k as usize * cols;
                    saxpy_accumulate(x, &cache.weights[w_base..w_base + cols], out_row);
                }
            });
        out_data
    };

    let mut out = Matrix {
        rows: activations.rows,
        cols: cache.cols,
        data: out_data,
    };

    if let Some(repair) = repair {
        if !repair.low_rank.is_empty() {
            apply_low_rank_repair_matmul(&mut out, activations, repair, cache.rows, cache.cols);
        }
    }
    apply_output_bias(&mut out, output_bias);
    apply_subspace_if_some(&mut out, activations, subspace);
    out
}

pub fn accumulate_q8_row(
    x: f32,
    q8: &Q8Matrix,
    k: u32,
    out_row: &mut [f32],
    outlier_row_cache: Option<&OutlierRowCache>,
    sparse_row_cache: Option<&SparseRowCache>,
) {
    if x == 0.0 {
        return;
    }
    let awq_inv = if (k as usize) < q8.awq_scales.len() && q8.awq_scales[k as usize] > 0.0 {
        1.0 / q8.awq_scales[k as usize]
    } else {
        1.0
    };
    let wbase = k as usize * q8.cols as usize;
    let mut j = 0u32;
    while j < q8.cols {
        let idx = wbase + j as usize;
        let scale = q8.scales[idx / q8.block as usize].to_f32() * awq_inv;
        let xs = x * scale;
        let end = (j + q8.block).min(q8.cols);
        let block_len = (end - j) as usize;
        accumulate_q8_block(xs, &q8.packed[idx..idx + block_len], &mut out_row[j as usize..]);
        j = end;
    }
    if let Some(outliers) = outlier_row_cache {
        if (k as usize) < outliers.by_row.len() {
            for &(col, val) in &outliers.by_row[k as usize] {
                let idx = wbase + col as usize;
                let deq = q8.packed[idx] as f32 * q8.scales[idx / q8.block as usize].to_f32() * awq_inv;
                out_row[col as usize] += x * (val - deq);
            }
        }
    }
    if let Some(sparse) = sparse_row_cache {
        if (k as usize) < sparse.by_row.len() {
            let spin = q8_row_spin(q8, k);
            for &(col, val) in &sparse.by_row[k as usize] {
                out_row[col as usize] += x * val * spin;
            }
        }
    }
}

pub fn matmul_q8_fused(
    activations: &Matrix,
    q8: &Q8Matrix,
    repair: Option<&Repair>,
    sparse_row_cache: Option<&SparseRowCache>,
    outlier_row_cache: Option<&OutlierRowCache>,
    output_bias: Option<&[f32]>,
    subspace: Option<&SubspaceAdapter>,
) -> Matrix {
    assert_eq!(activations.cols, q8.rows, "activation/q8 dimension mismatch");
    let batch = activations.rows as usize;
    let cols = q8.cols as usize;
    let k_rows = q8.rows as usize;
    let out_size = batch * cols;

    let out_data = if parallel_over_k(activations.rows, q8.rows) {
        let n_chunks = k_parallel_chunk_count(k_rows);
        let chunk = (k_rows + n_chunks - 1) / n_chunks;
        let partials: Vec<Vec<f32>> = (0..n_chunks)
            .into_par_iter()
            .map(|ci| {
                let k0 = ci * chunk;
                let k1 = (k0 + chunk).min(k_rows);
                let mut partial = vec![0.0f32; out_size];
                for k in k0..k1 {
                    let ku = k as u32;
                    let spin = q8_row_spin(q8, ku);
                    for b in 0..batch {
                        let x = activations.at(b as u32, ku) * spin;
                        let out_row = &mut partial[b * cols..(b + 1) * cols];
                        accumulate_q8_row(x, q8, ku, out_row, outlier_row_cache, sparse_row_cache);
                    }
                }
                partial
            })
            .collect();
        reduce_partials(&partials)
    } else {
        let mut out_data = vec![0.0f32; out_size];
        out_data
            .par_chunks_mut(cols)
            .enumerate()
            .for_each(|(b, out_row)| {
                let b = b as u32;
                for k in 0..q8.rows {
                    let spin = q8_row_spin(q8, k);
                    let x = activations.at(b, k) * spin;
                    accumulate_q8_row(x, q8, k, out_row, outlier_row_cache, sparse_row_cache);
                }
            });
        out_data
    };

    let mut out = Matrix {
        rows: activations.rows,
        cols: q8.cols,
        data: out_data,
    };

    if let Some(repair) = repair {
        if !repair.low_rank.is_empty() {
            apply_low_rank_repair_matmul(&mut out, activations, repair, q8.rows, q8.cols);
        }
    }
    apply_output_bias(&mut out, output_bias);
    apply_subspace_if_some(&mut out, activations, subspace);
    out
}

pub fn matmul_q8_repaired(
    activations: &Matrix,
    q8: &Q8Matrix,
    repair: Option<&Repair>,
    sparse_row_cache: Option<&SparseRowCache>,
    outlier_row_cache: Option<&OutlierRowCache>,
    output_bias: Option<&[f32]>,
    subspace: Option<&SubspaceAdapter>,
    prepacked: Option<&FusedWeightCache>,
) -> Matrix {
    if let Some(cache) = prepacked {
        if cache.weights.len() == q8.rows as usize * q8.cols as usize {
            return matmul_prepacked(activations, cache, repair, output_bias, subspace);
        }
    }
    matmul_q8_fused(
        activations,
        q8,
        repair,
        sparse_row_cache,
        outlier_row_cache,
        output_bias,
        subspace,
    )
}

pub fn matmul(a: &Matrix, b: &Matrix) -> Matrix {
    assert_eq!(a.cols, b.rows, "matmul dimension mismatch");
    let mut out = crate::io::make_matrix(a.rows, b.cols);
    let work = (a.rows as u64) * (a.cols as u64) * (b.cols as u64);
    let bcols = b.cols as usize;

    if work > 200_000 {
        out.data
            .par_chunks_mut(bcols)
            .enumerate()
            .for_each(|(i, out_row)| {
                let i = i as u32;
                for k in 0..a.cols {
                    let aik = a.at(i, k);
                    let bbase = k as usize * bcols;
                    for j in 0..b.cols {
                        out_row[j as usize] += aik * b.data[bbase + j as usize];
                    }
                }
            });
    } else {
        for i in 0..a.rows {
            let obase = i as usize * bcols;
            for k in 0..a.cols {
                let aik = a.at(i, k);
                let bbase = k as usize * bcols;
                for j in 0..b.cols {
                    out.data[obase + j as usize] += aik * b.data[bbase + j as usize];
                }
            }
        }
    }
    out
}

fn apply_output_bias(out: &mut Matrix, output_bias: Option<&[f32]>) {
    if let Some(bias) = output_bias {
        if !bias.is_empty() {
            let cols = out.cols as usize;
            for b in 0..out.rows {
                let obase = b as usize * cols;
                for j in 0..out.cols.min(bias.len() as u32) {
                    out.data[obase + j as usize] += bias[j as usize];
                }
            }
        }
    }
}

fn apply_subspace_if_some(
    out: &mut Matrix,
    activations: &Matrix,
    subspace: Option<&SubspaceAdapter>,
) {
    if let Some(sub) = subspace {
        if sub.rank > 0 {
            apply_subspace_correction(out, activations, sub);
        }
    }
}

fn apply_subspace_correction(out: &mut Matrix, activations: &Matrix, subspace: &SubspaceAdapter) {
    out.data
        .par_chunks_mut(out.cols as usize)
        .enumerate()
        .for_each(|(b, out_row)| {
            let b = b as u32;
            let mut proj = vec![0.0f32; subspace.rank as usize];
            for r in 0..subspace.rank {
                let mut sum = 0.0f64;
                for j in 0..subspace.in_dim {
                    sum += activations.at(b, j) as f64
                        * subspace.basis[r as usize * subspace.in_dim as usize + j as usize]
                            as f64;
                }
                proj[r as usize] = sum as f32;
            }
            for j in 0..out.cols.min(subspace.out_dim) {
                let mut sum = 0.0f64;
                for r in 0..subspace.rank {
                    sum += proj[r as usize] as f64
                        * subspace.coeff[r as usize * subspace.out_dim as usize + j as usize]
                            as f64;
                }
                out_row[j as usize] += sum as f32;
            }
        });
}
