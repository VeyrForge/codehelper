//! End-to-end runtime benchmark: actually executes a (synthetic) MoE on the CPU backend,
//! measuring decode throughput and — the headline — memory footprint. Shows the two
//! independent wins multiplying: scheduling (fewer experts resident) × Green Compress-style Q8
//! storage (smaller per expert). Lossless: identical math to dense, just less memory.

use std::time::Instant;

use engine_core::{CpuBackend, Eviction, MoeRuntime, WeightStore};

// xorshift64* for deterministic routing/input
fn rng(s: &mut u64) -> f32 {
    *s ^= *s >> 12;
    *s ^= *s << 25;
    *s ^= *s >> 27;
    (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32
}

fn route(experts: usize, k: usize, s: &mut u64) -> (Vec<u16>, Vec<f32>) {
    let mut c = Vec::with_capacity(k);
    while c.len() < k {
        let e = (rng(s) * experts as f32) as u16 % experts as u16;
        if !c.contains(&e) {
            c.push(e);
        }
    }
    (c, vec![1.0 / k as f32; k])
}

fn run(store: &WeightStore, capacity: usize, tokens: usize) -> (f64, engine_core::RuntimeMetrics, u64, u64) {
    let backend = CpuBackend;
    let mut rt = MoeRuntime::new(store, &backend, capacity, Eviction::Lru);
    let mut s = 12345u64;
    let x: Vec<f32> = (0..store.hidden).map(|_| rng(&mut s) - 0.5).collect();
    let mut out = vec![0.0f32; store.hidden];
    let t0 = Instant::now();
    for _ in 0..tokens {
        for l in 0..store.layers {
            let (experts, gates) = route(store.experts, 8, &mut s);
            rt.forward_layer(l, &x, &experts, &gates, &mut out);
        }
        rt.metrics.tokens += 1;
    }
    let tps = tokens as f64 / t0.elapsed().as_secs_f64();
    (tps, rt.metrics, rt.resident_footprint_bytes(), rt.full_footprint_bytes())
}

fn main() {
    // modest synthetic MoE (kept light); dims need not match OLMoE to exercise the engine.
    let (layers, experts, hidden, inter, k, tokens) = (8usize, 64usize, 256usize, 512usize, 8usize, 128usize);
    let cap = 16; // resident experts/layer (¼)

    println!("\nGreen Engine runtime — synthetic MoE on CPU backend");
    println!(
        "  {layers} layers, {experts} experts, hidden {hidden}, inter {inter}, top-{k}, {tokens} tokens, cache {cap}/{experts}\n"
    );

    for (label, quant) in [("f32 weights", false), ("Green Compress Q8 weights", true)] {
        let store = WeightStore::synthetic(layers, experts, hidden, inter, quant, 42);
        let (tps, m, resident, full) = run(&store, cap, tokens);
        println!("{label}:");
        println!(
            "  throughput {:>6.1} tok/s   expert FFNs {}   cold misses {}",
            tps, m.expert_calls, m.cold_misses
        );
        println!(
            "  memory: full store {:>6.1} MB  ->  scheduled-resident {:>5.1} MB  ({:.1}× smaller)",
            full as f64 / 1e6,
            resident as f64 / 1e6,
            full as f64 / resident as f64
        );
        println!("  bytes moved / token {:.2} MB\n", m.bytes_per_token() / 1e6);
    }

    let f32_full = WeightStore::synthetic(layers, experts, hidden, inter, false, 42).total_bytes() as f64;
    let q8_resident = {
        let s = WeightStore::synthetic(layers, experts, hidden, inter, true, 42);
        let avg = s.total_bytes() as f64 / (layers * experts) as f64;
        avg * cap as f64 * layers as f64
    };
    println!(
        "combined win (scheduling × Q8): full-f32 {:.1} MB  ->  resident-Q8 {:.1} MB  = {:.1}× less memory, lossless",
        f32_full / 1e6,
        q8_resident / 1e6,
        f32_full / q8_resident
    );
    println!("GPU backend: build with `--features gpu` (links crates/kernels CUDA/HIP via the ExpertBackend FFI).");
}
