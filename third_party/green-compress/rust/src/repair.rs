use rand::rngs::StdRng;
use rand::SeedableRng;
use rayon::prelude::*;

use crate::error::{fail, Result};
use crate::io::make_matrix;
use crate::q8::{q8_row_spin, q8_value_unspun};
use crate::types::{
    Matrix, OutlierRowCache, Q8Matrix, Repair, SparseColCache, SparseRowCache, SparseScoreMode,
    SparseTerm,
};
use crate::simd::saxpy_accumulate;
use crate::util::{norm2, normalize, sample_standard_normal};

pub fn build_sparse_row_cache(repair: &Repair, cols: u32) -> SparseRowCache {
    if repair.sparse.is_empty() {
        return SparseRowCache::default();
    }
    let mut rows = 0u32;
    for term in &repair.sparse {
        rows = rows.max((term.index / cols as u64) as u32 + 1);
    }
    let mut by_row = vec![Vec::new(); rows as usize];
    for term in &repair.sparse {
        let col = (term.index % cols as u64) as u32;
        let row = (term.index / cols as u64) as u32;
        by_row[row as usize].push((col, term.value));
    }
    SparseRowCache { by_row }
}

pub fn build_outlier_row_cache(outliers: &[SparseTerm], cols: u32) -> OutlierRowCache {
    if outliers.is_empty() {
        return OutlierRowCache::default();
    }
    let mut rows = 0u32;
    for term in outliers {
        rows = rows.max((term.index / cols as u64) as u32 + 1);
    }
    let mut by_row = vec![Vec::new(); rows as usize];
    for term in outliers {
        let col = (term.index % cols as u64) as u32;
        let row = (term.index / cols as u64) as u32;
        by_row[row as usize].push((col, term.value));
    }
    OutlierRowCache { by_row }
}

pub fn build_sparse_col_cache(repair: &Repair, cols: u32) -> SparseColCache {
    let mut by_col = vec![Vec::new(); cols as usize];
    for term in &repair.sparse {
        let col = (term.index % cols as u64) as u32;
        let row = (term.index / cols as u64) as u32;
        by_col[col as usize].push((row, term.value));
    }
    SparseColCache { by_col }
}

pub fn activation_row_rms(activations: &Matrix, weight_rows: u32) -> Vec<f64> {
    let samples = activations.rows.max(1) as f64;
    let mut rms = vec![0.0f64; weight_rows as usize];
    for b in 0..activations.rows {
        for r in 0..weight_rows.min(activations.cols) {
            let v = activations.at(b, r) as f64;
            rms[r as usize] += v * v;
        }
    }
    for r in rms.iter_mut() {
        *r = (*r / samples).sqrt();
    }
    rms
}

pub fn activation_row_energy(activations: &Matrix, weight_rows: u32) -> Vec<f64> {
    let mut energy = vec![0.0f64; weight_rows as usize];
    for b in 0..activations.rows {
        for r in 0..weight_rows.min(activations.cols) {
            let v = activations.at(b, r) as f64;
            energy[r as usize] += v * v;
        }
    }
    energy
}

pub fn weight_col_energy(weight: &Matrix) -> Vec<f64> {
    let mut energy = vec![0.0f64; weight.cols as usize];
    for c in 0..weight.cols {
        for r in 0..weight.rows {
            let v = weight.at(r, c) as f64;
            energy[c as usize] += v * v;
        }
    }
    energy
}

