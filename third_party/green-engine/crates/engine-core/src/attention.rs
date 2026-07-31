//! CPU reference multi-head self-attention (Llama / GQA) on f32 activations.
//!
//! Pieces: QKV projection → causal scores → softmax → value mix → (caller applies O-proj).
//! Past K/V are f32 buffers the forward agent owns; [`append_kv`] / [`attend_via_store`] are
//! the call sites into [`crate::kv::KvStore`] (kv agent owns the store impl — F16 on the wire).

use std::ops::Range;

use crate::kv::{AttentionResult, F16, KvRecall, KvStore};
use crate::output::{linear, WeightView};

/// `1 / sqrt(head_dim)` attention scale.
#[inline]
pub fn attn_scale(head_dim: usize) -> f32 {
    1.0 / (head_dim as f32).sqrt()
}

/// Project hidden → Q, K, V (decode: one token).
///
/// - `wq`: `[hidden, n_heads * head_dim]`
/// - `wk` / `wv`: `[hidden, n_kv_heads * head_dim]`
pub fn project_qkv(
    x: &[f32],
    wq: WeightView<'_>,
    wk: WeightView<'_>,
    wv: WeightView<'_>,
    q: &mut [f32],
    k: &mut [f32],
    v: &mut [f32],
) {
    linear(x, wq, None, q);
    linear(x, wk, None, k);
    linear(x, wv, None, v);
}

/// Numerically stable softmax over `logits` (in-place).
pub fn softmax_inplace(logits: &mut [f32]) {
    if logits.is_empty() {
        return;
    }
    let mut m = logits[0];
    for &v in logits.iter().skip(1) {
        if v > m {
            m = v;
        }
    }
    let mut sum = 0.0f32;
    for v in logits.iter_mut() {
        *v = (*v - m).exp();
        sum += *v;
    }
    let inv = if sum > 0.0 { 1.0 / sum } else { 0.0 };
    for v in logits.iter_mut() {
        *v *= inv;
    }
}

#[inline]
fn dot_f32(a: &[f32], b: &[f32]) -> f32 {
    let n = a.len();
    let mut sum = 0.0f32;
    let mut i = 0usize;
    while i + 4 <= n {
        sum += a[i] * b[i] + a[i + 1] * b[i + 1] + a[i + 2] * b[i + 2] + a[i + 3] * b[i + 3];
        i += 4;
    }
    while i < n { sum += a[i] * b[i]; i += 1; }
    sum
}

#[derive(Clone, Debug, Default)]
pub struct MhaScratch {
    pub keys_h: Vec<f32>,
    pub vals_h: Vec<f32>,
    pub out_h: Vec<f32>,
}

impl MhaScratch {
    pub fn ensure(&mut self, seq: usize, head_dim: usize) {
        let need = seq.saturating_mul(head_dim);
        if self.keys_h.len() < need { self.keys_h.resize(need, 0.0); }
        if self.vals_h.len() < need { self.vals_h.resize(need, 0.0); }
        if self.out_h.len() < head_dim { self.out_h.resize(head_dim, 0.0); }
    }
}

fn attend_one_head(
    h: usize, kv_h: usize, q: &[f32], k_new: &[f32], v_new: &[f32],
    past_k: &[f32], past_v: &[f32], past_len: usize, seq: usize, kv_dim: usize,
    head_dim: usize, scale: f32, scores: &mut [f32], mha: &mut MhaScratch, attn_out: &mut [f32],
) {
    mha.ensure(seq, head_dim);
    for t in 0..past_len {
        let src = t * kv_dim + kv_h * head_dim;
        let dst = t * head_dim;
        mha.keys_h[dst..dst + head_dim].copy_from_slice(&past_k[src..src + head_dim]);
        mha.vals_h[dst..dst + head_dim].copy_from_slice(&past_v[src..src + head_dim]);
    }
    let dst = past_len * head_dim;
    let src = kv_h * head_dim;
    mha.keys_h[dst..dst + head_dim].copy_from_slice(&k_new[src..src + head_dim]);
    mha.vals_h[dst..dst + head_dim].copy_from_slice(&v_new[src..src + head_dim]);
    let q_head = &q[h * head_dim..(h + 1) * head_dim];
    attention_scores_head(q_head, &mha.keys_h[..seq * head_dim], head_dim, scale, scores);
    softmax_inplace(scores);
    attention_mix_head(scores, &mha.vals_h[..seq * head_dim], head_dim, &mut mha.out_h);
    attn_out[h * head_dim..(h + 1) * head_dim].copy_from_slice(&mha.out_h);
}

