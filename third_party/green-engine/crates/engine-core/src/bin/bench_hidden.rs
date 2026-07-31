//! Hidden-state predictor benchmark: compares plain LRU, transition-keyed JIT prefetch, and
//! hidden-state-keyed JIT prefetch — by hit-rate and by modeled decode tokens/sec.
//!
//! Tokens/sec uses a HYBRID per-tier miss model (the fix the earlier benchmark demanded):
//! a miss costs min(transfer_from_RAM + GPU compute, CPU compute), so an offloaded expert is
//! either streamed or recomputed on CPU — whichever is cheaper. Constants are measured
//! calibrated to the real llama.cpp run (43.6 tok/s).

use engine_core::{hidden, Config, Engine, HiddenStates, Trace, WeightManifest, OLMOE_EXPERT_BYTES_FP16};

// measured constants + llama.cpp calibration
const CPU_MS: f64 = 0.1094; // one expert FFN on CPU
const GPU_MS: f64 = CPU_MS / 15.0; // assumed GPU speedup
const XFER_RAM_MS: f64 = 0.136; // Q4 expert over PCIe 4.0
const REST_GPU_MS: f64 = 8.94 / 15.0; // non-expert work/token (attn etc.), GPU tier
const N_ACTIVE: f64 = 8.0 * 16.0; // top_k * layers

fn hybrid_miss_ms() -> f64 {
    (XFER_RAM_MS + GPU_MS).min(CPU_MS) // stream-or-recompute, whichever is cheaper
}

fn tok_s_from_hit(hit: f64) -> f64 {
    let per = hit * GPU_MS + (1.0 - hit) * hybrid_miss_ms();
    1000.0 / (N_ACTIVE * per + REST_GPU_MS)
}

fn tok_s_llamacpp(frac_gpu: f64) -> f64 {
    // static split: resident on GPU w.p. frac_gpu, else computed on CPU (no transfer)
    let per = frac_gpu * GPU_MS + (1.0 - frac_gpu) * CPU_MS;
    1000.0 / (N_ACTIVE * per + REST_GPU_MS)
}

fn main() {
    let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/");
    let trace = Trace::load(format!("{dir}expert_trace.bin")).expect("export_trace.py first");
    let hs = HiddenStates::load(format!("{dir}hidden_trace.bin")).expect("export_hidden.py first");
    let man = WeightManifest::uniform(trace.layers, trace.experts, OLMOE_EXPERT_BYTES_FP16, 16);
    let k = trace.top_k;
    let e = trace.experts;

    let hid_tab = hs.predict_table(&trace, k);
    let trans_tab = hidden::transition_table(&trace, k);

    println!("\nGreen Engine — hidden-state predictor benchmark ({} tokens)\n", trace.tokens);
    println!(
        "predictor recall:  hidden {:.0}%   transition {:.0}%   (of the {} active experts)\n",
        hidden::table_recall(&trace, &hid_tab, k) * 100.0,
        hidden::table_recall(&trace, &trans_tab, k) * 100.0,
        k
    );

    println!("hit rate @ equal fast budget (main = budget-8, prefetch buffer = 8):");
    println!(
        "{:>8} | {:>7} | {:>10} | {:>9}",
        "resident", "LRU", "trans-JIT", "hid-JIT"
    );
    println!("{}", "-".repeat(44));
    let budgets = [12usize, 16, 20, 24, 32, 48];
    let mut hidrows = Vec::new();
    for &b in &budgets {
        let lru = Engine::new(Config::lru(b), &trace).run(&trace, OLMOE_EXPERT_BYTES_FP16).hit_rate();
        let tj = hidden::simulate_jit(&trace, &man, &trans_tab, k, b - 8, 8, true).hit_rate();
        let hj = hidden::simulate_jit(&trace, &man, &hid_tab, k, b - 8, 8, true).hit_rate();
        println!(
            "{:>7}% | {:>6.1}% | {:>9.1}% | {:>8.1}%",
            100 * b / e,
            lru * 100.0,
            tj * 100.0,
            hj * 100.0
        );
        hidrows.push((b, lru, hj));
    }

    println!("\nmodeled decode tokens/sec (hybrid RAM tier, lossless; llama.cpp anchor 43.6 tok/s CPU):");
    println!(
        "{:>8} | {:>11} | {:>10} | {:>10} | {:>9}",
        "resident", "llama.cpp", "green-LRU", "green-hid", "speedup"
    );
    println!("{}", "-".repeat(60));
    for (b, lru, hj) in hidrows {
        let lc = tok_s_llamacpp(b as f64 / e as f64);
        let g_lru = tok_s_from_hit(lru);
        let g_hid = tok_s_from_hit(hj);
        println!(
            "{:>7}% | {:>8.1} t/s | {:>8.1} | {:>8.1} | {:>7.2}x",
            100 * b / e,
            lc,
            g_lru,
            g_hid,
            g_hid / lc
        );
    }
    println!("\nfull-GPU ceiling: {:.0} tok/s   (all experts resident)", tok_s_from_hit(1.0));
    println!("quality: identical across all rows (real routing replayed; no expert dropped).");
}
