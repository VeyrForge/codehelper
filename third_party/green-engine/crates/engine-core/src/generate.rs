//! Public dense generate API for `ge run` / `ge chat serve` on `.green` packages.
//!
//! Package-level entry (tokenize + decode text). The weight graph and decode loop live in
//! [`crate::forward::DenseForward`]; this module validates the package and wraps it.
//!
//! Pipeline: [`GreenModel::open`] → optional Instruct chat template → tokenize →
//! [`DenseForward`] → sample (greedy / min_p) → decode text.
//! Decode uses [`crate::kv::PagedRamKvStore`] with a capped hot window.
//!
//! **Warm path:** [`DenseForward`] graphs are cached by dense.gguf path so repeat
//! `generate` / `ge chat` calls skip cold reload. Prefer decode-only metrics
//! ([`GenerateResult::decode_tok_s`]) for kernel benches.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Instant;

use crate::chat::{
    family_for_package, prepare_prompt, render_messages, ChatApplyMode, ChatMessage,
    ChatTemplateFamily,
};
use crate::forward::{DenseForward, ForwardError, GreedyGenerateOut};
use crate::green_model::{GreenModel, GreenModelError, LoadConfig};
use crate::kv::{KvKeyQuant, PagedKvMetrics};
use crate::sample::SampleParams;
use crate::tokenize::{load_for_package, TokenizeError};

/// Env override for MoE RAM expert LRU (default: all experts if tiny, else 8).
/// Default pool is **Global** (KEEP: +2.7–3.2 pp hit% vs PerLayer at CAP 8–10 on OLMoE).
/// `GE_MOE_SLOT_POOL=per` forces legacy per-layer caps.
fn moe_slot_budget(model: &GreenModel) -> crate::paged::SlotBudget {
    use crate::paged::{SlotBudget, SlotPool};
    let pool = match std::env::var("GE_MOE_SLOT_POOL")
        .unwrap_or_else(|_| "global".into())
        .to_ascii_lowercase()
        .as_str()
    {
        "per" | "perlayer" | "layer" => SlotPool::PerLayer,
        _ => SlotPool::Global,
    };
    let layers = model.manifest().layers.unwrap_or(1).max(1) as usize;
    if let Ok(v) = std::env::var("GE_MOE_SLOT_BUDGET") {
        let lower = v.to_ascii_lowercase();
        // Byte budgets need expert stride; only honor slot specs here (`8`, `8s`).
        if !lower.contains('m') && !lower.contains('g') {
            if let Some(b) = SlotBudget::parse(&v, 1, layers, pool) {
                return b;
            }
        }
    }
    let slots = if let Ok(v) = std::env::var("GE_MOE_RAM_EXPERTS") {
        v.parse::<usize>().ok().unwrap_or(8).max(1)
    } else {
        let per = model.manifest().experts_per_layer.unwrap_or(0) as usize;
        if per > 0 && per <= 8 {
            per
        } else {
            8
        }
    };
    SlotBudget {
        slots_per_layer: slots,
        pool,
    }
}

fn moe_top_k(model: &GreenModel) -> usize {
    model
        .manifest()
        .experts_used_per_token
        .unwrap_or(2)
        .max(1) as usize
}

/// Request for native dense generation.
#[derive(Clone, Debug)]
pub struct GenerateRequest {
    pub prompt: String,
    /// When set, rendered via the package chat template instead of wrapping `prompt`.
    pub messages: Option<Vec<ChatMessage>>,
    pub max_new_tokens: usize,
    /// Max sequence length (prompt + new). `None` → [`DenseForward::DEFAULT_CTX_LEN`] (4096).
    /// Clamped to [`DenseForward::MAX_CTX_LEN_CPU`] (32768) on the native CPU path.
    pub ctx_len: Option<usize>,
    /// Hot-window size for [`crate::kv::PagedRamKvStore`]. `None` → `GE_ATTN_WINDOW` or full `ctx_len`.
    pub kv_hot_cap: Option<usize>,
    /// StreamingLLM attention sinks. `None` → [`crate::kv::DEFAULT_ATTENTION_SINKS`].
    pub kv_sinks: Option<usize>,
    /// Key-lane quant. `None` → [`KvKeyQuant::auto_for_ctx`] (Q8V4 ≥8k / `GE_KV_AUTO_Q8V4`,
    /// Q8 at ≥4k, else F16).
    pub kv_key_quant: Option<KvKeyQuant>,
    /// Sampling (default: [`SampleParams::greedy`] for benches).
    pub sample: SampleParams,
    /// Instruct chat wrapping for bare prompts (default: Auto).
    pub chat: ChatApplyMode,
}

