//! Token sampling for dense generate (P1.5).
//!
//! Chain: light presence/frequency (+ optional near-1.0 repeat) → temperature /
//! greedy → min_p → top_p → multinomial.
//!
//! Benches keep [`SampleParams::greedy`] (`temperature = 0`). Chat defaults use
//! `min_p` and leave presence/frequency off unless the caller enables them.
//! Prefer those over heavy `repetition_penalty` (keep ≈1.0–1.05).

use std::collections::HashMap;

/// Sampling knobs for [`crate::forward::DenseForward::generate_paged`].
#[derive(Clone, Debug, PartialEq)]
pub struct SampleParams {
    /// `<= 0` → greedy argmax after penalties (bench default).
    pub temperature: f32,
    /// Nucleus sampling; `1.0` disables. Applied after min_p when sampling.
    pub top_p: f32,
    /// Keep tokens with `p >= min_p * max_p` (≈0.05–0.1 for chat). `0.0` disables.
    pub min_p: f32,
    /// OpenAI-style: subtract once per distinct token that already appeared.
    pub presence_penalty: f32,
    /// OpenAI-style: subtract `frequency_penalty * count(token)`.
    pub frequency_penalty: f32,
    /// HuggingFace-style multiplicative penalty. Prefer ≈1.0; avoid high values.
    pub repetition_penalty: f32,
    /// RNG seed for non-greedy sampling. `None` → derive from token counts + clock-ish mix.
    pub seed: Option<u64>,
}

impl SampleParams {
    /// Fair-bench / CLI default: pure greedy, no penalties.
    pub fn greedy() -> Self {
        SampleParams {
            temperature: 0.0,
            top_p: 1.0,
            min_p: 0.0,
            presence_penalty: 0.0,
            frequency_penalty: 0.0,
            repetition_penalty: 1.0,
            seed: Some(0),
        }
    }

    /// Interactive chat defaults (backlog P1.5): mild temp + min_p + light presence/frequency.
    pub fn chat() -> Self {
        SampleParams {
            temperature: 0.8,
            top_p: 0.95,
            min_p: 0.05,
            presence_penalty: 0.2,
            frequency_penalty: 0.1,
            repetition_penalty: 1.0,
            seed: None,
        }
    }

    pub fn is_greedy(&self) -> bool {
        self.temperature <= 0.0
    }

    pub fn needs_full_logits(&self) -> bool {
        if self.is_greedy() {
            (self.repetition_penalty - 1.0).abs() > 1e-6
                || self.presence_penalty.abs() > 1e-6
                || self.frequency_penalty.abs() > 1e-6
        } else {
            true
        }
    }
}

impl Default for SampleParams {
    fn default() -> Self {
        Self::greedy()
    }
}

/// Tiny xorshift64* — no `rand` dependency.
#[derive(Clone, Debug)]
pub struct XorShift64(u64);

impl XorShift64 {
    pub fn new(seed: u64) -> Self {
        XorShift64(seed | 1)
    }

    pub fn next_u64(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        self.0 = x;
        x
    }

    /// Uniform in `[0, 1)`.
    pub fn next_f32(&mut self) -> f32 {
        (self.next_u64() >> 11) as f32 / ((1u64 << 53) as f32)
    }
}

/// Apply presence / frequency / light repetition penalties in-place.
pub fn apply_penalties(
    logits: &mut [f32],
    prev_tokens: &[u32],
    params: &SampleParams,
) {
    if prev_tokens.is_empty() {
        return;
    }
    let mut counts: HashMap<u32, u32> = HashMap::new();
    for &t in prev_tokens {
        *counts.entry(t).or_insert(0) += 1;
    }
    let rep = params.repetition_penalty;
    let use_rep = (rep - 1.0).abs() > 1e-6;
    let use_pres = params.presence_penalty.abs() > 1e-6;
    let use_freq = params.frequency_penalty.abs() > 1e-6;
    if !use_rep && !use_pres && !use_freq {
        return;
    }
    for (&id, &count) in &counts {
        let i = id as usize;
        if i >= logits.len() {
            continue;
        }
        let mut v = logits[i];
        if use_rep {
            // HF: divide if >0 else multiply — dampens repeats without wrecking grammar at ~1.05.
            if v > 0.0 {
                v /= rep;
            } else {
                v *= rep;
            }
        }
        if use_pres {
            v -= params.presence_penalty;
        }
        if use_freq {
            v -= params.frequency_penalty * count as f32;
        }
        logits[i] = v;
    }
}

fn argmax(logits: &[f32]) -> u32 {
    let mut best_i = 0usize;
    let mut best_v = f32::NEG_INFINITY;
    for (i, &v) in logits.iter().enumerate() {
        if v > best_v {
            best_v = v;
            best_i = i;
        }
    }
    best_i as u32
}

