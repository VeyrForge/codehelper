//! Rotary position embeddings (RoPE) for Llama-like attention — CPU f32.
//!
//! ## Pairing mode (critical for GGUF)
//!
//! HuggingFace Llama uses **NeoX / rotate-half** `(i, i+head_dim/2)`. llama.cpp
//! converts Q/K weights with `LlamaModel.permute` so GGUF activations expect
//! **NORM / GPT-J interleaved** `(2i, 2i+1)` — see `LLAMA_ROPE_TYPE_NORM` for
//! `LLM_ARCH_LLAMA`. Applying NeoX RoPE to permuted GGUF weights matches at
//! position 0 (identity) then drifts as context grows.
//!
//! **Call after** QKV projection / before scores — on Q and K only (not V).
//! Layout must be `[seq × n_heads × head_dim]` (decode: `seq == 1`).
//!
//! ## Scaling (P1.3)
//! Honor GGUF `rope.scaling.*` when present. **Do not** stack an extra CLI
//! `--rope-scaling` on models that already ship native-long metadata (Llama 3.1/3.2
//! with large `freq_base` / `context_length`). YaRN / NTK / linear apply **only**
//! when the GGUF scaling type says so, or when deliberately extending a short-ctx base.

use crate::gguf_io::{GgufFile, GgufValue};

/// Which head dims are paired for the complex rotation (llama.cpp rope type).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum RopeMode {
    /// Interleaved pairs `(2i, 2i+1)` — `LLAMA_ROPE_TYPE_NORM` (Llama GGUF default).
    #[default]
    Norm,
    /// Rotate-half pairs `(i, i+head_dim/2)` — `LLAMA_ROPE_TYPE_NEOX` (Falcon, GPT-NeoX, HF raw).
    Neox,
}

/// How frequencies are adjusted beyond the trained window.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum RopeScaling {
    /// No extra scaling (use `theta` + `freq_scale` only). Native-long models.
    None,
    /// Linear position / frequency scale by `factor`.
    Linear {
        factor: f32,
    },
    /// NTK-aware: inflate `theta` by `factor` (common short→long extend).
    Ntk {
        factor: f32,
    },
    /// YaRN (GGUF yarn fields). Applied only when metadata requests it.
    Yarn {
        factor: f32,
        orig_ctx: u32,
        beta_fast: f32,
        beta_slow: f32,
        ext_factor: f32,
        attn_factor: f32,
    },
    /// Llama-3 frequency-band scaling (low/high factors).
    Llama3 {
        factor: f32,
        orig_ctx: u32,
        low_freq_factor: f32,
        high_freq_factor: f32,
    },
}

impl Default for RopeScaling {
    fn default() -> Self {
        RopeScaling::None
    }
}

/// RoPE configuration for one attention geometry.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct RopeConfig {
    /// Per-head dimension (must be even).
    pub head_dim: usize,
    /// Base frequency (`llama.rope.freq_base`, default 10000; Llama-3.2 uses 500000).
    pub theta: f32,
    /// Optional frequency scale (`llama.rope.freq_scale`, default 1.0).
    /// Already baked into GGUF when writers export scaled models — do not multiply again
    /// with a CLI override on native-long packs.
    pub freq_scale: f32,
    /// Trained / declared context length from GGUF (`{arch}.context_length`), if known.
    pub context_length: Option<u32>,
    /// Parsed `{arch}.rope.scaling.*` (None when absent or type=none).
    pub scaling: RopeScaling,
    /// Dim pairing — must match GGUF Q/K permute + llama.cpp `rope_type`.
    pub mode: RopeMode,
}

impl RopeConfig {
    /// Llama GGUF defaults: NORM interleaved pairs (after convert permute).
    pub fn llama(head_dim: usize) -> Self {
        RopeConfig {
            head_dim,
            theta: 10_000.0,
            freq_scale: 1.0,
            context_length: None,
            scaling: RopeScaling::None,
            mode: RopeMode::Norm,
        }
    }

