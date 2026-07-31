//! Uniform sub-8-bit block quantizer (Q5/Q6/Q7), bit-packed.
//!
//! Same shape as Q8 (symmetric per-block scale, f16) but `bits` bits per weight,
//! packed across byte boundaries. Real-perplexity validated on Llama-3.2-1B:
//! **Q7 = +0.28% ppl at −12% RAM** (passes the ≥99% gate), Q6 = +1.00% at −24%.
//! `green_q7` is the first sub-Q8 precision that keeps quality — see
//! Validated on Llama-3.2-1B: Q7 = +0.28% perplexity at −12% RAM vs Q8.

use rayon::prelude::*;

use crate::io::make_matrix;
use crate::repair::{add_repair_to_matrix, apply_repair_matmul};
use crate::simd::saxpy_accumulate;
use crate::types::{f16, Matrix, QnMatrix, Repair};

#[inline]
fn level(bits: u8) -> i32 {
    (1 << (bits - 1)) - 1 // 15,31,63 for 5,6,7
}

/// Pack `count` `bits`-wide unsigned codes (already in `codes`) into a byte vector.
fn pack_bits(codes: &[u16], bits: u8) -> Vec<u8> {
    let total_bits = codes.len() * bits as usize;
    let mut out = vec![0u8; total_bits.div_ceil(8)];
    let mut acc: u32 = 0;
    let mut nbits = 0u32;
    let mut oi = 0usize;
    for &c in codes {
        acc |= ((c as u32) & ((1u32 << bits) - 1)) << nbits;
        nbits += bits as u32;
        while nbits >= 8 {
            out[oi] = (acc & 0xFF) as u8;
            oi += 1;
            acc >>= 8;
            nbits -= 8;
        }
    }
    if nbits > 0 {
        out[oi] = (acc & 0xFF) as u8;
    }
    out
}

/// Read the `bits`-wide code at logical `index`.
#[inline]
fn get_code(packed: &[u8], index: usize, bits: u8) -> u16 {
    let bitpos = index * bits as usize;
    let bytepos = bitpos / 8;
    let shift = (bitpos % 8) as u32;
    // bits <= 7, so a code spans at most 2 bytes.
    let mut val = packed[bytepos] as u32;
    if bytepos + 1 < packed.len() {
        val |= (packed[bytepos + 1] as u32) << 8;
    }
    ((val >> shift) & ((1u32 << bits) - 1)) as u16
}

pub fn quantize_qn(matrix: &Matrix, bits: u8, block: u32) -> QnMatrix {
    assert!((5..=8).contains(&bits), "bits must be 5..=8");
    assert!(block > 0);
    let total = matrix.data.len();
    let lv = level(bits);
    let block_count = (total + block as usize - 1) / block as usize;
    let mut scales = vec![f16::from_f32(1.0); block_count];
    let mut codes = vec![0u16; total];
    for b in 0..block_count {
        let start = b * block as usize;
        let end = (start + block as usize).min(total);
        let mut max_abs = 0.0f32;
        for i in start..end {
            max_abs = max_abs.max(matrix.data[i].abs());
        }
        let scale = if max_abs > 0.0 { max_abs / lv as f32 } else { 1.0 };
        scales[b] = f16::from_f32(scale);
        let inv = 1.0 / scale;
        for i in start..end {
            let q = (matrix.data[i] * inv).round().clamp(-(lv as f32), lv as f32) as i32;
            codes[i] = (q + lv) as u16; // offset to unsigned [0, 2*lv]
        }
    }
    QnMatrix {
        rows: matrix.rows,
        cols: matrix.cols,
        bits,
        block,
        scales,
        packed: pack_bits(&codes, bits),
    }
}

pub fn dequantize_row_qn(qn: &QnMatrix, row: u32, row_out: &mut [f32]) {
    let lv = level(qn.bits);
    let cols = qn.cols as usize;
    let rbase = row as usize * cols;
    for (j, out) in row_out.iter_mut().enumerate().take(cols) {
        let idx = rbase + j;
        let scale = qn.scales[idx / qn.block as usize].to_f32();
        *out = (get_code(&qn.packed, idx, qn.bits) as i32 - lv) as f32 * scale;
    }
}

pub fn dequantize_qn(qn: &QnMatrix) -> Matrix {
    let mut m = make_matrix(qn.rows, qn.cols);
    let cols = qn.cols as usize;
    for r in 0..qn.rows {
        let base = r as usize * cols;
        dequantize_row_qn(qn, r, &mut m.data[base..base + cols]);
    }
    m
}

pub fn reconstruct_qn(qn: &QnMatrix, repair: Option<&Repair>) -> Matrix {
    let mut m = dequantize_qn(qn);
    add_repair_to_matrix(&mut m, repair);
    m
}

/// `out = activations @ dequant(Qn) (+ repair)` — RAM-lean per-row dequant + saxpy.
pub fn matmul_qn_repaired(activations: &Matrix, qn: &QnMatrix, repair: Option<&Repair>) -> Matrix {
    assert_eq!(activations.cols, qn.rows, "activation/qn dimension mismatch");
    let cols = qn.cols as usize;
    let mut out = make_matrix(activations.rows, qn.cols);
    out.data
        .par_chunks_mut(cols)
        .enumerate()
        .for_each(|(b, out_row)| {
            let b = b as u32;
            let mut w_row = vec![0.0f32; cols];
            for k in 0..qn.rows {
                dequantize_row_qn(qn, k, &mut w_row);
                saxpy_accumulate(activations.at(b, k), &w_row, out_row);
            }
        });
    if let Some(repair) = repair {
        apply_repair_matmul(&mut out, activations, repair, qn.rows, qn.cols, None);
    }
    out
}

