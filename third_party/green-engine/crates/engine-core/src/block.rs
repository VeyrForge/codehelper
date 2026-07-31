//! Llama-style attention **sublayer** composition (CPU reference).
//!
//! Pre-norm: `RMSNorm → QKV → (optional KvStore append) → MHA/GQA → O-proj → residual`.
//! FFN / MoE is owned by the expert path — this module stops after the attention residual.
//!
//! Forward agent entry points: [`AttnBlockWeights`], [`AttnScratch`], [`attn_block_decode`].

use crate::attention::{
    append_kv, multi_head_attend_decode, project_qkv, recall_kv_f32,
};
use crate::kv::KvStore;
use crate::norm::{residual_add, rms_norm, RMS_EPS_DEFAULT};
use crate::output::{out_proj, WeightView};

/// Weight views for one attention sublayer (borrowed f32 matrices).
#[derive(Clone, Copy, Debug)]
pub struct AttnBlockWeights<'a> {
    /// RMSNorm γ over `hidden`.
    pub attn_norm: &'a [f32],
    pub wq: WeightView<'a>,
    pub wk: WeightView<'a>,
    pub wv: WeightView<'a>,
    pub wo: WeightView<'a>,
    pub n_heads: usize,
    pub n_kv_heads: usize,
    pub eps: f32,
}

impl<'a> AttnBlockWeights<'a> {
    pub fn hidden(&self) -> usize {
        self.attn_norm.len()
    }

    pub fn head_dim(&self) -> usize {
        let q_out = self.wq.out_dim;
        assert_eq!(q_out % self.n_heads, 0);
        q_out / self.n_heads
    }

    pub fn with_defaults(
        attn_norm: &'a [f32],
        wq: WeightView<'a>,
        wk: WeightView<'a>,
        wv: WeightView<'a>,
        wo: WeightView<'a>,
        n_heads: usize,
        n_kv_heads: usize,
    ) -> Self {
        AttnBlockWeights {
            attn_norm,
            wq,
            wk,
            wv,
            wo,
            n_heads,
            n_kv_heads,
            eps: RMS_EPS_DEFAULT,
        }
    }
}

/// Scratch buffers for [`attn_block_decode`] (reuse across tokens).
pub struct AttnScratch {
    pub xn: Vec<f32>,
    pub q: Vec<f32>,
    pub k: Vec<f32>,
    pub v: Vec<f32>,
    pub attn: Vec<f32>,
    pub proj: Vec<f32>,
    pub scores: Vec<f32>,
}

impl AttnScratch {
    pub fn new(hidden: usize, n_heads: usize, n_kv_heads: usize, head_dim: usize, max_seq: usize) -> Self {
        AttnScratch {
            xn: vec![0.0; hidden],
            q: vec![0.0; n_heads * head_dim],
            k: vec![0.0; n_kv_heads * head_dim],
            v: vec![0.0; n_kv_heads * head_dim],
            attn: vec![0.0; n_heads * head_dim],
            proj: vec![0.0; hidden],
            scores: vec![0.0; max_seq],
        }
    }

    pub fn ensure_seq(&mut self, seq: usize) {
        if self.scores.len() < seq {
            self.scores.resize(seq, 0.0);
        }
    }
}

