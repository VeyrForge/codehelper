//! ggml Q4_0 weight repack + Q8_0 activation GEMV (decode hot path).
//!
//! Layouts:
//! - **R4 / 4×8** (default when `GE_REPACK=1`): ggml `block_q4_0x4` with interleave=8
//!   — research R6.5 gaming-thread win; not rows8 / Q4_0_8_8.
//! - **8×8** (`GE_REPACK_ROWS=8`): ggml `gemv_q4_b32_8x8_q8_0_lut_avx`
//!   (shuffle/blend immediates 177/170 = 0xB1/0xAA).

use std::sync::OnceLock;

use crate::attention::f32_to_f16_bits;
use crate::gguf_load::f16_to_f32;
use crate::quant_kernels::{ActQ8, Q4_0_BLOCK, Q4_0_BYTES};
use crate::sys;
use rayon::prelude::*;

pub const Q4_0_8X8_BYTES: usize = 8 * 2 + 128;
pub const GGML_Q4_0_8X8: u32 = 100;
/// 4 f16 deltas + 64 interleaved qs bytes (ggml `block_q4_0x4`, interleave 8).
pub const Q4_0_4X8_BYTES: usize = 4 * 2 + 64;
pub const GGML_Q4_0_4X8: u32 = 101;

pub fn repack_enabled() -> bool {
    static ON: OnceLock<bool> = OnceLock::new();
    *ON.get_or_init(|| {
        match std::env::var("GE_REPACK") {
            Ok(v) if matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES" | "r4" | "R4" | "4") => {
                true
            }
            Ok(v) if matches!(v.as_str(), "0" | "false" | "FALSE" | "no" | "NO") => false,
            // Default off: must opt in for A/B (8×8 previously lost vs production on this rig).
            Err(_) => false,
            _ => false,
        }
    })
}

/// Row interleave width: **4** (R4) by default when repack is on; `GE_REPACK_ROWS=8` for 8×8.
pub fn repack_nrows() -> usize {
    static N: OnceLock<usize> = OnceLock::new();
    *N.get_or_init(|| match std::env::var("GE_REPACK_ROWS").as_deref() {
        Ok("8") => 8,
        Ok("4") => 4,
        _ => match std::env::var("GE_REPACK").as_deref() {
            Ok("8" | "8x8" | "rows8") => 8,
            _ => 4,
        },
    })
}

/// AVX2/VNNI repack GEMV. Default ON when `GE_REPACK=1`; set `GE_REPACK_AVX=0` for scalar.
fn repack_avx_enabled() -> bool {
    static ON: OnceLock<bool> = OnceLock::new();
    *ON.get_or_init(|| match std::env::var("GE_REPACK_AVX").as_deref() {
        Ok("0" | "false" | "FALSE" | "no" | "NO") => false,
        Ok("1" | "true" | "TRUE" | "yes" | "YES") => true,
        _ => true,
    })
}

pub fn should_repack_tensor(name: &str) -> bool {
    name.contains("token_embd")
        || name.contains("ffn_gate")
        || name.contains("ffn_up")
        || name.contains("ffn_down")
        || name.contains("output")
        || name.contains("lm_head")
}

/// ggml `make_block_q4_0x4` with `blck_size_interleave` 4 or 8 (we use 8 → 4×8 GEMV).
fn make_block_q4_0x4(blocks: &[[u8; Q4_0_BYTES]; 4], interleave: usize) -> [u8; Q4_0_4X8_BYTES] {
    let mut out = [0u8; Q4_0_4X8_BYTES];
    for i in 0..4 {
        out[i * 2] = blocks[i][0];
        out[i * 2 + 1] = blocks[i][1];
    }
    if interleave == 8 {
        const XOR: u64 = 0x8888_8888_8888_8888;
        let end = Q4_0_BLOCK * 2 / interleave; // 8
        for i in 0..end {
            let src_id = i % 4;
            let src_off = (i / 4) * interleave;
            let dst_off = i * interleave;
            let mut elems = u64::from_le_bytes(
                blocks[src_id][2 + src_off..2 + src_off + 8]
                    .try_into()
                    .unwrap(),
            );
            elems ^= XOR;
            out[8 + dst_off..8 + dst_off + 8].copy_from_slice(&elems.to_le_bytes());
        }
    } else if interleave == 4 {
        const XOR: u32 = 0x8888_8888;
        let end = Q4_0_BLOCK * 2 / interleave; // 16
        for i in 0..end {
            let src_id = i % 4;
            let src_off = (i / 4) * interleave;
            let dst_off = i * interleave;
            let mut elems = u32::from_le_bytes(
                blocks[src_id][2 + src_off..2 + src_off + 4]
                    .try_into()
                    .unwrap(),
            );
            elems ^= XOR;
            out[8 + dst_off..8 + dst_off + 4].copy_from_slice(&elems.to_le_bytes());
        }
    } else {
        panic!("make_block_q4_0x4: unsupported interleave {interleave}");
    }
    out
}