    /// Read rope params from GGUF KV using `arch` prefix (e.g. `"llama"`).
    pub fn from_gguf(g: &GgufFile, arch: &str, n_heads: usize, n_embd: usize) -> Self {
        let head_dim = g
            .get(&format!("{arch}.rope.dimension_count"))
            .and_then(GgufValue::as_u64)
            .map(|v| v as usize)
            .or_else(|| {
                if n_heads > 0 && n_embd % n_heads == 0 {
                    Some(n_embd / n_heads)
                } else {
                    None
                }
            })
            .unwrap_or(64);
        // llama.cpp / GGUF use `llama.rope.freq_base`; some writers use `llama.rope_freq_base`.
        let theta = g
            .get(&format!("{arch}.rope.freq_base"))
            .or_else(|| g.get(&format!("{arch}.rope_freq_base")))
            .and_then(GgufValue::as_f64)
            .unwrap_or(10_000.0) as f32;
        let freq_scale = g
            .get(&format!("{arch}.rope.freq_scale"))
            .or_else(|| g.get(&format!("{arch}.rope_freq_scale")))
            .and_then(GgufValue::as_f64)
            .unwrap_or(1.0) as f32;
        let context_length = g
            .get(&format!("{arch}.context_length"))
            .and_then(GgufValue::as_u64)
            .map(|v| v as u32);
        let scaling = parse_rope_scaling(g, arch, context_length);
        let mode = rope_mode_for_arch(arch);
        RopeConfig {
            head_dim,
            theta,
            freq_scale,
            context_length,
            scaling,
            mode,
        }
    }

    /// Effective theta after NTK inflation (other scaling types keep base theta).
    pub fn effective_theta(&self) -> f32 {
        match self.scaling {
            RopeScaling::Ntk { factor } if factor > 0.0 => self.theta * factor,
            _ => self.theta,
        }
    }
}

fn parse_rope_scaling(g: &GgufFile, arch: &str, ctx: Option<u32>) -> RopeScaling {
    let typ = g
        .get(&format!("{arch}.rope.scaling.type"))
        .or_else(|| g.get(&format!("{arch}.rope.scaling_type")))
        .and_then(GgufValue::as_str)
        .unwrap_or("none")
        .to_ascii_lowercase();
    if typ.is_empty() || typ == "none" {
        return RopeScaling::None;
    }
    let factor = g
        .get(&format!("{arch}.rope.scaling.factor"))
        .or_else(|| g.get(&format!("{arch}.rope.scale_linear")))
        .and_then(GgufValue::as_f64)
        .unwrap_or(1.0) as f32;
    let orig_ctx = g
        .get(&format!("{arch}.rope.scaling.original_context_length"))
        .or_else(|| g.get(&format!("{arch}.rope.scaling.original_context")))
        .and_then(GgufValue::as_u64)
        .map(|v| v as u32)
        .or(ctx)
        .unwrap_or(2048);
    match typ.as_str() {
        "linear" | "scale" => RopeScaling::Linear { factor },
        "ntk" | "ntk-aware" | "ntk_aware" => RopeScaling::Ntk { factor },
        "yarn" => RopeScaling::Yarn {
            factor,
            orig_ctx,
            beta_fast: gguf_f32(g, arch, "rope.scaling.yarn_beta_fast", 32.0),
            beta_slow: gguf_f32(g, arch, "rope.scaling.yarn_beta_slow", 1.0),
            ext_factor: gguf_f32(g, arch, "rope.scaling.yarn_ext_factor", 1.0),
            attn_factor: gguf_f32(g, arch, "rope.scaling.yarn_attn_factor", 1.0),
        },
        "llama3" | "llama-3" => RopeScaling::Llama3 {
            factor,
            orig_ctx,
            low_freq_factor: gguf_f32(g, arch, "rope.scaling.low_freq_factor", 1.0),
            high_freq_factor: gguf_f32(g, arch, "rope.scaling.high_freq_factor", 4.0),
        },
        _ => RopeScaling::None,
    }
}

fn gguf_f32(g: &GgufFile, arch: &str, suffix: &str, default: f32) -> f32 {
    g.get(&format!("{arch}.{suffix}"))
        .and_then(GgufValue::as_f64)
        .map(|v| v as f32)
        .unwrap_or(default)
}

/// Match `llama_model_rope_type` for arches we serve as GGUF.
fn rope_mode_for_arch(arch: &str) -> RopeMode {
    let a = arch.to_ascii_lowercase();
    match a.as_str() {
        // LLAMA_ROPE_TYPE_NEOX in llama.cpp
        "falcon" | "grok" | "dbrx" | "gptneox" | "gpt-neox" | "phi2" | "phi3" | "qwen"
        | "qwen2" | "qwen2moe" | "qwen3" | "stablelm" | "olmoe" | "jamba" => RopeMode::Neox,
        // LLAMA_ROPE_TYPE_NORM (default for llama / mistral / gemma-class GGUF)
        _ => RopeMode::Norm,
    }
}

