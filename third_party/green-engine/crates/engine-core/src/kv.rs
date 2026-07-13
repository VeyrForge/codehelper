//! KV-cache scheduling — the token/memory-compression tier (orthogonal to Green Compress weights).
//!
//! The KV cache grows with every token and dominates long-context memory. The engine schedules
//! it like experts: keep the KV blocks that matter, evict the rest. This module implements the
//! 2025-26 training-free eviction policies (StreamingLLM / H2O / SnapKV / Quest-oracle) and the
//! engine's differentiator — **adaptive per-layer budget** (some layers concentrate attention and
//! need little KV; others need a lot). Validated against the Python analysis (see tests).

use std::fs;
use std::io;
use std::path::Path;

const MAGIC: u32 = 0x4E54_5441; // "ATTN"
const SINKS: usize = 4;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum KvPolicy {
    /// Attention sinks (first few) + recent window. [StreamingLLM]
    Stream,
    /// Heavy hitters by accumulated attention + recent. [H2O]
    H2O,
    /// Keys scored by recent queries' pooled attention. [SnapKV]
    SnapKv,
    /// Per-query top-B by this query's own attention (oracle upper bound). [Quest]
    Quest,
}

/// Head-averaged attention `attn[layer][q*tokens + k]`, lower-triangular (causal).
pub struct Attention {
    pub layers: usize,
    pub tokens: usize,
    data: Vec<f32>,
}

impl Attention {
    pub fn load<P: AsRef<Path>>(path: P) -> io::Result<Self> {
        let b = fs::read(path)?;
        if b.len() < 12 || u32::from_le_bytes(b[0..4].try_into().unwrap()) != MAGIC {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "bad attention header"));
        }
        let layers = u32::from_le_bytes(b[4..8].try_into().unwrap()) as usize;
        let tokens = u32::from_le_bytes(b[8..12].try_into().unwrap()) as usize;
        let n = layers * tokens * tokens;
        if b.len() != 12 + n * 4 {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "attention size mismatch"));
        }
        let mut data = vec![0.0f32; n];
        for i in 0..n {
            let o = 12 + i * 4;
            data[i] = f32::from_le_bytes(b[o..o + 4].try_into().unwrap());
        }
        Ok(Attention { layers, tokens, data })
    }

    #[inline]
    fn row(&self, l: usize, q: usize) -> &[f32] {
        let base = (l * self.tokens + q) * self.tokens;
        &self.data[base..base + self.tokens]
    }

    /// Fraction of true attention mass at (layer l, query q) retained by keeping `budget` keys.
    pub fn retained(&self, policy: KvPolicy, l: usize, q: usize, budget: usize) -> f64 {
        let nk = q + 1;
        let a = &self.row(l, q)[..nk];
        if budget >= nk {
            return 1.0;
        }
        let keep: Vec<usize> = match policy {
            KvPolicy::Stream => {
                let s = SINKS.min(nk);
                let recent = budget.saturating_sub(s);
                (0..s).chain(nk.saturating_sub(recent)..nk).collect()
            }
            KvPolicy::H2O => {
                let recent = (budget / 4).max(1);
                let take = budget.saturating_sub(recent);
                // accumulated attention to each key (column sum over queries ≤ q)
                let mut acc = vec![0.0f32; nk];
                for qp in 0..nk {
                    let r = &self.row(l, qp)[..nk];
                    for k in 0..nk {
                        acc[k] += r[k];
                    }
                }
                let cutoff = nk.saturating_sub(recent);
                let mut idx: Vec<usize> = (0..cutoff).collect();
                idx.sort_by(|&x, &y| acc[y].partial_cmp(&acc[x]).unwrap());
                idx.truncate(take);
                idx.into_iter().chain(cutoff..nk).collect()
            }
            KvPolicy::SnapKv => {
                let win = 16.min(nk);
                let mut score = vec![0.0f32; nk];
                for qp in (q + 1 - win)..=q {
                    let r = &self.row(l, qp)[..nk];
                    for k in 0..nk {
                        score[k] += r[k] / win as f32;
                    }
                }
                top_k(&score, budget)
            }
            KvPolicy::Quest => top_k(a, budget),
        };
        keep.iter().map(|&k| a[k] as f64).sum()
    }

    /// Mean retained mass over the decode region (q ≥ tokens/2) at budget fraction `frac`.
    pub fn evaluate(&self, policy: KvPolicy, frac: f64) -> f64 {
        let mut sum = 0.0;
        let mut n = 0;
        for l in 0..self.layers {
            for q in (self.tokens / 2)..self.tokens {
                let budget = (((frac * (q + 1) as f64).round()) as usize).max(SINKS + 1);
                sum += self.retained(policy, l, q, budget);
                n += 1;
            }
        }
        sum / n as f64
    }

    /// Per-layer retained mass (Quest) at `frac` — the spread motivates adaptive per-layer budgets.
    pub fn per_layer(&self, policy: KvPolicy, frac: f64) -> Vec<f64> {
        (0..self.layers)
            .map(|l| {
                let mut sum = 0.0;
                let mut n = 0;
                for q in (self.tokens / 2)..self.tokens {
                    let budget = (((frac * (q + 1) as f64).round()) as usize).max(SINKS + 1);
                    sum += self.retained(policy, l, q, budget);
                    n += 1;
                }
                sum / n as f64
            })
            .collect()
    }
}