fn attend_decode_dims(
    q: &[f32], k_new: &[f32], v_new: &[f32], past_k: &[f32], past_v: &[f32],
    n_heads: usize, n_kv_heads: usize, head_dim: usize, score_cap: usize,
) -> (usize, usize, usize, f32, usize) {
    assert!(n_heads > 0 && n_kv_heads > 0);
    assert_eq!(n_heads % n_kv_heads, 0);
    let kv_dim = n_kv_heads * head_dim;
    let past_len = if past_k.is_empty() { 0 } else { past_k.len() / kv_dim };
    let seq = past_len + 1;
    assert!(score_cap >= seq);
    (past_len, seq, kv_dim, attn_scale(head_dim), n_heads / n_kv_heads)
}

pub fn multi_head_attend_decode_reuse(
    q: &[f32], k_new: &[f32], v_new: &[f32], past_k: &[f32], past_v: &[f32],
    n_heads: usize, n_kv_heads: usize, head_dim: usize,
    scratch_scores: &mut [f32], mha: &mut MhaScratch, attn_out: &mut [f32],
) {
    let (past_len, seq, kv_dim, scale, reps) = attend_decode_dims(
        q, k_new, v_new, past_k, past_v, n_heads, n_kv_heads, head_dim, scratch_scores.len(),
    );
    let scores = &mut scratch_scores[..seq];
    for h in 0..n_heads {
        attend_one_head(h, h / reps, q, k_new, v_new, past_k, past_v, past_len, seq, kv_dim, head_dim, scale, scores, mha, attn_out);
    }
}

pub fn multi_head_attend_decode_parallel(
    q: &[f32], k_new: &[f32], v_new: &[f32], past_k: &[f32], past_v: &[f32],
    n_heads: usize, n_kv_heads: usize, head_dim: usize,
    mha_per_kv: &mut [MhaScratch], head_scores: &mut [f32], score_cap: usize, attn_out: &mut [f32],
) {
    let (past_len, seq, kv_dim, scale, reps) = attend_decode_dims(
        q, k_new, v_new, past_k, past_v, n_heads, n_kv_heads, head_dim, score_cap,
    );
    for kv_h in 0..n_kv_heads {
        for r in 0..reps {
            let h = kv_h * reps + r;
            let scores = &mut head_scores[h * score_cap..h * score_cap + seq];
            attend_one_head(h, kv_h, q, k_new, v_new, past_k, past_v, past_len, seq, kv_dim, head_dim, scale, scores, &mut mha_per_kv[kv_h], attn_out);
        }
    }
}

/// Causal attention scores for one query head against a KV head's key timeline.
///
/// `q_head`: `[head_dim]`, `keys`: `[seq * head_dim]` for one KV head (row-major tokens),
/// `scores`: `[seq]` (filled).
pub fn attention_scores_head(
    q_head: &[f32],
    keys: &[f32],
    head_dim: usize,
    scale: f32,
    scores: &mut [f32],
) {
    let seq = scores.len();
    assert_eq!(q_head.len(), head_dim);
    assert_eq!(keys.len(), seq * head_dim);
    for t in 0..seq {
        let base = t * head_dim;
        scores[t] = dot_f32(q_head, &keys[base..base + head_dim]) * scale;
    }
}

/// `out_head[d] = Σ_t scores[t] * values[t, d]`.
pub fn attention_mix_head(
    scores: &[f32],
    values: &[f32],
    head_dim: usize,
    out_head: &mut [f32],
) {
    let seq = scores.len();
    assert_eq!(out_head.len(), head_dim);
    assert_eq!(values.len(), seq * head_dim);
    for d in 0..head_dim {
        out_head[d] = 0.0;
    }
    for t in 0..seq {
        let s = scores[t];
        let base = t * head_dim;
        for d in 0..head_dim {
            out_head[d] += s * values[base + d];
        }
    }
}