impl GenerateRequest {
    pub fn new(prompt: impl Into<String>, max_new_tokens: usize) -> Self {
        GenerateRequest {
            prompt: prompt.into(),
            messages: None,
            max_new_tokens: max_new_tokens.max(1),
            ctx_len: None,
            kv_hot_cap: None,
            kv_sinks: None,
            kv_key_quant: None,
            sample: SampleParams::greedy(),
            chat: ChatApplyMode::Auto,
        }
    }

    pub fn chat_messages(messages: Vec<ChatMessage>, max_new_tokens: usize) -> Self {
        GenerateRequest {
            prompt: String::new(),
            messages: Some(messages),
            max_new_tokens: max_new_tokens.max(1),
            ctx_len: None,
            kv_hot_cap: None,
            kv_sinks: None,
            kv_key_quant: None,
            sample: SampleParams::chat(),
            chat: ChatApplyMode::Force,
        }
    }

    pub fn with_ctx_len(mut self, ctx: usize) -> Self {
        self.ctx_len = Some(ctx.max(1));
        self
    }

    pub fn with_kv_hot_cap(mut self, hot_cap: usize) -> Self {
        self.kv_hot_cap = Some(hot_cap.max(1));
        self
    }

    pub fn with_kv_sinks(mut self, sinks: usize) -> Self {
        self.kv_sinks = Some(sinks);
        self
    }

    pub fn with_kv_key_quant(mut self, q: KvKeyQuant) -> Self {
        self.kv_key_quant = Some(q);
        self
    }

    pub fn with_sample(mut self, sample: SampleParams) -> Self {
        self.sample = sample;
        self
    }

    pub fn with_chat(mut self, chat: ChatApplyMode) -> Self {
        self.chat = chat;
        self
    }
}

/// Result of a generate call.
#[derive(Clone, Debug)]
pub struct GenerateResult {
    pub prompt_tokens: Vec<u32>,
    pub new_tokens: Vec<u32>,
    pub text: String,
    /// Resolved ctx cap for this decode (prompt + new, clamped).
    pub ctx_len: usize,
    /// KV lane quant actually used (Q8V4 ≥8k / auto env, Q8 ≥4k, else F16 unless overridden).
    pub kv_key_quant: KvKeyQuant,
    /// M5 paged-KV residency counters from the live decode.
    pub kv_metrics: PagedKvMetrics,
    pub kv_seq_len: usize,
    pub kv_hot_cap: usize,
    pub kv_hot_bytes: usize,
    /// M5.4: reserved KV token capacity (grow-on-demand ≪ hot_cap on short prompts).
    pub kv_reserved_tokens: usize,
    /// Seconds to obtain [`DenseForward`] (0 on warm cache hit).
    pub load_secs: f64,
    /// Prefill seconds (prompt tokens).
    pub prefill_secs: f64,
    /// Decode-only seconds (new-token loop; weights already warm).
    pub decode_secs: f64,
    /// Seconds from decode start until first new token (0 if none).
    pub first_token_secs: f64,
    /// True when DenseForward came from the process cache (not a cold load).
    pub forward_cache_hit: bool,
}

impl GenerateResult {
    /// Time to first token: cold load (if any) + prefill + first decode sample.
    pub fn ttft_secs(&self) -> f64 {
        self.load_secs + self.prefill_secs + self.first_token_secs
    }

    /// Decode-only tok/s (excludes cold load + prefill).
    pub fn decode_tok_s(&self) -> f64 {
        let n = self.new_tokens.len();
        if n == 0 || self.decode_secs <= 0.0 {
            0.0
        } else {
            n as f64 / self.decode_secs
        }
    }

    /// Wall tok/s for load+prefill+decode of this call.
    pub fn wall_tok_s(&self) -> f64 {
        let n = self.new_tokens.len();
        let secs = self.load_secs + self.prefill_secs + self.decode_secs;
        if n == 0 || secs <= 0.0 {
            0.0
        } else {
            n as f64 / secs
        }
    }
}

