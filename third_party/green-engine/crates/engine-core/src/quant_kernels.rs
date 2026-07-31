//! SIMD fused Q4_0 / Q4_K block dots for decode GEMV.
//!
//! Dispatch ladder (runtime): scalar → AVX2+FMA → AVX-512F (+BW) → AVX-VNNI when the
//! activation-quantized int path is used. Keeps a scalar oracle for tests.
//!
//! Hot path for pack-model 1B is Q4_0 × Q8_0 (activations quantized once per GEMV),
//! matching llama.cpp's `ggml_vec_dot_q4_0_q8_0` shape — no full-block f32 scratch.

use crate::gguf_load::f16_to_f32;

pub const Q4_0_BLOCK: usize = 32;
pub const Q4_0_BYTES: usize = 18;
pub const QK_K: usize = 256;
pub const Q4_K_BYTES: usize = 144;

/// Which ISA the Q4 GEMV path selected (for benches / logs).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum GemvIsa {
    Scalar,
    Avx2Fma,
    Avx512,
    AvxVnni,
}

impl GemvIsa {
    pub fn as_str(self) -> &'static str {
        match self {
            GemvIsa::Scalar => "scalar",
            GemvIsa::Avx2Fma => "avx2+fma",
            GemvIsa::Avx512 => "avx512",
            GemvIsa::AvxVnni => "avxvnni",
        }
    }
}

pub fn has_avx_vnni() -> bool {
    #[cfg(target_arch = "x86_64")] {
        if std::is_x86_feature_detected!("avxvnni") { return true; }
        if std::is_x86_feature_detected!("avx512vnni") && std::is_x86_feature_detected!("avx512vl") { return true; }
    }
    false
}

/// AVX-512F + BW + VL for 16-wide f32 block dots (used when present; else AVX2).
#[inline]
pub fn has_avx512_f32() -> bool {
    #[cfg(target_arch = "x86_64")]
    {
        is_x86_feature_detected!("avx512f")
            && is_x86_feature_detected!("fma")
            && is_x86_feature_detected!("avx512bw")
            && is_x86_feature_detected!("avx512vl")
    }
    #[cfg(not(target_arch = "x86_64"))]
    {
        false
    }
}

/// One-time ISA probe for fused Q4 GEMV.
pub fn detect_gemv_isa() -> GemvIsa {
    #[cfg(target_arch = "x86_64")]
    {
        // VNNI (AVX-VNNI or AVX512-VNNI) prefers the int8 activation path.
        if is_x86_feature_detected!("avxvnni")
            || (is_x86_feature_detected!("avx512f") && is_x86_feature_detected!("avx512vnni"))
        {
            return GemvIsa::AvxVnni;
        }
        if is_x86_feature_detected!("avx512f") && is_x86_feature_detected!("fma") {
            return GemvIsa::Avx512;
        }
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            return GemvIsa::Avx2Fma;
        }
    }
    GemvIsa::Scalar
}

/// Q8_0-style activation pack: f32 scales (avoid f16 round-trip) + i8 quants.
#[derive(Clone, Debug)]
pub struct ActQ8 {
    pub scales: Vec<f32>,
    pub qs: Vec<i8>,
}

impl ActQ8 {
    pub fn quantize_into(&mut self, x: &[f32]) {
        let n_blocks = x.len() / Q4_0_BLOCK;
        self.scales.clear();
        self.qs.clear();
        self.scales.reserve(n_blocks);
        self.qs.reserve(n_blocks * Q4_0_BLOCK);
        for b in 0..n_blocks {
            let xb = &x[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
            let mut amax = 0.0f32;
            for &v in xb { amax = amax.max(v.abs()); }
            let d = amax / 127.0;
            let id = if d > 1e-12 { 1.0 / d } else { 0.0 };
            self.scales.push(d);
            for &v in xb {
                self.qs.push((v * id).round().clamp(-127.0, 127.0) as i8);
            }
        }
    }

    pub fn quantize(x: &[f32]) -> Self {
        let n_blocks = x.len() / Q4_0_BLOCK;
        let mut scales = Vec::with_capacity(n_blocks);
        let mut qs = Vec::with_capacity(n_blocks * Q4_0_BLOCK);
        for b in 0..n_blocks {
            let xb = &x[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
            let mut amax = 0.0f32;
            for &v in xb {
                amax = amax.max(v.abs());
            }
            let d = amax / 127.0;
            let id = if d > 1e-12 { 1.0 / d } else { 0.0 };
            scales.push(d);
            for &v in xb {
                let q = (v * id).round().clamp(-127.0, 127.0) as i8;
                qs.push(q);
            }
        }
        ActQ8 { scales, qs }
    }

    #[inline]
    pub fn n_blocks(&self) -> usize {
        self.scales.len()
    }
}

/// Fused Q4_0 block · f32[32] (scalar; correctness oracle).
#[inline(always)]
pub fn q4_0_block_dot_f32_scalar(block: &[u8], x: &[f32]) -> f32 {
    debug_assert!(block.len() >= Q4_0_BYTES);
    debug_assert!(x.len() >= Q4_0_BLOCK);
    let scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let qs = &block[2..18];
    let mut sum = 0.0f32;
    let mut j = 0usize;
    while j + 4 <= 16 {
        let b0 = qs[j];
        let b1 = qs[j + 1];
        let b2 = qs[j + 2];
        let b3 = qs[j + 3];
        sum += ((b0 & 0x0f) as i8 - 8) as f32 * x[j]
            + ((b0 >> 4) as i8 - 8) as f32 * x[j + 16]
            + ((b1 & 0x0f) as i8 - 8) as f32 * x[j + 1]
            + ((b1 >> 4) as i8 - 8) as f32 * x[j + 17]
            + ((b2 & 0x0f) as i8 - 8) as f32 * x[j + 2]
            + ((b2 >> 4) as i8 - 8) as f32 * x[j + 18]
            + ((b3 & 0x0f) as i8 - 8) as f32 * x[j + 3]
            + ((b3 >> 4) as i8 - 8) as f32 * x[j + 19];
        j += 4;
    }
    sum * scale
}

/// Fused Q4_0 × Q8 block (scalar).
#[inline(always)]
pub fn q4_0_block_dot_q8_scalar(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    let w_scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let wb = &block[2..18];
    let mut sum = 0i32;
    for j in 0..16 {
        let byte = wb[j];
        let q0 = (byte & 0x0f) as i8 - 8;
        let q1 = (byte >> 4) as i8 - 8;
        sum += q0 as i32 * qs[j] as i32;
        sum += q1 as i32 * qs[j + 16] as i32;
    }
    sum as f32 * w_scale
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_f32_avx2_partial(block: *const u8, x: *const f32) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let scale = f16_to_f32(u16::from_le_bytes([*block, *block.add(1)]));
    let vscale = _mm256_set1_ps(scale);
    let qs = _mm_loadu_si128(block.add(2) as *const __m128i);
    let low_mask = _mm_set1_epi8(0x0f);
    let off8 = _mm_set1_epi8(8);
    let q_lo = _mm_sub_epi8(_mm_and_si128(qs, low_mask), off8);
    let q_hi = _mm_sub_epi8(_mm_and_si128(_mm_srli_epi16(qs, 4), low_mask), off8);
    let mut acc = _mm256_setzero_ps();
    let q0 = _mm256_cvtepi8_epi32(q_lo);
    let x0 = _mm256_loadu_ps(x);
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q0), vscale), x0, acc);
    let q1 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_lo, 8));
    let x1 = _mm256_loadu_ps(x.add(8));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q1), vscale), x1, acc);
    let q2 = _mm256_cvtepi8_epi32(q_hi);
    let x2 = _mm256_loadu_ps(x.add(16));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q2), vscale), x2, acc);
    let q3 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_hi, 8));
    let x3 = _mm256_loadu_ps(x.add(24));
    _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q3), vscale), x3, acc)
}

