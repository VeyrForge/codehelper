//! Green Engine core — scheduling, MoE execution, and native `.green` dense generate.
//!
//! **Token paths**
//! - `.green` packages: [`generate`] / [`GreenModel::generate`] (`ge run`, `ge chat serve --model *.green`)
//! - ordinary GGUF: `ge` still routes through llama.cpp
//!
//! Experts are cacheable, schedulable units (hot in fast memory, cold on demand). The engine
//! decides residency per token/layer, then runs experts on a pluggable backend (CPU here).
//!
//! Layers:
//!   * scheduling — `cache`, `predictor`, `hidden`, `engine`
//!   * execution  — `tensor`, `weights`, `backend`, `runtime`, `paged`
//!   * forward    — `tokenize` → `embed` → `forward` ([`DenseForward`]) → [`generate`] (package API)
//!   * integration— `green_model`, `manifest`, optional `green`
//!
//! See `docs/BENCHMARKS.md`.

// scheduling
pub mod batching;
pub mod cache;
pub mod energy;
pub mod engine;
pub mod hidden;
pub mod kv;
pub mod predictor;
pub mod prefix;
pub mod serving;
pub mod trace;
// execution
pub mod attention;
pub mod backend;
pub mod block;
pub mod green_experts;
pub mod green_tiers;
pub mod hetero;
pub mod norm;
pub mod output;
pub mod paged;
pub mod runtime;
pub mod tensor;
pub mod weights;
// forward slice — tokenize → embed → RoPE → generate (gguf_io is crate-private)
pub mod gguf_io;
pub mod chat;
pub mod embed;
pub mod decode_profile;
pub mod forward;
pub mod generate;
pub mod ngram_spec;
pub mod speculative;
pub mod eagle3_sidecar;
#[cfg(feature = "gpu")]
pub mod gpu_gemv;
pub mod gpu_layer;
pub mod host_release;
pub mod resident;
pub mod gguf_load;
/// IQ2_XXS CPU dequant + LUT (step 3) + `gemv_via_blocks_legacy` GEMV (step 4) +
/// clear [`gguf_load::dequant_slice`] arm (`GE_IQ2=0` disables). No encode / CUDA.
pub mod iq2_xxs;
/// W33 / R32.3 — `GE_WEIGHT_FMT` NVFP4/MXFP4 scaffold (pack ready; GEMV fail-closed).
pub mod weight_fmt;
/// R19 step 2 — TRT-XQA JIT GQA decode attn CPU stub (32q/8kv, paged KV 16/32,
/// online softmax). Spike only: no CUDA, no large GEMM, no DLL. See module docs
/// for the R10 gate table and GPU port roadmap.
pub mod attn_xqa_stub;
pub mod quant_kernels;
pub mod quant_mat;
pub mod quant_repack;
pub mod rope;
pub mod sample;
pub mod tokenize;
// integration
pub mod green_model;
pub mod manifest;
#[cfg(feature = "green")]
pub mod green;
#[cfg(feature = "green")]
pub use green::{
    apply_attn_weight, apply_attn_weight_batch, GreenBase, GreenExpert, GreenMetrics,
    GreenPagedStore, GreenTensor, GreenWeightStore,
};
// portability
pub mod game_mode;
pub mod sys;