pub fn qn_runtime_bytes(qn: &QnMatrix) -> u64 {
    28 + qn.scales.len() as u64 * 2 + qn.packed.len() as u64
}

/// `qn-bench --in W.mx --activations X.mx [--bench-iters N]`: quality/RAM/speed of Q5–Q8.
pub fn cmd_qn_bench(args: &crate::types::Args) -> crate::error::Result<()> {
    use crate::io::load_matrix;
    use crate::matmul::matmul;
    use crate::util::{get_string, get_u32, rel_l2};
    use std::path::Path;
    use std::time::Instant;

    let w = load_matrix(Path::new(&get_string(args, "in", "")?))?;
    let x = load_matrix(Path::new(&get_string(args, "activations", "")?))?;
    let iters = get_u32(args, "bench-iters", 5, false)?.max(1);
    let fp = matmul(&x, &w);
    let params = w.data.len();
    println!("qn-bench  W={}x{}  X={}x{}  iters={}", w.rows, w.cols, x.rows, x.cols, iters);
    println!(
        "{:<8}{:>10}{:>8}{:>12}{:>12}",
        "prec", "acc%", "bpw", "RAM(MiB)", "matmul(ms)"
    );
    for bits in [8u8, 7, 6, 5] {
        let qn = quantize_qn(&w, bits, 32);
        let t0 = Instant::now();
        let mut out = make_matrix(x.rows, w.cols);
        for _ in 0..iters {
            out = matmul_qn_repaired(&x, &qn, None);
        }
        let ms = t0.elapsed().as_secs_f64() * 1000.0 / iters as f64;
        let acc = 100.0 * (1.0 - rel_l2(&fp.data, &out.data));
        let bytes = qn_runtime_bytes(&qn);
        let bpw = bytes as f64 * 8.0 / params as f64;
        println!(
            "Q{:<7}{:>10.4}{:>8.2}{:>12.3}{:>12.3}",
            bits, acc, bpw, bytes as f64 / 1024.0 / 1024.0, ms
        );
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::q8::quantize_q8;
    use crate::util::rel_l2;

    fn weight_like(rows: u32, cols: u32) -> Matrix {
        let mut m = make_matrix(rows, cols);
        for (i, v) in m.data.iter_mut().enumerate() {
            let t = (i as f32) * 0.017;
            *v = t.sin() * 0.3 + (t * 1.7).cos() * 0.15 + ((i % 89) as f32 - 44.0) * 0.003;
        }
        m
    }

    #[test]
    fn bitpack_roundtrip_all_widths() {
        for bits in [5u8, 6, 7] {
            let maxc = (1u16 << bits) - 1;
            let codes: Vec<u16> = (0..500).map(|i| (i as u16 * 7 + 3) & maxc).collect();
            let packed = pack_bits(&codes, bits);
            for (i, &c) in codes.iter().enumerate() {
                assert_eq!(get_code(&packed, i, bits), c, "bits={bits} i={i}");
            }
        }
    }

    #[test]
    fn qn_roundtrip_and_monotonic_quality() {
        // Higher bits → lower error; Q7 close to Q8.
        let m = weight_like(40, 96);
        let mut prev = f64::INFINITY;
        for bits in [5u8, 6, 7] {
            let rec = dequantize_qn(&quantize_qn(&m, bits, 32));
            let d = rel_l2(&m.data, &rec.data);
            assert!(d < prev, "bits={bits} drift {d} !< {prev}");
            prev = d;
        }
        // Q7 within ~2x of Q8 weight error.
        let q7 = rel_l2(&m.data, &dequantize_qn(&quantize_qn(&m, 7, 32)).data);
        let q8 = rel_l2(&m.data, &crate::q8::dequantize_q8(&quantize_q8(&m, 32)).data);
        assert!(q7 < q8 * 2.5, "q7 {q7} vs q8 {q8}");
    }

    #[test]
    fn matmul_qn_matches_full_dequant() {
        let w = weight_like(48, 64);
        let qn = quantize_qn(&w, 7, 32);
        let acts = Matrix {
            rows: 5,
            cols: 48,
            data: (0..(5 * 48)).map(|i| ((i % 11) as f32 - 5.0) * 0.02).collect(),
        };
        let lean = matmul_qn_repaired(&acts, &qn, None);
        let dense = crate::matmul::matmul(&acts, &dequantize_qn(&qn));
        assert!(rel_l2(&dense.data, &lean.data) < 1e-5);
    }

    #[test]
    fn qn_bpw_matches_bits() {
        let m = weight_like(64, 256);
        for (bits, lo, hi) in [(5u8, 5.4, 5.7), (6, 6.4, 6.7), (7, 7.4, 7.7)] {
            let qn = quantize_qn(&m, bits, 32);
            let bpw = qn_runtime_bytes(&qn) as f64 * 8.0 / m.data.len() as f64;
            assert!((lo..hi).contains(&bpw), "bits={bits} bpw={bpw}");
        }
    }
}