/// Row GEMV: Σ blocks Q4_0 · x (f32 path). AVX-512 or AVX2 accumulates blocks before hsum.
pub fn gemv_q4_0_row_f32(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    #[cfg(target_arch = "x86_64")]
    {
        if has_avx512_f32() {
            unsafe {
                return gemv_q4_0_row_f32_avx512(packed_row, x, in_dim);
            }
        }
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return gemv_q4_0_row_f32_avx2(packed_row, x, in_dim);
            }
        }
    }
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut sum = 0.0f32;
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        let x0 = b * Q4_0_BLOCK;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 2 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 3 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        b += 4;
    }
    while b < n_blocks {
        sum += q4_0_block_dot_f32(
            &packed_row[off..off + Q4_0_BYTES],
            &x[b * Q4_0_BLOCK..],
        );
        off += Q4_0_BYTES;
        b += 1;
    }
    sum + gemv_q4_0_row_f32_tail(packed_row, x, in_dim, off, n_blocks)
}

#[inline]
fn gemv_q4_0_row_f32_tail(
    packed_row: &[u8],
    x: &[f32],
    in_dim: usize,
    off: usize,
    n_blocks: usize,
) -> f32 {
    let rem = in_dim % Q4_0_BLOCK;
    if rem == 0 || off + Q4_0_BYTES > packed_row.len() {
        return 0.0;
    }
    let scale = f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]));
    let qs = &packed_row[off + 2..off + 18];
    let base = n_blocks * Q4_0_BLOCK;
    let mut sum = 0.0f32;
    for j in 0..rem.min(16) {
        let byte = qs[j];
        sum += ((byte & 0x0f) as i8 - 8) as f32 * scale * x[base + j];
    }
    for j in 0..rem.saturating_sub(16) {
        let byte = qs[j];
        sum += ((byte >> 4) as i8 - 8) as f32 * scale * x[base + 16 + j];
    }
    sum
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn gemv_q4_0_row_f32_avx2(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut acc = _mm256_setzero_ps();
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        for k in 0..4usize {
            let bi = b + k;
            acc = _mm256_add_ps(
                acc,
                q4_0_block_dot_f32_avx2_partial(
                    packed_row.as_ptr().add(off),
                    x.as_ptr().add(bi * Q4_0_BLOCK),
                ),
            );
            off += Q4_0_BYTES;
        }
        b += 4;
    }
    while b < n_blocks {
        acc = _mm256_add_ps(
            acc,
            q4_0_block_dot_f32_avx2_partial(
                packed_row.as_ptr().add(off),
                x.as_ptr().add(b * Q4_0_BLOCK),
            ),
        );
        off += Q4_0_BYTES;
        b += 1;
    }
    hsum256_ps(acc) + gemv_q4_0_row_f32_tail(packed_row, x, in_dim, off, n_blocks)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx512f", enable = "fma", enable = "avx512bw", enable = "avx512vl")]
