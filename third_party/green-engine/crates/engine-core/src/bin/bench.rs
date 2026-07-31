//! Green Engine benchmark harness: replays the captured routing trace through the engine
//! at a range of VRAM budgets and prints hit-rate + bytes/token for plain LRU vs the full engine.

use std::time::Instant;

use engine_core::{Config, Engine, Trace, WeightManifest, OLMOE_EXPERT_BYTES_FP16};

const EXPERT_BYTES: f64 = OLMOE_EXPERT_BYTES_FP16 as f64;

fn main() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/expert_trace.bin");
    let trace = match Trace::load(path) {
        Ok(t) => t,
        Err(e) => {
            eprintln!("could not load {path}: {e}");
            std::process::exit(1);
        }
    };
    let routing_no_cache = trace.top_k as f64 * EXPERT_BYTES * trace.layers as f64;

    println!(
        "\nGreen Engine (Rust) — {} tokens x {} layers, {} experts, top-{}\n",
        trace.tokens, trace.layers, trace.experts, trace.top_k
    );

    // Python reference (green_engine_sim.py budget sweep) for the parity column.
    let py_lru = [(10, 40.2), (12, 47.1), (16, 57.1), (20, 64.4), (24, 70.8), (32, 81.5), (48, 93.9)];
    let py_eng = [(10, 49.5), (12, 53.3), (16, 60.0), (20, 65.8), (24, 71.1), (32, 79.9), (48, 91.2)];

    println!(
        "{:>7} | {:>16} | {:>22} | {:>7}",
        "budget", "LRU hit (py)", "full engine hit (py)", "delta"
    );
    println!("{}", "-".repeat(64));

    let start = Instant::now();
    for i in 0..py_lru.len() {
        let b = py_lru[i].0 as usize;
        let lru = Engine::new(Config::lru(b), &trace).run(&trace, OLMOE_EXPERT_BYTES_FP16).hit_rate() * 100.0;
        let eng = Engine::new(Config::full(b - 4, 4), &trace).run(&trace, OLMOE_EXPERT_BYTES_FP16).hit_rate() * 100.0;
        println!(
            "{:>6}% | {:>9.1}% ({:>4.1}) | {:>15.1}% ({:>4.1}) | {:>+6.1}",
            (100 * b) / trace.experts,
            lru,
            py_lru[i].1,
            eng,
            py_eng[i].1,
            eng - lru
        );
    }
    let elapsed = start.elapsed();

    // bytes/token at a representative budget (full engine, lossless), fp16 vs Green Compress NF4
    let cfg = Config::full(24, 8);
    let fp16 = WeightManifest::uniform(trace.layers, trace.experts, OLMOE_EXPERT_BYTES_FP16, 16);
    // Load a real Green Compress manifest if present; otherwise fall back to a uniform NF4 estimate.
    let man_path = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/expert_manifest.txt");
    let nf4 = match WeightManifest::load(man_path, trace.layers, trace.experts) {
        Ok(m) => {
            println!("(loaded Green Compress manifest: {man_path})");
            m
        }
        Err(_) => WeightManifest::uniform(trace.layers, trace.experts, OLMOE_EXPERT_BYTES_FP16 * 28 / 100, 4),
    };
    let m = Engine::new(cfg, &trace).run_with_manifest(&trace, &fp16);
    let m_nf4 = Engine::new(cfg, &trace).run_with_manifest(&trace, &nf4);
    println!(
        "\nfull engine @ C24+P8: hit {:.1}%  prefetch waste {:.0}%  (routing-no-cache {:.0} MB/token)",
        m.hit_rate() * 100.0,
        m.prefetch_waste() * 100.0,
        routing_no_cache / 1e6,
    );
    println!(
        "  bytes/token: fp16 experts {:.0} MB   +Green Compress NF4 experts {:.0} MB  (scheduling × compression)",
        m.bytes_per_token() / 1e6,
        m_nf4.bytes_per_token() / 1e6,
    );
    println!(
        "\nran {} engine configs over {} tokens in {:.2?}  ({:.1} M expert-decisions/s)",
        2 * py_lru.len(),
        trace.tokens,
        elapsed,
        (2 * py_lru.len() * trace.tokens * trace.layers * trace.top_k) as f64
            / elapsed.as_secs_f64()
            / 1e6
    );
}
