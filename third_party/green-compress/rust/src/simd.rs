#[cfg(target_arch = "x86_64")]
use std::arch::x86_64::*;

/// Portable scalar reference. Used on CPUs without AVX2/FMA (old x86, ARM, etc.)
/// and as the correctness oracle for the SIMD paths (see tests).
pub(crate) fn saxpy_accumulate_scalar(x: f32, weights: &[f32], out: &mut [f32]) {
    let count = weights.len();
    let mut j = 0;
    while j + 4 <= count {
        out[j] += x * weights[j];
        out[j + 1] += x * weights[j + 1];
        out[j + 2] += x * weights[j + 2];
        out[j + 3] += x * weights[j + 3];
        j += 4;
    }
    while j < count {
        out[j] += x * weights[j];
        j += 1;
    }
}

pub fn saxpy_accumulate(x: f32, weights: &[f32], out: &mut [f32]) {
    debug_assert_eq!(weights.len(), out.len());

    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                saxpy_accumulate_avx2(x, weights, out);
            }
            return;
        }
    }

    saxpy_accumulate_scalar(x, weights, out);
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn saxpy_accumulate_avx2(x: f32, weights: &[f32], out: &mut [f32]) {
    let count = weights.len();
    let vx = _mm256_set1_ps(x);
    let mut j = 0;
    while j + 8 <= count {
        let vw = _mm256_loadu_ps(weights.as_ptr().add(j));
        let vo = _mm256_loadu_ps(out.as_ptr().add(j));
        _mm256_storeu_ps(out.as_mut_ptr().add(j), _mm256_fmadd_ps(vx, vw, vo));
        j += 8;
    }
    while j < count {
        *out.get_unchecked_mut(j) += x * *weights.get_unchecked(j);
        j += 1;
    }
}

pub(crate) fn dequantize_q8_block_scalar(scale: f32, packed: &[i8], out: &mut [f32]) {
    for i in 0..packed.len() {
        out[i] = packed[i] as f32 * scale;
    }
}

pub fn dequantize_q8_block(scale: f32, packed: &[i8], out: &mut [f32]) {
    debug_assert_eq!(packed.len(), out.len());

    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                dequantize_q8_block_avx2(scale, packed, out);
            }
            return;
        }
    }

    dequantize_q8_block_scalar(scale, packed, out);
}

pub(crate) fn accumulate_q8_block_scalar(xs: f32, packed: &[i8], out: &mut [f32]) {
    for i in 0..packed.len() {
        out[i] += xs * packed[i] as f32;
    }
}

pub fn accumulate_q8_block(xs: f32, packed: &[i8], out: &mut [f32]) {
    debug_assert_eq!(packed.len(), out.len());

    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                accumulate_q8_block_avx2(xs, packed, out);
            }
            return;
        }
    }

    accumulate_q8_block_scalar(xs, packed, out);
}

/// Runtime probe for AVX-VNNI (`_mm256_dpbusd_epi32` on Raptor Lake+).
pub fn avx_vnni_available() -> bool {
    #[cfg(target_arch = "x86_64")]
    {
        is_x86_feature_detected!("avxvnni")
    }
    #[cfg(not(target_arch = "x86_64"))]
    {
        false
    }
}