unsafe fn q4_0_block_dot_f32_avx512_partial(block: *const u8, x: *const f32) -> std::arch::x86_64::__m512 {
    use std::arch::x86_64::*;
    let scale = f16_to_f32(u16::from_le_bytes([*block, *block.add(1)]));
    let vscale = _mm512_set1_ps(scale);
    let qs = _mm_loadu_si128(block.add(2) as *const __m128i);
    let low_mask = _mm_set1_epi8(0x0f);
    let off8 = _mm_set1_epi8(8);
    let q_lo = _mm_sub_epi8(_mm_and_si128(qs, low_mask), off8);
    let q_hi = _mm_sub_epi8(_mm_and_si128(_mm_srli_epi16(qs, 4), low_mask), off8);
    let q0 = _mm512_cvtepi8_epi32(q_lo);
    let x0 = _mm512_loadu_ps(x);
    let acc = _mm512_fmadd_ps(_mm512_mul_ps(_mm512_cvtepi32_ps(q0), vscale), x0, _mm512_setzero_ps());
    let q1 = _mm512_cvtepi8_epi32(q_hi);
    let x1 = _mm512_loadu_ps(x.add(16));
    _mm512_fmadd_ps(_mm512_mul_ps(_mm512_cvtepi32_ps(q1), vscale), x1, acc)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx512f", enable = "fma", enable = "avx512bw", enable = "avx512vl")]
unsafe fn gemv_q4_0_row_f32_avx512(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut acc = _mm512_setzero_ps();
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        for k in 0..4usize {
            let bi = b + k;
            acc = _mm512_add_ps(
                acc,
                q4_0_block_dot_f32_avx512_partial(
                    packed_row.as_ptr().add(off),
                    x.as_ptr().add(bi * Q4_0_BLOCK),
                ),
            );
            off += Q4_0_BYTES;
        }
        b += 4;
    }
    while b < n_blocks {
        acc = _mm512_add_ps(
            acc,
            q4_0_block_dot_f32_avx512_partial(
                packed_row.as_ptr().add(off),
                x.as_ptr().add(b * Q4_0_BLOCK),
            ),
        );
        off += Q4_0_BYTES;
        b += 1;
    }
    hsum512_ps(acc) + gemv_q4_0_row_f32_tail(packed_row, x, in_dim, off, n_blocks)
}

