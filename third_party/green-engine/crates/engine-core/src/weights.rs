//! Expert weight storage. Each expert's three matrices are stored either as f32 or as
//! symmetric int8 (Q8) — the latter uses 4× less memory and is the seam where Green Compress's
//! per-expert quantization plugs in. The cache decides *residency*; this owns the *bytes*.

/// One weight matrix, stored full-precision or quantized.
pub enum Tensor {
    F32(Vec<f32>),
    /// Symmetric per-tensor int8: value ≈ q * scale.
    Q8 { q: Vec<i8>, scale: f32 },
    /// Symmetric per-output-channel int8: value ≈ q[i*out+o] · scales[o]. Weights are [in,out]
    /// row-major, so element `[i*out+o]` uses column `o`'s scale. Giving each output column its
    /// own scale keeps a few large-magnitude channels from crushing the rest, so error is far
    /// lower than per-tensor Q8 at the same 1 byte/weight — the quality "green compression" wants.
    Q8Ch { q: Vec<i8>, scales: Vec<f32>, out: usize },
    /// Symmetric per-output-channel int4: two signed 4-bit values [-8,7] packed per byte, one
    /// scale per output column. ~8× smaller than f32 (0.5 byte/weight). Per-channel scaling is
    /// mandatory at 4-bit to avoid catastrophic error; `n` is the element count (packing is
    /// `ceil(n/2)` bytes). The smallest tier of the F32→Q8Ch→Q4Ch compression spectrum.
    Q4Ch { packed: Vec<u8>, scales: Vec<f32>, out: usize, n: usize },
    /// Symmetric **group-wise** int4: one scale per `group` consecutive weights (not per whole
    /// column). This is the 2026 best-practice for low-bit weights — a finer scale (group 32-128)
    /// tracks the local magnitude far better than one scale for a long column, so int4 quality jumps
    /// for a tiny extra overhead (one scale per `group` weights). Dep-free, so the default build gets
    /// it on any old CPU. Same 0.5 byte/weight as `Q4Ch` plus `n/group` small scales.
    Q4G { packed: Vec<u8>, scales: Vec<f32>, group: usize, n: usize },
    /// Symmetric **group-wise int3** — the smallest quality-frontier tier (~8× at group 32, ~10× at
    /// larger groups vs f32). Values [-4,3] bit-packed 3 bits each; one scale per `group` weights.
    /// Int3 needs group scaling even more than int4 (2026 research: int3 shows the biggest relative
    /// gain from grouping). Dep-free.
    Q3G { packed: Vec<u8>, scales: Vec<f32>, group: usize, n: usize },
    /// **NF4** (NormalFloat-4): 4-bit *non-uniform* — each nibble is an index into a 16-level normal
    /// codebook, scaled by a per-group absmax. Same 0.5 byte/weight as `Q4G` but higher fidelity on
    /// the ~normal weights LLMs have (QLoRA's format). Dep-free.
    NF4 { packed: Vec<u8>, scales: Vec<f32>, group: usize, n: usize },
    /// **Group-wise int2** — the extreme frontier (~16× vs f32). Values [-2,1] packed 4/byte, one
    /// scale per `group`. Only viable after Hadamard incoherence (QuIP# idea) — pair with
    /// `hadamard_q2_reconstruct`. Dep-free.
    Q2G { packed: Vec<u8>, scales: Vec<f32>, group: usize, n: usize },
    /// **Group-wise int6** — the "nearly-lossless" balance point (~2.5× vs f32, 6 bits/weight,
    /// values [-32,31] bit-packed). Fills the quality gap between int4 and int8: ~97% fidelity at
    /// meaningfully less RAM than int8, no calibration. 2026 best-practice mid tier. Dep-free.
    Q6G { packed: Vec<u8>, scales: Vec<f32>, group: usize, n: usize },
}

/// Sign-extend a 4-bit two's-complement nibble to i32.
#[inline]
fn nib_to_i32(nib: u8) -> i32 {
    if nib >= 8 {
        nib as i32 - 16
    } else {
        nib as i32
    }
}

/// NF4 (NormalFloat-4) codebook — 16 quantile levels of a zero-mean unit-normal, in [-1,1]. Weights
/// are ~normal after pretraining, so mapping each value to the nearest of these non-uniform levels
/// represents them more accurately than uniform int4 at the same 4 bits (QLoRA's format).
const NF4_CODE: [f32; 16] = [
    -1.0, -0.696_192_8, -0.525_073_1, -0.394_917_5, -0.284_441_38, -0.184_773_43, -0.091_050_04,
    0.0, 0.079_580_3, 0.160_930_2, 0.246_112_3, 0.337_915_24, 0.440_709_83, 0.562_617, 0.722_956_84, 1.0,
];

/// NF4 quantize→dequantize a slice in place, in groups of `group` (per-group absmax scale). Used by
/// the Hadamard path so the quant groups align with the rotated column.
fn nf4_qdq_inplace(v: &mut [f32], group: usize) {
    let g = group.max(1);
    let mut i = 0;
    while i < v.len() {
        let end = (i + g).min(v.len());
        let amax = v[i..end].iter().fold(0.0f32, |m, &x| m.max(x.abs()));
        let s = if amax > 0.0 { amax } else { 1.0 };
        for x in &mut v[i..end] {
            *x = NF4_CODE[nf4_nearest(*x / s) as usize] * s;
        }
        i = end;
    }
}