/// Errors from [`generate`] / [`generate_from_path`].
#[derive(Debug)]
pub enum GenerateError {
    Model(GreenModelError),
    Forward(ForwardError),
    Tokenize(TokenizeError),
    NotReady(String),
}

impl std::fmt::Display for GenerateError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GenerateError::Model(e) => write!(f, "{e}"),
            GenerateError::Forward(e) => write!(f, "{e}"),
            GenerateError::Tokenize(e) => write!(f, "tokenize: {e}"),
            GenerateError::NotReady(s) => write!(f, "{s}"),
        }
    }
}

impl std::error::Error for GenerateError {}

impl From<GreenModelError> for GenerateError {
    fn from(value: GreenModelError) -> Self {
        GenerateError::Model(value)
    }
}

impl From<ForwardError> for GenerateError {
    fn from(value: ForwardError) -> Self {
        GenerateError::Forward(value)
    }
}

impl From<TokenizeError> for GenerateError {
    fn from(value: TokenizeError) -> Self {
        GenerateError::Tokenize(value)
    }
}

impl From<GenerateError> for GreenModelError {
    fn from(value: GenerateError) -> Self {
        match value {
            GenerateError::Model(e) => e,
            GenerateError::NotReady(_) => GreenModelError::GenerationNotReady,
            other => GreenModelError::Io(other.to_string()),
        }
    }
}

type FwdCache = Mutex<HashMap<(PathBuf, usize), Arc<DenseForward>>>;
type TokCache = Mutex<HashMap<(PathBuf, PathBuf), Arc<crate::tokenize::Tokenizer>>>;

fn forward_cache() -> &'static FwdCache {
    static CACHE: OnceLock<FwdCache> = OnceLock::new();
    CACHE.get_or_init(|| Mutex::new(HashMap::new()))
}

fn tokenizer_cache() -> &'static TokCache {
    static CACHE: OnceLock<TokCache> = OnceLock::new();
    CACHE.get_or_init(|| Mutex::new(HashMap::new()))
}

fn cache_key(dense: &Path) -> PathBuf {
    dense.canonicalize().unwrap_or_else(|_| dense.to_path_buf())
}

fn path_key(p: Option<&Path>) -> PathBuf {
    match p {
        Some(p) => p.canonicalize().unwrap_or_else(|_| p.to_path_buf()),
        None => PathBuf::from(""),
    }
}

/// Load or reuse a warm [`DenseForward`] for `dense.gguf` (+ optional metadata).
///
/// Concurrent-safe for single-user / light concurrency: miss races re-check under lock
/// and keep the first inserted graph (losers drop their duplicate load).
pub fn load_forward_cached(
    dense: &Path,
    meta: Option<&Path>,
    gpu_layers: usize,
) -> Result<(Arc<DenseForward>, f64, bool), GenerateError> {
    let gpu_layers = {
        let raw = if gpu_layers > 0 {
            gpu_layers
        } else {
            std::env::var("GE_GPU_LAYERS")
                .ok()
                .and_then(|v| v.parse().ok())
                .unwrap_or(0)
        };
        crate::game_mode::resolve_gpu_layers(raw)
    };
    let key = (cache_key(dense), gpu_layers);
    {
        let guard = forward_cache()
            .lock()
            .map_err(|_| GenerateError::NotReady("forward cache poisoned".into()))?;
        if let Some(fwd) = guard.get(&key) {
            return Ok((Arc::clone(fwd), 0.0, true));
        }
    }
    let t0 = Instant::now();
    let loaded = Arc::new(DenseForward::load_with_config(
        dense,
        meta,
        crate::gpu_layer::ForwardLoadConfig {
            gpu_layers,
            gpu_device: 0,
            release_lm_host: false, // DenseForward sets this from tied_output
        },
    )?);
    let load_secs = t0.elapsed().as_secs_f64();
    let mut guard = forward_cache()
        .lock()
        .map_err(|_| GenerateError::NotReady("forward cache poisoned".into()))?;
    if let Some(existing) = guard.get(&key) {
        // Another request won the race; reuse winner (warm from caller's POV).
        return Ok((Arc::clone(existing), 0.0, true));
    }
    guard.insert(key, Arc::clone(&loaded));
    Ok((loaded, load_secs, false))
}