/// Four consecutive row dots sharing activation block loads (lm_head argmax hot path).
///
/// Keeps `x` in L1 across four weight rows — ~1.3–1.6× vs four isolated row calls on
/// memory-bound decode (measured in `gemv_dot_microbench --rows4`).
pub fn gemv_q4_0_rows4_f32(
    packed: &[u8],
    row_bytes: usize,
    row0: usize,
    x: &[f32],
    in_dim: usize,
) -> [f32; 4] {
    debug_assert!(in_dim % Q4_0_BLOCK == 0);
    #[cfg(target_arch = "x86_64")]
    {
        if has_avx512_f32() {
            unsafe {
                return gemv_q4_0_rows4_f32_avx512(packed, row_bytes, row0, x, in_dim);
            }
        }
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return gemv_q4_0_rows4_f32_avx2(packed, row_bytes, row0, x, in_dim);
            }
        }
    }
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut sums = [0.0f32; 4];
    for b in 0..n_blocks {
        let xb = &x[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
        for r in 0..4 {
            let off = (row0 + r) * row_bytes + b * Q4_0_BYTES;
            sums[r] += q4_0_block_dot_f32(&packed[off..off + Q4_0_BYTES], xb);
        }
    }
    for r in 0..4 {
        let off = (row0 + r) * row_bytes + n_blocks * Q4_0_BYTES;
        sums[r] += gemv_q4_0_row_f32_tail(packed, x, in_dim, off, n_blocks);
    }
    sums
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn gemv_q4_0_rows4_f32_avx2(
    packed: &[u8],
    row_bytes: usize,
    row0: usize,
    x: &[f32],
    in_dim: usize,
) -> [f32; 4] {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut accs = [_mm256_setzero_ps(); 4];
    for b in 0..n_blocks {
        let x_ptr = x.as_ptr().add(b * Q4_0_BLOCK);
        for r in 0..4 {
            let block = packed.as_ptr().add((row0 + r) * row_bytes + b * Q4_0_BYTES);
            accs[r] = _mm256_add_ps(accs[r], q4_0_block_dot_f32_avx2_partial(block, x_ptr));
        }
    }
    let mut sums = [0.0f32; 4];
    for r in 0..4 {
        let off = (row0 + r) * row_bytes + n_blocks * Q4_0_BYTES;
        sums[r] = hsum256_ps(accs[r])
            + gemv_q4_0_row_f32_tail(packed, x, in_dim, off, n_blocks);
    }
    sums
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx512f", enable = "fma", enable = "avx512bw", enable = "avx512vl")]
unsafe fn gemv_q4_0_rows4_f32_avx512(
    packed: &[u8],
    row_bytes: usize,
    row0: usize,
    x: &[f32],
    in_dim: usize,
) -> [f32; 4] {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut accs = [_mm512_setzero_ps(); 4];
    for b in 0..n_blocks {
        let x_ptr = x.as_ptr().add(b * Q4_0_BLOCK);
        for r in 0..4 {
            let block = packed.as_ptr().add((row0 + r) * row_bytes + b * Q4_0_BYTES);
            accs[r] = _mm512_add_ps(accs[r], q4_0_block_dot_f32_avx512_partial(block, x_ptr));
        }
    }
    let mut sums = [0.0f32; 4];
    for r in 0..4 {
        let off = (row0 + r) * row_bytes + n_blocks * Q4_0_BYTES;
        sums[r] = hsum512_ps(accs[r])
            + gemv_q4_0_row_f32_tail(packed, x, in_dim, off, n_blocks);
    }
    sums
}

/// Fused gate+up row pair: one activation pass, two Q4_0 dots (SwiGLU FFN).
pub fn gemv_q4_0_gate_up_row_f32(
    gate_row: &[u8],
    up_row: &[u8],
    x: &[f32],
    in_dim: usize,
) -> (f32, f32) {
    debug_assert!(in_dim % Q4_0_BLOCK == 0);
    #[cfg(target_arch = "x86_64")]
    {
        if has_avx512_f32() {
            unsafe {
                return gemv_q4_0_gate_up_row_f32_avx512(gate_row, up_row, x, in_dim);
            }
        }
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return gemv_q4_0_gate_up_row_f32_avx2(gate_row, up_row, x, in_dim);
            }
        }
    }
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut g_sum = 0.0f32;
    let mut u_sum = 0.0f32;
    for b in 0..n_blocks {
        let xb = &x[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
        let off = b * Q4_0_BYTES;
        g_sum += q4_0_block_dot_f32(&gate_row[off..off + Q4_0_BYTES], xb);
        u_sum += q4_0_block_dot_f32(&up_row[off..off + Q4_0_BYTES], xb);
    }
    let tail_off = n_blocks * Q4_0_BYTES;
    (
        g_sum + gemv_q4_0_row_f32_tail(gate_row, x, in_dim, tail_off, n_blocks),
        u_sum + gemv_q4_0_row_f32_tail(up_row, x, in_dim, tail_off, n_blocks),
    )
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn gemv_q4_0_gate_up_row_f32_avx2(
    gate_row: &[u8],
    up_row: &[u8],
    x: &[f32],
    in_dim: usize,
) -> (f32, f32) {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut g_acc = _mm256_setzero_ps();
    let mut u_acc = _mm256_setzero_ps();
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        for k in 0..4usize {
            let bi = b + k;
            let x_ptr = x.as_ptr().add(bi * Q4_0_BLOCK);
            g_acc = _mm256_add_ps(
                g_acc,
                q4_0_block_dot_f32_avx2_partial(gate_row.as_ptr().add(off), x_ptr),
            );
            u_acc = _mm256_add_ps(
                u_acc,
                q4_0_block_dot_f32_avx2_partial(up_row.as_ptr().add(off), x_ptr),
            );
            off += Q4_0_BYTES;
        }
        b += 4;
    }
    while b < n_blocks {
        let x_ptr = x.as_ptr().add(b * Q4_0_BLOCK);
        g_acc = _mm256_add_ps(
            g_acc,
            q4_0_block_dot_f32_avx2_partial(gate_row.as_ptr().add(off), x_ptr),
        );
        u_acc = _mm256_add_ps(
            u_acc,
            q4_0_block_dot_f32_avx2_partial(up_row.as_ptr().add(off), x_ptr),
        );
        off += Q4_0_BYTES;
        b += 1;
    }
    let tail_off = off;
    (
        hsum256_ps(g_acc) + gemv_q4_0_row_f32_tail(gate_row, x, in_dim, tail_off, n_blocks),
        hsum256_ps(u_acc) + gemv_q4_0_row_f32_tail(up_row, x, in_dim, tail_off, n_blocks),
    )
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx512f", enable = "fma", enable = "avx512bw", enable = "avx512vl")]
unsafe fn gemv_q4_0_gate_up_row_f32_avx512(
    gate_row: &[u8],
    up_row: &[u8],
    x: &[f32],
    in_dim: usize,
) -> (f32, f32) {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut g_acc = _mm512_setzero_ps();
    let mut u_acc = _mm512_setzero_ps();
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        for k in 0..4usize {
            let bi = b + k;
            let x_ptr = x.as_ptr().add(bi * Q4_0_BLOCK);
            g_acc = _mm512_add_ps(
                g_acc,
                q4_0_block_dot_f32_avx512_partial(gate_row.as_ptr().add(off), x_ptr),
            );
            u_acc = _mm512_add_ps(
                u_acc,
                q4_0_block_dot_f32_avx512_partial(up_row.as_ptr().add(off), x_ptr),
            );
            off += Q4_0_BYTES;
        }
        b += 4;
    }
    while b < n_blocks {
        let x_ptr = x.as_ptr().add(b * Q4_0_BLOCK);
        g_acc = _mm512_add_ps(
            g_acc,
            q4_0_block_dot_f32_avx512_partial(gate_row.as_ptr().add(off), x_ptr),
        );
        u_acc = _mm512_add_ps(
            u_acc,
            q4_0_block_dot_f32_avx512_partial(up_row.as_ptr().add(off), x_ptr),
        );
        off += Q4_0_BYTES;
        b += 1;
    }
    let tail_off = off;
    (
        hsum512_ps(g_acc) + gemv_q4_0_row_f32_tail(gate_row, x, in_dim, tail_off, n_blocks),
        hsum512_ps(u_acc) + gemv_q4_0_row_f32_tail(up_row, x, in_dim, tail_off, n_blocks),
    )
}

/// Four gate+up row pairs sharing activation block loads (FFN hot path).
pub fn gemv_q4_0_gate_up_rows4_f32(
    gate_packed: &[u8],
    up_packed: &[u8],
    row_bytes: usize,
    row0: usize,
    x: &[f32],
    in_dim: usize,
) -> ([f32; 4], [f32; 4]) {
    debug_assert!(in_dim % Q4_0_BLOCK == 0);
    #[cfg(target_arch = "x86_64")]
    {
        if has_avx512_f32() {
            unsafe {
                return gemv_q4_0_gate_up_rows4_f32_avx512(
                    gate_packed, up_packed, row_bytes, row0, x, in_dim,
                );
            }
        }
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return gemv_q4_0_gate_up_rows4_f32_avx2(
                    gate_packed, up_packed, row_bytes, row0, x, in_dim,
                );
            }
        }
    }
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut g_sums = [0.0f32; 4];
    let mut u_sums = [0.0f32; 4];
    for b in 0..n_blocks {
        let xb = &x[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
        for r in 0..4 {
            let off = (row0 + r) * row_bytes + b * Q4_0_BYTES;
            g_sums[r] += q4_0_block_dot_f32(&gate_packed[off..off + Q4_0_BYTES], xb);
            u_sums[r] += q4_0_block_dot_f32(&up_packed[off..off + Q4_0_BYTES], xb);
        }
    }
    for r in 0..4 {
        let off = (row0 + r) * row_bytes + n_blocks * Q4_0_BYTES;
        g_sums[r] += gemv_q4_0_row_f32_tail(gate_packed, x, in_dim, off, n_blocks);
        u_sums[r] += gemv_q4_0_row_f32_tail(up_packed, x, in_dim, off, n_blocks);
    }
    (g_sums, u_sums)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn gemv_q4_0_gate_up_rows4_f32_avx2(
    gate_packed: &[u8],
    up_packed: &[u8],
    row_bytes: usize,
    row0: usize,
    x: &[f32],
    in_dim: usize,
) -> ([f32; 4], [f32; 4]) {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut g_accs = [_mm256_setzero_ps(); 4];
    let mut u_accs = [_mm256_setzero_ps(); 4];
    for b in 0..n_blocks {
        let x_ptr = x.as_ptr().add(b * Q4_0_BLOCK);
        for r in 0..4 {
            let g_block = gate_packed.as_ptr().add((row0 + r) * row_bytes + b * Q4_0_BYTES);
            let u_block = up_packed.as_ptr().add((row0 + r) * row_bytes + b * Q4_0_BYTES);
            g_accs[r] = _mm256_add_ps(g_accs[r], q4_0_block_dot_f32_avx2_partial(g_block, x_ptr));
            u_accs[r] = _mm256_add_ps(u_accs[r], q4_0_block_dot_f32_avx2_partial(u_block, x_ptr));
        }
    }
    let mut g_sums = [0.0f32; 4];
    let mut u_sums = [0.0f32; 4];
    for r in 0..4 {
        let off = (row0 + r) * row_bytes + n_blocks * Q4_0_BYTES;
        g_sums[r] = hsum256_ps(g_accs[r])
            + gemv_q4_0_row_f32_tail(gate_packed, x, in_dim, off, n_blocks);
        u_sums[r] = hsum256_ps(u_accs[r])
            + gemv_q4_0_row_f32_tail(up_packed, x, in_dim, off, n_blocks);
    }
    (g_sums, u_sums)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx512f", enable = "fma", enable = "avx512bw", enable = "avx512vl")]
unsafe fn gemv_q4_0_gate_up_rows4_f32_avx512(
    gate_packed: &[u8],
    up_packed: &[u8],
    row_bytes: usize,
    row0: usize,
    x: &[f32],
    in_dim: usize,
) -> ([f32; 4], [f32; 4]) {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut g_accs = [_mm512_setzero_ps(); 4];
    let mut u_accs = [_mm512_setzero_ps(); 4];
    for b in 0..n_blocks {
        let x_ptr = x.as_ptr().add(b * Q4_0_BLOCK);
        for r in 0..4 {
            let g_block = gate_packed.as_ptr().add((row0 + r) * row_bytes + b * Q4_0_BYTES);
            let u_block = up_packed.as_ptr().add((row0 + r) * row_bytes + b * Q4_0_BYTES);
            g_accs[r] = _mm512_add_ps(g_accs[r], q4_0_block_dot_f32_avx512_partial(g_block, x_ptr));
            u_accs[r] = _mm512_add_ps(u_accs[r], q4_0_block_dot_f32_avx512_partial(u_block, x_ptr));
        }
    }
    let mut g_sums = [0.0f32; 4];
    let mut u_sums = [0.0f32; 4];
    for r in 0..4 {
        let off = (row0 + r) * row_bytes + n_blocks * Q4_0_BYTES;
        g_sums[r] = hsum512_ps(g_accs[r])
            + gemv_q4_0_row_f32_tail(gate_packed, x, in_dim, off, n_blocks);
        u_sums[r] = hsum512_ps(u_accs[r])
            + gemv_q4_0_row_f32_tail(up_packed, x, in_dim, off, n_blocks);
    }
    (g_sums, u_sums)
}

/// Row GEMV: Σ blocks Q4_0 · ActQ8 (ggml vec_dot_q4_0_q8_0 FMA accumulate).
pub fn gemv_q4_0_row_q8(packed_row: &[u8], act: &ActQ8) -> f32 {
    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avxvnni") {
            unsafe {
                return gemv_q4_0_row_q8_avxvnni(packed_row, act);
            }
        }
        if is_x86_feature_detected!("avx512vnni") && is_x86_feature_detected!("avx512vl") {
            unsafe {
                return gemv_q4_0_row_q8_avx512vnni(packed_row, act);
            }
        }
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return gemv_q4_0_row_q8_avx2(packed_row, act);
            }
        }
    }
    let n_blocks = act.n_blocks();
    let mut sum = 0.0f32;
    let mut off = 0usize;
    for b in 0..n_blocks {
        let qs = &act.qs[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
        sum += q4_0_block_dot_q8(
            &packed_row[off..off + Q4_0_BYTES],
            qs,
            act.scales[b],
        );
        off += Q4_0_BYTES;
    }
    sum
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn gemv_q4_0_row_q8_avx2(packed_row: &[u8], act: &ActQ8) -> f32 {
    use std::arch::x86_64::*;
    let n_blocks = act.n_blocks();
    let mut acc = _mm256_setzero_ps();
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        for k in 0..4usize {
            let bi = b + k;
            let d = _mm256_set1_ps(
                f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]))
                    * act.scales[bi],
            );
            let qx = _mm256_sub_epi8(
                bytes_from_nibbles_32(packed_row.as_ptr().add(off + 2)),
                _mm256_set1_epi8(8),
            );
            let qy = _mm256_loadu_si256(
                act.qs.as_ptr().add(bi * Q4_0_BLOCK) as *const __m256i,
            );
            let q = mul_sum_i8_pairs_float_avx2(qx, qy);
            acc = _mm256_fmadd_ps(d, q, acc);
            off += Q4_0_BYTES;
        }
        b += 4;
    }
    while b < n_blocks {
        let d = _mm256_set1_ps(
            f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]))
                * act.scales[b],
        );
        let qx = _mm256_sub_epi8(
            bytes_from_nibbles_32(packed_row.as_ptr().add(off + 2)),
            _mm256_set1_epi8(8),
        );
        let qy = _mm256_loadu_si256(act.qs.as_ptr().add(b * Q4_0_BLOCK) as *const __m256i);
        let q = mul_sum_i8_pairs_float_avx2(qx, qy);
        acc = _mm256_fmadd_ps(d, q, acc);
        off += Q4_0_BYTES;
        b += 1;
    }
    hsum256_ps(acc)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma", enable = "avxvnni")]