/// Int8 weights × uint8 activations blocked dot (AVX-VNNI). Used when activations are pre-quantized.
#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "avxvnni")]
pub unsafe fn dot_i8_u8_vnni(weights: &[i8], activations: &[u8], out: &mut [f32], scale: f32) {
    let mut acc = _mm256_setzero_si256();
    let mut i = 0;
    while i + 32 <= weights.len().min(activations.len()) {
        let w = _mm256_loadu_si256(weights.as_ptr().add(i) as *const __m256i);
        let a = _mm256_loadu_si256(activations.as_ptr().add(i) as *const __m256i);
        acc = _mm256_dpbusd_epi32(acc, a, w);
        i += 32;
    }
    let mut tmp = [0i32; 8];
    _mm256_storeu_si256(tmp.as_mut_ptr() as *mut __m256i, acc);
    let sum: i32 = tmp.iter().sum();
    if !out.is_empty() {
        out[0] += sum as f32 * scale;
    }
    while i < weights.len().min(activations.len()) {
        out[0] += weights[i] as f32 * activations[i] as f32 * scale;
        i += 1;
    }
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn dequantize_q8_block_avx2(scale: f32, packed: &[i8], out: &mut [f32]) {
    let vscale = _mm256_set1_ps(scale);
    let count = packed.len();
    let mut j = 0;
    while j + 8 <= count {
        let bytes8 = _mm_loadl_epi64(packed.as_ptr().add(j) as *const __m128i);
        let i32v = _mm256_cvtepi8_epi32(bytes8);
        let wf = _mm256_cvtepi32_ps(i32v);
        _mm256_storeu_ps(out.as_mut_ptr().add(j), _mm256_mul_ps(wf, vscale));
        j += 8;
    }
    while j < count {
        *out.get_unchecked_mut(j) = *packed.get_unchecked(j) as f32 * scale;
        j += 1;
    }
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn accumulate_q8_block_avx2(xs: f32, packed: &[i8], out: &mut [f32]) {
    let vxs = _mm256_set1_ps(xs);
    let count = packed.len();
    let mut j = 0;
    while j + 32 <= count {
        for q in 0..4 {
            let off = j + q * 8;
            let bytes8 = _mm_loadl_epi64(packed.as_ptr().add(off) as *const __m128i);
            let i32v = _mm256_cvtepi8_epi32(bytes8);
            let wf = _mm256_cvtepi32_ps(i32v);
            let vo = _mm256_loadu_ps(out.as_ptr().add(off));
            _mm256_storeu_ps(out.as_mut_ptr().add(off), _mm256_fmadd_ps(vxs, wf, vo));
        }
        j += 32;
    }
    while j + 8 <= count {
        let bytes8 = _mm_loadl_epi64(packed.as_ptr().add(j) as *const __m128i);
        let i32v = _mm256_cvtepi8_epi32(bytes8);
        let wf = _mm256_cvtepi32_ps(i32v);
        let vo = _mm256_loadu_ps(out.as_ptr().add(j));
        _mm256_storeu_ps(out.as_mut_ptr().add(j), _mm256_fmadd_ps(vxs, wf, vo));
        j += 8;
    }
    while j < count {
        *out.get_unchecked_mut(j) += xs * *packed.get_unchecked(j) as f32;
        j += 1;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The dispatched fns pick AVX2/FMA when present; on this CPU that exercises the
    // SIMD path. These assert the SIMD result matches the portable scalar reference,
    // so the fallback used on old / non-x86 CPUs is provably equivalent.
    fn max_abs_diff(a: &[f32], b: &[f32]) -> f32 {
        a.iter().zip(b).map(|(x, y)| (x - y).abs()).fold(0.0, f32::max)
    }

    #[test]
    fn saxpy_simd_matches_scalar() {
        for len in [0usize, 1, 3, 7, 8, 15, 31, 32, 33, 100] {
            let w: Vec<f32> = (0..len).map(|i| (i as f32 - 13.0) * 0.031).collect();
            let base: Vec<f32> = (0..len).map(|i| (i as f32) * 0.017).collect();
            let (mut a, mut b) = (base.clone(), base.clone());
            saxpy_accumulate(1.7, &w, &mut a);
            saxpy_accumulate_scalar(1.7, &w, &mut b);
            assert!(max_abs_diff(&a, &b) < 1e-5, "saxpy len {len}");
        }
    }

    #[test]
    fn q8_block_simd_matches_scalar() {
        for len in [0usize, 1, 7, 8, 32, 40, 64, 65] {
            let packed: Vec<i8> = (0..len).map(|i| ((i as i32 % 255) - 127) as i8).collect();
            // dequantize
            let (mut a, mut b) = (vec![0.0f32; len], vec![0.0f32; len]);
            dequantize_q8_block(0.021, &packed, &mut a);
            dequantize_q8_block_scalar(0.021, &packed, &mut b);
            assert!(max_abs_diff(&a, &b) < 1e-6, "dequant len {len}");
            // accumulate
            let base: Vec<f32> = (0..len).map(|i| (i as f32) * 0.003).collect();
            let (mut c, mut d) = (base.clone(), base.clone());
            accumulate_q8_block(0.9, &packed, &mut c);
            accumulate_q8_block_scalar(0.9, &packed, &mut d);
            assert!(max_abs_diff(&c, &d) < 1e-5, "accumulate len {len}");
        }
    }
}
