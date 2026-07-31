#!/usr/bin/env python3
"""Install resident-model patch by reading 3bd7f37 from git (no checkout).

Recovery order (one-shot via ``crates/engine-core/_final.py``)::

    git checkout -- crates/engine-core/src/
    python crates/engine-core/_atomic_merge.py
    python scripts/install_resident.py --after-merge
    cargo build --release -p ge && cargo test ...
"""
from __future__ import annotations

import argparse
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BASE = "3bd7f37"


def git_show(rel: str) -> str:
    return subprocess.check_output(
        ["git", "show", f"{BASE}:{rel}"],
        cwd=ROOT,
        text=True,
        encoding="utf-8",
    )


DECODE_SESSION = '''
/// Reusable KV + scratch for warm serve paths (one alloc at startup, `clear` per request).
pub struct DecodeSession {
    kv: PagedRamKvStore,
    scratch: DecodeScratch,
    ctx_len: usize,
}

impl DecodeSession {
    pub fn new(
        fwd: &DenseForward,
        ctx_len: usize,
        kv_hot_cap: usize,
        kv_sinks: usize,
        key_quant: KvKeyQuant,
    ) -> Self {
        let mut ctx = ctx_len.max(1).min(DenseForward::MAX_CTX_LEN_CPU);
        if let Some(trained) = fwd.rope.context_length.map(|c| c as usize) {
            if trained >= 2048 {
                let soft = ((trained as f64) * 0.9).floor() as usize;
                ctx = ctx.min(soft.max(1));
            }
        }
        let hot_cap = kv_hot_cap.max(1).min(ctx);
        let kv_dim = fwd.n_kv_heads * fwd.head_dim;
        let mut kv = PagedRamKvStore::with_policy(
            fwd.n_layers,
            kv_dim,
            hot_cap,
            kv_sinks,
            key_quant,
        );
        kv.reserve_tokens(hot_cap);
        DecodeSession {
            kv,
            scratch: DecodeScratch::new(
                fwd.n_embd,
                fwd.n_heads,
                fwd.n_kv_heads,
                fwd.head_dim,
                fwd.layers[0].inter,
                fwd.n_vocab,
            ),
            ctx_len: ctx,
        }
    }

    pub fn reset(&mut self) {
        self.kv.clear();
    }

    pub(crate) fn ctx_len(&self) -> usize {
        self.ctx_len
    }
}

'''

SESSION_TAIL = '''
    pub fn generate_paged_ctx_session(
        &self,
        session: &mut DecodeSession,
        prompt_ids: &[u32],
        max_new: usize,
        stop_ids: &[u32],
        sample: &crate::sample::SampleParams,
    ) -> Result<GreedyGenerateOut, ForwardError> {
        if prompt_ids.is_empty() {
            return Err(ForwardError::Message("empty prompt tokens".into()));
        }
        session.reset();
        let ctx = session.ctx_len();
        let prompt_owned: Vec<u32>;
        let prompt_ids: &[u32] = if prompt_ids.len() >= ctx {
            let keep = ctx.saturating_sub(1).max(1);
            prompt_owned = prompt_ids[prompt_ids.len() - keep..].to_vec();
            prompt_owned.as_slice()
        } else {
            prompt_ids
        };
        if prompt_ids.is_empty() {
            return Err(ForwardError::Message("empty prompt tokens".into()));
        }

        let kv_dyn: &mut dyn KvStore = &mut session.kv;
        let scratch = &mut session.scratch;
        let mut token_pos = 0usize;

        let t_prefill = Instant::now();
        for &id in prompt_ids {
            self.embed_token(id, &mut scratch.hidden)?;
            self.forward_token(token_pos, kv_dyn, scratch)?;
            token_pos += 1;
        }
        let prefill_secs = t_prefill.elapsed().as_secs_f64();

        let seed = sample.seed.unwrap_or_else(|| {
            let mut h = prompt_ids.len() as u64;
            for &t in prompt_ids.iter().take(8) {
                h = h.wrapping_mul(0x9E3779B97F4A7C15).wrapping_add(t as u64);
            }
            h ^ ((max_new as u64) << 17)
        });
        let mut rng = crate::sample::XorShift64::new(seed);
        let mut history: Vec<u32> = prompt_ids.to_vec();
        history.reserve(max_new);
        let mut probs = vec![0.0f32; self.n_vocab];

        let mut generated = Vec::with_capacity(max_new);
        let mut first_token_secs = 0.0f64;
        let t_decode = Instant::now();
        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));
        for _ in 0..max_new {
            self.compute_logits(&scratch.hidden, &mut scratch.logits, &mut scratch.norm_tmp)?;
            let next = crate::sample::sample_token(
                &mut scratch.logits,
                &history,
                sample,
                &mut rng,
                &mut probs,
            );
            if stop_ids.iter().any(|&s| s == next) {
                break;
            }
            if generated.is_empty() {
                first_token_secs = t_decode.elapsed().as_secs_f64();
            }
            generated.push(next);
            history.push(next);
            self.embed_token(next, &mut scratch.hidden)?;
            self.forward_token(token_pos, kv_dyn, scratch)?;
            token_pos += 1;
        }
        let decode_secs = t_decode.elapsed().as_secs_f64();
        Ok(GreedyGenerateOut {
            new_tokens: generated,
            kv_metrics: session.kv.metrics(),
            kv_seq_len: session.kv.seq_len(),
            kv_hot_cap: session.kv.hot_cap(),
            kv_hot_bytes: session.kv.hot_bytes(),
            prefill_secs,
            decode_secs,
            first_token_secs,
        })
    }

    pub fn new_decode_session(
        &self,
        ctx_len: usize,
        kv_hot_cap: usize,
        kv_sinks: usize,
        key_quant: KvKeyQuant,
    ) -> DecodeSession {
        DecodeSession::new(self, ctx_len, kv_hot_cap, kv_sinks, key_quant)
    }

'''