pub fn repack_q4_0_4x8_mat(packed: &[u8], in_dim: usize, out_dim: usize) -> Result<Vec<u8>, String> {
    if in_dim % Q4_0_BLOCK != 0 {
        return Err(format!("repack4: in_dim {in_dim} not multiple of {Q4_0_BLOCK}"));
    }
    if out_dim % 4 != 0 {
        return Err(format!("repack4: out_dim {out_dim} not multiple of 4"));
    }
    let nblocks = in_dim / Q4_0_BLOCK;
    let src_row_bytes = nblocks * Q4_0_BYTES;
    let groups = out_dim / 4;
    let mut out = Vec::with_capacity(groups * nblocks * Q4_0_4X8_BYTES);
    for g in 0..groups {
        let row0 = g * 4;
        for x in 0..nblocks {
            let mut tmp = [[0u8; Q4_0_BYTES]; 4];
            for r in 0..4 {
                let off = (row0 + r) * src_row_bytes + x * Q4_0_BYTES;
                if off + Q4_0_BYTES > packed.len() {
                    return Err("repack4: OOB source".into());
                }
                tmp[r].copy_from_slice(&packed[off..off + Q4_0_BYTES]);
            }
            out.extend_from_slice(&make_block_q4_0x4(&tmp, 8));
        }
    }
    Ok(out)
}

fn gemv_group4_scalar(packed: &[u8], act: &ActQ8, group_x: usize, nb: usize, scores: &mut [f32; 4]) {
    // Port of ggml_gemv_q4_0_4x8_q8_0_generic (ncols=4, blocklen=8).
    let qk = Q4_0_BLOCK;
    let blocklen = 8usize;
    let ncols = 4usize;
    scores.fill(0.0);
    for l in 0..nb {
        let b_off = (group_x * nb + l) * Q4_0_4X8_BYTES;
        let act_scale = act.scales[l];
        let act_qs = &act.qs[l * qk..(l + 1) * qk];
        for k in 0..(qk / (2 * blocklen)) {
            for j in 0..ncols {
                let mut sumi = 0i32;
                for i in 0..blocklen {
                    let qi = k * ncols * blocklen + j * blocklen + i;
                    let byte = packed[b_off + 8 + qi];
                    let v0 = (byte << 4) as i8 as i32;
                    let v1 = (byte & 0xF0) as i8 as i32;
                    let aq0 = act_qs[k * blocklen + i] as i32;
                    let aq1 = act_qs[k * blocklen + i + qk / 2] as i32;
                    sumi += (v0 * aq0 + v1 * aq1) >> 4;
                }
                let w_scale =
                    f16_to_f32(u16::from_le_bytes([packed[b_off + j * 2], packed[b_off + j * 2 + 1]]));
                scores[j] += sumi as f32 * w_scale * act_scale;
            }
        }
    }
}

pub fn gemv_q4_0_4x8(act: &ActQ8, packed: &[u8], in_dim: usize, out_dim: usize, y: &mut [f32]) {
    assert_eq!(y.len(), out_dim);
    gemv_q4_0_4x8_dispatch(act, packed, in_dim, out_dim, y);
}