unsafe fn gemv_q4_0_row_q8_avxvnni(packed_row: &[u8], act: &ActQ8) -> f32 {
    use std::arch::x86_64::*;
    let n_blocks = act.n_blocks();
    let mut sum = 0.0f32;
    let mut off = 0usize;
    for b in 0..n_blocks {
        let w = f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]))
            * act.scales[b];
        let qx = _mm256_sub_epi8(
            bytes_from_nibbles_32(packed_row.as_ptr().add(off + 2)),
            _mm256_set1_epi8(8),
        );
        let qy =
            _mm256_loadu_si256(act.qs.as_ptr().add(b * Q4_0_BLOCK) as *const __m256i);
        let ax = _mm256_sign_epi8(qx, qx);
        let sy = _mm256_sign_epi8(qy, qx);
        let isum = hsum_epi32_256(_mm256_dpbusd_avx_epi32(_mm256_setzero_si256(), ax, sy));
        sum += isum as f32 * w;
        off += Q4_0_BYTES;
    }
    sum
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma", enable = "avx512f", enable = "avx512vnni", enable = "avx512vl")]
unsafe fn gemv_q4_0_row_q8_avx512vnni(packed_row: &[u8], act: &ActQ8) -> f32 {
    use std::arch::x86_64::*;
    let n_blocks = act.n_blocks();
    let mut sum = 0.0f32;
    let mut off = 0usize;
    for b in 0..n_blocks {
        let w = f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]))
            * act.scales[b];
        let qx = _mm256_sub_epi8(
            bytes_from_nibbles_32(packed_row.as_ptr().add(off + 2)),
            _mm256_set1_epi8(8),
        );
        let qy =
            _mm256_loadu_si256(act.qs.as_ptr().add(b * Q4_0_BLOCK) as *const __m256i);
        let ax = _mm256_sign_epi8(qx, qx);
        let sy = _mm256_sign_epi8(qy, qx);
        let isum = hsum_epi32_256(_mm256_dpbusd_epi32(_mm256_setzero_si256(), ax, sy));
        sum += isum as f32 * w;
        off += Q4_0_BYTES;
    }
    sum
}