pub use attention::{
    append_kv, append_kv_reuse, attend_via_store, attend_via_store_f32, attn_scale, f16_bits_to_f32,
    f16_slice_to_f32, f32_slice_to_f16, f32_to_f16_bits, multi_head_attend_decode,
    multi_head_attend_decode_parallel, multi_head_attend_decode_reuse, project_qkv, recall_kv_f32,
    recall_kv_f32_into, softmax_inplace, MhaScratch,
};
pub use backend::{CpuBackend, ExpertBackend, Scratch};
pub use block::{
    attn_block_decode, attn_block_decode_kv, attn_block_prefill_one, AttnBlockWeights, AttnScratch,
};
pub use cache::Eviction;
pub use engine::{Config, Engine, Metrics, Prefetch};
pub use green_tiers::{allocate_mixed, GreenTier};
pub use hidden::{HiddenStates, JitStats};
pub use green_experts::{
    apply_expert_ffn, apply_moe_ffn, ffn_moe_step, ffn_step_auto, route_topk, ExpertResidencyMetrics,
    FfnMode, MoeError, PackageExpertStore, RouteSelection,
};
pub use chat::{
    family_for_package, looks_instruct, looks_templated, prepare_prompt, render_llama3_instruct,
    render_messages, ChatApplyMode, ChatMessage, ChatTemplateFamily,
};
pub use embed::{EmbedError, EmbeddingTable};
pub use forward::{DecodeSession, DenseForward, ForwardError, GreedyGenerateOut};
pub use ngram_spec::{NgramSpecDrafter, NgramSpecStats};
pub use resident::{open_model_cached, ResidentModel};
pub use speculative::{verify_and_rewind, verify_draft_greedy, VerifyResult};
pub use generate::{
    ensure_ready as ensure_generate_ready, generate, generate_from_path, model_can_generate,
    GenerateError, GenerateRequest, GenerateResult,
};
pub use sample::{SampleParams, XorShift64};
pub use gguf_load::{load_gguf_f32, load_package_weights, DenseGgufWeights, GgufLoadError, LoadedTensor};
pub use green_model::{
    DenseWeightStore, ExpertHandle, ExpertProvider as GreenExpertProvider, GreenExpertStore,
    GreenModel, GreenModelError, LoadConfig, LoadState, StubExpertProvider, TensorHandle,
    TensorKind, Tokenizer,
};
pub use manifest::WeightManifest;
pub use rope::{
    apply_rope_inplace, apply_rope_inplace_cached, RopeCache, RopeConfig, RopeError, RopeMode,
    RopeScaling,
};
pub use tokenize::{load_for_package, TokenizeError, Tokenizer as GgufTokenizer};
pub use norm::{layer_norm, residual_add, residual_add_inplace, rms_norm, RMS_EPS_DEFAULT};
pub use output::{linear, lm_head, out_proj, WeightView};
// Two distinct ExpertProvider traits (do not unify):
//   `ExpertProvider`        = paged:: — MoE runtime `with_expert` (WeightStore / PackageExpertStore)
//   `GreenExpertProvider`   = green_model:: — package prefetch/acquire/evict seam
pub use paged::{
    dense_provider, ExpertProvider, PagedFormat, PagedMetrics, PagedWeightStore, Prefetcher,
    PredictivePrefetcher, SlotBudget, SlotPool,
};
pub use kv::{
    AttentionResult, F16, KvKeyQuant, KvPolicy, KvRecall, KvStore, PagedKvMetrics, PagedRamKvStore,
    RamKvStore, DEFAULT_ATTENTION_SINKS, DEFAULT_CHAT_ATTN_WINDOW, KV_KEY_QUANT_Q8_MIN_CTX,
    KV_KEY_QUANT_Q8V4_MIN_CTX, attn_window_from_env, resolve_kv_hot_cap,
};
pub use runtime::{dense_reference, MoeRuntime, RuntimeMetrics};
pub use trace::Trace;
pub use weights::{ExpertWeights, Tensor, WeightStore};

/// True when this binary was compiled with `--features gpu`.
#[inline]
pub fn native_gpu_feature_enabled() -> bool {
    cfg!(feature = "gpu")
}

/// True when CUDA decode can run (feature on, kernels linked, device present).
#[inline]
pub fn native_cuda_decode_ready() -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    {
        crate::gpu_gemv::gpu_available()
    }
    #[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
    {
        false
    }
}

/// OLMoE expert = 3 · hidden(2048) · moe_inter(1024) · 2 bytes (fp16). Default uniform size.
pub const OLMOE_EXPERT_BYTES_FP16: u64 = 3 * 2048 * 1024 * 2;

#[cfg(test)]
mod tests {
    use super::*;

    fn load() -> Trace {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/expert_trace.bin");
        Trace::load(path).expect("missing testdata/expert_trace.bin")
    }

    #[test]
    fn trace_shape_matches_capture() {
        let t = load();
        assert_eq!(t.tokens, 447);
        assert_eq!(t.layers, 16);
        assert_eq!(t.top_k, 8);
        assert_eq!(t.experts, 64);
    }

    /// LRU is deterministic, so the Rust port must reproduce the Python hit-rates
    /// (green_engine_sim.py budget sweep) to within rounding. This proves the trace
    /// loading, request ordering, and cache mechanics are faithful.
    #[test]
    fn lru_parity_with_python() {
        let t = load();
        let cases = [(10, 0.402), (16, 0.571), (24, 0.708), (32, 0.815), (48, 0.939)];
        for (cap, expected) in cases {
            let hr = Engine::new(Config::lru(cap), &t).run(&t, OLMOE_EXPERT_BYTES_FP16).hit_rate();
            assert!(
                (hr - expected).abs() < 0.01,
                "LRU C={cap}: rust {hr:.4} vs python {expected:.3}"
            );
        }
    }

    /// The full engine (reuse eviction + prefetch + salvage) must beat plain LRU at the
    /// same total fast-memory budget under memory pressure — the headline result.
    #[test]
    fn engine_beats_lru_under_memory_pressure() {
        let t = load();
        let budget = 12; // tight
        let lru = Engine::new(Config::lru(budget), &t).run(&t, OLMOE_EXPERT_BYTES_FP16).hit_rate();
        let eng = Engine::new(Config::full(budget - 4, 4), &t).run(&t, OLMOE_EXPERT_BYTES_FP16).hit_rate();
        assert!(eng > lru, "engine {eng:.4} should beat LRU {lru:.4} at budget {budget}");
    }