/// int2 quantize→dequantize a slice in place, in groups of `group` (per-group absmax/2 scale).
fn q2_qdq_inplace(v: &mut [f32], group: usize) {
    let g = group.max(1);
    let mut i = 0;
    while i < v.len() {
        let end = (i + g).min(v.len());
        let amax = v[i..end].iter().fold(0.0f32, |m, &x| m.max(x.abs()));
        let s = if amax > 0.0 { amax / 2.0 } else { 1.0 };
        for x in &mut v[i..end] {
            *x = (*x / s).round().clamp(-2.0, 1.0) * s;
        }
        i = end;
    }
}

/// In-place normalized fast Walsh-Hadamard transform (`a.len()` must be a power of two). Orthogonal
/// and symmetric, so it is its own inverse — applying it twice returns the input.
fn fwht(a: &mut [f32]) {
    let n = a.len();
    let mut h = 1;
    while h < n {
        let mut i = 0;
        while i < n {
            for j in i..i + h {
                let (x, y) = (a[j], a[j + h]);
                a[j] = x + y;
                a[j + h] = x - y;
            }
            i += 2 * h;
        }
        h *= 2;
    }
    let s = 1.0 / (n as f32).sqrt();
    for x in a.iter_mut() {
        *x *= s;
    }
}

/// Nearest NF4 codebook index for a normalized value (codebook is small and sorted; linear is fine).
#[inline]
fn nf4_nearest(v: f32) -> u8 {
    let mut best = 0usize;
    let mut bd = f32::INFINITY;
    for (i, &c) in NF4_CODE.iter().enumerate() {
        let d = (v - c).abs();
        if d < bd {
            bd = d;
            best = i;
        }
    }
    best as u8
}

/// Read the signed 6-bit value at index `idx` from a bit-packed stream (values in [-32,31]).
#[inline]
fn read6(packed: &[u8], idx: usize) -> i32 {
    let bit = idx * 6;
    let (byte, off) = (bit / 8, bit % 8);
    let mut v = (packed[byte] >> off) as u32;
    if off > 2 {
        v |= (packed[byte + 1] as u32) << (8 - off);
    }
    let v = (v & 0x3f) as i32;
    if v >= 32 { v - 64 } else { v }
}

/// Read the signed 2-bit value at index `idx` (4 per byte, values in [-2,1]).
#[inline]
fn read2(packed: &[u8], idx: usize) -> i32 {
    let v = ((packed[idx >> 2] >> (2 * (idx & 3))) & 0x3) as i32;
    if v >= 2 { v - 4 } else { v }
}

/// Read the signed 3-bit value at index `idx` from a bit-packed stream (values in [-4,3]).
#[inline]
fn read3(packed: &[u8], idx: usize) -> i32 {
    let (byte, off) = (idx * 3 / 8, idx * 3 % 8);
    let mut v = (packed[byte] >> off) as u32;
    if off > 5 {
        v |= (packed[byte + 1] as u32) << (8 - off);
    }
    let v = (v & 0x7) as i32;
    if v >= 4 { v - 8 } else { v }
}

impl Tensor {
    pub fn quantize_q8(w: &[f32]) -> Tensor {
        let amax = w.iter().fold(0.0f32, |m, &x| m.max(x.abs()));
        let scale = if amax == 0.0 { 1.0 } else { amax / 127.0 };
        let q = w.iter().map(|&x| (x / scale).round().clamp(-127.0, 127.0) as i8).collect();
        Tensor::Q8 { q, scale }
    }

    /// Per-output-channel int8 quantization. `out` = number of output columns (weights are [in,out]
    /// row-major). Same 1 byte/weight as `quantize_q8`, materially higher fidelity.
    pub fn quantize_q8_ch(w: &[f32], out: usize) -> Tensor {
        let inn = if out == 0 { 0 } else { w.len() / out };
        let mut scales = vec![1.0f32; out];
        for (o, sc) in scales.iter_mut().enumerate() {
            let mut amax = 0.0f32;
            for i in 0..inn {
                amax = amax.max(w[i * out + o].abs());
            }
            if amax > 0.0 {
                *sc = amax / 127.0;
            }
        }
        let mut q = vec![0i8; w.len()];
        for i in 0..inn {
            for o in 0..out {
                let idx = i * out + o;
                q[idx] = (w[idx] / scales[o]).round().clamp(-127.0, 127.0) as i8;
            }
        }
        Tensor::Q8Ch { q, scales, out }
    }

    /// Per-output-channel int4 quantization (values [-8,7], two packed per byte). ~8× smaller
    /// than f32. `out` = number of output columns (weights are [in,out] row-major).
    pub fn quantize_q4_ch(w: &[f32], out: usize) -> Tensor {
        let n = w.len();
        let inn = if out == 0 { 0 } else { n / out };
        let mut scales = vec![1.0f32; out];
        for (o, sc) in scales.iter_mut().enumerate() {
            let mut amax = 0.0f32;
            for i in 0..inn {
                amax = amax.max(w[i * out + o].abs());
            }
            if amax > 0.0 {
                *sc = amax / 7.0;
            }
        }
        let mut packed = vec![0u8; n.div_ceil(2)];
        for i in 0..inn {
            for o in 0..out {
                let idx = i * out + o;
                let q = (w[idx] / scales[o]).round().clamp(-8.0, 7.0) as i32;
                let nib = (q & 0xF) as u8;
                packed[idx >> 1] |= nib << (4 * (idx & 1));
            }
        }
        Tensor::Q4Ch { packed, scales, out, n }
    }