fn gemv_q4_0_4x8_dispatch(act: &ActQ8, packed: &[u8], in_dim: usize, out_dim: usize, y: &mut [f32]) {
    #[cfg(target_arch = "x86_64")]
    {
        if repack_avx_enabled() && gemv_repack_isa() >= RepackIsa::Avx2 {
            let mut run = || unsafe {
                gemv_q4_0_4x8_q8_0_avx2(act, packed, in_dim, y);
            };
            if rayon::current_thread_index().is_some() {
                run();
            } else {
                sys::decode_pool().install(run);
            }
            return;
        }
    }
    let nb = in_dim / Q4_0_BLOCK;
    let mut run = || {
        y.par_chunks_mut(4).enumerate().for_each(|(x, yo)| {
            let mut scores = [0.0f32; 4];
            gemv_group4_scalar(packed, act, x, nb, &mut scores);
            yo.copy_from_slice(&scores);
        });
    };
    if rayon::current_thread_index().is_some() {
        run();
    } else {
        sys::decode_pool().install(run);
    }
}

pub fn argmax_q4_0_4x8(act: &ActQ8, packed: &[u8], in_dim: usize, out_dim: usize) -> u32 {
    let mut y = vec![0.0f32; out_dim];
    gemv_q4_0_4x8_dispatch(act, packed, in_dim, out_dim, &mut y);
    let mut best_s = f32::NEG_INFINITY;
    let mut best_o = 0u32;
    for (o, &s) in y.iter().enumerate() {
        if s >= best_s {
            best_s = s;
            best_o = o as u32;
        }
    }
    best_o
}

fn make_block_q4_0x8(blocks: &[[u8; Q4_0_BYTES]; 8], interleave: usize) -> [u8; Q4_0_8X8_BYTES] {
    let mut out = [0u8; Q4_0_8X8_BYTES];
    // Bit-exact f16 deltas (ggml copies `in[i].d`).
    for i in 0..8 {
        out[i * 2] = blocks[i][0];
        out[i * 2 + 1] = blocks[i][1];
    }
    const XOR: u64 = 0x8888_8888_8888_8888;
    let end = Q4_0_BLOCK * 4 / interleave;
    for i in 0..end {
        let src_id = i % 8;
        let src_off = (i / 8) * interleave;
        let dst_off = i * interleave;
        let mut elems = u64::from_le_bytes(
            blocks[src_id][2 + src_off..2 + src_off + 8]
                .try_into()
                .unwrap(),
        );
        elems ^= XOR;
        out[16 + dst_off..16 + dst_off + 8].copy_from_slice(&elems.to_le_bytes());
    }
    out
}

pub fn repack_q4_0_mat(packed: &[u8], in_dim: usize, out_dim: usize) -> Result<Vec<u8>, String> {
    if in_dim % Q4_0_BLOCK != 0 {
        return Err(format!("repack: in_dim {in_dim} not multiple of {Q4_0_BLOCK}"));
    }
    if out_dim % 8 != 0 {
        return Err(format!("repack: out_dim {out_dim} not multiple of 8"));
    }
    let nblocks = in_dim / Q4_0_BLOCK;
    let src_row_bytes = nblocks * Q4_0_BYTES;
    let groups = out_dim / 8;
    let mut out = Vec::with_capacity(groups * nblocks * Q4_0_8X8_BYTES);
    for g in 0..groups {
        let row0 = g * 8;
        for x in 0..nblocks {
            let mut tmp = [[0u8; Q4_0_BYTES]; 8];
            for r in 0..8 {
                let off = (row0 + r) * src_row_bytes + x * Q4_0_BYTES;
                if off + Q4_0_BYTES > packed.len() {
                    return Err("repack: OOB source".into());
                }
                tmp[r].copy_from_slice(&packed[off..off + Q4_0_BYTES]);
            }
            out.extend_from_slice(&make_block_q4_0x8(&tmp, 8));
        }
    }
    Ok(out)
}