/// Errors from RoPE apply.
#[derive(Debug, PartialEq)]
pub enum RopeError {
    OddHeadDim(usize),
    Shape { expected: usize, got: usize },
    PosLen { seq: usize, pos: usize },
}

impl std::fmt::Display for RopeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RopeError::OddHeadDim(d) => write!(f, "head_dim {d} must be even"),
            RopeError::Shape { expected, got } => {
                write!(f, "activation length {got}, expected {expected}")
            }
            RopeError::PosLen { seq, pos } => {
                write!(f, "positions len {pos} != seq {seq}")
            }
        }
    }
}

impl std::error::Error for RopeError {}

/// Precomputed cos/sin tables for fast RoPE.
pub struct RopeCache {
    pub cfg: RopeConfig,
    cos: Vec<f32>,
    sin: Vec<f32>,
    max_seq: usize,
}

impl RopeCache {
    pub fn new(cfg: RopeConfig, max_seq: usize) -> Result<Self, RopeError> {
        if cfg.head_dim % 2 != 0 {
            return Err(RopeError::OddHeadDim(cfg.head_dim));
        }
        let half = cfg.head_dim / 2;
        let mut cos = vec![0.0f32; max_seq.saturating_mul(half)];
        let mut sin = vec![0.0f32; max_seq.saturating_mul(half)];
        for p in 0..max_seq {
            for i in 0..half {
                let (c, s) = rope_pair(p as u32, i, &cfg);
                cos[p * half + i] = c;
                sin[p * half + i] = s;
            }
        }
        Ok(RopeCache {
            cfg,
            cos,
            sin,
            max_seq,
        })
    }

    #[inline]
    fn pair(&self, pos: u32, i: usize) -> (f32, f32) {
        let p = pos as usize;
        let half = self.cfg.head_dim / 2;
        if p < self.max_seq {
            (self.cos[p * half + i], self.sin[p * half + i])
        } else {
            rope_pair(pos, i, &self.cfg)
        }
    }
}

/// Cos/sin for one (position, frequency-index) — **f64** angle math for long positions.
#[inline]
fn rope_pair(pos: u32, i: usize, cfg: &RopeConfig) -> (f32, f32) {
    let dim = cfg.head_dim as f64;
    let theta = cfg.effective_theta() as f64;
    let inv_freq = 1.0 / theta.powf((2 * i) as f64 / dim);
    let mut freq = inv_freq;

    // YaRN / Llama3 band scaling adjusts per-dim frequency; linear scales position.
    let pos_f = match cfg.scaling {
        RopeScaling::Linear { factor } if factor > 0.0 => (pos as f64) / (factor as f64),
        RopeScaling::Yarn {
            factor,
            orig_ctx,
            beta_fast,
            beta_slow,
            ext_factor,
            ..
        } => {
            freq = yarn_freq(freq, i, cfg.head_dim, factor, orig_ctx, beta_fast, beta_slow, ext_factor);
            pos as f64
        }
        RopeScaling::Llama3 {
            factor,
            orig_ctx,
            low_freq_factor,
            high_freq_factor,
        } => {
            freq = llama3_freq(freq, factor, orig_ctx, low_freq_factor, high_freq_factor);
            pos as f64
        }
        _ => pos as f64,
    };

    let angle = (pos_f * cfg.freq_scale as f64) * freq;
    (angle.cos() as f32, angle.sin() as f32)
}

/// YaRN wavelength ramp (llama.cpp-compatible shape; only when metadata says yarn).
fn yarn_freq(
    inv_freq: f64,
    i: usize,
    head_dim: usize,
    factor: f32,
    orig_ctx: u32,
    beta_fast: f32,
    beta_slow: f32,
    ext_factor: f32,
) -> f64 {
    if factor <= 1.0 {
        return inv_freq;
    }
    let _ = (i, head_dim, orig_ctx, beta_fast, beta_slow, ext_factor);
    // Conservative: scale by 1/factor on the inv-freq (NTK-ish yarn default path).
    // Full wavelength-ramp can land once we have yarn-scaled fixture GGUFs to golden-test.
    inv_freq / factor as f64
}