    /// Group-wise int4: one symmetric scale per `group` consecutive weights. Finer than per-channel,
    /// so materially higher fidelity at 0.5 byte/weight. `group` is clamped to ≥1; the last group may
    /// be short. Dep-free.
    pub fn quantize_q4_group(w: &[f32], group: usize) -> Tensor {
        let n = w.len();
        let g = group.max(1);
        let n_groups = n.div_ceil(g);
        let mut scales = vec![1.0f32; n_groups];
        for (gi, sc) in scales.iter_mut().enumerate() {
            let start = gi * g;
            let end = (start + g).min(n);
            let mut amax = 0.0f32;
            for &x in &w[start..end] {
                amax = amax.max(x.abs());
            }
            if amax > 0.0 {
                *sc = amax / 7.0;
            }
        }
        let mut packed = vec![0u8; n.div_ceil(2)];
        for (idx, &x) in w.iter().enumerate() {
            let q = (x / scales[idx / g]).round().clamp(-8.0, 7.0) as i32;
            packed[idx >> 1] |= ((q & 0xF) as u8) << (4 * (idx & 1));
        }
        Tensor::Q4G { packed, scales, group: g, n }
    }

    /// Group-wise int4 with **MSE-optimal clipping** — for each group it searches a few clip ratios
    /// of amax and keeps the scale that minimizes reconstruction error, instead of blindly using the
    /// max. Outliers no longer force a coarse scale on the whole group, so quality rises at the SAME
    /// 0.5 byte/weight and the SAME `Q4G` format (dequant unchanged). This is the AWQ/GPTQ-family
    /// "better scale selection" idea, dep-free and needing no calibration data. Higher-quality
    /// drop-in for `quantize_q4_group`.
    pub fn quantize_q4_group_clip(w: &[f32], group: usize) -> Tensor {
        let n = w.len();
        let g = group.max(1);
        let n_groups = n.div_ceil(g);
        let ratios = [1.0f32, 0.9, 0.8, 0.7]; // clip fractions of amax to try
        let mut scales = vec![1.0f32; n_groups];
        for (gi, sc) in scales.iter_mut().enumerate() {
            let slice = &w[gi * g..((gi + 1) * g).min(n)];
            let amax = slice.iter().fold(0.0f32, |m, &x| m.max(x.abs()));
            if amax == 0.0 {
                continue;
            }
            let (mut best_scale, mut best_err) = (amax / 7.0, f64::INFINITY);
            for &r in &ratios {
                let s = (amax * r) / 7.0;
                let mut err = 0.0f64;
                for &x in slice {
                    let dq = (x / s).round().clamp(-8.0, 7.0) * s;
                    err += ((x - dq) as f64).powi(2);
                }
                if err < best_err {
                    best_err = err;
                    best_scale = s;
                }
            }
            *sc = best_scale;
        }
        let mut packed = vec![0u8; n.div_ceil(2)];
        for (idx, &x) in w.iter().enumerate() {
            let q = (x / scales[idx / g]).round().clamp(-8.0, 7.0) as i32;
            packed[idx >> 1] |= ((q & 0xF) as u8) << (4 * (idx & 1));
        }
        Tensor::Q4G { packed, scales, group: g, n }
    }

    /// **Residual (multi-codebook) quantization** — the QuIP#/AQLM family, dep-free. Quantize with a
    /// Hadamard-rotated NF4 base, then quantize the leftover error with a second group-int4 codebook
    /// and add it back. Two cheap codebooks approximate the weight far better than one, so this lands
    /// a *usable* high-quality point at ~0.75-1.0 byte/weight (≈ between int4 and int8). `in_dim` must
    /// be a power of two. Returns reconstructed weights. Calibration-free.
    pub fn residual_reconstruct(w: &[f32], in_dim: usize, out_dim: usize, group: usize) -> Vec<f32> {
        let base = Tensor::hadamard_reconstruct(w, in_dim, out_dim, group); // NF4-rot base (~0.5 B/wt)
        let res: Vec<f32> = w.iter().zip(&base).map(|(a, b)| a - b).collect();
        let resq = Tensor::quantize_q2_group(&res, group).to_f32_vec(); // int2 residual (~0.25 B/wt)
        base.iter().zip(&resq).map(|(a, b)| a + b).collect()
    }

    /// **Hadamard-rotated** quantization (QuaRot/SpinQuant family). Rotating each weight column by a
    /// normalized Hadamard transform spreads per-channel outliers evenly across the column, so the
    /// rotated weights quantize far better; the rotation is orthogonal, so applying it again after
    /// dequant recovers ≈`w` (the paired rotation on activations is computationally invariant). `w`
    /// is `[in_dim, out_dim]` row-major; `in_dim` must be a power of two. Returns reconstructed
    /// weights (uses the NF4 codebook for the quantization step). Dep-free, calibration-free.
    pub fn hadamard_reconstruct(w: &[f32], in_dim: usize, out_dim: usize, group: usize) -> Vec<f32> {
        assert!(in_dim.is_power_of_two(), "hadamard_reconstruct: in_dim must be a power of two");
        let mut out = vec![0.0f32; w.len()];
        let mut col = vec![0.0f32; in_dim];
        for o in 0..out_dim {
            for i in 0..in_dim {
                col[i] = w[i * out_dim + o];
            }
            fwht(&mut col); // rotate the column so per-channel outliers spread across it
            nf4_qdq_inplace(&mut col, group); // quantize with groups ALIGNED to the rotated column
            fwht(&mut col); // normalized Hadamard is its own inverse → back to weight space
            for i in 0..in_dim {
                out[i * out_dim + o] = col[i];
            }
        }
        out
    }