pub fn sparse_mode_from_string(
    sparse_mode: &str,
    activations: &Matrix,
    weight_rows: u32,
    weight_cols: u32,
    weight_for_cols: Option<&Matrix>,
) -> Result<(SparseScoreMode, Vec<f64>, Vec<f64>)> {
    match sparse_mode {
        "magnitude" => Ok((SparseScoreMode::Magnitude, Vec::new(), Vec::new())),
        "activation" => Ok((
            SparseScoreMode::Activation,
            activation_row_rms(activations, weight_rows),
            Vec::new(),
        )),
        "output" => Ok((
            SparseScoreMode::Output,
            activation_row_energy(activations, weight_rows),
            Vec::new(),
        )),
        "imatrix" => {
            let row = activation_row_energy(activations, weight_rows);
            let col = weight_for_cols
                .map(weight_col_energy)
                .unwrap_or_else(|| vec![1.0; weight_cols as usize]);
            Ok((SparseScoreMode::Imatrix, row, col))
        }
        _ => Err(fail(
            "sparse-mode must be magnitude, activation, output, or imatrix",
        )),
    }
}

fn sparse_score(
    index: usize,
    residual: &[f32],
    cols: u32,
    mode: SparseScoreMode,
    row_weights: Option<&[f64]>,
    col_weights: Option<&[f64]>,
) -> f64 {
    let row = (index / cols as usize) as u32;
    let col = (index % cols as usize) as u32;
    let magnitude = residual[index].abs() as f64;
    match mode {
        SparseScoreMode::Magnitude => magnitude,
        SparseScoreMode::Imatrix => {
            let rw = row_weights
                .and_then(|w| w.get(row as usize))
                .copied()
                .unwrap_or(1.0);
            let cw = col_weights
                .and_then(|w| w.get(col as usize))
                .copied()
                .unwrap_or(1.0);
            rw * cw * magnitude * magnitude
        }
        SparseScoreMode::Output => {
            let rw = row_weights
                .and_then(|w| w.get(row as usize))
                .copied()
                .unwrap_or(1.0);
            rw * magnitude * magnitude
        }
        SparseScoreMode::Activation => {
            let rw = row_weights
                .and_then(|w| w.get(row as usize))
                .copied()
                .unwrap_or(1.0);
            magnitude * rw
        }
    }
}

pub fn mat_vec(matrix: &Matrix, v: &[f32], out: &mut [f32]) {
    out.fill(0.0);
    for r in 0..matrix.rows {
        let base = r as usize * matrix.cols as usize;
        let mut sum = 0.0f64;
        for c in 0..matrix.cols {
            sum += matrix.data[base + c as usize] as f64 * v[c as usize] as f64;
        }
        out[r as usize] = sum as f32;
    }
}

pub fn transposed_mat_vec(matrix: &Matrix, u: &[f32], out: &mut [f32]) {
    out.fill(0.0);
    for r in 0..matrix.rows {
        let ur = u[r as usize];
        let base = r as usize * matrix.cols as usize;
        for c in 0..matrix.cols {
            out[c as usize] += matrix.data[base + c as usize] * ur;
        }
    }
}

pub fn subtract_rank1(matrix: &mut Matrix, sigma: f32, u: &[f32], v: &[f32]) {
    for r in 0..matrix.rows {
        let left = sigma * u[r as usize];
        let base = r as usize * matrix.cols as usize;
        for c in 0..matrix.cols {
            matrix.data[base + c as usize] -= left * v[c as usize];
        }
    }
}

pub fn fit_low_rank(
    residual: &mut Matrix,
    rank: u32,
    iters: u32,
    seed: u32,
) -> Vec<crate::types::LowRankTerm> {
    let mut rng = StdRng::seed_from_u64(seed as u64);
    let mut terms = Vec::new();

    for _ in 0..rank {
        let mut v = vec![0.0f32; residual.cols as usize];
        for x in &mut v {
            *x = sample_standard_normal(&mut rng);
        }
        normalize(&mut v);

        let mut u = vec![0.0f32; residual.rows as usize];
        for _ in 0..iters {
            mat_vec(residual, &v, &mut u);
            normalize(&mut u);
            transposed_mat_vec(residual, &u, &mut v);
            normalize(&mut v);
        }

        mat_vec(residual, &v, &mut u);
        let sigma = norm2(&u);
        if sigma <= 1e-12 {
            break;
        }
        for x in &mut u {
            *x /= sigma;
        }

        subtract_rank1(residual, sigma, &u, &v);
        terms.push(crate::types::LowRankTerm { sigma, u, v });
    }
    terms
}