fn gemv_group_scalar(packed: &[u8], act: &ActQ8, group_x: usize, nb: usize, scores: &mut [f32; 8]) {
    let qk = Q4_0_BLOCK;
    let blocklen = 8usize;
    let ncols = 8usize;
    scores.fill(0.0);
    for l in 0..nb {
        let b_off = (group_x * nb + l) * Q4_0_8X8_BYTES;
        let act_scale = act.scales[l];
        let act_qs = &act.qs[l * qk..(l + 1) * qk];
        for k in 0..(qk / (2 * blocklen)) {
            for j in 0..ncols {
                let mut sumi = 0i32;
                for i in 0..blocklen {
                    let qi = k * ncols * blocklen + j * blocklen + i;
                    let byte = packed[b_off + 16 + qi];
                    let v0 = (byte << 4) as i8 as i32;
                    let v1 = (byte & 0xF0) as i8 as i32;
                    let aq0 = act_qs[k * blocklen + i] as i32;
                    let aq1 = act_qs[k * blocklen + i + qk / 2] as i32;
                    sumi += (v0 * aq0 + v1 * aq1) >> 4;
                }
                let w_scale =
                    f16_to_f32(u16::from_le_bytes([packed[b_off + j * 2], packed[b_off + j * 2 + 1]]));
                scores[j] += sumi as f32 * w_scale * act_scale;
            }
        }
    }
}

pub fn gemv_q4_0_8x8(act: &ActQ8, packed: &[u8], in_dim: usize, out_dim: usize, y: &mut [f32]) {
    assert_eq!(y.len(), out_dim);
    gemv_q4_0_8x8_dispatch(act, packed, in_dim, out_dim, y);
}

fn gemv_q4_0_8x8_dispatch(act: &ActQ8, packed: &[u8], in_dim: usize, out_dim: usize, y: &mut [f32]) {
    #[cfg(target_arch = "x86_64")]
    {
        if repack_avx_enabled() && gemv_repack_isa() >= RepackIsa::Avx2 {
            let mut run = || unsafe {
                gemv_q4_0_8x8_q8_0_avx2(act, packed, in_dim, y);
            };
            if rayon::current_thread_index().is_some() {
                run();
            } else {
                sys::decode_pool().install(run);
            }
            return;
        }
    }
    let nb = in_dim / Q4_0_BLOCK;
    let mut run = || {
        y.par_chunks_mut(8).enumerate().for_each(|(x, yo)| {
            let mut scores = [0.0f32; 8];
            gemv_group_scalar(packed, act, x, nb, &mut scores);
            yo.copy_from_slice(&scores);
        });
    };
    if rayon::current_thread_index().is_some() {
        run();
    } else {
        sys::decode_pool().install(run);
    }
}

pub fn argmax_q4_0_8x8(act: &ActQ8, packed: &[u8], in_dim: usize, out_dim: usize) -> u32 {
    let mut y = vec![0.0f32; out_dim];
    gemv_q4_0_8x8_dispatch(act, packed, in_dim, out_dim, &mut y);
    let mut best_s = f32::NEG_INFINITY;
    let mut best_o = 0u32;
    for (o, &s) in y.iter().enumerate() {
        if s >= best_s {
            best_s = s;
            best_o = o as u32;
        }
    }
    best_o
}

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
enum RepackIsa {
    Scalar,
    Avx2,
    AvxVnni,
}

fn gemv_repack_isa() -> RepackIsa {
    static ISA: OnceLock<RepackIsa> = OnceLock::new();
    *ISA.get_or_init(|| {
        #[cfg(target_arch = "x86_64")]
        {
            if std::arch::is_x86_feature_detected!("avx2") && std::arch::is_x86_feature_detected!("fma")
            {
                if std::arch::is_x86_feature_detected!("avxvnni") {
                    return RepackIsa::AvxVnni;
                }
                return RepackIsa::Avx2;
            }
        }
        RepackIsa::Scalar
    })
}