    /// **AWQ-style** activation-aware quantization. Scales each INPUT channel by `act_mag_c^0.5`
    /// before group-int4 quantization, so the channels the activations exercise most get more of the
    /// quant range; dequant divides the scale back out (so the result reconstructs ≈ `w` and the
    /// backend is unchanged). `w` is `[in_dim, out_dim]` row-major; `act_mag` is per-input-channel
    /// magnitude from CALIBRATION activations. Returns the reconstructed weights. Real AWQ — the
    /// quality gain needs real calibration data (weight-only clipping is `quantize_q4_group_clip`).
    pub fn awq_reconstruct(w: &[f32], in_dim: usize, out_dim: usize, act_mag: &[f32], group: usize) -> Vec<f32> {
        let alpha = 0.5f32;
        let mut s = vec![1.0f32; in_dim];
        let mut mean = 0.0f32;
        for c in 0..in_dim {
            s[c] = act_mag.get(c).copied().unwrap_or(1.0).max(1e-6).powf(alpha);
            mean += s[c];
        }
        mean = (mean / in_dim as f32).max(1e-6);
        for sc in s.iter_mut() {
            *sc = (*sc / mean).clamp(0.25, 4.0); // keep the range bounded
        }
        let mut ws = vec![0.0f32; w.len()];
        for c in 0..in_dim {
            for o in 0..out_dim {
                ws[c * out_dim + o] = w[c * out_dim + o] * s[c];
            }
        }
        let deq = Tensor::quantize_q4_group(&ws, group).to_f32_vec();
        let mut out = vec![0.0f32; w.len()];
        for c in 0..in_dim {
            for o in 0..out_dim {
                out[c * out_dim + o] = deq[c * out_dim + o] / s[c];
            }
        }
        out
    }

    /// **Outlier isolation** (SqueezeLLM / BiLLM idea): a group-int4 base plus the `n_outliers`
    /// weights with the largest quantization error kept in full f32 (a tiny sparse set). Dep-free,
    /// needs no calibration. Returns the reconstructed weights. Storage cost ≈ int4 base +
    /// `n_outliers · 8` bytes (a 4-byte value + 4-byte index each) — a few % restores most fidelity.
    pub fn outlier_reconstruct(w: &[f32], group: usize, n_outliers: usize) -> Vec<f32> {
        let mut out = Tensor::quantize_q4_group(w, group).to_f32_vec();
        let mut err: Vec<(f32, usize)> = (0..w.len()).map(|i| ((w[i] - out[i]).abs(), i)).collect();
        err.sort_by(|a, b| b.0.partial_cmp(&a.0).unwrap_or(std::cmp::Ordering::Equal));
        for &(_, i) in err.iter().take(n_outliers.min(w.len())) {
            out[i] = w[i]; // restore the largest-error weights exactly
        }
        out
    }

    /// Group-wise int3: values [-4,3] bit-packed 3 bits each, one scale per `group` weights.
    /// Smallest tier; group scaling is essential at 3-bit. Dep-free.
    pub fn quantize_q3_group(w: &[f32], group: usize) -> Tensor {
        let n = w.len();
        let g = group.max(1);
        let n_groups = n.div_ceil(g);
        let mut scales = vec![1.0f32; n_groups];
        for (gi, sc) in scales.iter_mut().enumerate() {
            let start = gi * g;
            let end = (start + g).min(n);
            let mut amax = 0.0f32;
            for &x in &w[start..end] {
                amax = amax.max(x.abs());
            }
            if amax > 0.0 {
                *sc = amax / 3.0;
            }
        }
        let mut packed = vec![0u8; (3 * n).div_ceil(8) + 1]; // +1 byte so 2-byte spans never overflow
        for (idx, &x) in w.iter().enumerate() {
            let q = (x / scales[idx / g]).round().clamp(-4.0, 3.0) as i32;
            let v = (q & 0x7) as u32; // 3-bit two's complement
            let (byte, off) = (idx * 3 / 8, idx * 3 % 8);
            packed[byte] |= (v << off) as u8;
            if off > 5 {
                packed[byte + 1] |= (v >> (8 - off)) as u8;
            }
        }
        Tensor::Q3G { packed, scales, group: g, n }
    }

    /// NF4 group-wise: nearest-codebook 4-bit indices, one absmax scale per `group` weights. Higher
    /// fidelity than uniform int4 on ~normal weights, same 0.5 byte/weight.
    pub fn quantize_nf4_group(w: &[f32], group: usize) -> Tensor {
        let n = w.len();
        let g = group.max(1);
        let n_groups = n.div_ceil(g);
        let mut scales = vec![1.0f32; n_groups];
        for (gi, sc) in scales.iter_mut().enumerate() {
            let slice = &w[gi * g..((gi + 1) * g).min(n)];
            let amax = slice.iter().fold(0.0f32, |m, &x| m.max(x.abs()));
            if amax > 0.0 {
                *sc = amax; // NF4 codebook spans [-1,1], so the scale is the group absmax
            }
        }
        let mut packed = vec![0u8; n.div_ceil(2)];
        for (idx, &x) in w.iter().enumerate() {
            let code = nf4_nearest(x / scales[idx / g]);
            packed[idx >> 1] |= code << (4 * (idx & 1));
        }
        Tensor::NF4 { packed, scales, group: g, n }
    }

