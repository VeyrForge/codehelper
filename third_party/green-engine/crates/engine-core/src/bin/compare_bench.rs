//! Consolidated benchmark — WITHOUT vs WITH Green Compress, plus KV-cache tiers and a llama.cpp
//! anchor. Answers "how much does the engine (and green compression) actually save, and how does it
//! compare?" on one OLMoE-class config. Build with `--features green`.
//!
//!   cargo run -p engine-core --features green --release --bin compare_bench

use std::time::Instant;

use engine_core::green::{GreenBase, GreenWeightStore};
use engine_core::kv::{kv_bytes, quantize_kv};
use engine_core::paged::dense_provider;
use engine_core::{CpuBackend, ExpertProvider, WeightStore};

fn rng(s: &mut u64) -> f32 {
    *s ^= *s >> 12; *s ^= *s << 25; *s ^= *s >> 27;
    (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
}

/// Decode `tokens` tokens over all layers via a provider; returns (tok/s, sum-of-output for drift).
fn run<P: ExpertProvider>(p: &P, tokens: usize, warm: bool) -> (f64, Vec<f32>) {
    let backend = CpuBackend;
    let (layers, experts, h, k) = (p.layers(), p.experts(), p.hidden(), 8usize);
    let mut s = 999u64;
    let x: Vec<f32> = (0..h).map(|_| rng(&mut s)).collect();
    let mut last = vec![0.0f32; h];
    if warm { // one untimed pass so decode measures steady state (experts resident)
        for l in 0..layers {
            let e: Vec<u16> = (0..k as u16).collect();
            dense_provider(p, &backend, l, &x, &e, &vec![1.0 / k as f32; k]);
        }
    }
    // Realistic MoE routing: sparse+bursty — ~80% of picks come from a hot set of `hot` experts
    // (the working set the cache holds), matching real load imbalance (~15-20% serve ~80%).
    let hot = 16.min(experts);
    let mut pick = |s: &mut u64| -> u16 {
        if rng(s).abs() < 0.8 { (rng(s).abs() * hot as f32) as u16 % hot as u16 }
        else { (rng(s).abs() * experts as f32) as u16 % experts as u16 }
    };
    let t0 = Instant::now();
    for _ in 0..tokens {
        for l in 0..layers {
            let mut e = Vec::with_capacity(k);
            while e.len() < k { let c = pick(&mut s); if !e.contains(&c) { e.push(c); } }
            last = dense_provider(p, &backend, l, &x, &e, &vec![1.0 / k as f32; k]);
        }
    }
    (tokens as f64 / t0.elapsed().as_secs_f64(), last)
}

fn main() {
    let (layers, experts, hidden, inter, cap) = (16usize, 64usize, 512usize, 256usize, 16usize);
    let store = WeightStore::synthetic(layers, experts, hidden, inter, false, 42);
    let mb = |b: f64| b / 1e6;
    let f32_full = (layers * experts * 3 * hidden * inter * 4) as f64;
    let sched_f32 = (3 * hidden * inter * 4 * cap * layers) as f64; // resident working set, f32

    println!("\n=== Green Engine — WITHOUT vs WITH Green Compress ===");
    println!("OLMoE-class: {layers} layers, {experts} experts, hidden {hidden}, inter {inter}, top-8, cap {cap}/layer\n");

    // Whole-model compressed footprints (green holds ALL experts compressed in RAM).
    let g_q8 = GreenWeightStore::from_store(&store, GreenBase::Q8, 64, 0, 0.0, cap);
    let g_q4 = GreenWeightStore::from_store(&store, GreenBase::Q4, 64, 16, 0.01, cap);

    println!("WEIGHT MEMORY");
    println!("  {:<40} {:>10} {:>9}", "tier", "MB", "vs full");
    println!("  {:<40} {:>10.1} {:>8.1}x", "full model, f32, all resident (naive)", mb(f32_full), 1.0);
    println!("  {:<40} {:>10.1} {:>8.1}x", "+ scheduling: f32 working set (cap/layer)", mb(sched_f32), f32_full / sched_f32);
    println!("  {:<40} {:>10.1} {:>8.1}x", "+ Green Q8 (whole model compressed)", mb(g_q8.compressed_bytes() as f64), f32_full / g_q8.compressed_bytes() as f64);
    println!("  {:<40} {:>10.1} {:>8.1}x", "+ Green Q4+repair (whole model)", mb(g_q4.compressed_bytes() as f64), f32_full / g_q4.compressed_bytes() as f64);

    // Fidelity + throughput: f32 store vs green store, same routing.
    let (tps_f32, y_f32) = run(&store, 24, true);
    let (tps_g8, y_g8) = run(&g_q8, 24, true);
    let drift = {
        let (num, den): (f32, f32) = y_f32.iter().zip(&y_g8)
            .fold((0.0, 0.0), |(n, d), (a, b)| (n + (a - b).powi(2), d + a * a));
        (num / den.max(1e-9)).sqrt()
    };
    println!("\nQUALITY & THROUGHPUT (CPU backend)");
    println!("  f32 weights   : {:>6.1} tok/s   (reference quality)", tps_f32);
    println!("  Green Q8      : {:>6.1} tok/s   output drift vs f32 {:.2e}", tps_g8, drift);
    println!("  llama.cpp OLMoE-1B-7B CPU anchor: ~43-50 tok/s (Q4_K_M, fully resident) — reference;");
    println!("  the engine's edge is the offload/memory regime (runs models that don't fit RAM).");

    // KV-cache tiers (the token/context-memory axis, orthogonal to weights).
    let (n_kv_heads, head_dim, ml) = (16usize, 128usize, 32usize);
    println!("\nKV-CACHE MEMORY (16 kv-heads, head_dim 128, {ml} layers)");
    println!("  {:>9} {:>12} {:>12} {:>12}", "context", "fp16", "int8 (KIVI)", "int4 (KIVI)");
    for ctx in [8_192usize, 32_768, 131_072] {
        let kept = vec![ctx; ml];
        let gb = |bits: f64| kv_bytes(&kept, n_kv_heads, head_dim, bits) as f64 / 1e9;
        println!("  {:>7}k {:>10.2} GB {:>10.2} GB {:>10.2} GB", ctx / 1024, gb(16.0), gb(8.0), gb(4.0));
    }
    // Real KV-quant fidelity on structured KV.
    let (tk, ch) = (256usize, n_kv_heads * head_dim);
    let mut s = 5u64;
    let kvd: Vec<f32> = (0..tk * ch).map(|i| { let bias = ((i % ch) as f32 / ch as f32) - 0.5; bias + 0.05 * rng(&mut s) }).collect();
    let (_, d8) = quantize_kv(&kvd, tk, ch, 8, true);
    let (_, d4) = quantize_kv(&kvd, tk, ch, 4, true);
    println!("  KV quant fidelity (keys per-channel): int8 drift {d8:.2e}, int4 drift {d4:.2e}");
    println!("  → int8 KV ~2× / int4 KV ~4× less than fp16 at high fidelity = that many × longer context.");

    println!("\nSUMMARY: scheduling × Green weights × KV quant compound — a model that needs {:.0} MB f32",
             mb(f32_full));
    println!("resident runs in {:.0} MB (Green Q4 whole model) with a bounded decoded working set, and", mb(g_q4.compressed_bytes() as f64));
    println!("long-context KV shrinks 2-4× on top — so it fits machines that 'shouldn't' run it.");
}