#[inline(always)]
pub fn q4_0_block_dot_f32(block: &[u8], x: &[f32]) -> f32 {
    #[cfg(target_arch = "x86_64")]
    {
        if has_avx512_f32() {
            unsafe {
                return hsum512_ps(q4_0_block_dot_f32_avx512_partial(block.as_ptr(), x.as_ptr()));
            }
        }
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return q4_0_block_dot_f32_avx2(block, x);
            }
        }
    }
    q4_0_block_dot_f32_scalar(block, x)
}

#[inline(always)]
pub fn q4_0_block_dot_q8(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return q4_0_block_dot_q8_avx2(block, qs, act_scale);
            }
        }
    }
    q4_0_block_dot_q8_scalar(block, qs, act_scale)
}

/// Q4_K fused block · f32[256] (scalar).
pub fn q4_k_block_dot_f32_scalar(block: &[u8], x: &[f32]) -> f32 {
    let d = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let dmin = f16_to_f32(u16::from_le_bytes([block[2], block[3]]));
    let (sc, m) = unpack_q4k_scales(&block[4..16]);
    let qs = &block[16..144];
    let mut sum = 0.0f32;
    for sub in 0..8 {
        let scale = d * sc[sub] as f32;
        let minv = dmin * m[sub] as f32;
        let g = sub / 2;
        let plane = sub % 2;
        let qs_off = g * 32;
        let shift = plane * 4;
        let x_off = sub * 32;
        let mut local = 0.0f32;
        let mut min_acc = 0.0f32;
        for j in 0..32 {
            let q = ((qs[qs_off + j] >> shift) & 0x0F) as f32;
            let xj = x[x_off + j];
            local += q * xj;
            min_acc += xj;
        }
        sum += scale * local - minv * min_acc;
    }
    sum
}

pub fn q4_k_block_dot_f32(block: &[u8], x: &[f32]) -> f32 {
    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return q4_k_block_dot_f32_avx2(block, x);
            }
        }
    }
    q4_k_block_dot_f32_scalar(block, x)
}

pub fn unpack_q4k_scales(scales: &[u8]) -> ([u8; 8], [u8; 8]) {
    let mut sc = [0u8; 8];
    let mut m = [0u8; 8];
    for i in 0..4 {
        sc[i] = scales[i] & 0x3F;
        m[i] = scales[i + 4] & 0x3F;
        sc[i + 4] = (scales[8 + i] & 0x0F) | ((scales[i] >> 2) & 0x30);
        m[i + 4] = (scales[8 + i] >> 4) | ((scales[i + 4] >> 2) & 0x30);
    }
    (sc, m)
}

#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn hsum512_ps(v: std::arch::x86_64::__m512) -> f32 {
    use std::arch::x86_64::*;
    let hi = _mm512_extractf32x8_ps(v, 1);
    let lo = _mm512_castps512_ps256(v);
    hsum256_ps(_mm256_add_ps(lo, hi))
}

#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn hsum256_ps(v: std::arch::x86_64::__m256) -> f32 {
    use std::arch::x86_64::*;
    let hi = _mm256_extractf128_ps(v, 1);
    let lo = _mm256_castps256_ps128(v);
    let sum = _mm_add_ps(lo, hi);
    let shuf = _mm_movehdup_ps(sum);
    let sum = _mm_add_ps(sum, shuf);
    let shuf = _mm_movehl_ps(shuf, sum);
    let sum = _mm_add_ss(sum, shuf);
    _mm_cvtss_f32(sum)
}


