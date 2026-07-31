use rayon::prelude::*;

use crate::error::fail;
use crate::io::make_matrix;
use crate::repair::{add_repair_to_matrix, apply_repair_matmul};
use crate::simd::saxpy_accumulate;
use crate::types::{Matrix, Q4Matrix, Repair, SparseRowCache};

fn pack_i4(value: i32) -> u8 {
    let shifted = value.clamp(-8, 7) + 8;
    (shifted & 15) as u8
}

fn unpack_i4(value: u8) -> i32 {
    (value & 15) as i32 - 8
}

pub fn quantize_q4(matrix: &Matrix, block: u32) -> Q4Matrix {
    assert!(block > 0, "block must be > 0");
    let total = matrix.data.len();
    let block_count = (total + block as usize - 1) / block as usize;
    let mut q4 = Q4Matrix {
        rows: matrix.rows,
        cols: matrix.cols,
        block,
        scales: vec![1.0; block_count],
        packed: vec![0u8; (total + 1) / 2],
    };

    for b in 0..block_count {
        let start = b * block as usize;
        let end = (start + block as usize).min(total);
        let mut max_abs = 0.0f32;
        for i in start..end {
            max_abs = max_abs.max(matrix.data[i].abs());
        }
        let scale = if max_abs > 0.0 { max_abs / 7.0 } else { 1.0 };
        q4.scales[b] = scale;
        for i in start..end {
            let quant = (matrix.data[i] / scale).round() as i32;
            let packed_value = pack_i4(quant);
            let byte_index = i / 2;
            if i & 1 == 0 {
                q4.packed[byte_index] = (q4.packed[byte_index] & 0xF0) | packed_value;
            } else {
                q4.packed[byte_index] = (q4.packed[byte_index] & 0x0F) | (packed_value << 4);
            }
        }
    }
    q4
}

pub fn q4_value_at(q4: &Q4Matrix, index: usize) -> f32 {
    let byte_index = index / 2;
    let byte = q4.packed[byte_index];
    let nibble = if index & 1 == 0 {
        byte & 15
    } else {
        (byte >> 4) & 15
    };
    unpack_i4(nibble) as f32 * q4.scales[index / q4.block as usize]
}

pub fn dequantize_row(q4: &Q4Matrix, row: u32, row_out: &mut [f32]) {
    let wbase = row as usize * q4.cols as usize;
    for j in 0..q4.cols as usize {
        let index = wbase + j;
        let byte_index = index / 2;
        let byte = q4.packed[byte_index];
        let nibble = if index & 1 == 0 {
            byte & 15
        } else {
            (byte >> 4) & 15
        };
        row_out[j] = unpack_i4(nibble) as f32 * q4.scales[index / q4.block as usize];
    }
}

pub fn q4_runtime_bytes(q4: &Q4Matrix) -> u64 {
    28 + q4.scales.len() as u64 * 4 + q4.packed.len() as u64
}

pub fn dequantize_q4(q4: &Q4Matrix) -> Matrix {
    let mut matrix = make_matrix(q4.rows, q4.cols);
    for i in 0..matrix.data.len() {
        matrix.data[i] = q4_value_at(q4, i);
    }
    matrix
}

pub fn reconstruct(q4: &Q4Matrix, repair: Option<&Repair>) -> Matrix {
    let mut matrix = dequantize_q4(q4);
    add_repair_to_matrix(&mut matrix, repair);
    matrix
}

pub fn matmul_q4_repaired(
    activations: &Matrix,
    q4: &Q4Matrix,
    repair: Option<&Repair>,
    _sparse_row_cache: Option<&SparseRowCache>,
) -> Matrix {
    if activations.cols != q4.rows {
        panic!("{}", fail("activation/q4 dimension mismatch"));
    }
    let mut out = make_matrix(activations.rows, q4.cols);
    let cols = q4.cols as usize;

    out.data
        .par_chunks_mut(cols)
        .enumerate()
        .for_each(|(b, out_row)| {
            let b = b as u32;
            for i in 0..q4.rows {
                let mut w_row = vec![0.0f32; cols];
                dequantize_row(q4, i, &mut w_row);
                let x = activations.at(b, i);
                saxpy_accumulate(x, &w_row, out_row);
            }
        });

    if let Some(repair) = repair {
        apply_repair_matmul(&mut out, activations, repair, q4.rows, q4.cols, None);
    }
    out
}
