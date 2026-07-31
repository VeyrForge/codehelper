//! Q4_K codec (llama.cpp super-block layout).
//!
//! 256-weight super-blocks split into 8 sub-blocks of 32. Each sub-block is
//! asymmetric 4-bit: `w = scale_j * q - min_j`, `q in [0,15]`. The 8 per-sub
//! `scale`/`min` are themselves quantized to 6 bits against fp16 super-scales
//! `d`/`dmin`. ~4.5 bits/weight. Step 1 of the mixed-precision policy (roadmap #1):
//! this is the codec + tests; a matmul path and repair integration come next.

use rayon::prelude::*;

use crate::io::make_matrix;
use crate::repair::{add_repair_to_matrix, apply_repair_matmul};
use crate::simd::saxpy_accumulate;
use crate::types::{f16, Matrix, Q4KMatrix, Repair};

const QK_K: usize = 256; // weights per super-block
const SUB: usize = 32; // weights per sub-block
const NSUB: usize = QK_K / SUB; // 8 sub-blocks

#[inline]
fn set_nibble(quants: &mut [u8], index: usize, value: u8) {
    let byte = index / 2;
    if index & 1 == 0 {
        quants[byte] = (quants[byte] & 0xF0) | (value & 0x0F);
    } else {
        quants[byte] = (quants[byte] & 0x0F) | ((value & 0x0F) << 4);
    }
}

#[inline]
fn get_nibble(quants: &[u8], index: usize) -> u8 {
    let byte = quants[index / 2];
    if index & 1 == 0 {
        byte & 0x0F
    } else {
        (byte >> 4) & 0x0F
    }
}

pub fn quantize_q4k(matrix: &Matrix) -> Q4KMatrix {
    let total = matrix.data.len();
    let nsb = (total + QK_K - 1) / QK_K;
    let mut q4k = Q4KMatrix {
        rows: matrix.rows,
        cols: matrix.cols,
        d: vec![f16::from_f32(0.0); nsb],
        dmin: vec![f16::from_f32(0.0); nsb],
        scales: vec![0u8; nsb * NSUB],
        mins: vec![0u8; nsb * NSUB],
        quants: vec![0u8; total.div_ceil(2)],
    };

    for sb in 0..nsb {
        let base = sb * QK_K;
        let mut sub_scale = [0.0f32; NSUB];
        let mut sub_min = [0.0f32; NSUB];

        // First level: per-sub asymmetric 4-bit; write the nibbles.
        for j in 0..NSUB {
            let s0 = base + j * SUB;
            if s0 >= total {
                break;
            }
            let s1 = (s0 + SUB).min(total);
            let mut mn = f32::INFINITY;
            let mut mx = f32::NEG_INFINITY;
            for i in s0..s1 {
                let v = matrix.data[i];
                mn = mn.min(v);
                mx = mx.max(v);
            }
            // Offset capped at 0 so its magnitude stores unsigned; the quant range
            // [offset, mx] still covers the block (offset ≤ mn).
            let offset = mn.min(0.0);
            let scale = if mx > offset { (mx - offset) / 15.0 } else { 1.0 };
            sub_scale[j] = scale;
            sub_min[j] = -offset; // ≥ 0; reconstruction subtracts it (w = scale*q - sub_min)
            let inv = 1.0 / scale;
            for i in s0..s1 {
                let q = ((matrix.data[i] - offset) * inv).round().clamp(0.0, 15.0) as u8;
                set_nibble(&mut q4k.quants, i, q);
            }
        }

        // Second level: 6-bit quantize the (non-negative) sub scales/mins vs fp16 super-scales.
        let smax = sub_scale.iter().cloned().fold(0.0f32, f32::max);
        let mmax = sub_min.iter().cloned().fold(0.0f32, f32::max);
        let sd = if smax > 0.0 { smax / 63.0 } else { 1.0 };
        let sm = if mmax > 0.0 { mmax / 63.0 } else { 1.0 };
        q4k.d[sb] = f16::from_f32(sd);
        q4k.dmin[sb] = f16::from_f32(sm);
        for j in 0..NSUB {
            q4k.scales[sb * NSUB + j] = (sub_scale[j] / sd).round().clamp(0.0, 63.0) as u8;
            q4k.mins[sb * NSUB + j] = (sub_min[j] / sm).round().clamp(0.0, 63.0) as u8;
        }
    }
    q4k
}

pub fn dequantize_q4k(q4k: &Q4KMatrix) -> Matrix {
    let mut matrix = make_matrix(q4k.rows, q4k.cols);
    let total = matrix.data.len();
    let nsb = q4k.d.len();
    for sb in 0..nsb {
        let base = sb * QK_K;
        let sd = q4k.d[sb].to_f32();
        let sm = q4k.dmin[sb].to_f32();
        for j in 0..NSUB {
            let s0 = base + j * SUB;
            if s0 >= total {
                break;
            }
            let s1 = (s0 + SUB).min(total);
            let scale = sd * q4k.scales[sb * NSUB + j] as f32;
            let minv = sm * q4k.mins[sb * NSUB + j] as f32;
            for i in s0..s1 {
                matrix.data[i] = scale * get_nibble(&q4k.quants, i) as f32 - minv;
            }
        }
    }
    matrix
}

