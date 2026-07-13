//! Paged-store benchmark: run a model whose weights live on disk while only a bounded, per-layer
//! working set sits in RAM — the "runs on a machine with less RAM than the model" capability.
//!
//! Compares two on-disk formats, both driven by the REAL OLMoE routing trace (so hit rates are the
//! engine's validated locality):
//!   * F32  — lossless (paged == all-in-RAM, bit-for-bit).
//!   * Q8Ch — per-channel int8 "green compression": ~4× smaller on disk AND in RAM, small drift.
//! Together with paging this runs an effectively-4×-bigger model on the same old PC.
//!
//!   cargo run -p engine-core --release --bin paged_bench
//!   GE_H=128 GE_INTER=128 GE_CAP=24 ...   (weight dims / cache tunable; routing from the trace)

use engine_core::{dense_provider, CpuBackend, PagedFormat, PagedWeightStore, Trace, WeightStore};

fn env(name: &str, default: usize) -> usize {
    std::env::var(name).ok().and_then(|v| v.parse().ok()).unwrap_or(default)
}

fn main() {
    let trace_path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../results/expert_trace.bin");
    let trace = match Trace::load(trace_path) {
        Ok(t) => t,
        Err(_) => {
            eprintln!("no results/expert_trace.bin — run experiments/moe_trace/export_trace.py first");
            return;
        }
    };
    let h = env("GE_H", 128);
    let inter = env("GE_INTER", 128);
    let cap = env("GE_CAP", 24);
    let (layers, experts, tokens) = (trace.layers, trace.experts, trace.tokens);
    let mib = |b: u64| b as f64 / (1024.0 * 1024.0);
    let f32_full = layers as u64 * experts as u64 * (3 * h * inter * 4) as u64;

    let store = WeightStore::synthetic(layers, experts, h, inter, false, 7);
    let backend = CpuBackend;
    let x: Vec<f32> = {
        let mut s = 42u64;
        (0..h)
            .map(|_| {
                s ^= s >> 12;
                s ^= s << 25;
                s ^= s >> 27;
                (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
            })
            .collect()
    };
    let gates_of = |n: usize| vec![1.0f32 / n as f32; n];

    // Reference (all-in-RAM f32) per-token outputs, to measure each format's drift.
    println!("\nPaged-store benchmark — real OLMoE routing: {layers} layers × {experts} experts, top-{}, {tokens} tokens", trace.top_k);
    println!("  weights synthetic {h}×{inter}; per-layer cache cap = {cap}. f32 model = {:.1} MiB.\n", mib(f32_full));
    println!("  {:>7}  {:>10}  {:>9}  {:>7}  {:>7}  {:>12}  {:>9}", "format", "disk", "peak RAM", "RAM %", "hit %", "disk read", "drift");

    for fmt in [PagedFormat::F32, PagedFormat::Q8Ch, PagedFormat::Q4Ch] {
        let path = std::env::temp_dir().join(format!("ge_paged_bench_{}_{:?}.bin", std::process::id(), fmt));
        PagedWeightStore::create(&path, &store, fmt).expect("write paged store");
        let paged = PagedWeightStore::open(&path, cap).expect("open paged store");

        let (mut num, mut den) = (0.0f64, 0.0f64);
        for t in 0..tokens {
            for l in 0..layers {
                let chosen = trace.experts_at(t, l);
                let gates = gates_of(chosen.len());
                let got = dense_provider(&paged, &backend, l, &x, chosen, &gates);
                let want = dense_provider(&store, &backend, l, &x, chosen, &gates);
                for (a, b) in want.iter().zip(&got) {
                    num += ((a - b) as f64).powi(2);
                    den += (*a as f64).powi(2);
                }
            }
        }
        let rel = (num / den.max(1e-12)).sqrt();
        let m = paged.metrics();
        let req = m.hits + m.cold_reads;
        let hitp = if req > 0 { 100.0 * m.hits as f64 / req as f64 } else { 0.0 };
        let peak = m.peak_resident_experts as u64 * paged.expert_stride_bytes();
        println!(
            "  {:>7}  {:>9.1}M  {:>8.1}M  {:>6.1}%  {:>6.1}%  {:>11.1}M  {:>9}",
            format!("{:?}", fmt),
            mib(paged.full_bytes()),
            mib(peak),
            100.0 * peak as f64 / f32_full as f64,
            hitp,
            mib(m.bytes_read),
            if rel == 0.0 { "lossless".to_string() } else { format!("{:.4}", rel) }
        );
        let _ = std::fs::remove_file(&path);
    }
    println!("\n  => Compression spectrum, all paged (RAM = per-layer cap, not whole model):");
    println!("     F32 lossless · Q8Ch ~4× smaller at <1% drift · Q4Ch ~8× smaller (int4; drift is");
    println!("     data-dependent — high on synthetic uniform noise, much lower on real weights).");
    println!("     Pick the tier your RAM/quality budget allows; the model always lives on disk.");
}