RESIDENT_RS = r'''//! Process-wide resident `.green` models for warm serve (`ge chat serve --model *.green`).
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
        let (fwd, load_secs, _) = load_forward_cached(dense, meta)?;
        let tok = load_tokenizer_cached(meta, model.tokenizer.path.as_deref())?;
        let stops = stop_token_ids(&tok);
        let ctx = ctx_len.max(1).min(DenseForward::MAX_CTX_LEN_CPU);
        let hot_cap = ctx;
        let session = fwd.new_decode_session(ctx, hot_cap, DEFAULT_ATTENTION_SINKS, KvKeyQuant::F16);
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
        let hot_cap = req.kv_hot_cap.unwrap_or(ctx_len).clamp(1, ctx_len);
        let sinks = req.kv_sinks.unwrap_or(DEFAULT_ATTENTION_SINKS);
        let key_quant = req.kv_key_quant.unwrap_or(KvKeyQuant::F16);
        let mut guard = self
            .session
            .lock()
            .map_err(|_| GenerateError::NotReady("decode session poisoned".into()))?;
        if guard.ctx_len() != ctx_len {
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
            kv_metrics: out.kv_metrics,
            kv_seq_len: out.kv_seq_len,
            kv_hot_cap: out.kv_hot_cap,
            kv_hot_bytes: out.kv_hot_bytes,
            load_secs: 0.0,
            prefill_secs: out.prefill_secs,
            decode_secs: out.decode_secs,
            first_token_secs: out.first_token_secs,
            forward_cache_hit: true,
        })
    }
}
'''


def patch_forward(text: str) -> str:
    if "pub struct DecodeSession" not in text:
        anchor = "/// Output of [`DenseForward::generate_greedy_paged`]"
        idx = text.find(anchor)
        if idx >= 0:
            text = text[:idx] + DECODE_SESSION + text[idx:]
    if "generate_paged_ctx_session" not in text:
        start = text.index("    pub fn generate_paged_ctx(")
        end = text.index("\n    fn embed_token(", start)
        delegate = '''    pub fn generate_paged_ctx(
        &self,
        prompt_ids: &[u32],
        max_new: usize,
        ctx_len: usize,
        kv_hot_cap: usize,
        kv_sinks: usize,
        key_quant: KvKeyQuant,
        stop_ids: &[u32],
        sample: &crate::sample::SampleParams,
    ) -> Result<GreedyGenerateOut, ForwardError> {
        let mut session = DecodeSession::new(self, ctx_len, kv_hot_cap, kv_sinks, key_quant);
        self.generate_paged_ctx_session(&mut session, prompt_ids, max_new, stop_ids, sample)
    }
'''
        text = text[:start] + delegate + SESSION_TAIL + text[end:]
    return text