pub fn fit_sparse(
    residual: &mut Matrix,
    sparse_frac: f32,
    score_mode: SparseScoreMode,
    row_weights: Option<&[f64]>,
    max_entries: usize,
    col_weights: Option<&[f64]>,
) -> Vec<SparseTerm> {
    if sparse_frac <= 0.0 {
        return Vec::new();
    }
    let total = residual.data.len();
    let mut keep = ((total as f64) * sparse_frac as f64).ceil() as usize;
    keep = keep.min(total);
    if max_entries > 0 {
        keep = keep.min(max_entries);
    }
    if keep == 0 {
        return Vec::new();
    }

    let mut indexes: Vec<usize> = (0..total).collect();
    let cols = residual.cols;
    indexes.select_nth_unstable_by(keep - 1, |&a, &b| {
        let sa = sparse_score(a, &residual.data, cols, score_mode, row_weights, col_weights);
        let sb = sparse_score(b, &residual.data, cols, score_mode, row_weights, col_weights);
        sb.partial_cmp(&sa).unwrap_or(std::cmp::Ordering::Equal)
    });
    indexes.truncate(keep);
    indexes.sort_unstable();

    let mut sparse = Vec::new();
    for index in indexes {
        let value = residual.data[index];
        if value != 0.0 {
            sparse.push(SparseTerm {
                index: index as u64,
                value,
            });
        }
    }
    sparse
}

fn sparse_greedy_score(
    index: usize,
    residual: &[f32],
    cols: u32,
    row_weights: Option<&[f64]>,
) -> f64 {
    let row = (index / cols as usize) as u32;
    let value = residual[index] as f64;
    let mut score = value * value;
    if let Some(w) = row_weights {
        if let Some(&rw) = w.get(row as usize) {
            score *= rw;
        }
    }
    score
}

pub fn fit_sparse_greedy(
    residual: &mut Matrix,
    max_entries: usize,
    row_weights: Option<&[f64]>,
) -> Vec<SparseTerm> {
    if max_entries == 0 {
        return Vec::new();
    }
    let total = residual.data.len();
    let pool_factor = (max_entries / 256 + 8).clamp(8, 64);
    let pool_cap = total.min((max_entries * pool_factor).max(4096));

    let mut pool: Vec<usize> = (0..total).collect();
    pool.select_nth_unstable_by(pool_cap.saturating_sub(1), |&a, &b| {
        let sa = sparse_greedy_score(a, &residual.data, residual.cols, row_weights);
        let sb = sparse_greedy_score(b, &residual.data, residual.cols, row_weights);
        sb.partial_cmp(&sa).unwrap_or(std::cmp::Ordering::Equal)
    });
    pool.truncate(pool_cap);

    let mut sparse = Vec::with_capacity(max_entries);
    let mut picked = vec![false; total];

    for _ in 0..max_entries {
        let mut best_index = None;
        let mut best_score = -1.0f64;
        for &index in &pool {
            if picked[index] {
                continue;
            }
            let score = sparse_greedy_score(index, &residual.data, residual.cols, row_weights);
            if score > best_score {
                best_score = score;
                best_index = Some(index);
            }
        }
        let Some(index) = best_index else { break };
        if best_score <= 1e-20 {
            break;
        }
        let value = residual.data[index];
        if value == 0.0 {
            picked[index] = true;
            continue;
        }
        sparse.push(SparseTerm {
            index: index as u64,
            value,
        });
        residual.data[index] = 0.0;
        picked[index] = true;
    }
    sparse
}