/// One decode step of the attention sublayer.
///
/// - `x`: residual stream `[hidden]`
/// - `past_k` / `past_v`: `[past_len * n_kv_heads * head_dim]` (may be empty)
/// - `out`: `x + o_proj(attn(...))` `[hidden]`
/// - Writes this token's `k`/`v` into `new_k` / `new_v` (`[n_kv_heads * head_dim]`)
/// - If `kv` is `Some`, also calls [`append_kv`] (KvStore seam).
///
/// **Generate-loop pattern with store-owned past** — prefer [`attn_block_decode_kv`]:
/// append current K/V, then recall `0..token` as past for the score mix.
pub fn attn_block_decode(
    x: &[f32],
    past_k: &[f32],
    past_v: &[f32],
    weights: &AttnBlockWeights<'_>,
    scratch: &mut AttnScratch,
    kv: Option<&mut dyn KvStore>,
    layer: usize,
    token: usize,
    new_k: &mut [f32],
    new_v: &mut [f32],
    out: &mut [f32],
) {
    let hidden = weights.hidden();
    let head_dim = weights.head_dim();
    let n_heads = weights.n_heads;
    let n_kv = weights.n_kv_heads;
    let kv_dim = n_kv * head_dim;
    assert_eq!(x.len(), hidden);
    assert_eq!(out.len(), hidden);
    assert_eq!(new_k.len(), kv_dim);
    assert_eq!(new_v.len(), kv_dim);
    assert_eq!(weights.wq.in_dim, hidden);
    assert_eq!(weights.wo.out_dim, hidden);

    let past_len = if past_k.is_empty() {
        0
    } else {
        past_k.len() / kv_dim
    };
    scratch.ensure_seq(past_len + 1);

    rms_norm(x, weights.attn_norm, weights.eps, &mut scratch.xn);
    project_qkv(
        &scratch.xn,
        weights.wq,
        weights.wk,
        weights.wv,
        &mut scratch.q,
        &mut scratch.k,
        &mut scratch.v,
    );
    new_k.copy_from_slice(&scratch.k);
    new_v.copy_from_slice(&scratch.v);

    if let Some(store) = kv {
        append_kv(store, layer, token, new_k, new_v);
    }

    multi_head_attend_decode(
        &scratch.q,
        &scratch.k,
        &scratch.v,
        past_k,
        past_v,
        n_heads,
        n_kv,
        head_dim,
        &mut scratch.scores,
        &mut scratch.attn,
    );
    out_proj(&scratch.attn, weights.wo, &mut scratch.proj);
    residual_add(x, &scratch.proj, out);
}

/// Decode step that owns past K/V via [`KvStore::recall`] (canonical generate-loop call site).
///
/// Order: project → append current K/V → recall `0..token` as past → attend → O → residual.
/// `token` must equal the store's next append index for `layer` (see [`KvStore::append`]).
pub fn attn_block_decode_kv(
    x: &[f32],
    weights: &AttnBlockWeights<'_>,
    scratch: &mut AttnScratch,
    kv: &mut dyn KvStore,
    layer: usize,
    token: usize,
    new_k: &mut [f32],
    new_v: &mut [f32],
    out: &mut [f32],
) {
    let hidden = weights.hidden();
    let head_dim = weights.head_dim();
    let n_heads = weights.n_heads;
    let n_kv = weights.n_kv_heads;
    let kv_dim = n_kv * head_dim;
    assert_eq!(x.len(), hidden);
    assert_eq!(out.len(), hidden);
    assert_eq!(new_k.len(), kv_dim);
    assert_eq!(new_v.len(), kv_dim);

    rms_norm(x, weights.attn_norm, weights.eps, &mut scratch.xn);
    project_qkv(
        &scratch.xn,
        weights.wq,
        weights.wk,
        weights.wv,
        &mut scratch.q,
        &mut scratch.k,
        &mut scratch.v,
    );
    new_k.copy_from_slice(&scratch.k);
    new_v.copy_from_slice(&scratch.v);
    append_kv(kv, layer, token, new_k, new_v);

    // Past = tokens before the one we just appended.
    let (past_k, past_v) = if token == 0 {
        (Vec::new(), Vec::new())
    } else {
        recall_kv_f32(kv, layer, 0..token)
    };
    scratch.ensure_seq(token + 1);
    multi_head_attend_decode(
        &scratch.q,
        &scratch.k,
        &scratch.v,
        &past_k,
        &past_v,
        n_heads,
        n_kv,
        head_dim,
        &mut scratch.scores,
        &mut scratch.attn,
    );
    out_proj(&scratch.attn, weights.wo, &mut scratch.proj);
    residual_add(x, &scratch.proj, out);
}

