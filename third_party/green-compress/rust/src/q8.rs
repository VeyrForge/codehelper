use rand::prelude::*;
use rand::rngs::StdRng;
use rand::SeedableRng;

use crate::io::make_matrix;
use crate::matmul::matmul_q8_repaired;
use crate::repair::add_repair_to_matrix;
use crate::simd::dequantize_q8_block;
use crate::types::{f16, Matrix, Q8Matrix};
use crate::util::rel_l2;

pub fn quantize_q8(matrix: &Matrix, block: u32) -> Q8Matrix {
    assert!(block > 0, "block must be > 0");
    let total = matrix.data.len();
    let block_count = (total + block as usize - 1) / block as usize;
    let mut q8 = Q8Matrix {
        rows: matrix.rows,
        cols: matrix.cols,
        block,
        scales: vec![f16::from_f32(1.0); block_count],
        packed: vec![0i8; total],
        awq_scales: Vec::new(),
        row_spin: Vec::new(),
    };

    for b in 0..block_count {
        let start = b * block as usize;
        let end = (start + block as usize).min(total);
        let mut max_abs = 0.0f32;
        for i in start..end {
            max_abs = max_abs.max(matrix.data[i].abs());
        }
        let scale = if max_abs > 0.0 { max_abs / 127.0 } else { 1.0 };
        q8.scales[b] = f16::from_f32(scale);
        for i in start..end {
            let quant = (matrix.data[i] / scale).round() as i32;
            q8.packed[i] = quant.clamp(-127, 127) as i8;
        }
    }
    q8
}

pub fn dequantize_row_q8(q8: &Q8Matrix, row: u32, row_out: &mut [f32]) {
    let awq_inv = if (row as usize) < q8.awq_scales.len() && q8.awq_scales[row as usize] > 0.0 {
        1.0 / q8.awq_scales[row as usize]
    } else {
        1.0
    };
    let wbase = row as usize * q8.cols as usize;
    let mut j = 0u32;
    while j < q8.cols {
        let idx = wbase + j as usize;
        let scale = q8.scales[idx / q8.block as usize].to_f32() * awq_inv;
        let end = (j + q8.block).min(q8.cols);
        let block_len = (end - j) as usize;
        dequantize_q8_block(
            scale,
            &q8.packed[idx..idx + block_len],
            &mut row_out[j as usize..],
        );
        j = end;
    }
}

pub fn compute_awq_scales(act_rms: &[f64], alpha: f32) -> Vec<f32> {
    let mut scales = vec![1.0f32; act_rms.len()];
    if act_rms.is_empty() {
        return scales;
    }
    let mean = (act_rms.iter().sum::<f64>() / act_rms.len() as f64).max(1e-12);
    for (i, &value) in act_rms.iter().enumerate() {
        let ratio = value / mean;
        let scaled = ratio.max(1e-12).powf(alpha as f64);
        scales[i] = (scaled as f32).clamp(0.25, 4.0);
    }
    scales
}

pub fn quantize_q8_awq(matrix: &Matrix, block: u32, act_rms: &[f64], alpha: f32) -> Q8Matrix {
    let awq = compute_awq_scales(act_rms, alpha);
    let mut scaled = matrix.clone();
    for r in 0..matrix.rows {
        let row_scale = awq.get(r as usize).copied().unwrap_or(1.0);
        for c in 0..matrix.cols {
            *scaled.at_mut(r, c) *= row_scale;
        }
    }
    let mut q8 = quantize_q8(&scaled, block);
    q8.awq_scales = awq;
    q8
}

pub fn generate_row_spin(rows: u32, seed: u32) -> Vec<f32> {
    let mut spin = vec![1.0f32; rows as usize];
    if rows == 0 {
        return spin;
    }
    let mut rng = StdRng::seed_from_u64(seed as u64);
    for r in 0..rows as usize {
        spin[r] = if rng.random_bool(0.5) { -1.0 } else { 1.0 };
    }
    spin
}