pub fn subtract_sparse(residual: &mut Matrix, sparse: &[SparseTerm]) {
    for term in sparse {
        let idx = term.index as usize;
        if idx >= residual.data.len() {
            panic!("sparse index out of range");
        }
        residual.data[idx] -= term.value;
    }
}

pub fn fit_repair(
    mut residual: Matrix,
    rank: u32,
    iters: u32,
    seed: u32,
    sparse_frac: f32,
    sparse_first: bool,
    sparse_mode: SparseScoreMode,
    row_weights: Option<&[f64]>,
    max_sparse_entries: usize,
    greedy_sparse: bool,
    repair_passes: u32,
    col_weights: Option<&[f64]>,
) -> Repair {
    let mut repair = Repair {
        rows: residual.rows,
        cols: residual.cols,
        low_rank: Vec::new(),
        sparse: Vec::new(),
    };
    let repair_passes = repair_passes.max(1);

    let fit_sparse_pass = |residual: &mut Matrix| -> Vec<SparseTerm> {
        if greedy_sparse {
            let mut keep = ((residual.data.len() as f64) * sparse_frac as f64).ceil() as usize;
            if max_sparse_entries > 0 {
                keep = keep.min(max_sparse_entries);
            }
            fit_sparse_greedy(residual, keep, row_weights)
        } else {
            fit_sparse(
                residual,
                sparse_frac,
                sparse_mode,
                row_weights,
                max_sparse_entries,
                col_weights,
            )
        }
    };

    if sparse_first {
        for _ in 0..repair_passes {
            let sparse = fit_sparse_pass(&mut residual);
            if sparse.is_empty() {
                break;
            }
            repair.sparse.extend(sparse.iter().cloned());
            subtract_sparse(&mut residual, &sparse);
        }
        repair.low_rank = fit_low_rank(&mut residual, rank, iters, seed);
    } else {
        repair.low_rank = fit_low_rank(&mut residual, rank, iters, seed);
        for _ in 0..repair_passes {
            let sparse = fit_sparse_pass(&mut residual);
            if sparse.is_empty() {
                break;
            }
            repair.sparse.extend(sparse.iter().cloned());
            subtract_sparse(&mut residual, &sparse);
        }
    }
    repair
}

pub fn add_repair_to_matrix(matrix: &mut Matrix, repair: Option<&Repair>) {
    let Some(repair) = repair else { return };
    assert_eq!(repair.rows, matrix.rows);
    assert_eq!(repair.cols, matrix.cols);
    for term in &repair.low_rank {
        for r in 0..matrix.rows {
            let left = term.sigma * term.u[r as usize];
            let base = r as usize * matrix.cols as usize;
            for c in 0..matrix.cols {
                matrix.data[base + c as usize] += left * term.v[c as usize];
            }
        }
    }
    for term in &repair.sparse {
        let idx = term.index as usize;
        if idx >= matrix.data.len() {
            panic!("sparse index out of range");
        }
        matrix.data[idx] += term.value;
    }
}

pub fn apply_low_rank_repair_matmul(
    out: &mut Matrix,
    activations: &Matrix,
    repair: &Repair,
    weight_rows: u32,
    weight_cols: u32,
) {
    if repair.low_rank.is_empty() {
        return;
    }
    let cols = weight_cols as usize;
    let k_dim = weight_rows as usize;
    out.data
        .par_chunks_mut(cols)
        .enumerate()
        .for_each(|(b, out_row)| {
            let abase = b * activations.cols as usize;
            for term in &repair.low_rank {
                let mut projection = 0.0f64;
                for i in 0..k_dim {
                    projection += activations.data[abase + i] as f64 * term.u[i] as f64;
                }
                let alpha = projection as f32 * term.sigma;
                saxpy_accumulate(alpha, &term.v, out_row);
            }
        });
}