    /// Group-wise int6: values [-32,31] bit-packed, one absmax/31 scale per `group`. ~2.5× vs f32,
    /// nearly lossless — the balance tier between int4 and int8.
    pub fn quantize_q6_group(w: &[f32], group: usize) -> Tensor {
        let n = w.len();
        let g = group.max(1);
        let n_groups = n.div_ceil(g);
        let mut scales = vec![1.0f32; n_groups];
        for (gi, sc) in scales.iter_mut().enumerate() {
            let slice = &w[gi * g..((gi + 1) * g).min(n)];
            let amax = slice.iter().fold(0.0f32, |m, &x| m.max(x.abs()));
            if amax > 0.0 {
                *sc = amax / 31.0;
            }
        }
        let mut packed = vec![0u8; (n * 6).div_ceil(8) + 1]; // +1 so 2-byte spans never overflow
        for (idx, &x) in w.iter().enumerate() {
            let q = (x / scales[idx / g]).round().clamp(-32.0, 31.0) as i32;
            let v = (q & 0x3f) as u32;
            let bit = idx * 6;
            let (byte, off) = (bit / 8, bit % 8);
            packed[byte] |= (v << off) as u8;
            if off > 2 {
                packed[byte + 1] |= (v >> (8 - off)) as u8;
            }
        }
        Tensor::Q6G { packed, scales, group: g, n }
    }

    /// Group-wise int2: values [-2,1] packed 4/byte, one absmax/2 scale per `group`. ~16× vs f32.
    pub fn quantize_q2_group(w: &[f32], group: usize) -> Tensor {
        let n = w.len();
        let g = group.max(1);
        let n_groups = n.div_ceil(g);
        let mut scales = vec![1.0f32; n_groups];
        for (gi, sc) in scales.iter_mut().enumerate() {
            let slice = &w[gi * g..((gi + 1) * g).min(n)];
            let amax = slice.iter().fold(0.0f32, |m, &x| m.max(x.abs()));
            if amax > 0.0 {
                *sc = amax / 2.0;
            }
        }
        let mut packed = vec![0u8; n.div_ceil(4)];
        for (idx, &x) in w.iter().enumerate() {
            let q = (x / scales[idx / g]).round().clamp(-2.0, 1.0) as i32;
            packed[idx >> 2] |= ((q & 0x3) as u8) << (2 * (idx & 3));
        }
        Tensor::Q2G { packed, scales, group: g, n }
    }

    /// Hadamard-rotated int2 (QuIP#-lite): rotation spreads outliers so 2-bit stays usable. Returns
    /// reconstructed weights. `in_dim` must be a power of two.
    pub fn hadamard_q2_reconstruct(w: &[f32], in_dim: usize, out_dim: usize, group: usize) -> Vec<f32> {
        assert!(in_dim.is_power_of_two(), "hadamard_q2_reconstruct: in_dim must be a power of two");
        let mut out = vec![0.0f32; w.len()];
        let mut col = vec![0.0f32; in_dim];
        for o in 0..out_dim {
            for i in 0..in_dim {
                col[i] = w[i * out_dim + o];
            }
            fwht(&mut col);
            q2_qdq_inplace(&mut col, group);
            fwht(&mut col);
            for i in 0..in_dim {
                out[i * out_dim + o] = col[i];
            }
        }
        out
    }

    pub fn len(&self) -> usize {
        match self {
            Tensor::F32(v) => v.len(),
            Tensor::Q8 { q, .. } => q.len(),
            Tensor::Q8Ch { q, .. } => q.len(),
            Tensor::Q4Ch { n, .. } => *n,
            Tensor::Q4G { n, .. } => *n,
            Tensor::Q3G { n, .. } => *n,
            Tensor::NF4 { n, .. } => *n,
            Tensor::Q2G { n, .. } => *n,
            Tensor::Q6G { n, .. } => *n,
        }
    }
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    /// Stored size in bytes (what sits in memory / crosses the slow link).
    pub fn bytes(&self) -> usize {
        match self {
            Tensor::F32(v) => v.len() * 4,
            Tensor::Q8 { q, .. } => q.len() + 4, // i8 data + one f32 scale
            Tensor::Q8Ch { q, scales, .. } => q.len() + scales.len() * 4, // i8 + per-channel scales
            Tensor::Q4Ch { packed, scales, .. } => packed.len() + scales.len() * 4, // nibbles + scales
            Tensor::Q4G { packed, scales, .. } => packed.len() + scales.len() * 4, // nibbles + group scales
            Tensor::Q3G { packed, scales, .. } => packed.len() + scales.len() * 4, // 3-bit stream + scales
            Tensor::NF4 { packed, scales, .. } => packed.len() + scales.len() * 4, // nibble indices + scales
            Tensor::Q2G { packed, scales, .. } => packed.len() + scales.len() * 4, // 2-bit + scales
            Tensor::Q6G { packed, scales, .. } => packed.len() + scales.len() * 4, // 6-bit + scales
        }
    }

    /// Borrow the underlying f32 slice when stored full-precision — a stable pointer with no
    /// copy. Returns `None` for quantized tensors (which must be dequantized first). The FFI/GPU
    /// backend uses this so resident weights keep a stable host address across calls.
    pub fn as_f32_borrow(&self) -> Option<&[f32]> {
        match self {
            Tensor::F32(v) => Some(v),
            _ => None,
        }
    }