fn llama3_freq(
    inv_freq: f64,
    factor: f32,
    orig_ctx: u32,
    low_freq_factor: f32,
    high_freq_factor: f32,
) -> f64 {
    if factor <= 1.0 || orig_ctx == 0 {
        return inv_freq;
    }
    // Wavelength λ = 2π / (pos_scale * inv_freq) — use inv_freq vs 2π/orig_ctx bands.
    let wave = if inv_freq > 0.0 {
        2.0 * std::f64::consts::PI / inv_freq
    } else {
        return inv_freq;
    };
    let low = (orig_ctx as f64) / (high_freq_factor as f64);
    let high = (orig_ctx as f64) / (low_freq_factor as f64).max(1e-6);
    if wave < low {
        inv_freq / factor as f64
    } else if wave > high {
        inv_freq
    } else {
        let smooth = (high - wave) / (high - low).max(1e-6);
        (1.0 - smooth) * inv_freq / factor as f64 + smooth * inv_freq
    }
}

/// Apply RoPE in-place to Q or K activations.
///
/// Layout: `x` is `[seq * n_heads * head_dim]` row-major (token-major, then head, then dim).
/// `positions[t]` is the absolute position for token `t`.
/// Pairing follows [`RopeConfig::mode`] (Norm for Llama GGUF, Neox for Falcon/HF-raw).
pub fn apply_rope_inplace(
    x: &mut [f32],
    positions: &[u32],
    n_heads: usize,
    cfg: &RopeConfig,
) -> Result<(), RopeError> {
    apply_rope_inplace_cached(x, positions, n_heads, cfg, None)
}

/// Same as [`apply_rope_inplace`] but reuses a [`RopeCache`] when provided.
pub fn apply_rope_inplace_cached(
    x: &mut [f32],
    positions: &[u32],
    n_heads: usize,
    cfg: &RopeConfig,
    cache: Option<&RopeCache>,
) -> Result<(), RopeError> {
    if cfg.head_dim % 2 != 0 {
        return Err(RopeError::OddHeadDim(cfg.head_dim));
    }
    let seq = positions.len();
    let expected = seq.saturating_mul(n_heads).saturating_mul(cfg.head_dim);
    if x.len() != expected {
        return Err(RopeError::Shape {
            expected,
            got: x.len(),
        });
    }
    let half = cfg.head_dim / 2;
    for (t, &pos) in positions.iter().enumerate() {
        for h in 0..n_heads {
            let base = (t * n_heads + h) * cfg.head_dim;
            for i in 0..half {
                let (cos, sin) = match cache {
                    Some(c) => c.pair(pos, i),
                    None => rope_pair(pos, i, cfg),
                };
                let (i0, i1) = match cfg.mode {
                    // llama.cpp LLAMA_ROPE_TYPE_NORM / GPT-J
                    RopeMode::Norm => (base + 2 * i, base + 2 * i + 1),
                    // llama.cpp LLAMA_ROPE_TYPE_NEOX / HF rotate-half
                    RopeMode::Neox => (base + i, base + i + half),
                };
                let x0 = x[i0];
                let x1 = x[i1];
                x[i0] = x0 * cos - x1 * sin;
                x[i1] = x0 * sin + x1 * cos;
            }
        }
    }
    Ok(())
}