pub fn apply_repair_matmul(
    out: &mut Matrix,
    activations: &Matrix,
    repair: &Repair,
    weight_rows: u32,
    weight_cols: u32,
    sparse_cache: Option<&SparseColCache>,
) {
    let cache = match sparse_cache {
        Some(c) => c.clone(),
        None => build_sparse_col_cache(repair, weight_cols),
    };

    if !repair.sparse.is_empty() {
        let active_cols: Vec<u32> = cache
            .by_col
            .iter()
            .enumerate()
            .filter(|(_, col)| !col.is_empty())
            .map(|(j, _)| j as u32)
            .collect();

        out.data
            .par_chunks_mut(out.cols as usize)
            .enumerate()
            .for_each(|(b, out_row)| {
                let abase = b * activations.cols as usize;
                for &j in &active_cols {
                    let col_entries = &cache.by_col[j as usize];
                    let mut delta = 0.0f32;
                    for &(row, val) in col_entries {
                        delta += activations.data[abase + row as usize] * val;
                    }
                    out_row[j as usize] += delta;
                }
            });
    }
    apply_low_rank_repair_matmul(out, activations, repair, weight_rows, weight_cols);
}

pub fn fit_fp16_outliers(
    original: &Matrix,
    q8: &Q8Matrix,
    outlier_frac: f32,
    score_mode: SparseScoreMode,
    row_weights: Option<&[f64]>,
    col_weights: Option<&[f64]>,
) -> Vec<SparseTerm> {
    if outlier_frac <= 0.0 {
        return Vec::new();
    }
    let mut residual = make_matrix(original.rows, original.cols);
    for i in 0..original.data.len() {
        residual.data[i] = original.data[i] - q8_value_unspun(q8, i);
    }
    let mut picked = fit_sparse(
        &mut residual,
        outlier_frac,
        score_mode,
        row_weights,
        0,
        col_weights,
    );
    for term in &mut picked {
        let row = (term.index / q8.cols as u64) as u32;
        term.value = original.data[term.index as usize] * q8_row_spin(q8, row);
    }
    picked
}

pub fn fit_output_bias(target: &Matrix, approx: &Matrix) -> Vec<f32> {
    assert_eq!(target.rows, approx.rows);
    assert_eq!(target.cols, approx.cols);
    let mut bias = vec![0.0f32; target.cols as usize];
    let inv = if target.rows > 0 {
        1.0 / target.rows as f32
    } else {
        1.0
    };
    for b in 0..target.rows {
        let base = b as usize * target.cols as usize;
        for j in 0..target.cols as usize {
            bias[j] += (target.data[base + j] - approx.data[base + j]) * inv;
        }
    }
    bias
}

pub fn outlier_runtime_bytes(outliers: &[SparseTerm]) -> u64 {
    outliers.len() as u64 * (8 + 4)
}

pub fn sparse_row_cache_bytes(cache: &SparseRowCache) -> u64 {
    let mut bytes = cache.by_row.len() as u64 * 24;
    for row in &cache.by_row {
        bytes += row.len() as u64 * (4 + 4);
    }
    bytes
}

pub fn outlier_row_cache_bytes(cache: &OutlierRowCache) -> u64 {
    let mut bytes = cache.by_row.len() as u64 * 24;
    for row in &cache.by_row {
        bytes += row.len() as u64 * (4 + 4);
    }
    bytes
}

pub fn repair_low_rank_bytes(repair: &Repair) -> u64 {
    let mut bytes = 24u64;
    for term in &repair.low_rank {
        bytes += 4 + term.u.len() as u64 * 4 + term.v.len() as u64 * 4;
    }
    bytes
}

pub fn repair_runtime_bytes(repair: &Repair) -> u64 {
    repair_low_rank_bytes(repair) + repair.sparse.len() as u64 * (8 + 4)
}

pub fn reconstruct_q8(q8: &Q8Matrix, repair: Option<&Repair>) -> Matrix {
    let mut matrix = crate::q8::dequantize_q8(q8);
    add_repair_to_matrix(&mut matrix, repair);
    matrix
}
