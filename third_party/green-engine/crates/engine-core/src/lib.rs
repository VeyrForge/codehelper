//! Green Engine core — a dynamic MoE expert scheduler.
//!
//! The engine treats experts as cacheable, schedulable units: hot in fast memory, cold
//! fetched/prefetched on demand. It decides, per token and layer, what should be resident,
//! then executes the experts on a pluggable compute backend (CPU here; GPU/C++ over FFI).
//!
//! Layers:
//!   * scheduling — `cache`, `predictor`, `hidden`, `engine` (validated, lossless policy)
//!   * execution  — `tensor`, `weights`, `backend`, `runtime` (actually computes MoE output)
//!   * integration— `manifest` (Green Compress per-expert compression seam)
//!
//! See `docs/green-engine-novel-idea.md` and `docs/benchmark-vs-llamacpp.md`.

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
pub mod backend;
pub mod green_tiers;
pub mod hetero;
pub mod paged;
pub mod runtime;
pub mod tensor;
pub mod weights;
// integration
pub mod manifest;
#[cfg(feature = "green")]
pub mod green;
// portability
pub mod sys;

pub use backend::{CpuBackend, ExpertBackend, Scratch};
pub use cache::Eviction;
pub use engine::{Config, Engine, Metrics, Prefetch};
pub use green_tiers::{allocate_mixed, GreenTier};
pub use hidden::{HiddenStates, JitStats};
pub use manifest::WeightManifest;
pub use paged::{dense_provider, ExpertProvider, PagedFormat, PagedMetrics, PagedWeightStore, Prefetcher, PredictivePrefetcher};
pub use predictor::LayerAheadPredictor;
pub use runtime::{dense_reference, MoeRuntime, RuntimeMetrics};
pub use trace::Trace;
pub use weights::{ExpertWeights, Tensor, WeightStore};

/// OLMoE expert = 3 · hidden(2048) · moe_inter(1024) · 2 bytes (fp16). Default uniform size.
pub const OLMOE_EXPERT_BYTES_FP16: u64 = 3 * 2048 * 1024 * 2;

#[cfg(test)]
mod tests {
    use super::*;

    fn load() -> Trace {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../results/expert_trace.bin");
        Trace::load(path).expect("run experiments/moe_trace/export_trace.py first")
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
        let hpath = concat!(env!("CARGO_MANIFEST_DIR"), "/../../results/hidden_trace.bin");
        // hidden_trace.bin is large (~56 MB) and not committed; skip if absent (regenerate via
        // experiments/moe_trace/{trace_hidden,export_hidden}.py).
        let hs = match hidden::HiddenStates::load(hpath) {
            Ok(h) => h,
            Err(_) => {
                eprintln!("skip hidden_predictor test: results/hidden_trace.bin absent");
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
        let p = concat!(env!("CARGO_MANIFEST_DIR"), "/../../results/attention_trace.bin");
        let att = kv::Attention::load(p).expect("run export_attention.py first");
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
        let p = concat!(env!("CARGO_MANIFEST_DIR"), "/../../results/attention_trace.bin");
        let att = kv::Attention::load(p).expect("run export_attention.py first");
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
        let p = concat!(env!("CARGO_MANIFEST_DIR"), "/../../results/dense_trace.bin");
        let t = Trace::load(p).expect("run experiments/moe_trace/trace_dense.py first");
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