pub fn load_tokenizer_cached(
    meta: Option<&Path>,
    sidecar: Option<&Path>,
) -> Result<Arc<crate::tokenize::Tokenizer>, GenerateError> {
    let key = (path_key(meta), path_key(sidecar));
    {
        let guard = tokenizer_cache()
            .lock()
            .map_err(|_| GenerateError::NotReady("tokenizer cache poisoned".into()))?;
        if let Some(tok) = guard.get(&key) {
            return Ok(Arc::clone(tok));
        }
    }
    let loaded = Arc::new(load_for_package(meta, sidecar)?);
    let mut guard = tokenizer_cache()
        .lock()
        .map_err(|_| GenerateError::NotReady("tokenizer cache poisoned".into()))?;
    if let Some(existing) = guard.get(&key) {
        return Ok(Arc::clone(existing));
    }
    guard.insert(key, Arc::clone(&loaded));
    Ok(loaded)
}

/// Whether a loaded [`GreenModel`] has the tensor index needed for dense generate.
///
/// Tied-embedding models (e.g. Llama-3.2) omit `output.weight` and reuse `token_embd.weight`
/// as the lm_head — [`GreenModel::output`] falls back to the embedding handle in that case.
pub fn model_can_generate(model: &GreenModel) -> bool {
    model.is_ready_for_forward()
        && model.embedding().is_some()
        && model.output().is_some()
        && model.dense_weights.len() > 128
}

/// Shared readiness diagnostic used by [`generate`] and [`ensure_ready`].
pub fn not_ready_message(model: &GreenModel) -> String {
    format!(
        "package is not ready for dense generate \
         (need ReadyForForward + embd/output + dense.gguf > 128 bytes; \
         load_state={:?}, embd={}, output={}, dense_bytes={})",
        model.load_state(),
        model.embedding().is_some(),
        model.output().is_some(),
        model.dense_weights.len()
    )
}

pub fn require_can_generate(model: &GreenModel) -> Result<(), GenerateError> {
    if model_can_generate(model) {
        Ok(())
    } else {
        Err(GenerateError::NotReady(not_ready_message(model)))
    }
}

/// Open package + run dense greedy generate. Preferred entry for `ge run`.
pub fn generate_from_path(
    path: &Path,
    req: &GenerateRequest,
) -> Result<GenerateResult, GenerateError> {
    let model = crate::resident::open_model_cached(
        path,
        &LoadConfig {
            verify_checksums: false,
        },
    )?;
    generate(model.as_ref(), req)
}

pub fn resolve_prompt_text(model: &GreenModel, req: &GenerateRequest) -> String {
    let meta = model.metadata.metadata_gguf.as_deref();
    let family = family_for_package(
        &model.metadata.model,
        &model.metadata.model,
        model.package_root(),
        meta,
    );
    if let Some(ref msgs) = req.messages {
        let fam = if family == ChatTemplateFamily::None {
            ChatTemplateFamily::Llama3
        } else {
            family
        };
        return render_messages(fam, msgs, true);
    }
    prepare_prompt(&req.prompt, req.chat, family)
}