    /// The hidden-state predictor (Q4) must recall the active set far better than the
    /// transition baseline — and a JIT-prefetch cache keyed on it must beat plain LRU.
    #[test]
    fn hidden_predictor_beats_transition() {
        let t = load();
        let hpath = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/hidden_trace.bin");
        // hidden_trace.bin is optional (~56 MB); skip if absent.
        let hs = match hidden::HiddenStates::load(hpath) {
            Ok(h) => h,
            Err(_) => {
                eprintln!("skip hidden_predictor test: testdata/hidden_trace.bin absent");
                return;
            }
        };
        let k = t.top_k;
        let hid_tab = hs.predict_table(&t, k);
        let trans_tab = hidden::transition_table(&t, k);
        let hid_recall = hidden::table_recall(&t, &hid_tab, k);
        let trans_recall = hidden::table_recall(&t, &trans_tab, k);
        assert!(hid_recall > 0.55, "hidden recall {hid_recall:.3} should be ~0.62");
        assert!(hid_recall > trans_recall + 0.10, "hidden {hid_recall:.3} ≫ trans {trans_recall:.3}");

        // JIT prefetch keyed on the hidden predictor, vs plain LRU at equal fast budget
        let man = WeightManifest::uniform(t.layers, t.experts, OLMOE_EXPERT_BYTES_FP16, 16);
        let lru = Engine::new(Config::lru(16), &t).run(&t, OLMOE_EXPERT_BYTES_FP16).hit_rate();
        let jit = hidden::simulate_jit(&t, &man, &hid_tab, k, 8, 8, true).hit_rate();
        assert!(jit > lru, "hidden-JIT {jit:.3} should beat LRU(16) {lru:.3}");
    }

    /// KV eviction (SOTA policies) must reproduce the Python analysis: ~2× KV compression at
    /// ~85-90% attention retained, Quest ≥ SnapKV ≥ random, and a large per-layer spread that
    /// justifies adaptive per-layer budgets.
    #[test]
    fn kv_eviction_matches_python() {
        let p = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/attention_trace.bin");
        let att = kv::Attention::load(p).expect("missing testdata/attention_trace.bin");
        use kv::KvPolicy::*;
        let snap = att.evaluate(SnapKv, 0.5);
        let quest = att.evaluate(Quest, 0.5);
        assert!((snap - 0.89).abs() < 0.05, "SnapKV@50% {snap:.3} (~0.89)");
        assert!(quest >= snap, "Quest {quest:.3} should bound SnapKV {snap:.3}");
        let pl = att.per_layer(Quest, 0.125);
        let spread = pl.iter().cloned().fold(0.0_f64, f64::max)
            - pl.iter().cloned().fold(1.0_f64, f64::min);
        assert!(spread > 0.3, "per-layer spread {spread:.2} should be large (adaptive budget wins)");
    }

    /// Adaptive per-layer KV budget must retain at least as much as a uniform split at the same
    /// total budget — the engine's differentiator (it sees every layer; PyramidKV is static).
    #[test]
    fn kv_adaptive_beats_uniform() {
        let p = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/attention_trace.bin");
        let att = kv::Attention::load(p).expect("missing testdata/attention_trace.bin");
        let q = att.tokens - 1;
        let total = (att.tokens * att.layers) / 4; // 25% overall budget
        let uni = att.total_retained(kv::KvPolicy::Quest, q, &att.allocate_uniform(q, total));
        let ada = att.total_retained(kv::KvPolicy::Quest, q, &att.allocate_adaptive(kv::KvPolicy::Quest, q, total));
        assert!(ada >= uni, "adaptive {ada:.3} should ≥ uniform {uni:.3}");
    }

    /// The engine must schedule a DENSE model's hot-neuron trace with the same code path
    /// (a neuron = a fine-grained expert) — extending the engine beyond MoE.
    #[test]
    fn engine_schedules_dense_neurons() {
        let p = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/dense_trace.bin");
        let t = Trace::load(p).expect("missing testdata/dense_trace.bin");
        assert!(t.experts > 1000, "dense neuron count");
        // caching 25% of neurons must beat the no-cache baseline (= top_k/experts)
        let cap = (t.experts / 4).max(t.top_k);
        let hr = Engine::new(Config::lru(cap), &t).run(&t, OLMOE_EXPERT_BYTES_FP16).hit_rate();
        assert!(hr > t.top_k as f64 / t.experts as f64, "caching should beat no-reuse baseline");
    }

    /// Green Compress integration: compressing experts (smaller per-expert bytes via NF4/GPTQ) must
    /// reduce bytes/token at the SAME schedule — the two systems stack multiplicatively.
    #[test]
    fn green_s1_manifest_stacks_with_scheduling() {
        let t = load();
        let eng = Engine::new(Config::lru(24), &t);
        let fp16 = WeightManifest::uniform(t.layers, t.experts, OLMOE_EXPERT_BYTES_FP16, 16);
        // NF4 ≈ 0.28× the fp16 size (Green Compress README: ~4.5 bits/weight incl. overhead)
        let nf4 = WeightManifest::uniform(t.layers, t.experts, OLMOE_EXPERT_BYTES_FP16 * 28 / 100, 4);
        let b_fp16 = eng.run_with_manifest(&t, &fp16).bytes_per_token();
        let b_nf4 = eng.run_with_manifest(&t, &nf4).bytes_per_token();
        // same hit-rate (schedule unchanged), ~3.5× less traffic from compression alone
        assert!(b_nf4 < b_fp16 * 0.30, "nf4 {b_nf4:.0} should be «  fp16 {b_fp16:.0}");
    }
}