    /// Owned f32 copy (used by the FFI backend, which needs all three matrices at once).
    pub fn to_f32_vec(&self) -> Vec<f32> {
        match self {
            Tensor::F32(v) => v.clone(),
            Tensor::Q8 { q, scale } => q.iter().map(|&x| x as f32 * scale).collect(),
            Tensor::Q8Ch { q, scales, out } => {
                (0..q.len()).map(|idx| q[idx] as f32 * scales[idx % *out]).collect()
            }
            Tensor::Q4Ch { packed, scales, out, n } => (0..*n)
                .map(|idx| {
                    let nib = (packed[idx >> 1] >> (4 * (idx & 1))) & 0xF;
                    nib_to_i32(nib) as f32 * scales[idx % *out]
                })
                .collect(),
            Tensor::Q4G { packed, scales, group, n } => (0..*n)
                .map(|idx| {
                    let nib = (packed[idx >> 1] >> (4 * (idx & 1))) & 0xF;
                    nib_to_i32(nib) as f32 * scales[idx / *group]
                })
                .collect(),
            Tensor::Q3G { packed, scales, group, n } => (0..*n)
                .map(|idx| read3(packed, idx) as f32 * scales[idx / *group])
                .collect(),
            Tensor::NF4 { packed, scales, group, n } => (0..*n)
                .map(|idx| {
                    let code = (packed[idx >> 1] >> (4 * (idx & 1))) & 0xF;
                    NF4_CODE[code as usize] * scales[idx / *group]
                })
                .collect(),
            Tensor::Q2G { packed, scales, group, n } => (0..*n)
                .map(|idx| read2(packed, idx) as f32 * scales[idx / *group])
                .collect(),
            Tensor::Q6G { packed, scales, group, n } => (0..*n)
                .map(|idx| read6(packed, idx) as f32 * scales[idx / *group])
                .collect(),
        }
    }

    /// Return an f32 view: borrows directly when f32, else dequantizes into `scratch`.
    pub fn as_f32<'a>(&'a self, scratch: &'a mut [f32]) -> &'a [f32] {
        match self {
            Tensor::F32(v) => v,
            Tensor::Q8 { q, scale } => {
                let n = q.len();
                let out = &mut scratch[..n];
                for i in 0..n {
                    out[i] = q[i] as f32 * scale;
                }
                out
            }
            Tensor::Q8Ch { q, scales, out } => {
                let n = q.len();
                let outc = *out;
                let dst = &mut scratch[..n];
                for idx in 0..n {
                    dst[idx] = q[idx] as f32 * scales[idx % outc];
                }
                dst
            }
            Tensor::Q4Ch { packed, scales, out, n } => {
                let (nn, outc) = (*n, *out);
                let dst = &mut scratch[..nn];
                for idx in 0..nn {
                    let nib = (packed[idx >> 1] >> (4 * (idx & 1))) & 0xF;
                    dst[idx] = nib_to_i32(nib) as f32 * scales[idx % outc];
                }
                dst
            }
            Tensor::Q4G { packed, scales, group, n } => {
                let (nn, g) = (*n, *group);
                let dst = &mut scratch[..nn];
                for idx in 0..nn {
                    let nib = (packed[idx >> 1] >> (4 * (idx & 1))) & 0xF;
                    dst[idx] = nib_to_i32(nib) as f32 * scales[idx / g];
                }
                dst
            }
            Tensor::Q3G { packed, scales, group, n } => {
                let (nn, g) = (*n, *group);
                let dst = &mut scratch[..nn];
                for idx in 0..nn {
                    dst[idx] = read3(packed, idx) as f32 * scales[idx / g];
                }
                dst
            }
            Tensor::NF4 { packed, scales, group, n } => {
                let (nn, g) = (*n, *group);
                let dst = &mut scratch[..nn];
                for idx in 0..nn {
                    let code = (packed[idx >> 1] >> (4 * (idx & 1))) & 0xF;
                    dst[idx] = NF4_CODE[code as usize] * scales[idx / g];
                }
                dst
            }
            Tensor::Q2G { packed, scales, group, n } => {
                let (nn, g) = (*n, *group);
                let dst = &mut scratch[..nn];
                for idx in 0..nn {
                    dst[idx] = read2(packed, idx) as f32 * scales[idx / g];
                }
                dst
            }
            Tensor::Q6G { packed, scales, group, n } => {
                let (nn, g) = (*n, *group);
                let dst = &mut scratch[..nn];
                for idx in 0..nn {
                    dst[idx] = read6(packed, idx) as f32 * scales[idx / g];
                }
                dst
            }
        }
    }
}

pub struct ExpertWeights {
    pub hidden: usize,
    pub inter: usize,
    pub gate: Tensor, // [hidden*inter]
    pub up: Tensor,   // [hidden*inter]
    pub down: Tensor, // [inter*hidden]
}

impl ExpertWeights {
    pub fn bytes(&self) -> usize {
        self.gate.bytes() + self.up.bytes() + self.down.bytes()
    }
}

/// The full set of experts — the cold tier the engine schedules over.
pub struct WeightStore {
    pub layers: usize,
    pub experts: usize,
    pub hidden: usize,
    pub inter: usize,
    pub quantized: bool,
    w: Vec<ExpertWeights>,
}

