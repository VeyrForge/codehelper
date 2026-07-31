//! CPU reference norms and residual for Llama-style blocks (f32 activations).
//!
//! Pre-norm transformers apply [`rms_norm`] (Llama / Mistral) or [`layer_norm`] before
//! attention / FFN; the sublayer output is then added back via [`residual_add`].

/// Default Llama RMSNorm epsilon.
pub const RMS_EPS_DEFAULT: f32 = 1e-5;

/// Default LayerNorm epsilon (BERT-style; less common in Llama).
pub const LN_EPS_DEFAULT: f32 = 1e-5;

/// `out[i] = weight[i] * x[i] / sqrt(mean(x²) + eps)` — Llama RMSNorm (no bias, no mean center).
pub fn rms_norm(x: &[f32], weight: &[f32], eps: f32, out: &mut [f32]) {
    assert_eq!(x.len(), weight.len(), "rms_norm: x/weight length mismatch");
    assert_eq!(x.len(), out.len(), "rms_norm: x/out length mismatch");
    let n = x.len();
    let mut ms = 0.0f32;
    for &v in x {
        ms += v * v;
    }
    ms /= n as f32;
    let inv = (ms + eps).sqrt().recip();
    for i in 0..n {
        out[i] = weight[i] * (x[i] * inv);
    }
}

/// Affine LayerNorm: center + scale, optional bias.
/// `out = weight * (x - mean) / sqrt(var + eps) + bias` (bias may be omitted).
pub fn layer_norm(x: &[f32], weight: &[f32], bias: Option<&[f32]>, eps: f32, out: &mut [f32]) {
    assert_eq!(x.len(), weight.len(), "layer_norm: x/weight length mismatch");
    assert_eq!(x.len(), out.len(), "layer_norm: x/out length mismatch");
    if let Some(b) = bias {
        assert_eq!(x.len(), b.len(), "layer_norm: bias length mismatch");
    }
    let n = x.len();
    let mut mean = 0.0f32;
    for &v in x {
        mean += v;
    }
    mean /= n as f32;
    let mut var = 0.0f32;
    for &v in x {
        let d = v - mean;
        var += d * d;
    }
    var /= n as f32;
    let inv = (var + eps).sqrt().recip();
    for i in 0..n {
        let y = weight[i] * ((x[i] - mean) * inv);
        out[i] = match bias {
            Some(b) => y + b[i],
            None => y,
        };
    }
}

/// `out[i] = residual[i] + delta[i]`.
pub fn residual_add(residual: &[f32], delta: &[f32], out: &mut [f32]) {
    assert_eq!(residual.len(), delta.len(), "residual_add: length mismatch");
    assert_eq!(residual.len(), out.len(), "residual_add: out length mismatch");
    for i in 0..residual.len() {
        out[i] = residual[i] + delta[i];
    }
}

/// In-place `x[i] += delta[i]`.
pub fn residual_add_inplace(x: &mut [f32], delta: &[f32]) {
    assert_eq!(x.len(), delta.len(), "residual_add_inplace: length mismatch");
    for i in 0..x.len() {
        x[i] += delta[i];
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rms_norm_unit_scale_on_unit_vector() {
        // x with RMS = 1, weight = 1 → out ≈ x
        let x = [1.0f32, -1.0, 1.0, -1.0];
        let w = [1.0f32; 4];
        let mut out = [0.0f32; 4];
        rms_norm(&x, &w, 1e-6, &mut out);
        for i in 0..4 {
            assert!((out[i] - x[i]).abs() < 1e-4, "out[{i}]={} vs {}", out[i], x[i]);
        }
    }

    #[test]
    fn rms_norm_scales_by_weight() {
        let x = [2.0f32, 0.0, 0.0, 0.0]; // mean sq = 1
        let w = [3.0f32, 3.0, 3.0, 3.0];
        let mut out = [0.0f32; 4];
        rms_norm(&x, &w, 0.0, &mut out);
        assert!((out[0] - 6.0).abs() < 1e-4);
        assert!(out[1].abs() < 1e-6);
    }

    #[test]
    fn layer_norm_zero_mean_unit_var() {
        let x = [1.0f32, 2.0, 3.0, 4.0];
        let w = [1.0f32; 4];
        let mut out = [0.0f32; 4];
        layer_norm(&x, &w, None, 1e-5, &mut out);
        let mean: f32 = out.iter().sum::<f32>() / 4.0;
        assert!(mean.abs() < 1e-4, "mean {mean}");
        let var: f32 = out.iter().map(|v| (v - mean) * (v - mean)).sum::<f32>() / 4.0;
        assert!((var - 1.0).abs() < 1e-3, "var {var}");
    }

    #[test]
    fn residual_add_matches() {
        let a = [1.0f32, 2.0, 3.0];
        let b = [0.5f32, -1.0, 1.0];
        let mut out = [0.0f32; 3];
        residual_add(&a, &b, &mut out);
        assert_eq!(out, [1.5, 1.0, 4.0]);
        let mut x = a;
        residual_add_inplace(&mut x, &b);
        assert_eq!(x, out);
    }
}