def patch_generate(text: str) -> str:
    text = text.replace("pub pub(crate) fn", "pub fn")
    text = text.replace("pub pub fn", "pub fn")
    for name in [
        "load_tokenizer_cached",
        "not_ready_message",
        "require_can_generate",
        "stop_token_ids",
        "resolve_prompt_text",
    ]:
        if f"pub fn {name}(" in text or f"pub(crate) fn {name}(" in text:
            continue
        text = text.replace(f"fn {name}(", f"pub fn {name}(", 1)
    old = '''    let model = GreenModel::open(
        path,
        &LoadConfig {
            verify_checksums: false,
        },
    )?;
    generate(&model, req)'''
    new = '''    let model = crate::resident::open_model_cached(
        path,
        &LoadConfig {
            verify_checksums: false,
        },
    )?;
    generate(model.as_ref(), req)'''
    if old in text and new not in text:
        text = text.replace(old, new, 1)
    text = text.replace(
        "load_forward_cached(&model.dense_weights.path, model.metadata.metadata_gguf.as_deref(), 0)",
        "load_forward_cached(&model.dense_weights.path, model.metadata.metadata_gguf.as_deref())",
    )
    return text


def patch_lib(text: str) -> str:
    if "pub mod resident;" not in text:
        text = text.replace("pub mod generate;\n", "pub mod generate;\npub mod resident;\n", 1)
    text = text.replace(
        "pub use forward::{DenseForward, ForwardError, GreedyGenerateOut};",
        "pub use forward::{DecodeSession, DenseForward, ForwardError, GreedyGenerateOut};",
        1,
    )
    if "pub use resident::" not in text:
        if "pub use generate::{" in text:
            text = text.replace(
                "pub use generate::{",
                "pub use resident::{open_model_cached, ResidentModel};\npub use generate::{",
                1,
            )
        else:
            text = text.replace(
                "};\npub use sample::{SampleParams",
                "};\npub use resident::{open_model_cached, ResidentModel};\npub use sample::{SampleParams",
                1,
            )
    return text


def patch_ge(text: str) -> str:
    if "use std::sync::{Arc, Mutex};" not in text:
        text = text.replace(
            "use engine_core::{GreenModel, GreenModelError, LoadConfig, LoadState};",
            "use std::sync::{Arc, Mutex};\n\nuse engine_core::{GreenModel, GreenModelError, LoadConfig, LoadState, ResidentModel};",
            1,
        )
    resident_fn = '''
fn try_dense_chat_resident(
    resident: &ResidentModel,
    messages: Vec<engine_core::ChatMessage>,
    max_new_tokens: usize,
    sample: engine_core::SampleParams,
    ctx_len: Option<usize>,
) -> Result<engine_core::GenerateResult, GreenModelError> {
    if !resident.can_generate() {
        return Err(GreenModelError::GenerationNotReady);
    }
    let mut req = engine_core::GenerateRequest::chat_messages(messages, max_new_tokens);
    req.sample = sample;
    if let Some(ctx) = ctx_len {
        req = req.with_ctx_len(ctx);
    }
    resident.generate(&req).map_err(GreenModelError::from)
}

'''
    if "try_dense_chat_resident" not in text:
        text = text.replace("fn log_generate_metrics(", resident_fn + "fn log_generate_metrics(", 1)
    text = text.replace(
        "match GreenModel::open(path, &LoadConfig::default())",
        "match engine_core::open_model_cached(path, &LoadConfig::default())",
        1,
    )
    text = text.replace("print_green_metadata(&m);", "print_green_metadata(m.as_ref());", 1)
    text = text.replace("try_dense_generate_ex(&m,", "try_dense_generate_ex(m.as_ref(),", 1)
    text = text.replace(
        "model: &GreenModel,\n    model_id: &str,\n    ctx_len: usize,\n) {",
        "resident: &Arc<Mutex<ResidentModel>>,\n    model_id: &str,\n    ctx_len: usize,\n) {",
        1,
    )
    text = text.replace(
        "match try_dense_chat_messages(model, chat_msgs, max_tokens, sample, Some(ctx_len)) {",
        '''let guard = match resident.lock() {
            Ok(g) => g,
            Err(_) => {
                http_json_response(
                    &mut stream,
                    500,
                    "Internal Server Error",
                    &serde_json::json!({"error": {"message": "model lock poisoned", "type": "server_error"}}),
                );
                return;
            }
        };
        match try_dense_chat_resident(&guard, chat_msgs, max_tokens, sample, Some(ctx_len)) {''',
        1,
    )
    old_serve = '''    let model = match GreenModel::open(path, &LoadConfig::default()) {
        Ok(m) => m,
        Err(e) => {
            eprintln!("ge chat serve [native]: {e}");
            phase1_gguf_hint();
            return 1;
        }
    };
    print_green_metadata(&model);
    if !model.can_generate() {
        eprintln!(
            "ge chat serve [native]: package opened but native generate is not ready.\\n  \\
Use a dense .green with embd/output (see ge run), or GGUF: ge chat serve --model file.gguf"
        );
        return 1;
    }
    if let Err(e) = model.ensure_generation_ready() {
        eprintln!("ge chat serve [native]: {e}");
        return 1;
    }
    eprintln!("ge chat serve [native]: DenseForward warmed (process cache)");
    let model_id = if model.metadata.model.is_empty() {
        CHAT_MODEL_NAME.to_string()
    } else {
        model.metadata.model.clone()
    };'''
    new_serve = '''    let resident = match ResidentModel::open(path, &LoadConfig::default(), ctx_len) {
        Ok(r) => Arc::new(Mutex::new(r)),
        Err(e) => {
            eprintln!("ge chat serve [native]: {e}");
            phase1_gguf_hint();
            return 1;
        }
    };
    {
        let guard = resident.lock().expect("resident lock");
        print_green_metadata(guard.model());
        if !guard.can_generate() {
            eprintln!(
                "ge chat serve [native]: package opened but native generate is not ready.\\n  \\
Use a dense .green with embd/output (see ge run), or GGUF: ge chat serve --model file.gguf"
            );
            return 1;
        }
    }
    eprintln!("ge chat serve [native]: resident model + decode session warmed");
    let model_id = {
        let guard = resident.lock().expect("resident lock");
        if guard.model().metadata.model.is_empty() {
            CHAT_MODEL_NAME.to_string()
        } else {
            guard.model().metadata.model.clone()
        }
    };'''
    if old_serve in text:
        text = text.replace(old_serve, new_serve, 1)
    text = text.replace(
        "handle_native_chat_conn(stream, &model, &model_id, ctx_len)",
        "handle_native_chat_conn(stream, &resident, &model_id, ctx_len)",
        1,
    )
    return text


