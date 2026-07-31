//! CPU+GPU heterogeneous benchmark + end-to-end "engine × Green Compress" memory.
//!  1. REAL multi-core CPU expert throughput (measured on this machine).
//!  2. Heterogeneous split: CPU (measured) + GPU (modeled) running concurrently vs either alone.
//!  3. End-to-end: load the real Green Compress manifest, stack compression × scheduling.

use std::sync::Arc;
use std::thread;
use std::time::Instant;

use engine_core::backend::{ExpertBackend, Scratch};
use engine_core::hetero::Hetero;
use engine_core::{CpuBackend, WeightManifest, WeightStore, OLMOE_EXPERT_BYTES_FP16};

fn cpu_throughput(store: Arc<WeightStore>, threads: usize, per_thread: usize) -> f64 {
    let t0 = Instant::now();
    let mut hs = Vec::new();
    for _ in 0..threads {
        let s = store.clone();
        hs.push(thread::spawn(move || {
            let b = CpuBackend;
            let mut sc = Scratch::new(s.hidden, s.inter);
            let mut out = vec![0.0f32; s.hidden];
            let x = vec![0.01f32; s.hidden];
            for i in 0..per_thread {
                b.compute_expert(s.get(0, (i % s.experts as usize) as u16), &x, &mut sc, &mut out);
            }
        }));
    }
    for h in hs {
        h.join().unwrap();
    }
    (threads * per_thread) as f64 / t0.elapsed().as_secs_f64()
}

fn main() {
    // modest expert dims to keep the CPU burst short
    let store = Arc::new(WeightStore::synthetic(1, 32, 1024, 2048, false, 7));
    let per_thread = 400;

    println!("\n=== 1. Real multi-core CPU expert throughput (AMD 16-core) ===\n");
    let mut single = 0.0;
    for &t in &[1usize, 4, 8, 16] {
        let tp = cpu_throughput(store.clone(), t, per_thread);
        if t == 1 {
            single = tp;
        }
        println!("  {:>2} threads: {:>8.0} experts/s   ({:>4.1}× vs 1 thread)", t, tp, tp / single);
    }
    let tp16 = cpu_throughput(store.clone(), 16, per_thread);
    let cpu_ms_par = 1000.0 / (tp16 / 16.0) / 16.0; // effective per-expert ms with 16-way parallelism
    println!("\n  effective per-expert CPU time at 16 threads: {:.4} ms", cpu_ms_par);

    // 2. heterogeneous CPU+GPU split (CPU measured above, GPU modeled)
    println!("\n=== 2. Heterogeneous CPU+GPU split (concurrent) ===\n");
    let gpu_ms = 0.0073; // assumed (RTX-class), see hw_constants.json
    let h = Hetero { cpu_ms: cpu_ms_par, gpu_ms, transfer_ms: 0.14 };
    let n = 128; // active expert-computes per token (top-8 × 16 layers)
    let (g, t_het) = h.optimal_split(n);
    let cpu_only = h.cpu_only_ms(n);
    let gpu_off = h.gpu_offload_ms(n, 0.25);
    println!("  {n} active experts/token: CPU-only {:.2} ms | GPU+offload(25% res) {:.2} ms", cpu_only, gpu_off);
    println!(
        "  hetero (concurrent): {g} on GPU + {} on CPU -> {:.2} ms/token",
        n - g, t_het
    );
    println!(
        "  speedup vs CPU-only {:.1}×  |  vs naive GPU-offload {:.1}×",
        cpu_only / t_het, gpu_off / t_het
    );

    // 3. end-to-end: engine scheduling × Green Compress compression (real manifest)
    println!("\n=== 3. End-to-end: engine × Green Compress (real manifest) ===\n");
    let layers = 16usize;
    let experts = 64usize;
    let man_path = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/expert_manifest.txt");
    match WeightManifest::load(man_path, layers, experts) {
        Ok(man) => {
            let mut comp = 0u64;
            let mut drift = 0.0f64;
            let mut bits = 0.0f64;
            for l in 0..layers {
                for e in 0..experts {
                    comp += man.expert_bytes(l, e as u16);
                    drift += man.expert_drift(l, e as u16) as f64;
                    bits += man.expert_bits(l, e as u16) as f64;
                }
            }
            let nexp = (layers * experts) as f64;
            let full = OLMOE_EXPERT_BYTES_FP16 * (layers * experts) as u64;
            let resident_frac = 0.375; // engine keeps ⅜ resident (from the offload sweep)
            let scheduled_comp = (comp as f64 * resident_frac) as u64;
            println!("  Green Compress: {:.0} bits/expert avg, drift {:.3} avg (quality cost)", bits / nexp, drift / nexp);
            println!("  full fp16 weights:            {:>7.0} MB", full as f64 / 1e6);
            println!("  + Green Compress compression:       {:>7.0} MB  ({:.1}× smaller)", comp as f64 / 1e6, full as f64 / comp as f64);
            println!("  + engine residency (⅜):       {:>7.0} MB  ({:.1}× smaller total, lossless schedule)",
                     scheduled_comp as f64 / 1e6, full as f64 / scheduled_comp as f64);
        }
        Err(e) => println!("  (manifest not loadable: {e} — run Green Compress make_manifest)"),
    }
    println!("\n  CPU+GPU concurrency is measured(CPU)+modeled(GPU); compression numbers are the real manifest.");
}