/// Decode-step multi-head / GQA attention over `past_k`/`past_v` **plus** the new token's `k`/`v`.
///
/// Layout:
/// - `q`: `[n_heads * head_dim]`
/// - `k` / `v` (new): `[n_kv_heads * head_dim]`
/// - `past_k` / `past_v`: `[past_len * n_kv_heads * head_dim]`
/// - `attn_out`: `[n_heads * head_dim]`
///
/// `scratch_scores` must hold at least `past_len + 1` floats.
pub fn multi_head_attend_decode(
    q: &[f32],
    k_new: &[f32],
    v_new: &[f32],
    past_k: &[f32],
    past_v: &[f32],
    n_heads: usize,
    n_kv_heads: usize,
    head_dim: usize,
    scratch_scores: &mut [f32],
    attn_out: &mut [f32],
) {
    assert!(n_heads > 0 && n_kv_heads > 0);
    assert_eq!(n_heads % n_kv_heads, 0, "GQA: n_heads must be divisible by n_kv_heads");
    let kv_dim = n_kv_heads * head_dim;
    let q_dim = n_heads * head_dim;
    assert_eq!(q.len(), q_dim);
    assert_eq!(k_new.len(), kv_dim);
    assert_eq!(v_new.len(), kv_dim);
    assert_eq!(attn_out.len(), q_dim);
    let past_len = if past_k.is_empty() {
        0
    } else {
        assert_eq!(past_k.len() % kv_dim, 0);
        assert_eq!(past_k.len(), past_v.len());
        past_k.len() / kv_dim
    };
    let seq = past_len + 1;
    assert!(
        scratch_scores.len() >= seq,
        "scratch_scores too small: need {seq}"
    );
    let scores = &mut scratch_scores[..seq];
    let scale = attn_scale(head_dim);
    let reps = n_heads / n_kv_heads;

    // Gather per-KV-head key/value timelines into temporary contiguous buffers only when needed
    // for clarity on tiny dims — allocate once per call (decode is not the hot path yet).
    let mut mha = MhaScratch::default();
    multi_head_attend_decode_reuse(
        q, k_new, v_new, past_k, past_v, n_heads, n_kv_heads, head_dim, scratch_scores, &mut mha, attn_out,
    );
}

// ---- f16 bridge + KvStore call sites ----------------------------------------------------------

/// IEEE 754 binary16 bits from f32 (round-to-nearest-even), no `half` crate.
pub fn f32_to_f16_bits(v: f32) -> F16 {
    let bits = v.to_bits();
    let sign = ((bits >> 16) & 0x8000) as u32;
    let mut exp = ((bits >> 23) & 0xff) as i32;
    let mut mant = bits & 0x7f_ffff;

    if exp == 255 {
        // Inf / NaN
        if mant == 0 {
            return (sign | 0x7c00) as u16;
        }
        return (sign | 0x7c00 | (mant >> 13)) as u16;
    }
    if exp == 0 {
        // Zero / subnormal → flush to signed zero in f16
        return sign as u16;
    }

    // Re-bias
    exp = exp - 127 + 15;
    if exp >= 31 {
        // Overflow → Inf
        return (sign | 0x7c00) as u16;
    }
    if exp <= 0 {
        // Underflow → subnormal or zero
        if exp < -10 {
            return sign as u16;
        }
        mant |= 0x800_000;
        let shift = (1 - exp) as u32;
        let mut m = mant >> (shift + 13);
        let rem = mant >> (shift + 12);
        if (rem & 1) != 0 {
            m += 1;
        }
        return (sign | m) as u16;
    }

    let mut half = (sign as u32) | ((exp as u32) << 10) | (mant >> 13);
    // Round to nearest even
    if (mant & 0x1000) != 0 {
        half += 1;
    }
    half as u16
}

/// f32 from IEEE 754 binary16 bits.
pub fn f16_bits_to_f32(h: F16) -> f32 {
    let sign = ((h as u32) & 0x8000) << 16;
    let exp = ((h as u32) >> 10) & 0x1f;
    let mant = (h as u32) & 0x3ff;
    let bits = if exp == 0 {
        if mant == 0 {
            sign
        } else {
            // Subnormal
            let mut m = mant;
            let mut e = -1i32;
            while (m & 0x400) == 0 {
                m <<= 1;
                e -= 1;
            }
            m &= 0x3ff;
            let exp_f = (127 - 15 + e) as u32;
            sign | (exp_f << 23) | (m << 13)
        }
    } else if exp == 31 {
        sign | 0x7f80_0000 | (mant << 13)
    } else {
        let exp_f = (exp as i32 - 15 + 127) as u32;
        sign | (exp_f << 23) | (mant << 13)
    };
    f32::from_bits(bits)
}

pub fn f32_slice_to_f16(src: &[f32]) -> Vec<F16> {
    src.iter().copied().map(f32_to_f16_bits).collect()
}