/// Run dense or MoE generate on an opened [`GreenModel`].
///
/// Dense packages use a process-wide warm [`DenseForward`] cache.
/// MoE packages (`experts-*.greenpack`) load via [`DenseForward::load_moe`] +
/// [`PackageExpertStore`] (not cached — expert LRU is per-call).
/// Note: each `ge run` process still cold-loads once; decode-only tok/s is in
/// [`GenerateResult::decode_tok_s`].
pub fn generate(model: &GreenModel, req: &GenerateRequest) -> Result<GenerateResult, GenerateError> {
    require_can_generate(model)?;
    let dense = &model.dense_weights.path;
    let meta = model.metadata.metadata_gguf.as_deref();
    let moe = model.experts.has_experts();
    let (fwd, load_secs, forward_cache_hit) = if moe {
        let store = model
            .package_expert_store_budget(moe_slot_budget(model))
            .map_err(|e| GenerateError::Forward(ForwardError::Moe(e.to_string())))?
            .ok_or_else(|| {
                GenerateError::NotReady(
                    "MoE package indexed experts but PackageExpertStore returned None".into(),
                )
            })?;
        let t0 = Instant::now();
        let loaded = Arc::new(DenseForward::load_moe(
            dense,
            meta,
            store,
            moe_top_k(model),
        )?);
        (loaded, t0.elapsed().as_secs_f64(), false)
    } else {
        load_forward_cached(dense, meta, 0)?
    };

    // Prefer package tokenizer.json / *.gguf sidecar; metadata.gguf supplies merges when JSON omits them.
    let tok = load_tokenizer_cached(meta, model.tokenizer.path.as_deref())?;
    let prompt_text = resolve_prompt_text(model, req);
    let prompt_tokens = tok.encode(&prompt_text)?;
    let ctx_len = req
        .ctx_len
        .unwrap_or(DenseForward::DEFAULT_CTX_LEN)
        .clamp(1, DenseForward::MAX_CTX_LEN_CPU);
    let hot_cap = crate::kv::resolve_kv_hot_cap(
        req.kv_hot_cap,
        ctx_len,
        req.messages.is_some(),
    );
    let sinks = req
        .kv_sinks
        .unwrap_or(crate::kv::DEFAULT_ATTENTION_SINKS);
    let mut key_quant = req
        .kv_key_quant
        .or_else(KvKeyQuant::from_env)
        .unwrap_or_else(|| KvKeyQuant::auto_for_ctx(ctx_len));
    // Decode CUDA graphs (`GE_CUDA_GRAPH=1`) only capture the Q8 device-KV path
    // (`try_graph` requires GE_KV_DTYPE_Q8). Quiet harnesses that force F16 made
    // graphs a no-op (Wave 28 SOLO BELOW vs ISO +10.9%). Coerce F16→Q8 so opt-in
    // graphs actually arm; leave Q8V4 alone (graphs stay idle there).
    if matches!(key_quant, KvKeyQuant::F16) && crate::gpu_layer::cuda_graph_env_enabled() {
        static GRAPH_KV_WARN: std::sync::Once = std::sync::Once::new();
        GRAPH_KV_WARN.call_once(|| {
            eprintln!(
                "ge: GE_CUDA_GRAPH=1 requires Q8 KV (F16 disables try_graph) — using Q8"
            );
        });
        key_quant = KvKeyQuant::Q8;
    }
    let stops = if std::env::var("GE_BENCH_IGNORE_EOS").ok().as_deref() == Some("1") {
        Vec::new()
    } else {
        stop_token_ids(&tok)
    };
    let out: GreedyGenerateOut = fwd.generate_paged_ctx(
        &prompt_tokens,
        req.max_new_tokens,
        ctx_len,
        hot_cap,
        sinks,
        key_quant,
        &stops,
        &req.sample,
    )?;
    let text = tok
        .decode(&out.new_tokens)
        .unwrap_or_else(|_| {
            out.new_tokens
                .iter()
                .map(|t| t.to_string())
                .collect::<Vec<_>>()
                .join(" ")
        });
    Ok(GenerateResult {
        prompt_tokens,
        new_tokens: out.new_tokens,
        text,
        ctx_len,
        kv_key_quant: key_quant,
        kv_metrics: out.kv_metrics,
        kv_seq_len: out.kv_seq_len,
        kv_hot_cap: out.kv_hot_cap,
        kv_hot_bytes: out.kv_hot_bytes,
        kv_reserved_tokens: out.kv_reserved_tokens,
        load_secs,
        prefill_secs: out.prefill_secs,
        decode_secs: out.decode_secs,
        first_token_secs: out.first_token_secs,
        forward_cache_hit,
    })
}

pub fn stop_token_ids(tok: &crate::tokenize::Tokenizer) -> Vec<u32> {
    let mut stops = Vec::new();
    if let Some(eos) = tok.eos_id {
        stops.push(eos);
    }
    for name in ["<|eot_id|>", "<|end_of_text|>", "</s>"] {
        if let Some(id) = tok.lookup_token(name) {
            if !stops.contains(&id) {
                stops.push(id);
            }
        }
    }
    for id in [128001u32, 128009u32] {
        if (id as usize) < tok.tokens.len() && !stops.contains(&id) {
            stops.push(id);
        }
    }
    stops
}