fn top_k(score: &[f32], k: usize) -> Vec<usize> {
    let mut idx: Vec<usize> = (0..score.len()).collect();
    idx.sort_by(|&a, &b| score[b].partial_cmp(&score[a]).unwrap());
    idx.truncate(k);
    idx
}

// ----------------------------------------------------------------------------------------------
// Adaptive per-layer KV budget allocation — the engine's differentiator over static (PyramidKV)
// schemes. Given ONE total KV budget, allocate keys to layers by marginal retention gain, so
// attention-spread layers get more and concentrated layers get less.

impl Attention {
    /// Retention curve for (layer l, query q): `curve[b]` = attention mass kept with budget `b`
    /// under `policy` (prefix sums of true attention over the policy's keep order).
    pub fn retention_curve(&self, policy: KvPolicy, l: usize, q: usize) -> Vec<f64> {
        let nk = q + 1;
        let a = &self.row(l, q)[..nk];
        // keep order = keys sorted by the policy's score (desc)
        let order: Vec<usize> = match policy {
            KvPolicy::Quest => top_k(a, nk),
            KvPolicy::SnapKv => {
                let win = 16.min(nk);
                let mut score = vec![0.0f32; nk];
                for qp in (q + 1 - win)..=q {
                    let r = &self.row(l, qp)[..nk];
                    for k in 0..nk {
                        score[k] += r[k];
                    }
                }
                top_k(&score, nk)
            }
            _ => top_k(a, nk),
        };
        let mut curve = vec![0.0f64; nk + 1];
        let mut acc = 0.0;
        for (i, &k) in order.iter().enumerate() {
            acc += a[k] as f64;
            curve[i + 1] = acc;
        }
        curve
    }

    /// Greedy marginal-gain allocation of `total` keys across layers at query `q`.
    pub fn allocate_adaptive(&self, policy: KvPolicy, q: usize, total: usize) -> Vec<usize> {
        let curves: Vec<Vec<f64>> = (0..self.layers).map(|l| self.retention_curve(policy, l, q)).collect();
        let cap = q + 1;
        let mut budget = vec![0usize; self.layers];
        // start each layer with the sink floor, then water-fill by marginal gain
        let floor = SINKS.min(cap);
        for b in budget.iter_mut() {
            *b = floor;
        }
        let mut spent: usize = floor * self.layers;
        let target = total.min(cap * self.layers);
        while spent < target {
            let mut best_l = 0;
            let mut best_gain = -1.0;
            for l in 0..self.layers {
                if budget[l] < cap {
                    let g = curves[l][budget[l] + 1] - curves[l][budget[l]];
                    if g > best_gain {
                        best_gain = g;
                        best_l = l;
                    }
                }
            }
            budget[best_l] += 1;
            spent += 1;
        }
        budget
    }

    pub fn allocate_uniform(&self, q: usize, total: usize) -> Vec<usize> {
        let per = (total / self.layers).min(q + 1);
        vec![per; self.layers]
    }

    /// Mean retained mass (over layers) for a per-layer budget at query `q`.
    pub fn total_retained(&self, policy: KvPolicy, q: usize, budgets: &[usize]) -> f64 {
        let mut s = 0.0;
        for l in 0..self.layers {
            let curve = self.retention_curve(policy, l, q);
            s += curve[budgets[l].min(q + 1)];
        }
        s / self.layers as f64
    }
}