pub fn apply_row_spin(matrix: &mut Matrix, row_spin: &[f32]) {
    if row_spin.is_empty() {
        return;
    }
    for r in 0..matrix.rows {
        let sign = row_spin.get(r as usize).copied().unwrap_or(1.0);
        if sign == 1.0 {
            continue;
        }
        let base = r as usize * matrix.cols as usize;
        for c in 0..matrix.cols as usize {
            matrix.data[base + c] *= sign;
        }
    }
}

pub fn quantize_q8_awq_spin(
    matrix: &Matrix,
    block: u32,
    act_rms: &[f64],
    row_spin: &[f32],
    alpha: f32,
) -> Q8Matrix {
    let awq = compute_awq_scales(act_rms, alpha);
    let mut scaled = matrix.clone();
    for r in 0..matrix.rows {
        let row_scale = awq.get(r as usize).copied().unwrap_or(1.0);
        for c in 0..matrix.cols {
            *scaled.at_mut(r, c) *= row_scale;
        }
    }
    apply_row_spin(&mut scaled, row_spin);
    let mut q8 = quantize_q8(&scaled, block);
    q8.awq_scales = awq;
    q8.row_spin = row_spin.to_vec();
    q8
}

pub fn q8_value_at(q8: &Q8Matrix, index: usize) -> f32 {
    let row = (index / q8.cols as usize) as u32;
    let awq_inv = if (row as usize) < q8.awq_scales.len() && q8.awq_scales[row as usize] > 0.0 {
        1.0 / q8.awq_scales[row as usize]
    } else {
        1.0
    };
    q8.packed[index] as f32 * q8.scales[index / q8.block as usize].to_f32() * awq_inv
}

pub fn q8_row_spin(q8: &Q8Matrix, row: u32) -> f32 {
    if (row as usize) < q8.row_spin.len() && q8.row_spin[row as usize] != 0.0 {
        q8.row_spin[row as usize]
    } else {
        1.0
    }
}

pub fn q8_value_unspun(q8: &Q8Matrix, index: usize) -> f32 {
    let row = (index / q8.cols as usize) as u32;
    q8_value_at(q8, index) * q8_row_spin(q8, row)
}

pub fn dequantize_q8(q8: &Q8Matrix) -> Matrix {
    let mut matrix = make_matrix(q8.rows, q8.cols);
    for i in 0..matrix.data.len() {
        matrix.data[i] = q8_value_unspun(q8, i);
    }
    matrix
}

pub fn q8_runtime_bytes(q8: &Q8Matrix) -> u64 {
    28
        + q8.scales.len() as u64 * 2
        + q8.packed.len() as u64
        + q8.awq_scales.len() as u64 * 4
        + q8.row_spin.len() as u64 * 4
}

pub fn pick_best_row_spin(
    quant_target: &Matrix,
    activations: &Matrix,
    full_out: &Matrix,
    quant_block: u32,
    act_rms: &[f64],
    seed: u32,
    trials: u32,
) -> Vec<f32> {
    let mut best = generate_row_spin(quant_target.rows, seed + 7);
    let mut best_drift = 1e30f64;
    for t in 0..trials {
        let spin = generate_row_spin(quant_target.rows, seed + 7 + t * 997);
        let q8 = quantize_q8_awq_spin(quant_target, quant_block, act_rms, &spin, 0.5);
        let q8_out = matmul_q8_repaired(activations, &q8, None, None, None, None, None, None);
        let drift = rel_l2(&full_out.data, &q8_out.data);
        if drift < best_drift {
            best_drift = drift;
            best = spin;
        }
    }
    best
}

pub fn reconstruct_q8(q8: &Q8Matrix, repair: Option<&crate::types::Repair>) -> Matrix {
    let mut matrix = dequantize_q8(q8);
    add_repair_to_matrix(&mut matrix, repair);
    matrix
}