pub fn f16_slice_to_f32(src: &[F16]) -> Vec<f32> {
    src.iter().copied().map(f16_bits_to_f32).collect()
}

/// **KvStore call site** — append this token's K/V (f32 → F16 on the wire).
pub fn f32_slice_to_f16_into(src: &[f32], dst: &mut Vec<F16>) {
    dst.clear();
    dst.reserve(src.len());
    for &v in src { dst.push(f32_to_f16_bits(v)); }
}

pub fn append_kv_reuse(
    store: &mut dyn KvStore,
    layer: usize,
    token: usize,
    key: &[f32],
    value: &[f32],
    k16: &mut Vec<F16>,
    v16: &mut Vec<F16>,
) {
    f32_slice_to_f16_into(key, k16);
    f32_slice_to_f16_into(value, v16);
    store.append(layer, token, k16, v16);
}

pub fn append_kv(store: &mut dyn KvStore, layer: usize, token: usize, key: &[f32], value: &[f32]) {
    let k16 = f32_slice_to_f16(key);
    let v16 = f32_slice_to_f16(value);
    store.append(layer, token, &k16, &v16);
}

/// **KvStore call site** — recall past K/V and convert to f32 for [`multi_head_attend_decode`].
///
/// Returns `(past_k, past_v)` with layout `[tokens * dim]` matching the store.
pub fn recall_kv_f32(store: &dyn KvStore, layer: usize, range: Range<usize>) -> (Vec<f32>, Vec<f32>) {
    let rec: KvRecall = store.recall(layer, range);
    (
        f16_slice_to_f32(&rec.keys),
        f16_slice_to_f32(&rec.values),
    )
}

/// **KvStore call site** — `attend` stub passthrough (kv backend does not own attention math).
pub fn recall_kv_f32_into(
    store: &dyn KvStore,
    layer: usize,
    range: Range<usize>,
    keys: &mut Vec<f32>,
    values: &mut Vec<f32>,
) {
    store.recall_f32_into(layer, range, keys, values);
}

pub fn attend_via_store(
    store: &dyn KvStore,
    layer: usize,
    query: &[f32],
    range: Range<usize>,
) -> AttentionResult {
    let q16 = f32_slice_to_f16(query);
    store.attend(layer, &q16, range)
}

