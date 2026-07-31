//! Process-wide resident `.green` models for warm serve (`ge chat serve --model *.green`).
//!
//! Keeps [`GreenModel`], warm [`DenseForward`], tokenizer, and a reusable [`DecodeSession`]
//! in RAM so HTTP requests skip package reload and KV/scratch allocation.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Instant;

use crate::forward::{DecodeSession, DenseForward, GreedyGenerateOut};
use crate::generate::{
    load_forward_cached, load_tokenizer_cached, model_can_generate, not_ready_message,
    require_can_generate, resolve_prompt_text, stop_token_ids, GenerateError, GenerateRequest,
    GenerateResult,
};
use crate::green_model::{GreenModel, LoadConfig};
use crate::kv::{KvKeyQuant, DEFAULT_ATTENTION_SINKS};

type ModelCache = Mutex<HashMap<PathBuf, Arc<GreenModel>>>;

fn model_cache() -> &'static ModelCache {
    static CACHE: OnceLock<ModelCache> = OnceLock::new();
    CACHE.get_or_init(|| Mutex::new(HashMap::new()))
}

fn package_key(path: &Path) -> PathBuf {
    path.canonicalize().unwrap_or_else(|_| path.to_path_buf())
}

/// Open or reuse a [`GreenModel`] for `path` (process-wide singleton per package root).
pub fn open_model_cached(
    path: &Path,
    cfg: &LoadConfig,
) -> Result<Arc<GreenModel>, GenerateError> {
    let key = package_key(path);
    {
        let guard = model_cache()
            .lock()
            .map_err(|_| GenerateError::NotReady("model cache poisoned".into()))?;
        if let Some(m) = guard.get(&key) {
            return Ok(Arc::clone(m));
        }
    }
    let loaded = Arc::new(GreenModel::open(path, cfg)?);
    let mut guard = model_cache()
        .lock()
        .map_err(|_| GenerateError::NotReady("model cache poisoned".into()))?;
    if let Some(existing) = guard.get(&key) {
        return Ok(Arc::clone(existing));
    }
    guard.insert(key, Arc::clone(&loaded));
    Ok(loaded)
}

/// Warm resident model: cached package + forward graph + reusable decode session.
pub struct ResidentModel {
    model: Arc<GreenModel>,
    fwd: Arc<DenseForward>,
    tok: Arc<crate::tokenize::Tokenizer>,
    stops: Vec<u32>,
    default_ctx_len: usize,
    session: Mutex<DecodeSession>,
}

impl ResidentModel {
    /// Cold-open once: package index, weight graph, KV/scratch; pre-warm with 1-token discard.
    pub fn open(path: &Path, cfg: &LoadConfig, ctx_len: usize) -> Result<Self, GenerateError> {
        let t0 = Instant::now();
        let model = open_model_cached(path, cfg)?;
        require_can_generate(&model)?;
        let dense = &model.dense_weights.path;
        let meta = model.metadata.metadata_gguf.as_deref();
        let (fwd, load_secs, _) = load_forward_cached(dense, meta, 0)?;
        let tok = load_tokenizer_cached(meta, model.tokenizer.path.as_deref())?;
        let stops = stop_token_ids(&tok);
        let ctx = ctx_len.max(1).min(DenseForward::MAX_CTX_LEN_CPU);
        let hot_cap = ctx;
        let key_quant = KvKeyQuant::auto_for_ctx(ctx);
        let session = fwd.new_decode_session(ctx, hot_cap, DEFAULT_ATTENTION_SINKS, key_quant);
        let resident = ResidentModel {
            model,
            fwd,
            tok,
            stops,
            default_ctx_len: ctx,
            session: Mutex::new(session),
        };
        resident.prewarm_discard()?;
        let open_secs = t0.elapsed().as_secs_f64();
        eprintln!(
            "resident: opened in {:.2}s (forward load {:.2}s); decode session pre-warmed",
            open_secs, load_secs
        );
        Ok(resident)
    }

    pub fn model(&self) -> &GreenModel {
        &self.model
    }

    pub fn can_generate(&self) -> bool {
        model_can_generate(&self.model)
    }

    fn prewarm_discard(&self) -> Result<(), GenerateError> {
        let mut req = GenerateRequest::new("", 1);
        req.max_new_tokens = 1;
        let _ = self.generate(&req)?;
        Ok(())
    }

    /// Generate with resident weights + session (forward cache hit; `load_secs` = 0).
    pub fn generate(&self, req: &GenerateRequest) -> Result<GenerateResult, GenerateError> {
        if !model_can_generate(&self.model) {
            return Err(GenerateError::NotReady(not_ready_message(&self.model)));
        }
        let prompt_text = resolve_prompt_text(&self.model, req);
        let prompt_tokens = self.tok.encode(&prompt_text)?;
        let ctx_len = req
            .ctx_len
            .unwrap_or(self.default_ctx_len)
            .clamp(1, DenseForward::MAX_CTX_LEN_CPU);
        let hot_cap = crate::kv::resolve_kv_hot_cap(
            req.kv_hot_cap,
            ctx_len,
            req.messages.is_some(),
        );
        let sinks = req.kv_sinks.unwrap_or(DEFAULT_ATTENTION_SINKS);
        let key_quant = req
            .kv_key_quant
            .or_else(KvKeyQuant::from_env)
            .unwrap_or_else(|| KvKeyQuant::auto_for_ctx(ctx_len));
        let mut guard = self
            .session
            .lock()
            .map_err(|_| GenerateError::NotReady("decode session poisoned".into()))?;
        if guard.ctx_len() != ctx_len
            || guard.hot_cap() != hot_cap
            || guard.sinks() != sinks
            || guard.key_quant() != key_quant
        {
            *guard = self.fwd.new_decode_session(ctx_len, hot_cap, sinks, key_quant);
        }
        let out: GreedyGenerateOut = self.fwd.generate_paged_ctx_session(
            &mut guard,
            &prompt_tokens,
            req.max_new_tokens,
            &self.stops,
            &req.sample,
        )?;
        let text = self
            .tok
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
            load_secs: 0.0,
            prefill_secs: out.prefill_secs,
            decode_secs: out.decode_secs,
            first_token_secs: out.first_token_secs,
            forward_cache_hit: true,
        })
    }
}