/// Convenience: build cache from GGUF KV (loader hook).
pub fn cache_from_gguf(
    g: &GgufFile,
    arch: &str,
    n_heads: usize,
    n_embd: usize,
    max_seq: usize,
) -> Result<RopeCache, RopeError> {
    let cfg = RopeConfig::from_gguf(g, arch, n_heads, n_embd);
    RopeCache::new(cfg, max_seq)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn identity_at_position_zero() {
        let cfg = RopeConfig::llama(4);
        let mut x = vec![1.0f32, 2.0, 3.0, 4.0]; // 1 tok, 1 head
        let before = x.clone();
        apply_rope_inplace(&mut x, &[0], 1, &cfg).unwrap();
        for (a, b) in x.iter().zip(&before) {
            assert!((a - b).abs() < 1e-6, "{a} vs {b}");
        }
    }

    #[test]
    fn rotation_is_norm_preserving() {
        let cfg = RopeConfig {
            head_dim: 4,
            theta: 10_000.0,
            freq_scale: 1.0,
            context_length: None,
            scaling: RopeScaling::None,
            mode: RopeMode::Norm,
        };
        let mut x = vec![0.5f32, -1.0, 2.0, 0.25];
        let n0: f32 = x.iter().map(|v| v * v).sum::<f32>().sqrt();
        apply_rope_inplace(&mut x, &[3], 1, &cfg).unwrap();
        let n1: f32 = x.iter().map(|v| v * v).sum::<f32>().sqrt();
        assert!((n0 - n1).abs() < 1e-5, "norm {n0} → {n1}");
    }

    #[test]
    fn long_position_finite_and_norm_preserving() {
        // Llama-3.2-class theta; positions past 512 must stay finite (f64 angles).
        let cfg = RopeConfig {
            head_dim: 64,
            theta: 500_000.0,
            freq_scale: 1.0,
            context_length: Some(131_072),
            scaling: RopeScaling::None,
            mode: RopeMode::Norm,
        };
        let mut x = vec![0.0f32; 64];
        for (i, v) in x.iter_mut().enumerate() {
            *v = (i as f32) * 0.01;
        }
        let n0: f32 = x.iter().map(|v| v * v).sum::<f32>().sqrt();
        for &pos in &[512u32, 4096, 8192, 65536] {
            let mut y = x.clone();
            apply_rope_inplace(&mut y, &[pos], 1, &cfg).unwrap();
            assert!(y.iter().all(|v| v.is_finite()), "non-finite at pos {pos}");
            let n1: f32 = y.iter().map(|v| v * v).sum::<f32>().sqrt();
            assert!((n0 - n1).abs() < 1e-3, "norm drift at {pos}: {n0} → {n1}");
        }
    }

    #[test]
    fn norm_pairs_adjacent_dims() {
        // head_dim=4 Norm: pairs (0,1) and (2,3) — not NeoX (0,2)/(1,3).
        let cfg = RopeConfig {
            head_dim: 4,
            theta: 10_000.0,
            freq_scale: 1.0,
            context_length: None,
            scaling: RopeScaling::None,
            mode: RopeMode::Norm,
        };
        let mut x = vec![1.0f32, 0.0, 0.0, 0.0];
        apply_rope_inplace(&mut x, &[1], 1, &cfg).unwrap();
        assert!(x[2].abs() < 1e-6 && x[3].abs() < 1e-6, "odd pair should stay 0: {x:?}");
        assert!((x[0] * x[0] + x[1] * x[1] - 1.0).abs() < 1e-5, "pair norm: {x:?}");
    }

    #[test]
    fn neox_pairs_first_with_third() {
        let cfg = RopeConfig {
            head_dim: 4,
            theta: 10_000.0,
            freq_scale: 1.0,
            context_length: None,
            scaling: RopeScaling::None,
            mode: RopeMode::Neox,
        };
        let mut x = vec![1.0f32, 0.0, 0.0, 0.0];
        apply_rope_inplace(&mut x, &[1], 1, &cfg).unwrap();
        assert!(x[1].abs() < 1e-6 && x[3].abs() < 1e-6, "odd dims should stay 0: {x:?}");
        assert!((x[0] * x[0] + x[2] * x[2] - 1.0).abs() < 1e-5, "pair norm: {x:?}");
    }

    #[test]
    fn multi_head_layout() {
        let cfg = RopeConfig::llama(2);
        let mut x = vec![
            1.0, 0.0, // t0 h0
            0.0, 1.0, // t0 h1
            1.0, 0.0, // t1 h0
            0.0, 1.0, // t1 h1
        ];
        apply_rope_inplace(&mut x, &[0, 1], 2, &cfg).unwrap();
        assert!((x[0] - 1.0).abs() < 1e-5 && x[1].abs() < 1e-5);
        let n: f32 = (x[4] * x[4] + x[5] * x[5]).sqrt();
        assert!((n - 1.0).abs() < 1e-5, "rotated head norm {n}");
    }

    #[test]
    fn cache_matches_uncached() {
        let cfg = RopeConfig::llama(8);
        let cache = RopeCache::new(cfg, 16).unwrap();
        let pos = [0u32, 3, 7, 15];
        let mut a = vec![0.1f32; 4 * 2 * 8];
        for (i, v) in a.iter_mut().enumerate() {
            *v = (i as f32) * 0.01;
        }
        let mut b = a.clone();
        apply_rope_inplace(&mut a, &pos, 2, &cfg).unwrap();
        apply_rope_inplace_cached(&mut b, &pos, 2, &cfg, Some(&cache)).unwrap();
        for (x, y) in a.iter().zip(&b) {
            assert!((x - y).abs() < 1e-5);
        }
    }

    #[test]
    fn ntk_changes_effective_theta() {
        let mut cfg = RopeConfig::llama(8);
        cfg.scaling = RopeScaling::Ntk { factor: 2.0 };
        assert!((cfg.effective_theta() - 20_000.0).abs() < 1e-3);
    }
}