/// Convenience: [`attend_via_store`] then convert output lanes to f32.
pub fn attend_via_store_f32(
    store: &dyn KvStore,
    layer: usize,
    query: &[f32],
    range: Range<usize>,
) -> Vec<f32> {
    let res = attend_via_store(store, layer, query, range);
    f16_slice_to_f32(&res.output)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::kv::{AttentionResult, KvRecall, KvStore};
    use std::collections::HashMap;

    /// Minimal in-memory KvStore for call-site tests (prefer [`crate::kv::RamKvStore`] in integration).
    struct MemKv {
        dim: usize,
        /// layer → list of (token, k, v) in append order
        layers: HashMap<usize, Vec<(usize, Vec<F16>, Vec<F16>)>>,
    }

    impl MemKv {
        fn new(dim: usize) -> Self {
            MemKv {
                dim,
                layers: HashMap::new(),
            }
        }
    }

    impl KvStore for MemKv {
        fn append(&mut self, layer: usize, token: usize, key: &[F16], value: &[F16]) {
            assert_eq!(key.len(), self.dim);
            assert_eq!(value.len(), self.dim);
            self.layers
                .entry(layer)
                .or_default()
                .push((token, key.to_vec(), value.to_vec()));
        }

        fn recall(&self, layer: usize, range: Range<usize>) -> KvRecall {
            let empty = Vec::new();
            let entries = self.layers.get(&layer).unwrap_or(&empty);
            let start = range.start.min(entries.len());
            let end = range.end.min(entries.len());
            let slice = &entries[start..end];
            let mut keys = Vec::with_capacity(slice.len() * self.dim);
            let mut values = Vec::with_capacity(slice.len() * self.dim);
            for (_, k, v) in slice {
                keys.extend_from_slice(k);
                values.extend_from_slice(v);
            }
            KvRecall {
                keys,
                values,
                tokens: slice.len(),
                dim: self.dim,
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
        }

        fn truncate(&mut self, new_len: usize) {
            for entries in self.layers.values_mut() {
                if entries.len() > new_len {
                    entries.truncate(new_len);
                }
            }
        }

        fn attend(&self, layer: usize, query: &[F16], range: Range<usize>) -> AttentionResult {
            let _ = (layer, range, query);
            AttentionResult {
                output: vec![0; query.len()],
            }
        }
    }

    #[test]
    fn softmax_sums_to_one() {
        let mut v = [1.0f32, 2.0, 3.0];
        softmax_inplace(&mut v);
        let s: f32 = v.iter().sum();
        assert!((s - 1.0).abs() < 1e-5);
        assert!(v[2] > v[1] && v[1] > v[0]);
    }

    #[test]
    fn f16_roundtrip_reasonable() {
        for &x in &[0.0f32, 1.0, -1.0, 0.5, 3.14159, 65504.0] {
            let h = f32_to_f16_bits(x);
            let y = f16_bits_to_f32(h);
            let tol = if x.abs() > 1000.0 { 32.0 } else { 1e-2 };
            assert!((x - y).abs() <= tol, "x={x} y={y}");
        }
    }

    #[test]
    fn single_token_self_attention_is_value() {
        // past empty → softmax([q·k]) = 1 → out = v
        let n_heads = 1;
        let n_kv = 1;
        let head_dim = 4;
        let q = [1.0f32, 0.0, 0.0, 0.0];
        let k = [1.0f32, 0.0, 0.0, 0.0];
        let v = [0.25f32, 0.5, 0.75, 1.0];
        let mut scores = [0.0f32; 1];
        let mut out = [0.0f32; 4];
        multi_head_attend_decode(
            &q, &k, &v, &[], &[], n_heads, n_kv, head_dim, &mut scores, &mut out,
        );
        for i in 0..4 {
            assert!((out[i] - v[i]).abs() < 1e-5);
        }
    }

    #[test]
    fn two_token_causal_prefers_matching_key() {
        let n_heads = 1;
        let n_kv = 1;
        let head_dim = 2;
        // past token 0: k=[1,0] v=[10,0]; new: k=[0,1] v=[0,20]
        let past_k = [1.0f32, 0.0];
        let past_v = [10.0f32, 0.0];
        let k_new = [0.0f32, 1.0];
        let v_new = [0.0f32, 20.0];
        // query aligned with new key
        let q = [0.0f32, 10.0];
        let mut scores = [0.0f32; 2];
        let mut out = [0.0f32; 2];
        multi_head_attend_decode(
            &q, &k_new, &v_new, &past_k, &past_v, n_heads, n_kv, head_dim, &mut scores, &mut out,
        );
        // Should put most mass on token 1 → out ≈ v_new
        assert!(out[1] > out[0], "out={out:?}");
        assert!((out[1] - 20.0).abs() < 1.0, "out={out:?}");
    }

    #[test]
    fn append_kv_call_site_stores_f16() {
        let mut kv = MemKv::new(2);
        let key = [1.0f32, -0.5];
        let val = [0.25f32, 0.75];
        append_kv(&mut kv, 0, 0, &key, &val);
        let entry = &kv.layers.get(&0).unwrap()[0];
        assert_eq!(entry.0, 0);
        assert_eq!(entry.1.len(), 2);
        assert_eq!(entry.2.len(), 2);
        let k_back = f16_slice_to_f32(&entry.1);
        assert!((k_back[0] - 1.0).abs() < 1e-2);
        let (pk, pv) = recall_kv_f32(&kv, 0, 0..1);
        assert_eq!(pk.len(), 2);
        assert_eq!(pv.len(), 2);
    }

    #[test]
    fn project_qkv_dims() {
        let hidden = 4;
        let n_heads = 2;
        let n_kv = 1;
        let head_dim = 2;
        let wq = vec![0.0f32; hidden * n_heads * head_dim];
        let wk = vec![0.0f32; hidden * n_kv * head_dim];
        let wv = vec![0.0f32; hidden * n_kv * head_dim];
        // identity-ish: first row of each maps x[0]
        // leave zeros — just check shapes don't panic
        let x = [1.0f32, 0.0, 0.0, 0.0];
        let mut q = vec![0.0f32; n_heads * head_dim];
        let mut k = vec![0.0f32; n_kv * head_dim];
        let mut v = vec![0.0f32; n_kv * head_dim];
        project_qkv(
            &x,
            WeightView::new(&wq, hidden, n_heads * head_dim),
            WeightView::new(&wk, hidden, n_kv * head_dim),
            WeightView::new(&wv, hidden, n_kv * head_dim),
            &mut q,
            &mut k,
            &mut v,
        );
        assert_eq!(q.len(), 4);
        assert_eq!(k.len(), 2);
    }
}