/// Ensure the package can materialize a forward graph (dense cache or MoE load).
pub fn ensure_ready(model: &GreenModel) -> Result<(), GenerateError> {
    require_can_generate(model)?;
    if model.experts.has_experts() {
        let store = model
            .package_expert_store_budget(moe_slot_budget(model))
            .map_err(|e| GenerateError::Forward(ForwardError::Moe(e.to_string())))?
            .ok_or_else(|| {
                GenerateError::NotReady(
                    "MoE package indexed experts but PackageExpertStore returned None".into(),
                )
            })?;
        let _ = DenseForward::load_moe(
            &model.dense_weights.path,
            model.metadata.metadata_gguf.as_deref(),
            store,
            moe_top_k(model),
        )?;
    } else {
        let _ = load_forward_cached(
            &model.dense_weights.path,
            model.metadata.metadata_gguf.as_deref(),
            0,
        )?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tiny_green() -> Option<std::path::PathBuf> {
        let mut candidates = vec![
            std::path::PathBuf::from(r"../green-compress\scripts\fixtures\tiny.green"),
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../../green-compress/scripts/fixtures/tiny.green"),
            Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/tiny.green"),
            Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/mini_green"),
        ];
        for c in candidates.drain(..) {
            if c.join("manifest.json").is_file() && c.join("dense.gguf").is_file() {
                // Prefer packages with real dense payloads (skip stub 64-byte mini_green).
                if std::fs::metadata(c.join("dense.gguf"))
                    .map(|m| m.len() > 128)
                    .unwrap_or(false)
                {
                    return Some(c);
                }
            }
        }
        None
    }

    #[test]
    fn native_generate_smoke() {
        let Some(path) = tiny_green() else {
            eprintln!("skip native_generate_smoke: tiny.green fixture missing");
            return;
        };
        let model = GreenModel::open(&path, &LoadConfig::default()).expect("open tiny.green");
        assert!(model.is_ready_for_forward());
        assert!(model_can_generate(&model), "expected can_generate for tiny.green");
        ensure_ready(&model).expect("ensure_ready");
        let res = generate(
            &model,
            &GenerateRequest::new("a", 1).with_ctx_len(512),
        )
        .expect("generate 1 token");
        assert_eq!(res.new_tokens.len(), 1, "expected exactly 1 new token");
        assert_eq!(res.kv_seq_len, res.prompt_tokens.len() + res.new_tokens.len());
        assert!(res.kv_hot_cap >= 1);
        assert!(
            res.forward_cache_hit,
            "ensure_ready should warm DenseForward cache before generate"
        );
        let res2 = generate(
            &model,
            &GenerateRequest::new("a", 1).with_ctx_len(512),
        )
        .expect("warm generate");
        assert!(res2.forward_cache_hit, "second generate must hit forward cache");
        assert!(res2.load_secs == 0.0);
        println!(
            "native_generate_smoke: prompt_tokens={:?} new={:?} text={:?} kv_seq={} hot_cap={} page_ins={} ttft={:.4}s",
            res.prompt_tokens,
            res.new_tokens,
            res.text,
            res.kv_seq_len,
            res.kv_hot_cap,
            res.kv_metrics.page_ins,
            res.ttft_secs()
        );
    }

    #[test]
    fn native_generate_multi_token_paged_kv() {
        let Some(path) = tiny_green() else {
            eprintln!("skip native_generate_multi_token_paged_kv: tiny.green fixture missing");
            return;
        };
        let model = GreenModel::open(&path, &LoadConfig::default()).expect("open tiny.green");
        assert!(model_can_generate(&model));
        // Tight hot window → StreamingLLM physically drops middle tokens.
        let hot_cap = 2usize;
        let n_new = 4usize;
        let res = generate(
            &model,
            &GenerateRequest::new("a", n_new)
                .with_ctx_len(64)
                .with_kv_hot_cap(hot_cap)
                .with_kv_sinks(1),
        )
        .expect("generate n>1 with capped KV");
        assert_eq!(res.new_tokens.len(), n_new, "expected {n_new} new tokens");
        assert_eq!(res.kv_hot_cap, hot_cap);
        assert_eq!(
            res.kv_seq_len, hot_cap,
            "resident seq must equal hot_cap after eviction"
        );
        assert!(
            res.kv_metrics.evictions >= 1,
            "expected StreamingLLM evictions; metrics={:?}",
            res.kv_metrics
        );
        assert!(
            res.kv_metrics.cold_tokens_touched >= 1,
            "expected dropped tokens; metrics={:?}",
            res.kv_metrics
        );
        assert!(
            res.kv_hot_bytes > 0,
            "hot residency bytes should be non-zero after decode"
        );
        println!(
            "native_generate_multi_token_paged_kv: new={:?} text={:?} seq={} hot_bytes={} metrics={:?}",
            res.new_tokens, res.text, res.kv_seq_len, res.kv_hot_bytes, res.kv_metrics
        );
    }

    #[test]
    fn native_generate_seq_gt_512_tiny() {
        let Some(path) = tiny_green() else {
            eprintln!("skip native_generate_seq_gt_512_tiny: tiny.green fixture missing");
            return;
        };
        let model = GreenModel::open(&path, &LoadConfig::default()).expect("open tiny.green");
        // Bypass stop-token early exit (tiny vocab hits eos/bos quickly under greedy).
        let dense = &model.dense_weights.path;
        let meta = model.metadata.metadata_gguf.as_deref();
        let fwd = DenseForward::load(dense, meta).expect("load forward");
        let ctx = 600usize;
        let n_new = 520usize;
        let prompt = [1u32, 0];
        let out = fwd
            .generate_paged_ctx(
                &prompt,
                n_new,
                ctx,
                ctx,
                4,
                crate::kv::KvKeyQuant::F16,
                &[],
                &crate::sample::SampleParams::greedy(),
            )
            .expect("seq>512 generate");
        let total = prompt.len() + out.new_tokens.len();
        assert!(
            total > 512,
            "expected seq>512, got prompt={} new={}",
            prompt.len(),
            out.new_tokens.len()
        );
        assert_eq!(out.kv_seq_len, total.min(ctx));
        println!(
            "native_generate_seq_gt_512_tiny: total={} kv_seq={} hot_bytes={}",
            total, out.kv_seq_len, out.kv_hot_bytes
        );
    }

    #[test]
    fn smoke_1b_if_present() {
        let home = std::env::var("USERPROFILE").unwrap_or_default();
        let p = std::env::var("GE_1B_GREEN")
            .map(std::path::PathBuf::from)
            .unwrap_or_else(|_| {
                std::path::PathBuf::from(format!(r"{home}\.green\models\Llama-3.2-1B.green"))
            });
        if !p.join("manifest.json").is_file() {
            eprintln!("skip smoke_1b_if_present: missing {}", p.display());
            return;
        }
        eprintln!("smoke_1b: open {}", p.display());
        let model = GreenModel::open(
            &p,
            &LoadConfig {
                verify_checksums: false,
            },
        )
        .expect("open 1B");
        assert!(model_can_generate(&model), "can_generate must be true");
        eprintln!(
            "smoke_1b: can_generate layers≈{} — loading forward…",
            model.num_layers()
        );
        let dense = &model.dense_weights.path;
        let meta = model.metadata.metadata_gguf.as_deref();
        let fwd = crate::forward::DenseForward::load(dense, meta).expect("fwd load");
        eprintln!(
            "smoke_1b: rope.theta={} head_dim={} n_layers={} n_heads={} n_kv={}",
            fwd.rope.theta, fwd.head_dim, fwd.n_layers, fwd.n_heads, fwd.n_kv_heads
        );
        // Bare Instruct prompt — Auto chat template + greedy (bench-stable).
        let res = generate(
            &model,
            &GenerateRequest::new("Explain gravity in one sentence.", 48)
                .with_chat(crate::chat::ChatApplyMode::Force)
                .with_sample(crate::sample::SampleParams::greedy()),
        )
        .expect("generate chat");
        eprintln!(
            "smoke_1b: prompt_len={} new={:?} text={:?}",
            res.prompt_tokens.len(),
            res.new_tokens,
            res.text
        );
        assert!(!res.new_tokens.is_empty());
        let t = res.text.trim();
        assert!(t.len() > 8, "expected a short coherent answer, got {t:?}");
        // Fail closed on pure greedy stutter (same token id for whole decode).
        let all_same = res.new_tokens.windows(2).all(|w| w[0] == w[1]);
        assert!(
            !all_same || res.new_tokens.len() < 4,
            "decode collapsed to a single repeated token (id={:?}); text={t:?}",
            res.new_tokens.first()
        );
    }
}