#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn sum_i16_pairs_float(dot: std::arch::x86_64::__m256i) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let ones = _mm256_set1_epi16(1);
    _mm256_cvtepi32_ps(_mm256_madd_epi16(ones, dot))
}

#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn mul_sum_us8_pairs_float_avx2(
    ax: std::arch::x86_64::__m256i,
    sy: std::arch::x86_64::__m256i,
) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    if is_x86_feature_detected!("avxvnni") {
        let zero = _mm256_setzero_si256();
        return _mm256_cvtepi32_ps(_mm256_dpbusd_avx_epi32(zero, ax, sy));
    }
    if is_x86_feature_detected!("avx512vnni") && is_x86_feature_detected!("avx512vl") {
        let zero = _mm256_setzero_si256();
        return _mm256_cvtepi32_ps(_mm256_dpbusd_epi32(zero, ax, sy));
    }
    let axl = _mm256_castsi256_si128(ax);
    let axh = _mm256_extracti128_si256(ax, 1);
    let syl = _mm256_castsi256_si128(sy);
    let syh = _mm256_extracti128_si256(sy, 1);
    let dotl = _mm_maddubs_epi16(axl, syl);
    let doth = _mm_maddubs_epi16(axh, syh);
    sum_i16_pairs_float(_mm256_set_m128i(doth, dotl))
}

#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn mul_sum_i8_pairs_float_avx2(
    x: std::arch::x86_64::__m256i,
    y: std::arch::x86_64::__m256i,
) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let ax = _mm256_sign_epi8(x, x);
    let sy = _mm256_sign_epi8(y, x);
    mul_sum_us8_pairs_float_avx2(ax, sy)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_f32_avx2(block: &[u8], x: &[f32]) -> f32 {
    use std::arch::x86_64::*;
    let scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let vscale = _mm256_set1_ps(scale);
    let qs = _mm_loadu_si128(block.as_ptr().add(2) as *const __m128i);
    let low_mask = _mm_set1_epi8(0x0f);
    let off8 = _mm_set1_epi8(8);
    let q_lo = _mm_sub_epi8(_mm_and_si128(qs, low_mask), off8);
    let q_hi = _mm_sub_epi8(_mm_and_si128(_mm_srli_epi16(qs, 4), low_mask), off8);

    // Process 8+8 low, then 8+8 high via cvtepi8_epi32 → f32 FMA.
    let mut acc = _mm256_setzero_ps();

    // low nibbles → x[0..15]
    let q0 = _mm256_cvtepi8_epi32(q_lo);
    let x0 = _mm256_loadu_ps(x.as_ptr());
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q0), vscale), x0, acc);
    let q1 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_lo, 8));
    let x1 = _mm256_loadu_ps(x.as_ptr().add(8));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q1), vscale), x1, acc);

    // high nibbles → x[16..31]
    let q2 = _mm256_cvtepi8_epi32(q_hi);
    let x2 = _mm256_loadu_ps(x.as_ptr().add(16));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q2), vscale), x2, acc);
    let q3 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_hi, 8));
    let x3 = _mm256_loadu_ps(x.as_ptr().add(24));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q3), vscale), x3, acc);

    hsum256_ps(acc)
}