def write(rel: str, content: str) -> None:
    path = ROOT / rel
    path.write_text(content, encoding="utf-8", newline="\n")


def install_full() -> None:
    """Restore engine-core from BASE commit, patch forward + wiring (standalone install)."""
    listed = subprocess.check_output(
        ["git", "ls-tree", "-r", "--name-only", BASE, "crates/engine-core/src/"],
        cwd=ROOT,
        text=True,
        encoding="utf-8",
    ).splitlines()
    for rel in listed:
        if rel.endswith("/"):
            continue
        write(rel, git_show(rel))
    write("crates/engine-core/src/forward.rs", patch_forward(git_show("crates/engine-core/src/forward.rs")))
    write("crates/engine-core/src/generate.rs", patch_generate(git_show("crates/engine-core/src/generate.rs")))
    write("crates/engine-core/src/lib.rs", patch_lib(git_show("crates/engine-core/src/lib.rs")))
    write("crates/ge/src/main.rs", patch_ge(git_show("crates/ge/src/main.rs")))
    write("crates/engine-core/src/resident.rs", RESIDENT_RS)
    print("installed resident patch from", BASE, f"({len(listed)} engine-core files)")


def install_after_merge() -> None:
    """Wire resident after _atomic_merge.py — do not touch forward.rs or other perf patches."""
    gen_path = ROOT / "crates/engine-core/src/generate.rs"
    lib_path = ROOT / "crates/engine-core/src/lib.rs"
    ge_path = ROOT / "crates/ge/src/main.rs"
    gen = gen_path.read_text(encoding="utf-8")
    lib = lib_path.read_text(encoding="utf-8")
    ge = ge_path.read_text(encoding="utf-8")
    gen = patch_generate(gen.replace("pub pub fn", "pub fn"))
    lib = patch_lib(lib)
    ge = patch_ge(ge)
    gen_path.write_bytes(gen.encode("utf-8"))
    lib_path.write_bytes(lib.encode("utf-8"))
    ge_path.write_bytes(ge.encode("utf-8"))
    write("crates/engine-core/src/resident.rs", RESIDENT_RS)
    print("installed resident wiring (--after-merge); forward.rs left to atomic merge")


def main() -> None:
    parser = argparse.ArgumentParser(description="Install warm-serve ResidentModel wiring")
    parser.add_argument(
        "--after-merge",
        action="store_true",
        help="Run after _atomic_merge.py; patch resident.rs/generate/lib/ge only",
    )
    args = parser.parse_args()
    if args.after_merge:
        install_after_merge()
    else:
        install_full()


if __name__ == "__main__":
    main()