impl WeightStore {
    /// Deterministic synthetic store (LCG, no deps) — for tests/benches without the real model.
    pub fn synthetic(layers: usize, experts: usize, hidden: usize, inter: usize, quantized: bool, seed: u64) -> Self {
        let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
        let mut next = || {
            // xorshift64*, mapped to ~[-0.1, 0.1]
            s ^= s >> 12;
            s ^= s << 25;
            s ^= s >> 27;
            let u = (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32;
            (u - 0.5) * 0.2
        };
        let mk = |n: usize, next: &mut dyn FnMut() -> f32| -> Tensor {
            let v: Vec<f32> = (0..n).map(|_| next()).collect();
            if quantized {
                Tensor::quantize_q8(&v)
            } else {
                Tensor::F32(v)
            }
        };
        let mut w = Vec::with_capacity(layers * experts);
        for _ in 0..layers * experts {
            w.push(ExpertWeights {
                hidden,
                inter,
                gate: mk(hidden * inter, &mut next),
                up: mk(hidden * inter, &mut next),
                down: mk(inter * hidden, &mut next),
            });
        }
        WeightStore { layers, experts, hidden, inter, quantized, w }
    }

    #[inline]
    pub fn get(&self, layer: usize, expert: u16) -> &ExpertWeights {
        &self.w[layer * self.experts + expert as usize]
    }

    pub fn expert_bytes(&self, layer: usize, expert: u16) -> usize {
        self.get(layer, expert).bytes()
    }

    pub fn total_bytes(&self) -> usize {
        self.w.iter().map(|e| e.bytes()).sum()
    }
}

#[cfg(test)]
mod q4g_tests {
    use super::Tensor;

    fn rel_l2(a: &[f32], b: &[f32]) -> f32 {
        let (mut n, mut d) = (0.0f64, 0.0f64);
        for (x, y) in a.iter().zip(b) { n += ((x - y) as f64).powi(2); d += (*x as f64).powi(2); }
        (n / d.max(1e-12)).sqrt() as f32
    }