#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn bytes_from_nibbles_32(qs_ptr: *const u8) -> std::arch::x86_64::__m256i {
    use std::arch::x86_64::*;
    let tmp = _mm_loadu_si128(qs_ptr as *const __m128i);
    _mm256_and_si256(_mm256_set1_epi8(0x0f), _mm256_set_m128i(_mm_srli_epi16(tmp, 4), tmp))
}
#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn hsum_epi32_256(v: std::arch::x86_64::__m256i) -> i32 {
    use std::arch::x86_64::*;
    let s = _mm_add_epi32(_mm256_castsi256_si128(v), _mm256_extracti128_si256(v, 1));
    let s = _mm_add_epi32(s, _mm_shuffle_epi32(s, 0xEE));
    _mm_cvtsi128_si32(_mm_add_epi32(s, _mm_shuffle_epi32(s, 0x01)))
}
#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_q8_avx2(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let w = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    if is_x86_feature_detected!("avxvnni") {
        let ax = _mm256_sign_epi8(qx, qx);
        let sy = _mm256_sign_epi8(qy, qx);
        let isum = hsum_epi32_256(_mm256_dpbusd_avx_epi32(_mm256_setzero_si256(), ax, sy));
        return isum as f32 * w;
    }
    if is_x86_feature_detected!("avx512vnni") && is_x86_feature_detected!("avx512vl") {
        let ax = _mm256_sign_epi8(qx, qx);
        let sy = _mm256_sign_epi8(qy, qx);
        let isum = hsum_epi32_256(_mm256_dpbusd_epi32(_mm256_setzero_si256(), ax, sy));
        return isum as f32 * w;
    }
    let q = mul_sum_i8_pairs_float_avx2(qx, qy);
    hsum256_ps(_mm256_mul_ps(_mm256_set1_ps(w), q))
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_k_block_dot_f32_avx2(block: &[u8], x: &[f32]) -> f32 {
    use std::arch::x86_64::*;
    let d = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let dmin = f16_to_f32(u16::from_le_bytes([block[2], block[3]]));
    let (sc, m) = unpack_q4k_scales(&block[4..16]);
    let qs = block.as_ptr().add(16);
    let mut sum = 0.0f32;
    for sub in 0..8 {
        let scale = d * sc[sub] as f32;
        let minv = dmin * m[sub] as f32;
        let g = sub / 2;
        let plane = sub % 2;
        let qs_off = g * 32;
        let shift = plane * 4;
        let x_off = sub * 32;
        let mut local = _mm256_setzero_ps();
        let mut min_acc = _mm256_setzero_ps();
        // 32 quants → 4× AVX2
        for t in 0..4 {
            let base = t * 8;
            let mut qbuf = [0i32; 8];
            for j in 0..8 {
                let byte = *qs.add(qs_off + base + j);
                qbuf[j] = ((byte >> shift) & 0x0F) as i32;
            }
            let vq = _mm256_cvtepi32_ps(_mm256_loadu_si256(qbuf.as_ptr() as *const __m256i));
            let vx = _mm256_loadu_ps(x.as_ptr().add(x_off + base));
            local = _mm256_fmadd_ps(vq, vx, local);
            min_acc = _mm256_add_ps(min_acc, vx);
        }
        sum += scale * hsum256_ps(local) - minv * hsum256_ps(min_acc);
    }
    sum
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pack_q4_0_block(scale: f32, nibbles: &[u8; 16]) -> [u8; Q4_0_BYTES] {
        let mut b = [0u8; Q4_0_BYTES];
        // crude f32→f16 for 0.25 = 0x3400
        let bits = if (scale - 0.25).abs() < 1e-6 {
            0x3400u16
        } else if (scale - 1.0).abs() < 1e-6 {
            0x3c00u16
        } else {
            // fallback: store via bit cast approx using f16_to_f32 inverse for common test scales
            0x3c00u16
        };
        b[0] = (bits & 0xff) as u8;
        b[1] = (bits >> 8) as u8;
        b[2..18].copy_from_slice(nibbles);
        b
    }

    #[test]
    fn q4_0_f32_simd_matches_scalar() {
        let mut nib = [0u8; 16];
        for j in 0..16 {
            nib[j] = ((j as u8) & 0x0f) | ((((j + 5) as u8) & 0x0f) << 4);
        }
        let block = pack_q4_0_block(0.25, &nib);
        let x: Vec<f32> = (0..32).map(|i| (i as f32) * 0.07 - 1.1).collect();
        let a = q4_0_block_dot_f32_scalar(&block, &x);
        let b = q4_0_block_dot_f32(&block, &x);
        assert!((a - b).abs() < 1e-4, "f32 path {a} vs {b}");
    }

    #[test]
    fn q4_0_q8_simd_matches_scalar() {
        let mut nib = [0u8; 16];
        for j in 0..16 {
            nib[j] = ((j as u8) & 0x0f) | ((((j + 3) as u8) & 0x0f) << 4);
        }
        let block = pack_q4_0_block(0.25, &nib);
        let x: Vec<f32> = (0..32).map(|i| (i as f32) * 0.03 - 0.4).collect();
        let act = ActQ8::quantize(&x);
        let qs = &act.qs[..32];
        let a = q4_0_block_dot_q8_scalar(&block, qs, act.scales[0]);
        let b = q4_0_block_dot_q8(&block, qs, act.scales[0]);
        assert!((a - b).abs() < 1e-3, "q8 path {a} vs {b}");
    }

    #[test]
    fn q4_0_rows4_matches_single_row() {
        let in_dim = 128usize;
        let n_blocks = in_dim / 32;
        let rb = n_blocks * Q4_0_BYTES;
        let mut packed = vec![0u8; 4 * rb];
        for row in 0..4 {
            for b in 0..n_blocks {
                let off = row * rb + b * Q4_0_BYTES;
                packed[off] = 0x00;
                packed[off + 1] = 0x34;
                for j in 0..16 {
                    packed[off + 2 + j] = ((j + row) as u8 & 0x0f) | ((((j + 3) as u8) & 0x0f) << 4);
                }
            }
        }
        let x: Vec<f32> = (0..in_dim).map(|i| (i as f32) * 0.01 - 0.3).collect();
        let batch = gemv_q4_0_rows4_f32(&packed, rb, 0, &x, in_dim);
        for r in 0..4 {
            let single = gemv_q4_0_row_f32(&packed[r * rb..(r + 1) * rb], &x, in_dim);
            assert!((batch[r] - single).abs() < 1e-4, "row {r}: batch={} single={}", batch[r], single);
        }
    }

    #[test]
    fn q4_0_gate_up_rows4_matches_single_row() {
        let in_dim = 128usize;
        let n_blocks = in_dim / 32;
        let rb = n_blocks * Q4_0_BYTES;
        let mut gate = vec![0u8; 4 * rb];
        let mut up = vec![0u8; 4 * rb];
        for row in 0..4 {
            for b in 0..n_blocks {
                let off = row * rb + b * Q4_0_BYTES;
                gate[off] = 0x00;
                gate[off + 1] = 0x34;
                up[off] = 0x00;
                up[off + 1] = 0x38;
                for j in 0..16 {
                    gate[off + 2 + j] = ((j + row) as u8 & 0x0f) | ((((j + 5) as u8) & 0x0f) << 4);
                    up[off + 2 + j] = ((j + row + 2) as u8 & 0x0f) | ((((j + 7) as u8) & 0x0f) << 4);
                }
            }
        }
        let x: Vec<f32> = (0..in_dim).map(|i| (i as f32) * 0.01 - 0.3).collect();
        let (g_batch, u_batch) = gemv_q4_0_gate_up_rows4_f32(&gate, &up, rb, 0, &x, in_dim);
        for r in 0..4 {
            let (g_single, u_single) = gemv_q4_0_gate_up_row_f32(
                &gate[r * rb..(r + 1) * rb],
                &up[r * rb..(r + 1) * rb],
                &x,
                in_dim,
            );
            assert!((g_batch[r] - g_single).abs() < 1e-4, "gate row {r}");
            assert!((u_batch[r] - u_single).abs() < 1e-4, "up row {r}");
        }
    }

}