/// Softmax into `probs` (same length as logits). Returns max probability after softmax.
fn softmax_into(logits: &[f32], probs: &mut [f32]) -> f32 {
    let mut m = f32::NEG_INFINITY;
    for &v in logits {
        if v > m {
            m = v;
        }
    }
    let mut sum = 0.0f32;
    for (i, &v) in logits.iter().enumerate() {
        let e = (v - m).exp();
        probs[i] = e;
        sum += e;
    }
    let inv = if sum > 0.0 { 1.0 / sum } else { 0.0 };
    let mut max_p = 0.0f32;
    for p in probs.iter_mut() {
        *p *= inv;
        if *p > max_p {
            max_p = *p;
        }
    }
    max_p
}

/// Filter by min_p and optional top_p; renormalize surviving mass; return candidate (id, p) pairs.
fn filter_candidates(probs: &[f32], min_p: f32, top_p: f32) -> Vec<(u32, f32)> {
    let mut max_p = 0.0f32;
    for &p in probs {
        if p > max_p {
            max_p = p;
        }
    }
    let thresh = if min_p > 0.0 {
        min_p * max_p
    } else {
        0.0
    };
    let mut cands: Vec<(u32, f32)> = probs
        .iter()
        .enumerate()
        .filter_map(|(i, &p)| {
            if p > thresh || (thresh == 0.0 && p > 0.0) {
                Some((i as u32, p))
            } else {
                None
            }
        })
        .collect();
    if cands.is_empty() {
        // Fallback: keep argmax of probs.
        let id = argmax(probs);
        return vec![(id, 1.0)];
    }
    cands.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));
    if top_p < 1.0 - 1e-6 {
        let mut acc = 0.0f32;
        let mut keep = Vec::new();
        for (id, p) in cands {
            keep.push((id, p));
            acc += p;
            if acc >= top_p {
                break;
            }
        }
        cands = keep;
    }
    let sum: f32 = cands.iter().map(|(_, p)| *p).sum();
    if sum > 0.0 {
        for c in &mut cands {
            c.1 /= sum;
        }
    }
    cands
}

fn multinomial(cands: &[(u32, f32)], rng: &mut XorShift64) -> u32 {
    let mut r = rng.next_f32();
    for &(id, p) in cands {
        if r < p {
            return id;
        }
        r -= p;
    }
    cands.last().map(|(id, _)| *id).unwrap_or(0)
}

/// Sample next token id from logits given history and params.
pub fn sample_token(
    logits: &mut [f32],
    prev_tokens: &[u32],
    params: &SampleParams,
    rng: &mut XorShift64,
    scratch_probs: &mut [f32],
) -> u32 {
    apply_penalties(logits, prev_tokens, params);
    if params.is_greedy() {
        return argmax(logits);
    }
    let temp = params.temperature.max(1e-5);
    for v in logits.iter_mut() {
        *v /= temp;
    }
    assert_eq!(logits.len(), scratch_probs.len());
    let _max_p = softmax_into(logits, scratch_probs);
    let cands = filter_candidates(scratch_probs, params.min_p, params.top_p);
    multinomial(&cands, rng)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn greedy_is_argmax() {
        let mut logits = vec![0.1f32, 2.0, 0.5];
        let params = SampleParams::greedy();
        let mut rng = XorShift64::new(1);
        let mut probs = vec![0.0; 3];
        let id = sample_token(&mut logits, &[], &params, &mut rng, &mut probs);
        assert_eq!(id, 1);
    }

    #[test]
    fn presence_penalty_lowers_seen() {
        let mut logits = vec![1.0f32, 1.0];
        let params = SampleParams {
            presence_penalty: 0.5,
            ..SampleParams::greedy()
        };
        apply_penalties(&mut logits, &[0], &params);
        assert!(logits[0] < logits[1]);
    }

    #[test]
    fn min_p_filters_tail() {
        // After softmax of [10, 0, 0] ≈ [1, ~0, ~0]; min_p keeps only peak.
        let probs = [0.95f32, 0.04, 0.01];
        let c = filter_candidates(&probs, 0.1, 1.0);
        assert_eq!(c.len(), 1);
        assert_eq!(c[0].0, 0);
    }

    #[test]
    fn frequency_scales_with_count() {
        let mut logits = vec![0.0f32, 0.0];
        let params = SampleParams {
            frequency_penalty: 0.2,
            ..SampleParams::greedy()
        };
        apply_penalties(&mut logits, &[1, 1, 1], &params);
        assert!((logits[1] - (-0.6)).abs() < 1e-5);
        assert_eq!(logits[0], 0.0);
    }
}