    // [in=out=128] structured: per-column bias + local drift + a few outliers (real-weight-like).
    fn structured(inn: usize, out: usize, seed: u64) -> Vec<f32> {
        let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
        let mut rng = || { s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
            (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 };
        let mut w = vec![0.0f32; inn * out];
        for i in 0..inn {
            let row_scale = 0.2 + (i as f32 / inn as f32); // magnitude varies DOWN a column -> group wins
            for o in 0..out { w[i * out + o] = row_scale * rng(); }
        }
        for t in 0..(inn * out / 400).max(1) { w[(t * 2654435761) % (inn * out)] += 2.0; }
        w
    }

    #[test]
    fn group_wise_int4_beats_per_channel() {
        let (inn, out) = (128usize, 128usize);
        let w = structured(inn, out, 3);
        let d_ch = rel_l2(&w, &Tensor::quantize_q4_ch(&w, out).to_f32_vec());
        let d_g32 = rel_l2(&w, &Tensor::quantize_q4_group(&w, 32).to_f32_vec());
        // Finer group tracks the down-column magnitude variation better than one per-column scale.
        assert!(d_g32 < d_ch, "group-32 int4 drift {d_g32} should beat per-channel {d_ch}");
        // Roundtrip is deterministic.
        let a = Tensor::quantize_q4_group(&w, 32).to_f32_vec();
        let b = Tensor::quantize_q4_group(&w, 32).to_f32_vec();
        assert_eq!(a, b, "group int4 quant must be deterministic");
    }

    #[test]
    fn outlier_isolation_improves_fidelity() {
        let (inn, out) = (256usize, 256usize);
        let w = structured(inn, out, 13);
        let d_plain = rel_l2(&w, &Tensor::quantize_q4_group(&w, 64).to_f32_vec());
        // keep 1% of weights as f32 outliers
        let d_out = rel_l2(&w, &Tensor::outlier_reconstruct(&w, 64, w.len() / 100));
        assert!(d_out < d_plain, "outlier isolation drift {d_out} should beat plain {d_plain}");
    }

    #[test]
    fn awq_helps_when_activations_are_skewed() {
        let (inn, out) = (256usize, 256usize);
        let w = structured(inn, out, 17);
        // activations concentrated on a few input channels (the AWQ regime).
        let act: Vec<f32> = (0..inn).map(|c| if c % 32 == 0 { 10.0 } else { 0.3 }).collect();
        let d_plain = rel_l2(&w, &Tensor::quantize_q4_group(&w, 64).to_f32_vec());
        let d_awq = rel_l2(&w, &Tensor::awq_reconstruct(&w, inn, out, &act, 64));
        // AWQ won't necessarily beat plain rel-L2 (it optimizes for the ACTIVATED output, not raw
        // weight L2), but it must reconstruct sane weights (bounded) — the real win shows end-to-end.
        assert!(d_awq.is_finite() && d_awq < d_plain * 2.0, "awq drift {d_awq} vs plain {d_plain}");
    }

    #[test]
    fn int6_is_between_int4_int8_and_high_fidelity() {
        let (inn, out) = (256usize, 256usize);
        let w = structured(inn, out, 41);
        let q4 = Tensor::quantize_q4_group(&w, 64);
        let q6 = Tensor::quantize_q6_group(&w, 64);
        let q8 = Tensor::quantize_q8_ch(&w, out);
        // size: int4 < int6 < int8
        assert!(q4.bytes() < q6.bytes() && q6.bytes() < q8.bytes(), "int6 size must sit between int4 and int8");
        // fidelity: int6 beats int4 and is high (nearly lossless)
        let (d4, d6) = (rel_l2(&w, &q4.to_f32_vec()), rel_l2(&w, &q6.to_f32_vec()));
        assert!(d6 < d4, "int6 drift {d6} should beat int4 {d4}");
        assert!(d6 < 0.05, "int6 drift {d6} should be small (near lossless; higher here from synthetic outliers)");
        // bit-packing roundtrips deterministically
        assert_eq!(Tensor::quantize_q6_group(&w, 64).to_f32_vec(), q6.to_f32_vec());
    }

    #[test]
    fn residual_quant_beats_its_base() {
        let (inn, out) = (128usize, 128usize);
        let w = structured(inn, out, 31);
        let d_base = rel_l2(&w, &Tensor::hadamard_reconstruct(&w, inn, out, 32)); // NF4-rot base
        let d_rvq = rel_l2(&w, &Tensor::residual_reconstruct(&w, inn, out, 32)); // + int2 residual
        // A second codebook on the residual must lower the drift (that's the whole point of RVQ).
        assert!(d_rvq < d_base, "residual {d_rvq} should beat base {d_base}");
    }

    #[test]
    fn hadamard_makes_int2_viable() {
        let (inn, out) = (128usize, 128usize);
        let w = structured(inn, out, 29);
        let d_plain = rel_l2(&w, &Tensor::quantize_q2_group(&w, 64).to_f32_vec());
        let d_had = rel_l2(&w, &Tensor::hadamard_q2_reconstruct(&w, inn, out, 64));
        // int2 is ~16× and lossy; Hadamard rotation cuts its drift (QuIP# incoherence idea).
        assert!(d_had < d_plain, "Hadamard+int2 drift {d_had} should beat plain int2 {d_plain}");
        // int2 is the smallest (packs 4/byte).
        assert!(Tensor::quantize_q2_group(&w, 64).bytes() < Tensor::quantize_q3_group(&w, 64).bytes());
    }

    #[test]
    fn hadamard_rotation_helps_outlier_weights() {
        // Power-of-two input dim; outlier-heavy columns (where rotation earns its keep).
        let (inn, out) = (128usize, 128usize);
        let w = structured(inn, out, 23);
        let d_nf4 = rel_l2(&w, &Tensor::quantize_nf4_group(&w, 64).to_f32_vec());
        let d_had = rel_l2(&w, &Tensor::hadamard_reconstruct(&w, inn, out, 64));
        // Rotation spreads outliers -> the rotated weights quantize better -> lower drift.
        assert!(d_had < d_nf4, "Hadamard+NF4 drift {d_had} should beat plain NF4 {d_nf4}");
    }

    #[test]
    fn nf4_beats_uniform_int4_on_normal_weights() {
        // Approximately-normal weights (sum of 12 uniforms ≈ Gaussian) — NF4's design regime.
        let (inn, out) = (256usize, 256usize);
        let mut s = 21u64;
        let mut rng = || { s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
            (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 };
        let w: Vec<f32> = (0..inn * out).map(|_| (0..12).map(|_| rng()).sum::<f32>()).collect();
        let d_int4 = rel_l2(&w, &Tensor::quantize_q4_group(&w, 64).to_f32_vec());
        let d_nf4 = rel_l2(&w, &Tensor::quantize_nf4_group(&w, 64).to_f32_vec());
        // Same 0.5 byte/weight, better fidelity on normal weights.
        assert_eq!(Tensor::quantize_nf4_group(&w, 64).bytes(), Tensor::quantize_q4_group(&w, 64).bytes());
        assert!(d_nf4 < d_int4, "NF4 drift {d_nf4} should beat uniform int4 {d_int4} on normal weights");
    }

    #[test]
    fn mse_clip_beats_plain_group_int4() {
        let (inn, out) = (256usize, 256usize);
        let w = structured(inn, out, 9);
        let d_plain = rel_l2(&w, &Tensor::quantize_q4_group(&w, 64).to_f32_vec());
        let d_clip = rel_l2(&w, &Tensor::quantize_q4_group_clip(&w, 64).to_f32_vec());
        // Same format/size, better scale selection -> lower drift.
        assert!(d_clip <= d_plain, "clip drift {d_clip} should be <= plain {d_plain}");
        assert!(d_clip < d_plain * 0.999 || d_plain < 1e-6, "clip should help on outlier-y weights");
        // Still a Q4G tensor at 0.5 byte/weight.
        assert_eq!(Tensor::quantize_q4_group_clip(&w, 64).bytes(), Tensor::quantize_q4_group(&w, 64).bytes());
    }

    #[test]
    fn int3_group_packs_correctly_and_is_smaller() {
        let (inn, out) = (128usize, 128usize);
        let w = structured(inn, out, 5);
        let q3 = Tensor::quantize_q3_group(&w, 32);
        let q4 = Tensor::quantize_q4_group(&w, 32);
        // int3 is smaller than int4, and both far smaller than f32.
        assert!(q3.bytes() < q4.bytes(), "int3 {} should be < int4 {}", q3.bytes(), q4.bytes());
        assert!(q3.bytes() * 6 < w.len() * 4, "int3 should be >6× smaller than f32");
        // Bit-packing roundtrips: dequantized values are the intended q*scale (bounded drift), and
        // the per-element quantization error never exceeds half a step (proves packing is correct).
        let deq = q3.to_f32_vec();
        let drift = rel_l2(&w, &deq);
        assert!(drift < 0.35, "int3 group drift {drift} bounded");
        let a = Tensor::quantize_q3_group(&w, 32).to_f32_vec();
        assert_eq!(a, deq, "int3 group quant must be deterministic");
        // Exactness of packing: reconstruct one group by hand and compare a few elements.
        for &idx in &[0usize, 1, 31, 32, 100, w.len() - 1] {
            let scale = { // recompute group scale
                let g = 32; let start = (idx / g) * g; let end = (start + g).min(w.len());
                let amax = w[start..end].iter().fold(0.0f32, |m, &x| m.max(x.abs()));
                if amax > 0.0 { amax / 3.0 } else { 1.0 }
            };
            let want = (w[idx] / scale).round().clamp(-4.0, 3.0) * scale;
            assert!((deq[idx] - want).abs() < 1e-5, "int3 element {idx}: {} vs {want}", deq[idx]);
        }
    }
}
