//! Dense CPU forward graph: embed -> layers (attn+RoPE+FFN) -> logits / greedy decode.
//!
//! Low-level weight graph used by [`crate::generate`]. Prefer [`crate::generate::generate`]
//! (or [`crate::GreenModel::generate`]) for package-level entry; call [`DenseForward`] directly
//! only when you already have GGUF paths and want the graph without tokenize/decode-text.
//!
//! 1B packs keep 2D weights quantized ([`crate::quant_mat::QuantMat`]) and stream-dequant
//! during GEMV so peak RAM stays ~emb f32 + packed layers (~1.5 GB) instead of ~5 GB.

use std::time::Instant;

use crate::attention::{
    append_kv_reuse, multi_head_attend_decode, multi_head_attend_decode_parallel,
    recall_kv_f32, recall_kv_f32_into, MhaScratch,
};
use crate::backend::{CpuBackend, Scratch as ExpertScratch};
use crate::decode_profile;
use crate::embed::EmbeddingTable;
use crate::gguf_io::read_gguf;
use crate::gguf_load::{load_package_weights_lean, GgufLoadError, LeanGgufWeights, LeanTensor};
use crate::green_experts::{apply_moe_ffn, route_topk, PackageExpertStore};
use crate::kv::{F16, KvKeyQuant, KvStore, PagedKvMetrics, PagedRamKvStore};
use crate::paged::ExpertProvider;
use crate::norm::{residual_add, rms_norm, RMS_EPS_DEFAULT};
use crate::gpu_layer::{self, ForwardLoadConfig, GpuBundle, LayerMats};
use crate::quant_mat::{gemv_use_q8_act, gemv_use_repack_q8, lm_head_use_q8_act, project_qkv_quant, project_qkv_quant_shared, swiglu_quant, swiglu_quant_shared, swiglu_quant_repack, GemvActScratch, QuantMat};
use crate::quant_repack;
use crate::sys;
use crate::rope::{apply_rope_inplace, RopeConfig, RopeMode, RopeScaling};

#[inline]
fn prof_start() -> Option<Instant> {
    if decode_profile::enabled() {
        Some(Instant::now())
    } else {
        None
    }
}

#[inline]
fn prof_add(slot: &std::sync::atomic::AtomicU64, t: Option<Instant>) {
    if let Some(t0) = t {
        decode_profile::add_ns(slot, t0.elapsed().as_nanos());
    }
}

/// Errors from building / running the dense forward graph.
#[derive(Debug)]
pub enum ForwardError {
    Gguf(GgufLoadError),
    Missing(&'static str),
    Dim(String),
    Moe(String),
    Rope(String),
    Message(String),
}

impl std::fmt::Display for ForwardError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ForwardError::Gguf(e) => write!(f, "{e}"),
            ForwardError::Missing(s) => write!(f, "missing {s}"),
            ForwardError::Dim(s) => write!(f, "dim: {s}"),
            ForwardError::Moe(s) => write!(f, "ffn: {s}"),
            ForwardError::Rope(s) => write!(f, "rope: {s}"),
            ForwardError::Message(s) => write!(f, "{s}"),
        }
    }
}

impl std::error::Error for ForwardError {}

impl From<GgufLoadError> for ForwardError {
    fn from(value: GgufLoadError) -> Self {
        ForwardError::Gguf(value)
    }
}

/// Per-layer FFN: dense SwiGLU (Llama) or MoE router + paged experts.
enum LayerFfn {
    Dense {
        gate: QuantMat,
        up: QuantMat,
        down: QuantMat,
        inter: usize,
    },
    /// Router is `ffn_gate_inp` -> expert logits; experts live in [`DenseForward::experts`].
    Moe {
        router: QuantMat,
        top_k: usize,
        inter: usize,
        n_experts: usize,
    },
}

impl LayerFfn {
    fn inter(&self) -> usize {
        match self {
            LayerFfn::Dense { inter, .. } | LayerFfn::Moe { inter, .. } => *inter,
        }
    }
}

struct LayerWeights {
    attn_norm: Vec<f32>,
    ffn_norm: Vec<f32>,
    wq: QuantMat,
    wk: QuantMat,
    wv: QuantMat,
    wo: QuantMat,
    ffn: LayerFfn,
}

impl LayerWeights {
    fn inter(&self) -> usize {
        self.ffn.inter()
    }
}

/// Hints when loading MoE layers (`ffn_gate_inp` + [`PackageExpertStore`]).
#[derive(Clone, Copy, Debug)]
struct MoeLoadHints {
    top_k: usize,
    inter: usize,
    n_experts: usize,
}