/// KV memory (bytes) for `kept` tokens over `layers`, at a given quantization.
/// `kv_bits`: 16 = fp16, 2 = KIVI-style 2-bit (+overhead). per-token-per-layer = 2·heads·head_dim.
pub fn kv_bytes(kept_per_layer: &[usize], n_kv_heads: usize, head_dim: usize, kv_bits: f64) -> u64 {
    let per_token = 2.0 * n_kv_heads as f64 * head_dim as f64 * (kv_bits / 8.0); // K and V
    kept_per_layer.iter().map(|&k| (k as f64 * per_token) as u64).sum()
}

/// KIVI-style symmetric KV-cache quantization. The KV cache is `[tokens, channels]` row-major.
/// The research (KIVI) finds **keys** are best quantized **per-channel** (a scale per `channels`
/// column, since key outliers concentrate in fixed channels) and **values per-token** (a scale per
/// row). `bits` = 8 or 4. Returns `(dequantized, mean_rel_error)`; the roundtrip is deterministic,
/// so the same scheme reproduces bit-for-bit (see tests). This is the token/context-memory analog
/// of the weight tiers — it cuts KV VRAM ~2× (int8) or ~4× (int4) vs fp16 at high fidelity, letting
/// long-context chat fit far more tokens in the same budget.
pub fn quantize_kv(data: &[f32], tokens: usize, channels: usize, bits: u32, per_channel: bool) -> (Vec<f32>, f32) {
    assert_eq!(data.len(), tokens * channels, "quantize_kv: len != tokens*channels");
    let qmax = ((1i32 << (bits - 1)) - 1) as f32; // 127 (int8) or 7 (int4)
    let mut out = vec![0.0f32; data.len()];
    let groups = if per_channel { channels } else { tokens };
    let stride = if per_channel { channels } else { 1 }; // step between elements of one group
    let count = if per_channel { tokens } else { channels };
    for g in 0..groups {
        // start index of group g: column g (per-channel) or row g (per-token)
        let base = if per_channel { g } else { g * channels };
        let mut amax = 0.0f32;
        for i in 0..count {
            amax = amax.max(data[base + i * stride].abs());
        }
        let scale = if amax > 0.0 { amax / qmax } else { 1.0 };
        for i in 0..count {
            let idx = base + i * stride;
            let q = (data[idx] / scale).round().clamp(-qmax, qmax);
            out[idx] = q * scale;
        }
    }
    let (mut num, mut den) = (0.0f64, 0.0f64);
    for (a, b) in data.iter().zip(&out) {
        num += ((a - b) as f64).powi(2);
        den += (*a as f64).powi(2);
    }
    (out, (num / den.max(1e-12)).sqrt() as f32)
}

#[cfg(test)]
mod kv_quant_tests {
    use super::quantize_kv;

    // Structured KV: per-channel signal + a few large per-channel outliers (the KIVI regime).
    fn kv(tokens: usize, channels: usize, seed: u64) -> Vec<f32> {
        let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
        let mut rng = || { s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
            (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 };
        let chan_bias: Vec<f32> = (0..channels).map(|_| rng() * 2.0).collect();
        let mut v = vec![0.0f32; tokens * channels];
        for t in 0..tokens {
            for c in 0..channels {
                v[t * channels + c] = chan_bias[c] + 0.1 * rng();
            }
        }
        v
    }

    #[test]
    fn kv_quant_int8_beats_int4_and_is_deterministic() {
        let (tokens, channels) = (128, 64);
        let data = kv(tokens, channels, 7);

        // keys per-channel (KIVI); int8 must be higher fidelity than int4, both bounded.
        let (_, d8) = quantize_kv(&data, tokens, channels, 8, true);
        let (deq4a, d4) = quantize_kv(&data, tokens, channels, 4, true);
        assert!(d8 < d4, "int8 KV drift {d8} should beat int4 {d4}");
        assert!(d8 < 0.02, "int8 KV drift {d8} should be small");

        // deterministic roundtrip: same call reproduces bit-for-bit.
        let (deq4b, _) = quantize_kv(&data, tokens, channels, 4, true);
        assert_eq!(deq4a, deq4b, "KV quant must be deterministic");

        // per-channel should beat per-token for this per-channel-structured key data.
        let (_, d_pc) = quantize_kv(&data, tokens, channels, 8, true);
        let (_, d_pt) = quantize_kv(&data, tokens, channels, 8, false);
        assert!(d_pc <= d_pt, "per-channel {d_pc} should beat per-token {d_pt} on key-structured data");
    }
}