/// Port of ggml `gemv_q4_b32_8x8_q8_0_lut_avx`.
#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma", enable = "f16c")]
unsafe fn gemv_q4_0_8x8_q8_0_avx2(act: &ActQ8, packed: &[u8], in_dim: usize, y: &mut [f32]) {
    use std::arch::x86_64::*;
    let nb = in_dim / Q4_0_BLOCK;
    let use_vnni = gemv_repack_isa() >= RepackIsa::AvxVnni;
    let signextendlut = _mm256_permute2f128_si256(
        _mm256_castsi128_si256(_mm_set_epi8(
            -1, -2, -3, -4, -5, -6, -7, -8, 7, 6, 5, 4, 3, 2, 1, 0,
        )),
        _mm256_castsi128_si256(_mm_set_epi8(
            -1, -2, -3, -4, -5, -6, -7, -8, 7, 6, 5, 4, 3, 2, 1, 0,
        )),
        0,
    );
    let finalpermutemask = _mm256_set_epi32(7, 5, 3, 1, 6, 4, 2, 0);
    let m4b = _mm256_set1_epi8(0x0f);
    let changemask = _mm_set_epi8(15, 14, 7, 6, 13, 12, 5, 4, 11, 10, 3, 2, 9, 8, 1, 0);

    y.par_chunks_mut(8).enumerate().for_each(|(x, yo)| {
        let mut acc_row = _mm256_setzero_ps();
        for b in 0..nb {
            let b_off = (x * nb + b) * Q4_0_8X8_BYTES;
            let rhs_0123_0 = _mm256_loadu_si256(packed.as_ptr().add(b_off + 16) as *const __m256i);
            let rhs_4567_0 = _mm256_loadu_si256(packed.as_ptr().add(b_off + 48) as *const __m256i);
            let rhs_0123_1 = _mm256_loadu_si256(packed.as_ptr().add(b_off + 80) as *const __m256i);
            let rhs_4567_1 = _mm256_loadu_si256(packed.as_ptr().add(b_off + 112) as *const __m256i);

            let rv_0123_0 = _mm256_shuffle_epi8(signextendlut, _mm256_and_si256(rhs_0123_0, m4b));
            let rv_4567_0 = _mm256_shuffle_epi8(signextendlut, _mm256_and_si256(rhs_4567_0, m4b));
            let rv_0123_1 = _mm256_shuffle_epi8(signextendlut, _mm256_and_si256(rhs_0123_1, m4b));
            let rv_4567_1 = _mm256_shuffle_epi8(signextendlut, _mm256_and_si256(rhs_4567_1, m4b));
            let rv_0123_2 = _mm256_shuffle_epi8(
                signextendlut,
                _mm256_and_si256(_mm256_srli_epi16(rhs_0123_0, 4), m4b),
            );
            let rv_4567_2 = _mm256_shuffle_epi8(
                signextendlut,
                _mm256_and_si256(_mm256_srli_epi16(rhs_4567_0, 4), m4b),
            );
            let rv_0123_3 = _mm256_shuffle_epi8(
                signextendlut,
                _mm256_and_si256(_mm256_srli_epi16(rhs_0123_1, 4), m4b),
            );
            let rv_4567_3 = _mm256_shuffle_epi8(
                signextendlut,
                _mm256_and_si256(_mm256_srli_epi16(rhs_4567_1, 4), m4b),
            );

            let col_scale = _mm256_cvtph_ps(_mm_shuffle_epi8(
                _mm_loadu_si128(packed.as_ptr().add(b_off) as *const __m128i),
                changemask,
            ));
            let row_scale = _mm256_set1_ps(act.scales[b]);
            let qs = act.qs.as_ptr().add(b * Q4_0_BLOCK);
            let mut lhs0 = _mm256_castsi128_si256(_mm_loadu_si128(qs as *const __m128i));
            let mut lhs1 = _mm256_castsi128_si256(_mm_loadu_si128(qs.add(16) as *const __m128i));
            lhs0 = _mm256_permute2f128_si256(lhs0, lhs0, 0);
            lhs1 = _mm256_permute2f128_si256(lhs1, lhs1, 0);

            let mut iacc = _mm256_setzero_si256();
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(rv_0123_0, _mm256_shuffle_epi32(rv_4567_0, 177), 170),
                _mm256_shuffle_epi32(lhs0, 0),
                use_vnni,
            );
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(_mm256_shuffle_epi32(rv_0123_0, 177), rv_4567_0, 170),
                _mm256_shuffle_epi32(lhs0, 85),
                use_vnni,
            );
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(rv_0123_1, _mm256_shuffle_epi32(rv_4567_1, 177), 170),
                _mm256_shuffle_epi32(lhs0, 170),
                use_vnni,
            );
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(_mm256_shuffle_epi32(rv_0123_1, 177), rv_4567_1, 170),
                _mm256_shuffle_epi32(lhs0, 255),
                use_vnni,
            );
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(rv_0123_2, _mm256_shuffle_epi32(rv_4567_2, 177), 170),
                _mm256_shuffle_epi32(lhs1, 0),
                use_vnni,
            );
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(_mm256_shuffle_epi32(rv_0123_2, 177), rv_4567_2, 170),
                _mm256_shuffle_epi32(lhs1, 85),
                use_vnni,
            );
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(rv_0123_3, _mm256_shuffle_epi32(rv_4567_3, 177), 170),
                _mm256_shuffle_epi32(lhs1, 170),
                use_vnni,
            );
            iacc = mul_sum_i8_acc(
                iacc,
                _mm256_blend_epi32(_mm256_shuffle_epi32(rv_0123_3, 177), rv_4567_3, 170),
                _mm256_shuffle_epi32(lhs1, 255),
                use_vnni,
            );

            acc_row = _mm256_fmadd_ps(
                _mm256_cvtepi32_ps(iacc),
                _mm256_mul_ps(col_scale, row_scale),
                acc_row,
            );
        }
        let acc_row = _mm256_permutevar8x32_ps(acc_row, finalpermutemask);
        _mm256_storeu_ps(yo.as_mut_ptr(), acc_row);
    });
}

