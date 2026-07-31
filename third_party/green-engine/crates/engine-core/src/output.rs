//! Linear / output-projection helpers for Llama-style blocks (f32 + weight views).
//!
//! Weight layout matches [`crate::tensor::matvec`]: row-major `[in_dim * out_dim]`,
//! so `y[o] = Σ_i x[i] · w[i * out_dim + o]`. Callers obtain views from
//! [`crate::weights::Tensor::as_f32`] / `as_f32_borrow` without touching the loader.

use crate::tensor::matvec;

/// Borrowed row-major f32 matrix used by attention / output projections.
#[derive(Clone, Copy, Debug)]
pub struct WeightView<'a> {
    pub data: &'a [f32],
    pub in_dim: usize,
    pub out_dim: usize,
}

impl<'a> WeightView<'a> {
    pub fn new(data: &'a [f32], in_dim: usize, out_dim: usize) -> Self {
        assert_eq!(
            data.len(),
            in_dim * out_dim,
            "WeightView: len {} != {}×{}",
            data.len(),
            in_dim,
            out_dim
        );
        WeightView {
            data,
            in_dim,
            out_dim,
        }
    }

    /// Dense GEMV into `y` (`y.len() == out_dim`).
    #[inline]
    pub fn gemv(&self, x: &[f32], y: &mut [f32]) {
        matvec(x, self.data, self.in_dim, self.out_dim, y);
    }
}

/// `out = x @ weight` (+ optional bias). `out` length = `weight.out_dim`.
pub fn linear(x: &[f32], weight: WeightView<'_>, bias: Option<&[f32]>, out: &mut [f32]) {
    assert_eq!(x.len(), weight.in_dim, "linear: x dim mismatch");
    assert_eq!(out.len(), weight.out_dim, "linear: out dim mismatch");
    weight.gemv(x, out);
    if let Some(b) = bias {
        assert_eq!(b.len(), out.len(), "linear: bias length mismatch");
        for i in 0..out.len() {
            out[i] += b[i];
        }
    }
}

/// Attention output projection: `out = attn_out @ wo` (no bias in Llama).
#[inline]
pub fn out_proj(attn_out: &[f32], wo: WeightView<'_>, out: &mut [f32]) {
    linear(attn_out, wo, None, out);
}

/// Vocabulary / LM-head style projection (same math as [`linear`], named for the forward agent).
#[inline]
pub fn lm_head(hidden: &[f32], weight: WeightView<'_>, logits: &mut [f32]) {
    linear(hidden, weight, None, logits);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn linear_identity_square() {
        // 2×2 identity
        let w = [1.0f32, 0.0, 0.0, 1.0];
        let view = WeightView::new(&w, 2, 2);
        let x = [3.0f32, -1.0];
        let mut y = [0.0f32; 2];
        linear(&x, view, None, &mut y);
        assert_eq!(y, x);
    }

    #[test]
    fn linear_with_bias() {
        let w = [1.0f32, 0.0, 0.0, 1.0];
        let view = WeightView::new(&w, 2, 2);
        let x = [1.0f32, 2.0];
        let b = [0.5f32, -0.5];
        let mut y = [0.0f32; 2];
        linear(&x, view, Some(&b), &mut y);
        assert!((y[0] - 1.5).abs() < 1e-6);
        assert!((y[1] - 1.5).abs() < 1e-6);
    }

    #[test]
    fn out_proj_tiny() {
        // wo: in=2 out=2, scales by 2
        let w = [2.0f32, 0.0, 0.0, 2.0];
        let wo = WeightView::new(&w, 2, 2);
        let attn = [1.0f32, -1.0];
        let mut out = [0.0f32; 2];
        out_proj(&attn, wo, &mut out);
        assert!((out[0] - 2.0).abs() < 1e-6);
        assert!((out[1] + 2.0).abs() < 1e-6);
    }
}
