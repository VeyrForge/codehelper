//! Runs the SAME engine scheduler on a DENSE model's hot-neuron trace (a neuron = a fine-grained
//! expert), proving the engine extends beyond MoE to dense models — llama.cpp's main workload.
//! Loads `testdata/dense_trace.bin`.

use engine_core::{Config, Engine, Trace, OLMOE_EXPERT_BYTES_FP16};

fn main() {
    let path = std::env::var("GE_TRACE")
        .unwrap_or_else(|_| concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/dense_trace.bin").into());
    let trace = match Trace::load(&path) {
        Ok(t) => t,
        Err(e) => {
            eprintln!("could not load {path}: {e}");
            std::process::exit(1);
        }
    };
    println!(
        "\nDense neuron scheduling — {} tokens × {} layers, {} neurons/layer, hot=top-{} per token\n",
        trace.tokens, trace.layers, trace.experts, trace.top_k
    );
    println!("  resident neurons (cache) | hit rate (LRU) | neurons NOT loaded/token");
    println!("  {}", "-".repeat(58));
    for frac in [0.10, 0.15, 0.25, 0.40] {
        let cap = ((trace.experts as f64 * frac).round() as usize).max(trace.top_k);
        let m = Engine::new(Config::lru(cap), &trace).run(&trace, OLMOE_EXPERT_BYTES_FP16);
        let avoided = trace.top_k as f64 * (1.0 - m.hit_rate());
        println!(
            "  {:>5.0}% ({:>4}/{:<5}) | {:>11.1}% | only {:>5.1} of {} fetched cold",
            frac * 100.0,
            cap,
            trace.experts,
            m.hit_rate() * 100.0,
            avoided,
            trace.top_k
        );
    }
    println!("\n  Same cache/scheduler as MoE experts — the engine is model-class-agnostic.");
    println!("  Hot neurons stay resident; cold ones are skipped/paged. Lossless if top-k covers the");
    println!("  activation mass (see dense_stats.json for the magnitude-concentration numbers).");
}