/// AVX2 GEMV for ggml Q4_0 4×8 interleaved blocks (R4).
/// Faithful to `gemv_group4_scalar` / ggml generic 4x8 (blocklen=8, ncols=4).
#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma", enable = "f16c")]
unsafe fn gemv_q4_0_4x8_q8_0_avx2(act: &ActQ8, packed: &[u8], in_dim: usize, y: &mut [f32]) {
    use std::arch::x86_64::*;
    let nb = in_dim / Q4_0_BLOCK;
    let mask_lo = _mm_set1_epi8(0x0F);
    let mask_hi = _mm_set1_epi8(0xF0u8 as i8);
    let ones = _mm_set1_epi16(1);

    y.par_chunks_mut(4).enumerate().for_each(|(x, yo)| {
        let mut acc = [0.0f32; 4];
        for b in 0..nb {
            let b_off = (x * nb + b) * Q4_0_4X8_BYTES;
            let act_scale = act.scales[b];
            let aq = act.qs.as_ptr().add(b * Q4_0_BLOCK);
            let qs = packed.as_ptr().add(b_off + 8);
            let mut scales = [0.0f32; 4];
            for j in 0..4 {
                scales[j] = f16_to_f32(u16::from_le_bytes([
                    packed[b_off + j * 2],
                    packed[b_off + j * 2 + 1],
                ])) * act_scale;
            }
            for k in 0..2usize {
                let a_lo = _mm_loadl_epi64(aq.add(k * 8) as *const __m128i); // upper 64 zeroed
                let a_hi = _mm_loadl_epi64(aq.add(k * 8 + 16) as *const __m128i);
                for j in 0..4usize {
                    let q = _mm_loadl_epi64(qs.add(k * 32 + j * 8) as *const __m128i);
                    // Per-byte <<4 on low nibbles (mask first so slli_epi16 cannot cross bytes).
                    let v0 = _mm_slli_epi16(_mm_and_si128(q, mask_lo), 4);
                    let v1 = _mm_and_si128(q, mask_hi);
                    let ax0 = _mm_sign_epi8(v0, v0);
                    let sy0 = _mm_sign_epi8(a_lo, v0);
                    let ax1 = _mm_sign_epi8(v1, v1);
                    let sy1 = _mm_sign_epi8(a_hi, v1);
                    let d0 = _mm_madd_epi16(ones, _mm_maddubs_epi16(ax0, sy0));
                    let d1 = _mm_madd_epi16(ones, _mm_maddubs_epi16(ax1, sy1));
                    let d = _mm_add_epi32(d0, d1);
                    // Only low two i32 lanes are live (upper 8 q/a bytes are zero).
                    let sumi = (_mm_cvtsi128_si32(d) + _mm_extract_epi32(d, 1)) >> 4;
                    acc[j] += sumi as f32 * scales[j];
                }
            }
        }
        yo.copy_from_slice(&acc);
    });
}