/// Dequantize one weight row into `row_out` (length = cols). Used by the matmul so
/// weights stay Q4_K-packed in RAM — only one row is materialized at a time.
pub fn dequantize_row_q4k(q4k: &Q4KMatrix, row: u32, row_out: &mut [f32]) {
    let cols = q4k.cols as usize;
    let rbase = row as usize * cols;
    for (j, out) in row_out.iter_mut().enumerate().take(cols) {
        let idx = rbase + j;
        let sb = idx / QK_K;
        let sub = (idx % QK_K) / SUB;
        let scale = q4k.d[sb].to_f32() * q4k.scales[sb * NSUB + sub] as f32;
        let minv = q4k.dmin[sb].to_f32() * q4k.mins[sb * NSUB + sub] as f32;
        *out = scale * get_nibble(&q4k.quants, idx) as f32 - minv;
    }
}

pub fn reconstruct_q4k(q4k: &Q4KMatrix, repair: Option<&Repair>) -> Matrix {
    let mut matrix = dequantize_q4k(q4k);
    add_repair_to_matrix(&mut matrix, repair);
    matrix
}

/// GEMV/GEMM: `out = activations @ dequant(Q4_K) (+ repair)`. Weights are dequantized
/// one row at a time (RAM-lean), then a saxpy accumulate — same structure as the Q4 path.
pub fn matmul_q4k_repaired(
    activations: &Matrix,
    q4k: &Q4KMatrix,
    repair: Option<&Repair>,
) -> Matrix {
    assert_eq!(activations.cols, q4k.rows, "activation/q4k dimension mismatch");
    let cols = q4k.cols as usize;
    let mut out = make_matrix(activations.rows, q4k.cols);
    out.data
        .par_chunks_mut(cols)
        .enumerate()
        .for_each(|(b, out_row)| {
            let b = b as u32;
            let mut w_row = vec![0.0f32; cols];
            for k in 0..q4k.rows {
                dequantize_row_q4k(q4k, k, &mut w_row);
                saxpy_accumulate(activations.at(b, k), &w_row, out_row);
            }
        });
    if let Some(repair) = repair {
        apply_repair_matmul(&mut out, activations, repair, q4k.rows, q4k.cols, None);
    }
    out
}

/// Runtime bytes: fp16 d/dmin + 6-bit scales/mins (1 B each) per super-block + 4-bit quants.
pub fn q4k_runtime_bytes(q4k: &Q4KMatrix) -> u64 {
    let nsb = q4k.d.len() as u64;
    8 + nsb * (2 + 2 + NSUB as u64 + NSUB as u64) + q4k.quants.len() as u64
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::q4::quantize_q4;
    use crate::util::rel_l2;

    fn weight_like_matrix(rows: u32, cols: u32) -> Matrix {
        // Zero-centred, slightly heavy-tailed — the shape of real transformer weights
        // (e.g. ffn_down min≈-0.62/max≈0.43). Each 32-block has min < 0, which is the
        // regime Q4_K's `w = scale*q - min` (unsigned min) targets.
        let mut m = make_matrix(rows, cols);
        for (i, v) in m.data.iter_mut().enumerate() {
            let t = (i as f32) * 0.017;
            *v = t.sin() * 0.3 + (t * 1.7).cos() * 0.15 + ((i % 89) as f32 - 44.0) * 0.003;
        }
        m
    }

    #[test]
    fn q4k_roundtrip_is_deterministic_and_bounded() {
        let m = weight_like_matrix(16, 40); // 640 weights → 3 super-blocks incl. a partial
        let a = quantize_q4k(&m);
        let b = quantize_q4k(&m);
        assert_eq!(a.quants, b.quants);
        assert_eq!(a.scales, b.scales);
        let rec = dequantize_q4k(&a);
        assert_eq!(rec.data.len(), m.data.len());
        let drift = rel_l2(&m.data, &rec.data);
        // 4-bit weight reconstruction is inherently ~0.08 rel-L2 (cf. real ffn_down 0.081).
        assert!(drift < 0.15, "q4k roundtrip drift {drift}");
    }

    #[test]
    fn q4k_beats_naive_q4_on_weight_like_data() {
        // Matches the Python finding: super-block asymmetric Q4_K < naive symmetric Q4 error.
        let m = weight_like_matrix(32, 64);
        let q4k_rec = dequantize_q4k(&quantize_q4k(&m));
        let q4_rec = crate::q4::dequantize_q4(&quantize_q4(&m, 32));
        let d_q4k = rel_l2(&m.data, &q4k_rec.data);
        let d_q4 = rel_l2(&m.data, &q4_rec.data);
        assert!(d_q4k < d_q4, "q4k {d_q4k} should beat naive q4 {d_q4}");
    }

    #[test]
    fn matmul_q4k_matches_full_dequant() {
        // The RAM-lean per-row matmul must equal dequantize-then-dense-matmul.
        let w = weight_like_matrix(48, 64); // rows=k=48, cols=out=64
        let q4k = quantize_q4k(&w);
        let acts = Matrix {
            rows: 5,
            cols: 48,
            data: (0..(5 * 48)).map(|i| ((i % 11) as f32 - 5.0) * 0.02).collect(),
        };
        let lean = matmul_q4k_repaired(&acts, &q4k, None);
        let dense = crate::matmul::matmul(&acts, &dequantize_q4k(&q4k));
        assert!(rel_l2(&dense.data, &lean.data) < 1e-5, "q4k matmul parity");
    }

    #[test]
    fn q4k_runtime_bytes_near_4p5_bpw() {
        let m = weight_like_matrix(64, 256); // 16384 weights, multiple of 256
        let q4k = quantize_q4k(&m);
        let bpw = q4k_runtime_bytes(&q4k) as f64 * 8.0 / m.data.len() as f64;
        assert!((4.4..4.8).contains(&bpw), "bpw {bpw}");
    }
}