/// Prefill helper: run [`attn_block_decode`] with empty past (first token / seq_len=1 path).
#[inline]
pub fn attn_block_prefill_one(
    x: &[f32],
    weights: &AttnBlockWeights<'_>,
    scratch: &mut AttnScratch,
    kv: Option<&mut dyn KvStore>,
    layer: usize,
    token: usize,
    new_k: &mut [f32],
    new_v: &mut [f32],
    out: &mut [f32],
) {
    attn_block_decode(
        x, &[], &[], weights, scratch, kv, layer, token, new_k, new_v, out,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::attention::f16_slice_to_f32;
    use crate::kv::{AttentionResult, F16, KvRecall, KvStore};
    use std::collections::HashMap;
    use std::ops::Range;

    struct MemKv {
        layers: HashMap<usize, Vec<(usize, Vec<F16>, Vec<F16>)>>,
        dim: usize,
    }

    impl MemKv {
        fn new() -> Self {
            MemKv {
                layers: HashMap::new(),
                dim: 0,
            }
        }
    }

    impl KvStore for MemKv {
        fn append(&mut self, layer: usize, token: usize, key: &[F16], value: &[F16]) {
            if self.dim == 0 {
                self.dim = key.len();
            }
            self.layers
                .entry(layer)
                .or_default()
                .push((token, key.to_vec(), value.to_vec()));
        }

        fn recall(&self, layer: usize, range: Range<usize>) -> KvRecall {
            let empty = Vec::new();
            let rows = self.layers.get(&layer).unwrap_or(&empty);
            let start = range.start.min(rows.len());
            let end = range.end.min(rows.len());
            let dim = self.dim;
            let mut keys = Vec::with_capacity((end - start) * dim);
            let mut values = Vec::with_capacity((end - start) * dim);
            for i in start..end {
                keys.extend_from_slice(&rows[i].1);
                values.extend_from_slice(&rows[i].2);
            }
            KvRecall {
                keys,
                values,
                tokens: end - start,
                dim,
            }
        }

        fn seq_len(&self) -> usize {
            self.layers.get(&0).map(|v| v.len()).unwrap_or(0)
        }

        fn layers(&self) -> usize {
            self.layers.keys().copied().max().map(|m| m + 1).unwrap_or(0)
        }

        fn dim(&self) -> usize {
            self.dim
        }

        fn clear(&mut self) {
            self.layers.clear();
            self.dim = 0;
        }

        fn truncate(&mut self, new_len: usize) {
            for rows in self.layers.values_mut() {
                if rows.len() > new_len {
                    rows.truncate(new_len);
                }
            }
            if new_len == 0 {
                self.dim = 0;
            }
        }

        fn attend(&self, _layer: usize, query: &[F16], _range: Range<usize>) -> AttentionResult {
            AttentionResult {
                output: vec![0; query.len()],
            }
        }
    }

    fn tiny_identity_weights() -> (Vec<f32>, Vec<f32>, Vec<f32>, Vec<f32>, Vec<f32>, Vec<f32>) {
        // hidden=4, n_heads=2, n_kv=2, head_dim=2 → q/k/v/o are 4×4
        let hidden = 4usize;
        let norm = vec![1.0f32; hidden];
        let eye = |n: usize| {
            let mut w = vec![0.0f32; n * n];
            for i in 0..n {
                w[i * n + i] = 1.0;
            }
            w
        };
        let wq = eye(hidden);
        let wk = eye(hidden);
        let wv = eye(hidden);
        let wo = eye(hidden);
        (norm, wq, wk, wv, wo, vec![0.0f32; 0])
    }

    #[test]
    fn attn_block_first_token_with_kv_append() {
        let (norm, wq, wk, wv, wo, _) = tiny_identity_weights();
        let hidden = 4;
        let n_heads = 2;
        let n_kv = 2;
        let head_dim = 2;
        let weights = AttnBlockWeights::with_defaults(
            &norm,
            WeightView::new(&wq, hidden, n_heads * head_dim),
            WeightView::new(&wk, hidden, n_kv * head_dim),
            WeightView::new(&wv, hidden, n_kv * head_dim),
            WeightView::new(&wo, hidden, n_heads * head_dim),
            n_heads,
            n_kv,
        );
        let mut scratch = AttnScratch::new(hidden, n_heads, n_kv, head_dim, 8);
        let x = [1.0f32, 0.0, 0.0, 0.0];
        let mut out = [0.0f32; 4];
        let mut new_k = [0.0f32; 4];
        let mut new_v = [0.0f32; 4];
        let mut kv = MemKv::new();
        attn_block_decode(
            &x,
            &[],
            &[],
            &weights,
            &mut scratch,
            Some(&mut kv),
            0,
            0,
            &mut new_k,
            &mut new_v,
            &mut out,
        );
        // Identity QKV + O, single token → attn_out = v = xn ≈ x (rms of e0), residual ≈ 2x-ish
        assert!(kv.layers.get(&0).unwrap().len() == 1);
        let stored_k = f16_slice_to_f32(&kv.layers.get(&0).unwrap()[0].1);
        assert!((stored_k[0] - new_k[0]).abs() < 0.05);
        // residual stream should move (not all zeros)
        assert!(out.iter().any(|&v| v.abs() > 1e-3), "out={out:?}");
    }

    #[test]
    fn attn_block_second_token_uses_past() {
        let (norm, wq, wk, wv, wo, _) = tiny_identity_weights();
        let hidden = 4;
        let n_heads = 2;
        let n_kv = 2;
        let head_dim = 2;
        let weights = AttnBlockWeights::with_defaults(
            &norm,
            WeightView::new(&wq, hidden, n_heads * head_dim),
            WeightView::new(&wk, hidden, n_kv * head_dim),
            WeightView::new(&wv, hidden, n_kv * head_dim),
            WeightView::new(&wo, hidden, n_heads * head_dim),
            n_heads,
            n_kv,
        );
        let mut scratch = AttnScratch::new(hidden, n_heads, n_kv, head_dim, 8);
        let mut past_k = Vec::new();
        let mut past_v = Vec::new();
        let mut new_k = [0.0f32; 4];
        let mut new_v = [0.0f32; 4];
        let mut out0 = [0.0f32; 4];
        let x0 = [1.0f32, 0.0, 0.0, 0.0];
        attn_block_decode(
            &x0, &[], &[], &weights, &mut scratch, None, 0, 0, &mut new_k, &mut new_v, &mut out0,
        );
        past_k.extend_from_slice(&new_k);
        past_v.extend_from_slice(&new_v);

        let x1 = [0.0f32, 1.0, 0.0, 0.0];
        let mut out1 = [0.0f32; 4];
        attn_block_decode(
            &x1,
            &past_k,
            &past_v,
            &weights,
            &mut scratch,
            None,
            0,
            1,
            &mut new_k,
            &mut new_v,
            &mut out1,
        );
        assert!(out1.iter().any(|&v| v.abs() > 1e-3));
    }

    #[test]
    fn attn_block_decode_kv_uses_ram_store() {
        use crate::kv::RamKvStore;
        let (norm, wq, wk, wv, wo, _) = tiny_identity_weights();
        let hidden = 4;
        let n_heads = 2;
        let n_kv = 2;
        let head_dim = 2;
        let weights = AttnBlockWeights::with_defaults(
            &norm,
            WeightView::new(&wq, hidden, n_heads * head_dim),
            WeightView::new(&wk, hidden, n_kv * head_dim),
            WeightView::new(&wv, hidden, n_kv * head_dim),
            WeightView::new(&wo, hidden, n_heads * head_dim),
            n_heads,
            n_kv,
        );
        let mut scratch = AttnScratch::new(hidden, n_heads, n_kv, head_dim, 8);
        let mut kv = RamKvStore::new(1, n_kv * head_dim);
        let mut new_k = [0.0f32; 4];
        let mut new_v = [0.0f32; 4];
        let mut out0 = [0.0f32; 4];
        attn_block_decode_kv(
            &[1.0, 0.0, 0.0, 0.0],
            &weights,
            &mut scratch,
            &mut kv,
            0,
            0,
            &mut new_k,
            &mut new_v,
            &mut out0,
        );
        let mut out1 = [0.0f32; 4];
        attn_block_decode_kv(
            &[0.0, 1.0, 0.0, 0.0],
            &weights,
            &mut scratch,
            &mut kv,
            0,
            1,
            &mut new_k,
            &mut new_v,
            &mut out1,
        );
        assert_eq!(kv.seq_len(), 2);
        assert!(out1.iter().any(|&v| v.abs() > 1e-3));
    }
}