#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn mul_sum_i8_acc(
    acc: std::arch::x86_64::__m256i,
    x: std::arch::x86_64::__m256i,
    y: std::arch::x86_64::__m256i,
    use_vnni: bool,
) -> std::arch::x86_64::__m256i {
    use std::arch::x86_64::*;
    let ax = _mm256_sign_epi8(x, x);
    let sy = _mm256_sign_epi8(y, x);
    if use_vnni {
        return _mm256_dpbusd_avx_epi32(acc, ax, sy);
    }
    let ones = _mm256_set1_epi16(1);
    let dot = _mm256_maddubs_epi16(ax, sy);
    _mm256_add_epi32(acc, _mm256_madd_epi16(ones, dot))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::quant_kernels::{gemv_q4_0_row_q8, ActQ8, Q4_0_BYTES};

    fn make_packed_distinct_scales(in_dim: usize, out_dim: usize) -> Vec<u8> {
        let nblocks_row = in_dim / Q4_0_BLOCK;
        let mut packed = vec![0u8; out_dim * nblocks_row * Q4_0_BYTES];
        for o in 0..out_dim {
            for x in 0..nblocks_row {
                let off = o * nblocks_row * Q4_0_BYTES + x * Q4_0_BYTES;
                let scale = 0.25f32 + (o as f32) * 0.07 + (x as f32) * 0.01;
                let bits = f32_to_f16_bits(scale);
                packed[off] = (bits & 0xff) as u8;
                packed[off + 1] = (bits >> 8) as u8;
                for j in 0..16usize {
                    let lo = ((o * 3 + j * 5 + x * 7) & 0x0f) as u8;
                    let hi = ((o * 5 + j * 3 + x * 11 + 1) & 0x0f) as u8;
                    packed[off + 2 + j] = lo | (hi << 4);
                }
            }
        }
        packed
    }

    #[test]
    fn repack_gemv_matches_q4_0_q8_rows() {
        let in_dim = 256usize;
        let out_dim = 64usize;
        let nblocks_row = in_dim / Q4_0_BLOCK;
        let packed = make_packed_distinct_scales(in_dim, out_dim);
        let repacked = repack_q4_0_mat(&packed, in_dim, out_dim).expect("repack");
        let x: Vec<f32> = (0..in_dim).map(|i| ((i as f32) * 0.017 - 0.4).sin()).collect();
        let act = ActQ8::quantize(&x);
        let mut y_ref = vec![0.0f32; out_dim];
        let rb = nblocks_row * Q4_0_BYTES;
        for o in 0..out_dim {
            y_ref[o] = gemv_q4_0_row_q8(&packed[o * rb..(o + 1) * rb], &act);
        }
        let mut y_rep = vec![0.0f32; out_dim];
        let nb = in_dim / Q4_0_BLOCK;
        y_rep.chunks_mut(8).enumerate().for_each(|(g, yo)| {
            let mut scores = [0.0f32; 8];
            gemv_group_scalar(&repacked, &act, g, nb, &mut scores);
            yo.copy_from_slice(&scores);
        });
        let max = y_ref
            .iter()
            .zip(&y_rep)
            .map(|(a, b)| (a - b).abs())
            .fold(0.0f32, f32::max);
        assert!(max < 0.05, "scalar repack vs q4_0_q8 max diff {max}");
    }

    #[test]
    fn repack4_gemv_matches_q4_0_q8_rows() {
        let in_dim = 256usize;
        let out_dim = 64usize;
        let nblocks_row = in_dim / Q4_0_BLOCK;
        let packed = make_packed_distinct_scales(in_dim, out_dim);
        let repacked = repack_q4_0_4x8_mat(&packed, in_dim, out_dim).expect("repack4");
        let x: Vec<f32> = (0..in_dim).map(|i| ((i as f32) * 0.017 - 0.4).sin()).collect();
        let act = ActQ8::quantize(&x);
        let mut y_ref = vec![0.0f32; out_dim];
        let rb = nblocks_row * Q4_0_BYTES;
        for o in 0..out_dim {
            y_ref[o] = gemv_q4_0_row_q8(&packed[o * rb..(o + 1) * rb], &act);
        }
        let mut y_rep = vec![0.0f32; out_dim];
        let nb = in_dim / Q4_0_BLOCK;
        y_rep.chunks_mut(4).enumerate().for_each(|(g, yo)| {
            let mut scores = [0.0f32; 4];
            gemv_group4_scalar(&repacked, &act, g, nb, &mut scores);
            yo.copy_from_slice(&scores);
        });
        let max = y_ref
            .iter()
            .zip(&y_rep)
            .map(|(a, b)| (a - b).abs())
            .fold(0.0f32, f32::max);
        assert!(max < 0.05, "scalar R4 repack vs q4_0_q8 max diff {max}");
    }

    #[cfg(target_arch = "x86_64")]
    #[test]
    fn repack4_avx_matches_scalar() {
        if !(std::arch::is_x86_feature_detected!("avx2")
            && std::arch::is_x86_feature_detected!("fma"))
        {
            return;
        }
        let in_dim = 2048usize;
        let out_dim = 256usize;
        let packed = make_packed_distinct_scales(in_dim, out_dim);
        let repacked = repack_q4_0_4x8_mat(&packed, in_dim, out_dim).expect("repack4");
        let x: Vec<f32> = (0..in_dim)
            .map(|i| ((i as f32) * 0.013 - 0.55).cos() * 0.7)
            .collect();
        let act = ActQ8::quantize(&x);
        let nb = in_dim / Q4_0_BLOCK;
        let mut y_scalar = vec![0.0f32; out_dim];
        y_scalar.chunks_mut(4).enumerate().for_each(|(g, yo)| {
            let mut scores = [0.0f32; 4];
            gemv_group4_scalar(&repacked, &act, g, nb, &mut scores);
            yo.copy_from_slice(&scores);
        });
        let mut y_avx = vec![0.0f32; out_dim];
        unsafe {
            gemv_q4_0_4x8_q8_0_avx2(&act, &repacked, in_dim, &mut y_avx);
        }
        let mut max = 0.0f32;
        let mut cos_num = 0.0f64;
        let mut cos_a = 0.0f64;
        let mut cos_b = 0.0f64;
        for (a, b) in y_scalar.iter().zip(&y_avx) {
            max = max.max((a - b).abs());
            cos_num += (*a as f64) * (*b as f64);
            cos_a += (*a as f64) * (*a as f64);
            cos_b += (*b as f64) * (*b as f64);
        }
        let cos = cos_num / (cos_a.sqrt() * cos_b.sqrt() + 1e-30);
        assert!(
            max < 1e-3 && cos >= 0.999999,
            "r4 avx vs scalar max_diff={max} cos={cos}"
        );
    }

    #[cfg(target_arch = "x86_64")]
    #[test]
    fn repack_avx_matches_scalar_distinct_scales() {
        if !(std::arch::is_x86_feature_detected!("avx2")
            && std::arch::is_x86_feature_detected!("fma"))
        {
            return;
        }
        let in_dim = 2048usize;
        let out_dim = 256usize;
        let packed = make_packed_distinct_scales(in_dim, out_dim);
        let repacked = repack_q4_0_mat(&packed, in_dim, out_dim).expect("repack");
        let x: Vec<f32> = (0..in_dim)
            .map(|i| ((i as f32) * 0.013 - 0.55).cos() * 0.7)
            .collect();
        let act = ActQ8::quantize(&x);
        let nb = in_dim / Q4_0_BLOCK;
        let mut y_scalar = vec![0.0f32; out_dim];
        y_scalar.chunks_mut(8).enumerate().for_each(|(g, yo)| {
            let mut scores = [0.0f32; 8];
            gemv_group_scalar(&repacked, &act, g, nb, &mut scores);
            yo.copy_from_slice(&scores);
        });
        let mut y_avx = vec![0.0f32; out_dim];
        unsafe {
            gemv_q4_0_8x8_q8_0_avx2(&act, &repacked, in_dim, &mut y_avx);
        }
        let mut max = 0.0f32;
        let mut cos_num = 0.0f64;
        let mut cos_a = 0.0f64;
        let mut cos_b = 0.0f64;
        for (a, b) in y_scalar.iter().zip(&y_avx) {
            max = max.max((a - b).abs());
            cos_num += (*a as f64) * (*b as f64);
            cos_a += (*a as f64) * (*a as f64);
            cos_b += (*b as f64) * (*b as f64);
        }
        let cos = cos_num / (cos_a.sqrt() * cos_b.sqrt() + 1e-30);
        assert!(
            max < 1e-3 && cos >= 0.999999,
            "avx vs scalar max_diff={max} cos={cos}"
        );
    }
}