/// Materialized dense (or MoE) model ready for CPU decode.
pub struct DenseForward {
    pub n_vocab: usize,
    pub n_embd: usize,
    pub n_layers: usize,
    pub n_heads: usize,
    pub n_kv_heads: usize,
    pub head_dim: usize,
    pub rope: RopeConfig,
    emb: EmbeddingTable,
    emb_lm: Option<QuantMat>,
    output: Vec<f32>,
    output_norm: Option<Vec<f32>>,
    layers: Vec<LayerWeights>,
    output_row_major: bool,
    tied_output: bool,
    rms_eps: f32,
    gpu: GpuBundle,
    /// Present when any layer uses [`LayerFfn::Moe`].
    experts: Option<PackageExpertStore>,
}


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
        let kv = PagedRamKvStore::with_policy(
            fwd.n_layers,
            kv_dim,
            hot_cap,
            kv_sinks,
            key_quant,
        );
        DecodeSession {
            kv,
            scratch: DecodeScratch::new(
                fwd.n_embd,
                fwd.n_heads,
                fwd.n_kv_heads,
                fwd.head_dim,
                fwd.layers[0].inter(),
                fwd.n_vocab,
                fwd.moe_scratch_dims(),
                hot_cap,
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

    #[inline]
    pub(crate) fn hot_cap(&self) -> usize {
        self.kv.hot_cap()
    }

    #[inline]
    pub(crate) fn sinks(&self) -> usize {
        self.kv.sinks()
    }

    #[inline]
    pub(crate) fn key_quant(&self) -> KvKeyQuant {
        self.kv.key_quant()
    }
}

/// Output of [`DenseForward::generate_greedy_paged`] (tokens + M5 KV residency metrics).
#[derive(Clone, Debug)]
pub struct GreedyGenerateOut {
    pub new_tokens: Vec<u32>,
    pub kv_metrics: PagedKvMetrics,
    pub kv_seq_len: usize,
    pub kv_hot_cap: usize,
    pub kv_hot_bytes: usize,
    /// M5.4: token capacity reserved (not full `-c` under grow-on-demand).
    pub kv_reserved_tokens: usize,
    /// Prefill wall seconds (prompt tokens through the stack).
    pub prefill_secs: f64,
    /// Decode-only wall seconds (new-token loop; excludes cold weight load).
    pub decode_secs: f64,
    /// Seconds from decode-loop start until the first new token is sampled (0 if none).
    pub first_token_secs: f64,
}

impl DenseForward {
    /// Default context length for native CPU decode when the request omits `ctx_len`.
    pub const DEFAULT_CTX_LEN: usize = 4096;
    /// Hard cap for native CPU path (KV residency + scratch). M5.4: 32k with
    /// grow-on-demand reserve — peak RSS stays near short-ctx, not full `-c`.
    pub const MAX_CTX_LEN_CPU: usize = 32768;
    /// Default hot window (= default ctx). Eviction only when `hot_cap` < seq.
    pub const DEFAULT_KV_HOT_CAP: usize = 4096;

    /// Prefill `prompt_ids` and return vocab logits for the last position (debug / parity).
    pub fn prefill_logits(&self, prompt_ids: &[u32]) -> Result<Vec<f32>, ForwardError> {
        if prompt_ids.is_empty() {
            return Err(ForwardError::Message("empty prompt".into()));
        }
        let kv_dim = self.n_kv_heads * self.head_dim;
        let mut kv = PagedRamKvStore::new(self.n_layers, kv_dim, prompt_ids.len().max(1));
        let mut scratch = DecodeScratch::new(
            self.n_embd,
            self.n_heads,
            self.n_kv_heads,
            self.head_dim,
            self.layers[0].inter(),
            self.n_vocab,
            self.moe_scratch_dims(),
            prompt_ids.len().max(1),
        );
        let mut token_pos = 0usize;
        for &id in prompt_ids {
            // R27.1: forward_token owns embed (device GET_ROWS or host row).
            self.forward_token(id, token_pos, &mut kv, &mut scratch)?;
            token_pos += 1;
        }
        let mut logits = vec![0.0f32; self.n_vocab];
        self.compute_logits(&mut scratch.hidden, &mut logits, &mut scratch.norm_tmp)?;
        Ok(logits)
    }

    /// Same as [`Self::prefill_logits`] but keeps K/V in f32 (no F16 KV round-trip) for parity.
    pub fn prefill_logits_f32_kv(&self, prompt_ids: &[u32]) -> Result<Vec<f32>, ForwardError> {
        if prompt_ids.is_empty() {
            return Err(ForwardError::Message("empty prompt".into()));
        }
        let kv_dim = self.n_kv_heads * self.head_dim;
        let mut past_k: Vec<Vec<f32>> = (0..self.n_layers).map(|_| Vec::new()).collect();
        let mut past_v: Vec<Vec<f32>> = (0..self.n_layers).map(|_| Vec::new()).collect();
        let mut scratch = DecodeScratch::new(
            self.n_embd,
            self.n_heads,
            self.n_kv_heads,
            self.head_dim,
            self.layers[0].inter(),
            self.n_vocab,
            self.moe_scratch_dims(),
            prompt_ids.len().max(1),
        );
        for (token_pos, &id) in prompt_ids.iter().enumerate() {
            self.embed_token(id, &mut scratch.hidden)?;
            scratch.residual.copy_from_slice(&scratch.hidden);
            for (layer_i, layer) in self.layers.iter().enumerate() {
                rms_norm(
                    &scratch.residual,
                    &layer.attn_norm,
                    self.rms_eps,
                    &mut scratch.attn.xn,
                );
                if gemv_use_q8_act() && layer.wq.ggml_type == crate::quant_mat::GGML_Q4_0 {
                    scratch.gemv_act.quantize_into(&scratch.attn.xn);
                    project_qkv_quant_shared(
                        &layer.wq,
                        &layer.wk,
                        &layer.wv,
                        &scratch.gemv_act,
                        &mut scratch.attn.q,
                        &mut scratch.attn.k,
                        &mut scratch.attn.v,
                    );
                } else {
                    project_qkv_quant(
                        &layer.wq,
                        &layer.wk,
                        &layer.wv,
                        &scratch.attn.xn,
                        &mut scratch.attn.q,
                        &mut scratch.attn.k,
                        &mut scratch.attn.v,
                    );
                }
                let positions = [token_pos as u32];
                apply_rope_inplace(
                    &mut scratch.attn.q,
                    &positions,
                    self.n_heads,
                    &self.rope,
                )
                .map_err(|e| ForwardError::Rope(e.to_string()))?;
                apply_rope_inplace(
                    &mut scratch.attn.k,
                    &positions,
                    self.n_kv_heads,
                    &self.rope,
                )
                .map_err(|e| ForwardError::Rope(e.to_string()))?;
                let seq = past_k[layer_i].len() / kv_dim + 1;
                scratch.attn.ensure_parallel(seq, self.head_dim);
                let score_cap = scratch.attn.score_cap();
                multi_head_attend_decode_parallel(
                    &scratch.attn.q,
                    &scratch.attn.k,
                    &scratch.attn.v,
                    &past_k[layer_i],
                    &past_v[layer_i],
                    self.n_heads,
                    self.n_kv_heads,
                    self.head_dim,
                    &mut scratch.attn.mha_per_kv,
                    &mut scratch.attn.head_scores,
                    score_cap,
                    &mut scratch.attn.attn,
                );
                past_k[layer_i].extend_from_slice(&scratch.attn.k);
                past_v[layer_i].extend_from_slice(&scratch.attn.v);
                layer.wo.gemv(&scratch.attn.attn, &mut scratch.attn.proj);
                residual_add(
                    &scratch.residual,
                    &scratch.attn.proj,
                    &mut scratch.layer_out,
                );
                rms_norm(
                    &scratch.layer_out,
                    &layer.ffn_norm,
                    self.rms_eps,
                    &mut scratch.attn.xn,
                );
                self.apply_ffn(layer_i, layer, &mut scratch)?;
                residual_add(
                    &scratch.layer_out,
                    &scratch.ffn_out,
                    &mut scratch.residual,
                );
            }
            scratch.hidden.copy_from_slice(&scratch.residual);
        }
        let mut logits = vec![0.0f32; self.n_vocab];
        self.compute_logits(&mut scratch.hidden, &mut logits, &mut scratch.norm_tmp)?;
        Ok(logits)
    }

    /// Build from package dense + metadata GGUF paths (lean quantized load).
    pub fn load(
        dense_gguf: &std::path::Path,
        metadata_gguf: Option<&std::path::Path>,
    ) -> Result<Self, ForwardError> {
        Self::load_with_config(dense_gguf, metadata_gguf, ForwardLoadConfig::default())
    }

    pub fn load_with_config(
        dense_gguf: &std::path::Path,
        metadata_gguf: Option<&std::path::Path>,
        config: ForwardLoadConfig,
    ) -> Result<Self, ForwardError> {
        Self::load_inner(dense_gguf, metadata_gguf, config, None, 1)
    }

    /// Load a MoE package: dense attn + `ffn_gate_inp` routers + [`PackageExpertStore`].
    ///
    /// Dense Llama/Hermes packages should keep using [`Self::load`] / [`Self::load_with_config`].
    /// GPU layer offload is forced off (experts stay on the CPU paging path).
    pub fn load_moe(
        dense_gguf: &std::path::Path,
        metadata_gguf: Option<&std::path::Path>,
        experts: PackageExpertStore,
        top_k: usize,
    ) -> Result<Self, ForwardError> {
        let mut config = ForwardLoadConfig::default();
        if config.gpu_layers > 0 {
        }
        config.gpu_layers = 0;
        Self::load_inner(dense_gguf, metadata_gguf, config, Some(experts), top_k.max(1))
    }

    fn load_inner(
        dense_gguf: &std::path::Path,
        metadata_gguf: Option<&std::path::Path>,
        mut config: ForwardLoadConfig,
        experts: Option<PackageExpertStore>,
        top_k: usize,
    ) -> Result<Self, ForwardError> {
        let weights = load_package_weights_lean(dense_gguf, metadata_gguf)?;
        let meta = metadata_gguf
            .filter(|p| p.is_file())
            .map(|p| read_gguf(p, false))
            .transpose()
            .map_err(|e| ForwardError::Message(e.to_string()))?;

        // Fall back to dense GGUF KV when metadata omits arch keys.
        let dense_kv = read_gguf(dense_gguf, false)
            .map_err(|e| ForwardError::Message(e.to_string()))?;
        let arch_src = meta.as_ref().unwrap_or(&dense_kv);

        let arch = arch_src
            .get("general.architecture")
            .and_then(|v| v.as_str())
            .unwrap_or("llama");

        let n_embd = kv_usize(Some(arch_src), &format!("{arch}.embedding_length")).unwrap_or(64);
        let n_layers = kv_usize(Some(arch_src), &format!("{arch}.block_count")).unwrap_or(1);
        let n_heads =
            kv_usize(Some(arch_src), &format!("{arch}.attention.head_count")).unwrap_or(4);
        let n_kv_heads = kv_usize(Some(arch_src), &format!("{arch}.attention.head_count_kv"))
            .unwrap_or(n_heads);
        let rope = RopeConfig::from_gguf(arch_src, arch, n_heads, n_embd);
        let rms_eps = kv_f32(Some(arch_src), &format!("{arch}.attention.layer_norm_rms_epsilon"))
            .unwrap_or(RMS_EPS_DEFAULT);

        let emb_t = weights.require("token_embd.weight")?;
        // Mmap quant emb: dequant one row per token — do not materialize ~2 GiB f32 (8B).
        let emb = EmbeddingTable::from_lean(emb_t, n_embd)
            .map_err(|e| ForwardError::Message(e.to_string()))?;
        let n_vocab = emb.n_vocab;

        // Untied (Llama-3.1 8B): pack has a real `output.weight` - use it as lm_head.
        // Tied (Llama-3.2 1B/3B): no `output.weight` - reuse token_embd.
        // Never treat "same nelems as embd" as tied: untied lm_head always matches that size.
        let (emb_lm, out_data, row_major, tied_output) =
            if let Some(out_t) = weights.get("output.weight") {
                let mut lm = out_t.to_quant_mat().map_err(ForwardError::from)?;
                lm = orient_in(lm, n_embd);
                lm = orient_out(lm, n_vocab);
                lm = crate::quant_mat::maybe_repack_q4_0(lm, "output.weight");
                // Keep Q4 packed for greedy/sample; avoid materializing ~2GB f32 logits table.
                (Some(lm), Vec::new(), true, false)
            } else {
                let mut lm = if let Some(q4) = weights.get("token_embd.weight.q4") {
                    q4.to_quant_mat().map_err(ForwardError::from)?
                } else {
                    emb_t.to_quant_mat().map_err(ForwardError::from)?
                };
                lm = orient_in(lm, n_embd);
                lm = orient_out(lm, n_vocab);
                lm = crate::quant_mat::maybe_repack_q4_0(lm, "token_embd.weight");
                (Some(lm), Vec::new(), true, true)
            };

        let output_norm = weights
            .get("output_norm.weight")
            .and_then(|t| t.as_f32_slice().map(|s| s.to_vec()))
            .or_else(|| {
                weights
                    .get("output_norm.weight")
                    .and_then(|t| t.to_f32_owned().ok())
            });

        let moe_hints = experts.as_ref().map(|store| MoeLoadHints {
            top_k: top_k.min(store.experts().max(1)),
            inter: store.inter(),
            n_experts: store.experts(),
        });
        let mut layers = Vec::with_capacity(n_layers);
        for layer in 0..n_layers {
            layers.push(load_layer(&weights, layer, n_embd, moe_hints.as_ref())?);
        }
        if layers.is_empty() {
            return Err(ForwardError::Message("no layers loaded".into()));
        }
        if moe_hints.is_some() {
            let moe_layers = layers.iter().filter(|l| matches!(l.ffn, LayerFfn::Moe { .. })).count();
            if moe_layers == 0 {
                return Err(ForwardError::Moe(
                    "PackageExpertStore provided but no ffn_gate_inp / MoE layers found".into(),
                ));
            }
        }

        let any_moe = layers.iter().any(|l| matches!(l.ffn, LayerFfn::Moe { .. }));
        let gpu = if any_moe || config.gpu_layers == 0 {
            if any_moe && config.gpu_layers > 0 {
                eprintln!("ge: MoE layers present - GPU FFN offload disabled");
            }
            GpuBundle::none()
        } else {
            let mat_views: Vec<LayerMats> = layers
                .iter()
                .filter_map(|l| Self::layer_mats_dense(l))
                .collect();
            if mat_views.len() != n_layers {
                return Err(ForwardError::Message(
                    "internal: mixed dense/MoE GPU upload not supported".into(),
                ));
            }
            // M5.5 / R28: untied lm_head is a separate mmap range — release after H2D.
            // Tied emb/lm share one range; never unlock (embed still needs host rows).
            config.release_lm_host = !tied_output;
            GpuBundle::load(
                &mat_views,
                n_layers,
                config,
                emb_lm.as_ref(),
                output_norm.as_deref(),
            )
        };

        Ok(DenseForward {
            n_vocab,
            n_embd,
            n_layers,
            n_heads,
            n_kv_heads,
            head_dim: rope.head_dim,
            rope,
            emb,
            emb_lm,
            output: out_data,
            output_norm,
            layers,
            output_row_major: row_major,
            tied_output,
            rms_eps,
            gpu,
            experts,
        })
    }

    fn moe_scratch_dims(&self) -> Option<(usize, usize)> {
        let store = self.experts.as_ref()?;
        Some((store.experts(), store.inter()))
    }

    pub fn is_moe(&self) -> bool {
        self.experts.is_some()
    }

    pub fn expert_metrics(&self) -> Option<crate::green_experts::ExpertResidencyMetrics> {
        self.experts.as_ref().map(|s| s.metrics())
    }

    fn layer_mats_dense(layer: &LayerWeights) -> Option<LayerMats<'_>> {
        match &layer.ffn {
            LayerFfn::Dense { gate, up, down, .. } => Some(LayerMats {
                wq: &layer.wq,
                wk: &layer.wk,
                wv: &layer.wv,
                wo: &layer.wo,
                gate,
                up,
                down,
                attn_norm: &layer.attn_norm,
                ffn_norm: &layer.ffn_norm,
            }),
            LayerFfn::Moe { .. } => None,
        }
    }

    pub fn gpu_layers(&self) -> usize {
        self.gpu.gpu_layers
    }

    pub fn generate_greedy(
        &self,
        prompt_ids: &[u32],
        max_new: usize,
    ) -> Result<Vec<u32>, ForwardError> {
        Ok(self
            .generate_greedy_paged(prompt_ids, max_new, Self::DEFAULT_KV_HOT_CAP)?
            .new_tokens)
    }

    pub fn generate_greedy_paged(
        &self,
        prompt_ids: &[u32],
        max_new: usize,
        kv_hot_cap: usize,
    ) -> Result<GreedyGenerateOut, ForwardError> {
        self.generate_greedy_paged_stops(prompt_ids, max_new, kv_hot_cap, &[])
    }

    /// Greedy decode; stops (without emitting) when a sampled id is in `stop_ids`.
    pub fn generate_greedy_paged_stops(
        &self,
        prompt_ids: &[u32],
        max_new: usize,
        kv_hot_cap: usize,
        stop_ids: &[u32],
    ) -> Result<GreedyGenerateOut, ForwardError> {
        self.generate_paged(
            prompt_ids,
            max_new,
            kv_hot_cap,
            stop_ids,
            &crate::sample::SampleParams::greedy(),
        )
    }

    /// Decode with [`crate::sample::SampleParams`]; stop ids are not emitted.
    pub fn generate_paged(
        &self,
        prompt_ids: &[u32],
        max_new: usize,
        kv_hot_cap: usize,
        stop_ids: &[u32],
        sample: &crate::sample::SampleParams,
    ) -> Result<GreedyGenerateOut, ForwardError> {
        self.generate_paged_ctx(
            prompt_ids,
            max_new,
            Self::DEFAULT_CTX_LEN,
            kv_hot_cap,
            crate::kv::DEFAULT_ATTENTION_SINKS,
            KvKeyQuant::F16,
            stop_ids,
            sample,
        )
    }

    /// Full CPU decode with ctx / KV policy (sinks + key quant).
    #[allow(clippy::too_many_arguments)]
    pub fn generate_paged_ctx(
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
        self.gpu.clear_kv();
        // MC.2: mirror host StreamingLLM window onto device KV (peak MiB ≈ hot_cap).
        self.gpu.set_kv_window(
            session.kv.hot_cap() as u32,
            session.kv.sinks() as u32,
        );
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
        session.kv.reserve_for_request(prompt_ids.len(), max_new);
        // R28.2: stage device KV for the *prompt* (+ small headroom), not prompt+max_new.
        // Decode append grows via kernel `ensure_kv_cap(t+1)` — avoids full `-c` VRAM at start.
        let kv_stage = prompt_ids
            .len()
            .saturating_add(crate::kv::KV_INITIAL_RESERVE_MIN)
            .max(crate::kv::KV_INITIAL_RESERVE_MIN)
            .min(session.kv.hot_cap());
        self.gpu.ensure_kv_cap(kv_stage as u32);

        let kv_dyn: &mut dyn KvStore = &mut session.kv;
        let scratch = &mut session.scratch;
        // Attn score scratch grows with used seq up to hot window — never pre-size to full -c.
        scratch_prepare_attn_window(scratch, kv_stage, self.head_dim);
        let mut token_pos = 0usize;

        let t_prefill = Instant::now();
        for &id in prompt_ids {
            // R27.1: device quantized embed inside forward_token when possible.
            self.forward_token(id, token_pos, kv_dyn, scratch)?;
            token_pos += 1;
        }
        let prefill_secs = t_prefill.elapsed().as_secs_f64();
        // Decode-only CUDA graphs: arm after prefill (opt-in GE_CUDA_GRAPH=1; default off).
        if self.gpu.gpu_layers > 0 {
            gpu_layer::graph_arm(&self.gpu);
        }

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
        let mut generated = Vec::with_capacity(max_new);
        let mut first_token_secs = 0.0f64;
        let t_decode = Instant::now();
        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));
        decode_profile::reset();

        // GE_NGRAM_SPEC: PLD batch-K draft γ∈{1..2} CPU / {1..4} GPU + greedy verify + KvStore::truncate.
        // CPU-only by default; GPU batch-K (γ up to 4) needs GE_NGRAM_SPEC_GPU=1.
        let pld_gpu = self.gpu.gpu_layers > 0;
        let ngram_on = crate::ngram_spec::enabled()
            && (self.gpu.gpu_layers == 0 || crate::ngram_spec::gpu_pld_allowed());
        if crate::ngram_spec::enabled()
            && self.gpu.gpu_layers > 0
            && !crate::ngram_spec::gpu_pld_allowed()
        {
            static PLD_GPU_WARN: std::sync::Once = std::sync::Once::new();
            PLD_GPU_WARN.call_once(|| {
                eprintln!(
                    "ge: PLD (GE_NGRAM_SPEC) disabled on GPU until verify safe — use GE_NGRAM_SPEC_GPU=1 to override"
                );
            });
        }
        let mut ngram = if ngram_on {
            let mut d = crate::ngram_spec::NgramSpecDrafter::with_gpu(pld_gpu);
            d.seed_from_prompt(prompt_ids);
            Some(d)
        } else {
            None
        };
        let pld_k_max = crate::ngram_spec::effective_gamma(pld_gpu);

        let sample_next = |history: &[u32],
                           scratch: &mut DecodeScratch,
                           rng: &mut crate::sample::XorShift64|
         -> Result<u32, ForwardError> {
            let t_lm = if decode_profile::enabled() {
                Some(std::time::Instant::now())
            } else {
                None
            };
            let next = if sample.needs_full_logits() {
                scratch.ensure_full_logits();
                self.compute_logits(
                    &mut scratch.hidden,
                    &mut scratch.logits,
                    &mut scratch.norm_tmp,
                )?;
                crate::sample::sample_token(
                    &mut scratch.logits,
                    history,
                    sample,
                    rng,
                    &mut scratch.probs,
                )
            } else {
                let need_logits = match self.emb_lm.as_ref() {
                    Some(lm)
                        if lm.ggml_type == crate::quant_mat::GGML_Q4_0
                            || lm.ggml_type == crate::quant_repack::GGML_Q4_0_4X8
                            || lm.ggml_type == crate::quant_repack::GGML_Q4_0_8X8 =>
                    {
                        false
                    }
                    _ => true,
                };
                if need_logits {
                    scratch.ensure_full_logits();
                }
                self.logits_argmax_greedy(
                    &mut scratch.hidden,
                    &mut scratch.norm_tmp,
                    &mut scratch.logits,
                    &mut scratch.gemv_act,
                )?
            };
            if let Some(t0) = t_lm {
                decode_profile::add_ns(
                    &decode_profile::accum().lm_head_ns,
                    t0.elapsed().as_nanos(),
                );
                decode_profile::accum()
                    .tokens
                    .fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            }
            Ok(next)
        };

        'decode: while generated.len() < max_new {
            // --- PLD batch-K path (γ≤4 GPU / γ≤2 CPU): draft → serial verify → truncate rewind ---
            if let Some(ref mut d) = ngram {
                let room = max_new - generated.len();
                let drafts = d.draft(&history);
                let gamma = drafts.len().min(room).min(pld_k_max);
                if gamma > 0 {
                    let draft_slice = &drafts[..gamma];
                    let seq_before = kv_dyn.seq_len();
                    let pos_start = token_pos;
                    let hidden_save = scratch.hidden.clone();
                    let mut targets = Vec::with_capacity(gamma);
                    for &tok in draft_slice {
                        let pred = sample_next(&history, scratch, &mut rng)?;
                        targets.push(pred);
                        self.forward_token(tok, token_pos, kv_dyn, scratch)?;
                        token_pos += 1;
                    }
                    let mut vr =
                        crate::speculative::verify_draft_greedy(draft_slice, &targets);
                    if vr.accepted == gamma {
                        let bonus_pred = sample_next(&history, scratch, &mut rng)?;
                        vr.bonus_token = Some(bonus_pred);
                    }
                    crate::speculative::rewind_rejected_drafts(
                        kv_dyn,
                        seq_before,
                        vr.accepted,
                    );

                    let mut stop = false;
                    if vr.accepted < gamma {
                        // rewind kept accepted suffix in KV; re-forward must append at seq_before.
                        kv_dyn.truncate(seq_before);
                        if self.gpu.gpu_layers > 0 {
                            self.gpu.truncate_kv(seq_before as u32);
                        }
                        scratch.hidden.copy_from_slice(&hidden_save);
                        token_pos = pos_start;
                        for &tok in &draft_slice[..vr.accepted] {
                            if stop_ids.iter().any(|&s| s == tok) {
                                stop = true;
                                break;
                            }
                            if generated.is_empty() {
                                first_token_secs = t_decode.elapsed().as_secs_f64();
                            }
                            generated.push(tok);
                            history.push(tok);
                            d.observe_committed(&history);
                            self.forward_token(tok, token_pos, kv_dyn, scratch)?;
                            token_pos += 1;
                        }
                    } else if self.gpu.gpu_layers > 0 {
                        self.gpu
                            .truncate_kv(seq_before.saturating_add(vr.accepted) as u32);
                    }
                    if vr.accepted >= gamma {
                        for &tok in draft_slice {
                            if stop_ids.iter().any(|&s| s == tok) {
                                stop = true;
                                break;
                            }
                            if generated.is_empty() {
                                first_token_secs = t_decode.elapsed().as_secs_f64();
                            }
                            generated.push(tok);
                            history.push(tok);
                            d.observe_committed(&history);
                        }
                    }
                    if !stop {
                        if let Some(corr) = vr.bonus_token {
                            if stop_ids.iter().any(|&s| s == corr) {
                                stop = true;
                            } else if generated.len() < max_new {
                                if generated.is_empty() {
                                    first_token_secs = t_decode.elapsed().as_secs_f64();
                                }
                                generated.push(corr);
                                history.push(corr);
                                d.observe_committed(&history);
                                self.forward_token(corr, token_pos, kv_dyn, scratch)?;
                                token_pos += 1;
                            }
                        }
                    }
                    d.on_step(gamma, vr.accepted);
                    crate::game_mode::pace_between_tokens();
                    if stop || generated.len() >= max_new {
                        break 'decode;
                    }
                    continue 'decode;
                }
            }

            // --- Dense tg1 fallback ---
            let next = sample_next(&history, scratch, &mut rng)?;
            if stop_ids.iter().any(|&s| s == next) {
                break;
            }
            if generated.is_empty() {
                first_token_secs = t_decode.elapsed().as_secs_f64();
            }
            generated.push(next);
            history.push(next);
            if let Some(ref mut d) = ngram {
                d.observe_committed(&history);
            }
            self.forward_token(next, token_pos, kv_dyn, scratch)?;
            token_pos += 1;
            crate::game_mode::pace_between_tokens();
        }
        let decode_secs = t_decode.elapsed().as_secs_f64();
        decode_profile::report();
        if let Some(d) = ngram {
            let st = d.into_stats();
            if st.steps > 0 {
                eprintln!("{}", st.report_line());
            }
        }
        Ok(GreedyGenerateOut {
            new_tokens: generated,
            kv_metrics: session.kv.metrics(),
            kv_seq_len: session.kv.seq_len(),
            kv_hot_cap: session.kv.hot_cap(),
            kv_hot_bytes: session.kv.hot_bytes(),
            kv_reserved_tokens: session.kv.reserved_capacity_tokens(),
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


    fn apply_ffn(
        &self,
        layer_i: usize,
        layer: &LayerWeights,
        scratch: &mut DecodeScratch,
    ) -> Result<(), ForwardError> {
        match &layer.ffn {
            LayerFfn::Dense { gate, up, down, .. } => {
                if layer_i < self.gpu.gpu_layers || self.gpu.stream_overflow {
                    let mats = Self::layer_mats_dense(layer).ok_or_else(|| {
                        ForwardError::Moe("GPU FFN requires dense weights".into())
                    })?;
                    gpu_layer::ffn_swiglu(
                        &self.gpu,
                        &mats,
                        layer_i,
                        &scratch.attn.xn,
                        &mut scratch.ffn_g,
                        &mut scratch.ffn_u,
                        &mut scratch.ffn_out,
                    );
                    // Overlap next overflow H2D with residual/next-layer CPU attn.
                    if self.gpu.stream_overflow {
                        let next = layer_i + 1;
                        if next >= self.gpu.gpu_layers && next < self.layers.len() {
                            if let Some(next_mats) = Self::layer_mats_dense(&self.layers[next]) {
                                gpu_layer::stream_prefetch_start(&self.gpu, next, &next_mats);
                            }
                        }
                    }
                } else if gemv_use_repack_q8()
                    && (gate.ggml_type == crate::quant_repack::GGML_Q4_0_4X8
                        || up.ggml_type == crate::quant_repack::GGML_Q4_0_4X8
                        || gate.ggml_type == crate::quant_repack::GGML_Q4_0_8X8
                        || up.ggml_type == crate::quant_repack::GGML_Q4_0_8X8)
                {
                    scratch.gemv_act.quantize_into(&scratch.attn.xn);
                    swiglu_quant_repack(
                        &scratch.gemv_act,
                        gate,
                        up,
                        down,
                        &mut scratch.ffn_g,
                        &mut scratch.ffn_u,
                        &mut scratch.ffn_out,
                    );
                } else if gemv_use_q8_act() && gate.ggml_type == crate::quant_mat::GGML_Q4_0 {
                    scratch.gemv_act.quantize_into(&scratch.attn.xn);
                    swiglu_quant_shared(
                        &scratch.gemv_act,
                        gate,
                        up,
                        down,
                        &mut scratch.ffn_g,
                        &mut scratch.ffn_u,
                        &mut scratch.ffn_out,
                    );
                } else {
                    swiglu_quant(
                        &scratch.attn.xn,
                        gate,
                        up,
                        down,
                        &mut scratch.ffn_g,
                        &mut scratch.ffn_u,
                        &mut scratch.ffn_out,
                    );
                }
                Ok(())
            }
            LayerFfn::Moe {
                router,
                top_k,
                n_experts,
                ..
            } => {
                let store = self.experts.as_ref().ok_or_else(|| {
                    ForwardError::Moe("MoE layer without PackageExpertStore".into())
                })?;
                let moe = scratch.moe.as_mut().ok_or_else(|| {
                    ForwardError::Moe("MoE scratch not allocated".into())
                })?;
                if moe.router_logits.len() < *n_experts {
                    moe.router_logits.resize(*n_experts, 0.0);
                }
                router.gemv(&scratch.attn.xn, &mut moe.router_logits[..*n_experts]);
                let route = route_topk(&moe.router_logits[..*n_experts], *top_k);
                store.prefetch_experts(layer_i, &route.experts);
                apply_moe_ffn(
                    store,
                    &CpuBackend,
                    layer_i,
                    &scratch.attn.xn,
                    &route.experts,
                    &route.gates,
                    &mut moe.expert,
                    &mut scratch.ffn_out,
                );
                // Routing-aware hot-set (reuse-distance + router-prob gate weights).
                store.observe_routing(layer_i, &route.experts, Some(&route.gates));
                Ok(())
            }
        }
    }

    fn embed_token(&self, id: u32, out: &mut [f32]) -> Result<(), ForwardError> {
        self.emb
            .write_row(id, out)
            .map_err(|e| ForwardError::Message(e.to_string()))
    }

    fn forward_token(
        &self,
        token_id: u32,
        token: usize,
        kv: &mut dyn KvStore,
        scratch: &mut DecodeScratch,
    ) -> Result<(), ForwardError> {
        let rope_gpu_ok = matches!(
            self.rope.scaling,
            RopeScaling::None | RopeScaling::Ntk { .. }
        );
        // Partial attn_block_fused (H2D xn per layer). 8B default ON (opt-out GE_GPU_ATTN_8B=0):
        // post graph_arm-dup cleanup, ATTN1 med ≥ ATTN0 + idx11/Hello + ge --chat stable.
        // Full layer_fused stack (residual resident, device attn+FFN): always on for GPU layers —
        // gating it behind gpu_attn_fuse regressed 8B ~33→~13 tok/s with fixed RoPE + IGNORE_EOS parity.
        let gpu_attn_block_fuse = self.n_embd < 4096
            || std::env::var("GE_GPU_ATTN_8B").ok().as_deref() != Some("0");
        let n_gpu = if rope_gpu_ok { self.gpu.gpu_layers } else { 0 };
        let mut start_layer = 0usize;

        // R27.1 / llama #25962: opt-in device GET_ROWS (GE_DEVICE_EMBED=1). Default host embed + H2D
        // — quiet ISO −3.6% on Q4_0 `.green` (row dequant already cheap; no full-table thrash).
        let residual_on_device = if n_gpu > 0 && n_gpu >= self.layers.len() {
            gpu_layer::residual_from_embed(&self.gpu, token_id)
        } else {
            false
        };
        if !residual_on_device {
            self.embed_token(token_id, &mut scratch.hidden)?;
            scratch.residual.copy_from_slice(&scratch.hidden);
        }
        if n_gpu > 0
            && (residual_on_device
                || gpu_layer::residual_begin(&self.gpu, &scratch.residual))
        {
            let mut fused_upto = 0usize;
            let rope_neox = matches!(self.rope.mode, RopeMode::Neox);
            let theta = self.rope.effective_theta();
            let freq_scale = self.rope.freq_scale;
            for layer_i in 0..n_gpu {
                let layer = &self.layers[layer_i];
                let mats = Self::layer_mats_dense(layer).ok_or_else(|| {
                    ForwardError::Moe("GPU path requires dense FFN layers".into())
                })?;
                let t_layer = prof_start();
                if !gpu_layer::layer_fused(
                    &self.gpu,
                    &mats,
                    layer_i,
                    token as u32,
                    self.n_heads,
                    self.n_kv_heads,
                    self.head_dim,
                    theta,
                    freq_scale,
                    rope_neox,
                    self.rms_eps,
                ) {
                    break;
                }
                // Charge fused layer time to qkv+ffn buckets (~attn vs FFN share).
                if let Some(t0) = t_layer {
                    let ns = t0.elapsed().as_nanos();
                    decode_profile::add_ns(&decode_profile::accum().qkv_ns, ns / 3);
                    decode_profile::add_ns(&decode_profile::accum().ffn_ns, ns - ns / 3);
                }
                let kv_dim = self.n_kv_heads * self.head_dim;
                scratch.attn.new_k[..kv_dim].fill(0.0);
                scratch.attn.new_v[..kv_dim].fill(0.0);
                let slot = kv.seq_len();
                let t_kv_a = prof_start();
                append_kv_reuse(
                    kv,
                    layer_i,
                    slot,
                    &scratch.attn.new_k[..kv_dim],
                    &scratch.attn.new_v[..kv_dim],
                    &mut scratch.kv_k16,
                    &mut scratch.kv_v16,
                );
                prof_add(&decode_profile::accum().kv_append_ns, t_kv_a);
                fused_upto = layer_i + 1;
            }
            if fused_upto == self.layers.len() {
                // Full stack on device - keep residual resident for lm_head argmax.
                gpu_layer::set_residual_live(&self.gpu, true);
            } else if !gpu_layer::residual_end(&self.gpu, &mut scratch.residual) {
                if fused_upto > 0 {
                    return Err(ForwardError::Message(
                        "GPU residual D2H failed after fused layers".into(),
                    ));
                }
            }
            start_layer = fused_upto;
        }

        if start_layer >= self.layers.len() {
            // Host residual/hidden may be stale when residual_live; greedy path uses device argmax.
            if !gpu_layer::residual_is_live(&self.gpu) {
                scratch.hidden.copy_from_slice(&scratch.residual);
            }
            return Ok(());
        }

        for layer_i in start_layer..self.layers.len() {
            let layer = &self.layers[layer_i];
            // After fused resident stack (no host apply_ffn), start first overflow upload
            // before CPU attn so H2D overlaps attention.
            if self.gpu.stream_overflow && layer_i >= self.gpu.gpu_layers {
                if let Some(mats) = Self::layer_mats_dense(layer) {
                    gpu_layer::stream_prefetch_start(&self.gpu, layer_i, &mats);
                }
            }
            rms_norm(
                &scratch.residual,
                &layer.attn_norm,
                self.rms_eps,
                &mut scratch.attn.xn,
            );

            // GPU fused path: QKV + RoPE + device KV + GQA + WO (one H2D / one D2H).
            let mut fused_gpu = false;
            if layer_i < self.gpu.gpu_layers && rope_gpu_ok && gpu_attn_block_fuse {
                let mats = Self::layer_mats_dense(layer).ok_or_else(|| {
                    ForwardError::Moe("GPU path requires dense FFN layers".into())
                })?;
                let t_qkv = prof_start();
                fused_gpu = gpu_layer::attn_block_fused(
                    &self.gpu,
                    &mats,
                    layer_i,
                    &scratch.attn.xn,
                    &mut scratch.attn.proj,
                    token as u32,
                    self.n_heads,
                    self.n_kv_heads,
                    self.head_dim,
                    self.rope.effective_theta(),
                    self.rope.freq_scale,
                    matches!(self.rope.mode, RopeMode::Neox),
                );
                if fused_gpu {
                    prof_add(&decode_profile::accum().qkv_ns, t_qkv);
                    // CPU KV placeholder keeps layer_len lockstep for partial offload / metrics.
                    let kv_dim = self.n_kv_heads * self.head_dim;
                    scratch.attn.new_k[..kv_dim].fill(0.0);
                    scratch.attn.new_v[..kv_dim].fill(0.0);
                    let slot = kv.seq_len();
                    let t_kv_a = prof_start();
                    append_kv_reuse(
                        kv,
                        layer_i,
                        slot,
                        &scratch.attn.new_k[..kv_dim],
                        &scratch.attn.new_v[..kv_dim],
                        &mut scratch.kv_k16,
                        &mut scratch.kv_v16,
                    );
                    prof_add(&decode_profile::accum().kv_append_ns, t_kv_a);
                }
            }

            if !fused_gpu {
            let t_qkv = prof_start();
            if layer_i < self.gpu.gpu_layers {
                let mats = Self::layer_mats_dense(layer).ok_or_else(|| {
                    ForwardError::Moe("GPU path requires dense FFN layers".into())
                })?;
                gpu_layer::attn_qkv_gemv(
                    &self.gpu, &mats, layer_i, &scratch.attn.xn,
                    &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,
                );
            } else if gemv_use_q8_act() && layer.wq.ggml_type == crate::quant_mat::GGML_Q4_0 {
                scratch.gemv_act.quantize_into(&scratch.attn.xn);
                project_qkv_quant_shared(
                    &layer.wq, &layer.wk, &layer.wv, &scratch.gemv_act,
                    &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,
                );
            } else {
                project_qkv_quant(
                    &layer.wq, &layer.wk, &layer.wv, &scratch.attn.xn,
                    &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,
                );
            }
            prof_add(&decode_profile::accum().qkv_ns, t_qkv);

            let t_rope = prof_start();
            let positions = [token as u32];
            apply_rope_inplace(&mut scratch.attn.q, &positions, self.n_heads, &self.rope)
                .map_err(|e| ForwardError::Rope(e.to_string()))?;
            apply_rope_inplace(
                &mut scratch.attn.k,
                &positions,
                self.n_kv_heads,
                &self.rope,
            )
            .map_err(|e| ForwardError::Rope(e.to_string()))?;
            prof_add(&decode_profile::accum().rope_ns, t_rope);

            let t_kv_a = prof_start();
            scratch.attn.new_k.copy_from_slice(&scratch.attn.k);
            scratch.attn.new_v.copy_from_slice(&scratch.attn.v);
            // Append at resident end (absolute `token` is for RoPE only; eviction may shrink store).
            let slot = kv.seq_len();
            // Layers append in order; layer 0's seq_len leads. Use per-call length before this layer.
            // `append_kv` -> store ignores absolute index and uses layer_len.
            append_kv_reuse(
                kv, layer_i, slot, &scratch.attn.new_k, &scratch.attn.new_v,
                &mut scratch.kv_k16, &mut scratch.kv_v16,
            );
            prof_add(&decode_profile::accum().kv_append_ns, t_kv_a);

            let t_kv_r = prof_start();
            let resident = kv.seq_len();
            if resident <= 1 {
                scratch.past_k.clear();
                scratch.past_v.clear();
            } else {
                recall_kv_f32_into(kv, layer_i, 0..resident - 1, &mut scratch.past_k, &mut scratch.past_v);
            }
            prof_add(&decode_profile::accum().kv_recall_ns, t_kv_r);

            scratch.attn.ensure_parallel(resident, self.head_dim);
            let score_cap = scratch.attn.score_cap();
            let t_attn = prof_start();
            multi_head_attend_decode_parallel(
                &scratch.attn.q, &scratch.attn.k, &scratch.attn.v,
                &scratch.past_k, &scratch.past_v,
                self.n_heads, self.n_kv_heads, self.head_dim,
                &mut scratch.attn.mha_per_kv, &mut scratch.attn.head_scores,
                score_cap, &mut scratch.attn.attn,
            );
            prof_add(&decode_profile::accum().attn_ns, t_attn);

            let t_wo = prof_start();
            if layer_i < self.gpu.gpu_layers {
                let mats = Self::layer_mats_dense(layer).ok_or_else(|| {
                    ForwardError::Moe("GPU path requires dense FFN layers".into())
                })?;
                gpu_layer::wo_gemv(&self.gpu, &mats, layer_i, &scratch.attn.attn, &mut scratch.attn.proj);
            } else {
                layer.wo.gemv(&scratch.attn.attn, &mut scratch.attn.proj);
            }
            prof_add(&decode_profile::accum().wo_ns, t_wo);
            } // !fused_gpu

            residual_add(
                &scratch.residual,
                &scratch.attn.proj,
                &mut scratch.layer_out,
            );

            rms_norm(
                &scratch.layer_out,
                &layer.ffn_norm,
                self.rms_eps,
                &mut scratch.attn.xn,
            );
            let t_ffn = prof_start();
            self.apply_ffn(layer_i, layer, scratch)?;
            prof_add(&decode_profile::accum().ffn_ns, t_ffn);
            residual_add(
                &scratch.layer_out,
                &scratch.ffn_out,
                &mut scratch.residual,
            );
        }
        scratch.hidden.copy_from_slice(&scratch.residual);
        Ok(())
    }

    fn logits_argmax_greedy(
        &self,
        hidden: &mut [f32],
        norm_tmp: &mut [f32],
        logits: &mut [f32],
        gemv_act: &mut GemvActScratch,
    ) -> Result<u32, ForwardError> {
        // Full GPU stack: device residual -> output RMSNorm -> lm_head -> argmax (one u32 D2H).
        // R19: async argmax — begin (no sync), let caller do CPU work, end drains event.
        // Falls back to sync path when GE_ASYNC_ARGMAX=0 (default) or util>75%.
        if let Some(id) = gpu_layer::try_residual_argmax(
            &self.gpu,
            self.rms_eps,
            self.output_norm.is_some(),
        ) {
            return Ok(id);
        }
        // Pull residual to host if it was left device-resident (argmax unavailable).
        if gpu_layer::sync_residual_live(&self.gpu, hidden) {
            // hidden already filled by residual_end
        }
        let h = if let Some(ref wn) = self.output_norm {
            rms_norm(hidden, wn, self.rms_eps, norm_tmp);
            norm_tmp.as_ref()
        } else {
            hidden
        };
        if let Some(ref lm) = self.emb_lm {
            if lm.ggml_type == crate::quant_repack::GGML_Q4_0_4X8 && gemv_use_repack_q8() {
                let _ = logits;
                gemv_act.quantize_into(h);
                return Ok(quant_repack::argmax_q4_0_4x8(
                    &gemv_act.act, &lm.packed, lm.in_dim, lm.out_dim,
                ));
            }
            if lm.ggml_type == crate::quant_repack::GGML_Q4_0_8X8 && gemv_use_repack_q8() {
                let _ = logits;
                gemv_act.quantize_into(h);
                return Ok(quant_repack::argmax_q4_0_8x8(
                    &gemv_act.act, &lm.packed, lm.in_dim, lm.out_dim,
                ));
            }
            if lm.ggml_type == crate::quant_mat::GGML_Q4_0 {
                let _ = logits;
                // Prefer resident CUDA lm_head (tied embd or untied output.weight).
                if let Some(id) = gpu_layer::lm_head_argmax(&self.gpu, h) {
                    return Ok(id);
                }
                if lm_head_use_q8_act() {
                    gemv_act.quantize_into(h);
                    return Ok(lm.argmax_gemv_act(h, &gemv_act.act));
                }
                return Ok(lm.argmax_gemv(h));
            }
            if !self.tied_output {
                lm.gemv(h, logits);
                let mut best_id = 0u32;
                let mut best_score = f32::NEG_INFINITY;
                for (i, &score) in logits.iter().enumerate() {
                    if score >= best_score {
                        best_score = score;
                        best_id = i as u32;
                    }
                }
                return Ok(best_id);
            }
        }
        let table = if self.tied_output {
            self.emb.as_dense_weight().unwrap_or(&[])
        } else {
            self.output.as_slice()
        };
        if table.len() < self.n_vocab.saturating_mul(self.n_embd) {
            return Err(ForwardError::Message(
                "lm_head fallback needs dense emb/output; quant path requires emb_lm".into(),
            ));
        }
        let n_embd = self.n_embd;
        let n_vocab = self.n_vocab;
        let (_best_score, best_id) = sys::decode_pool().install(|| {
            use rayon::prelude::*;
            (0..n_vocab)
                .into_par_iter()
                .map(|v| {
                    let row = &table[v * n_embd..(v + 1) * n_embd];
                    let mut s = 0.0f32;
                    let mut i = 0usize;
                    while i + 4 <= n_embd {
                        s += h[i] * row[i]
                            + h[i + 1] * row[i + 1]
                            + h[i + 2] * row[i + 2]
                            + h[i + 3] * row[i + 3];
                        i += 4;
                    }
                    while i < n_embd {
                        s += h[i] * row[i];
                        i += 1;
                    }
                    (s, v as u32)
                })
                .reduce(
                    || (f32::NEG_INFINITY, 0u32),
                    |a, b| if a.0 > b.0 { a } else { b },
                )
        });
        Ok(best_id)
    }

    fn compute_logits(
        &self,
        hidden: &mut [f32],
        logits: &mut [f32],
        norm_tmp: &mut [f32],
    ) -> Result<(), ForwardError> {
        let _ = gpu_layer::sync_residual_live(&self.gpu, hidden);
        let h = if let Some(ref wn) = self.output_norm {
            rms_norm(hidden, wn, self.rms_eps, norm_tmp);
            norm_tmp.as_ref()
        } else {
            hidden
        };
        // Q4/F32 lm_head: prefer resident CUDA. Untied must keep using emb_lm
        // (never fall through to empty f32 output table - ec2e867).
        if let Some(ref lm) = self.emb_lm {
            let can_gpu = lm.ggml_type == crate::quant_mat::GGML_Q4_0
                || lm.ggml_type == crate::quant_mat::GGML_F32;
            if can_gpu && gpu_layer::lm_head_gemv(&self.gpu, h, logits) {
                return Ok(());
            }
            // Tied or untied: packed lm_head (mmap) — never require dense emb.weight.
            lm.gemv(h, logits);
            return Ok(());
        }
        let table = if self.tied_output {
            self.emb.as_dense_weight().unwrap_or(&[])
        } else {
            self.output.as_slice()
        };
        if table.len() < self.n_vocab.saturating_mul(self.n_embd) {
            return Err(ForwardError::Message(
                "compute_logits needs emb_lm or dense emb/output table".into(),
            ));
        }
        let n_embd = self.n_embd;
        if self.output_row_major || self.tied_output {
            use rayon::prelude::*;
            sys::decode_pool().install(|| {
                logits.par_iter_mut().enumerate().for_each(|(v, out)| {
                let row = &table[v * n_embd..(v + 1) * n_embd];
                let mut s = 0.0f32;
                // Blocked for auto-vectorization / ILP.
                let mut i = 0usize;
                while i + 4 <= n_embd {
                    s += h[i] * row[i]
                        + h[i + 1] * row[i + 1]
                        + h[i + 2] * row[i + 2]
                        + h[i + 3] * row[i + 3];
                    i += 4;
                }
                while i < n_embd {
                    s += h[i] * row[i];
                    i += 1;
                }
                *out = s;
                });
            });
        } else {
            for o in 0..self.n_vocab {
                let mut s = 0.0f32;
                for i in 0..n_embd {
                    s += h[i] * table[i * self.n_vocab + o];
                }
                logits[o] = s;
            }
        }
        Ok(())
    }
}

struct MoeScratch {
    router_logits: Vec<f32>,
    expert: ExpertScratch,
}

/// Reused buffers for one decode session (no per-token / per-layer heap alloc).
struct DecodeScratch {
    hidden: Vec<f32>,
    residual: Vec<f32>,
    layer_out: Vec<f32>,
    ffn_out: Vec<f32>,
    ffn_g: Vec<f32>,
    ffn_u: Vec<f32>,
    logits: Vec<f32>,
    norm_tmp: Vec<f32>,
    probs: Vec<f32>,
    gemv_act: GemvActScratch,
    past_k: Vec<f32>,
    past_v: Vec<f32>,
    kv_k16: Vec<F16>,
    kv_v16: Vec<F16>,
    attn: AttnBufs,
    moe: Option<MoeScratch>,
    n_vocab: usize,
}

impl DecodeScratch {
    fn new(
        hidden: usize,
        n_heads: usize,
        n_kv: usize,
        head_dim: usize,
        inter: usize,
        n_vocab: usize,
        moe_dims: Option<(usize, usize)>,
        max_attn_seq: usize,
    ) -> Self {
        let moe = moe_dims.map(|(n_experts, moe_inter)| MoeScratch {
            router_logits: vec![0.0; n_experts.max(1)],
            expert: ExpertScratch::new(hidden, moe_inter.max(1)),
        });
        DecodeScratch {
            hidden: vec![0.0; hidden],
            residual: vec![0.0; hidden],
            layer_out: vec![0.0; hidden],
            ffn_out: vec![0.0; hidden],
            ffn_g: vec![0.0; inter],
            ffn_u: vec![0.0; inter],
            // Lazy: greedy argmax uses emb_lm without full vocab buffers (~1 MiB each).
            logits: Vec::new(),
            norm_tmp: vec![0.0; hidden],
            probs: Vec::new(),
            gemv_act: GemvActScratch::new(hidden),
            past_k: Vec::new(),
            past_v: Vec::new(),
            kv_k16: Vec::with_capacity(n_kv * head_dim),
            kv_v16: Vec::with_capacity(n_kv * head_dim),
            attn: AttnBufs::new(hidden, n_heads, n_kv, head_dim, max_attn_seq),
            moe,
            n_vocab,
        }
    }

    fn ensure_full_logits(&mut self) {
        if self.logits.len() != self.n_vocab {
            self.logits.resize(self.n_vocab, 0.0);
        }
        if self.probs.len() != self.n_vocab {
            self.probs.resize(self.n_vocab, 0.0);
        }
    }
}

/// M5.4: set attn score starter to used/window; do **not** allocate full `-c` upfront.
fn scratch_prepare_attn_window(scratch: &mut DecodeScratch, window: usize, head_dim: usize) {
    let starter = window
        .min(scratch.attn.max_attn_seq.max(1))
        .min(crate::kv::KV_INITIAL_RESERVE_MIN.max(8))
        .max(1);
    scratch.attn.ensure_parallel(starter, head_dim);
}

struct AttnBufs {
    n_heads: usize,
    n_kv: usize,
    /// Ceiling for score buffers (= hot window). Never size to unused full `-c`.
    max_attn_seq: usize,
    xn: Vec<f32>,
    q: Vec<f32>,
    k: Vec<f32>,
    v: Vec<f32>,
    attn: Vec<f32>,
    proj: Vec<f32>,
    scores: Vec<f32>,
    new_k: Vec<f32>,
    new_v: Vec<f32>,
    mha_per_kv: Vec<MhaScratch>,
    head_scores: Vec<f32>,
    score_len: usize,
}

impl AttnBufs {
    fn new(hidden: usize, n_heads: usize, n_kv: usize, head_dim: usize, max_attn_seq: usize) -> Self {
        AttnBufs {
            n_heads,
            n_kv,
            max_attn_seq: max_attn_seq.max(1),
            xn: vec![0.0; hidden],
            q: vec![0.0; n_heads * head_dim],
            k: vec![0.0; n_kv * head_dim],
            v: vec![0.0; n_kv * head_dim],
            attn: vec![0.0; n_heads * head_dim],
            proj: vec![0.0; hidden],
            // Starter size — grow with used seq via ensure_parallel (not full -c).
            scores: vec![0.0; 8],
            new_k: vec![0.0; n_kv * head_dim],
            new_v: vec![0.0; n_kv * head_dim],
            mha_per_kv: vec![MhaScratch::default(); n_kv],
            head_scores: vec![0.0; n_heads * 8],
            score_len: 8,
        }
    }

    fn score_cap(&self) -> usize {
        self.score_len
    }

    fn ensure_parallel(&mut self, seq: usize, head_dim: usize) {
        let seq = seq.min(self.max_attn_seq.max(1));
        if self.score_len < seq {
            self.score_len = seq;
            self.scores.resize(seq, 0.0);
            self.head_scores.resize(self.n_heads * seq, 0.0);
        }
        for m in &mut self.mha_per_kv {
            m.ensure(seq, head_dim);
        }
    }

    fn ensure_seq(&mut self, seq: usize) {
        let seq = seq.min(self.max_attn_seq.max(1));
        if self.scores.len() < seq {
            self.scores.resize(seq, 0.0);
        }
    }
}

fn kv_usize(g: Option<&crate::gguf_io::GgufFile>, key: &str) -> Option<usize> {
    g?.get(key)?.as_u64().map(|v| v as usize)
}

fn kv_f32(g: Option<&crate::gguf_io::GgufFile>, key: &str) -> Option<f32> {
    g?.get(key)?.as_f64().map(|v| v as f32)
}

fn output_table(
    t: &LeanTensor,
    n_embd: usize,
    n_vocab: usize,
) -> Result<(usize, Vec<f32>, bool), ForwardError> {
    let shape = t.shape();
    if shape.len() != 2 {
        return Err(ForwardError::Dim(format!("output shape {shape:?}")));
    }
    let d0 = shape[0];
    let d1 = shape[1];
    let data = t
        .to_f32_owned()
        .map_err(|e| ForwardError::Message(e.to_string()))?;
    if d0 == n_vocab && d1 == n_embd {
        Ok((n_vocab, data, true))
    } else if d0 == n_embd && d1 == n_vocab {
        Ok((n_vocab, data, false))
    } else if d1 == n_embd {
        Ok((d0, data, true))
    } else {
        Err(ForwardError::Dim(format!(
            "output {shape:?} vs vocab={n_vocab} embd={n_embd}"
        )))
    }
}

fn load_layer(
    w: &LeanGgufWeights,
    layer: usize,
    n_embd: usize,
    moe: Option<&MoeLoadHints>,
) -> Result<LayerWeights, ForwardError> {
    let p = format!("blk.{layer}.");
    let attn_norm = w
        .get(&format!("{p}attn_norm.weight"))
        .map(|t| {
            t.to_f32_owned()
                .unwrap_or_else(|_| vec![1.0f32; n_embd])
        })
        .unwrap_or_else(|| vec![1.0f32; n_embd]);
    let ffn_norm = w
        .get(&format!("{p}ffn_norm.weight"))
        .map(|t| {
            t.to_f32_owned()
                .unwrap_or_else(|_| vec![1.0f32; n_embd])
        })
        .unwrap_or_else(|| vec![1.0f32; n_embd]);

    let wq = orient_in(require_mat(w, &format!("{p}attn_q.weight"))?, n_embd);
    let wk = orient_in(require_mat(w, &format!("{p}attn_k.weight"))?, n_embd);
    let wv = orient_in(require_mat(w, &format!("{p}attn_v.weight"))?, n_embd);
    let wo = require_mat(w, &format!("{p}attn_output.weight"))?;
    let wo = if wo.out_dim == n_embd {
        wo
    } else {
        orient_out(wo, n_embd)
    };

    let dense_gate = format!("{p}ffn_gate.weight");
    let ffn = if w.get(&dense_gate).is_some() {
        let gate = crate::quant_mat::maybe_repack_q4_0(
            orient_in(require_mat(w, &dense_gate)?, n_embd),
            &dense_gate,
        );
        let up = crate::quant_mat::maybe_repack_q4_0(
            orient_in(require_mat(w, &format!("{p}ffn_up.weight"))?, n_embd),
            &format!("{p}ffn_up.weight"),
        );
        let down = crate::quant_mat::maybe_repack_q4_0(
            orient_out(require_mat(w, &format!("{p}ffn_down.weight"))?, n_embd),
            &format!("{p}ffn_down.weight"),
        );
        if gate.in_dim != n_embd || up.in_dim != n_embd || down.out_dim != n_embd {
            return Err(ForwardError::Dim(format!(
                "layer {layer} FFN dims gate={}x{} up={}x{} down={}x{} embd={n_embd}",
                gate.in_dim, gate.out_dim, up.in_dim, up.out_dim, down.in_dim, down.out_dim
            )));
        }
        let inter = gate.out_dim;
        if up.out_dim != inter || down.in_dim != inter {
            return Err(ForwardError::Dim(format!(
                "layer {layer} FFN inter mismatch gate_out={} up_out={} down_in={}",
                gate.out_dim, up.out_dim, down.in_dim
            )));
        }
        LayerFfn::Dense {
            gate,
            up,
            down,
            inter,
        }
    } else if let Some(hints) = moe {
        let router_name = format!("{p}ffn_gate_inp.weight");
        let router = orient_in(require_mat(w, &router_name)?, n_embd);
        if router.in_dim != n_embd {
            return Err(ForwardError::Dim(format!(
                "layer {layer} router in_dim={} != embd={n_embd}",
                router.in_dim
            )));
        }
        if router.out_dim != hints.n_experts {
            return Err(ForwardError::Dim(format!(
                "layer {layer} router out_dim={} != n_experts={}",
                router.out_dim, hints.n_experts
            )));
        }
        LayerFfn::Moe {
            router,
            top_k: hints.top_k.min(hints.n_experts).max(1),
            inter: hints.inter,
            n_experts: hints.n_experts,
        }
    } else {
        return Err(ForwardError::Missing("ffn_gate.weight (dense) or ffn_gate_inp.weight (MoE)"));
    };

    Ok(LayerWeights {
        attn_norm,
        ffn_norm,
        wq,
        wk,
        wv,
        wo,
        ffn,
    })
}

fn require_mat(w: &LeanGgufWeights, name: &str) -> Result<QuantMat, ForwardError> {
    let t = w.require(name)?;
    t.to_quant_mat().map_err(ForwardError::from)
}

/// Pack shards may label GGUF dims as ggml `[ne0=in, ne1=out]` or `[ne0=out, ne1=in]`.
/// Physical payload is row-major `(out, in)` once labels match; swap labels only.
fn orient_in(mut m: QuantMat, expect_in: usize) -> QuantMat {
    if m.in_dim != expect_in && m.out_dim == expect_in {
        std::mem::swap(&mut m.in_dim, &mut m.out_dim);
    }
    m
}

fn orient_out(mut m: QuantMat, expect_out: usize) -> QuantMat {
    if m.out_dim != expect_out && m.in_dim == expect_out {
        std::mem::swap(&mut m.in_dim, &mut m.out_dim);
    }
    m
}
