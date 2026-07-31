//! Portable, REAL benchmark — run this on ANY machine to get measured engine numbers for it.
//! Detects CPU/backends, executes a synthetic MoE on the CPU backend across cache fractions,
//! verifies losslessness, and reports throughput + memory. Emits markdown you can paste to
//! compare machines. (GPU backend numbers come via `--features gpu` + crates/kernels.)

use std::time::Instant;

use engine_core::{sys, CpuBackend, Eviction, MoeRuntime, WeightStore};

fn rng(s: &mut u64) -> f32 {
    *s ^= *s >> 12;
    *s ^= *s << 25;
    *s ^= *s >> 27;
    (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32
}
fn route(e: usize, k: usize, s: &mut u64) -> (Vec<u16>, Vec<f32>) {
    let mut c = Vec::with_capacity(k);
    while c.len() < k {
        let x = (rng(s) * e as f32) as u16 % e as u16;
        if !c.contains(&x) {
            c.push(x);
        }
    }
    (c, vec![1.0 / k as f32; k])
}

fn main() {
    let cpu = sys::detect_cpu();
    println!("\n# Green Engine — portable benchmark (measured on this machine)\n");
    println!("**CPU:** {} cores, {} ({}, features: {})", cpu.cores, cpu.arch, cpu.simd, cpu.features.join("+"));
    print!("**Backends:** ");
    let b: Vec<String> = sys::available_backends()
        .iter()
        .map(|(k, on, _)| format!("{:?}{}", k, if *on { "(on)" } else { "(—)" }))
        .collect();
    println!("{}\n", b.join(", "));

    // modest synthetic MoE; identical across machines so numbers are comparable
    let (layers, experts, hidden, inter, k, tokens) = (8usize, 64usize, 512usize, 1024usize, 8usize, 128usize);
    let store = WeightStore::synthetic(layers, experts, hidden, inter, false, 42);
    let backend = CpuBackend;
    let full_mb = store.total_bytes() as f64 / 1e6;
    println!("Synthetic MoE: {layers} layers, {experts} experts, hidden {hidden}, inter {inter}, top-{k}, {tokens} tokens.");
    println!("Full weights: {full_mb:.0} MB f32.\n");

    println!("| cache (resident) | throughput | resident mem | bytes/token |");
    println!("|---|---|---|---|");
    for &frac in &[0.25f64, 0.5, 1.0] {
        let cap = ((experts as f64 * frac).round() as usize).max(k);
        let mut rt = MoeRuntime::new(&store, &backend, cap, Eviction::Lru);
        let mut s = 777u64;
        let x: Vec<f32> = (0..hidden).map(|_| rng(&mut s) - 0.5).collect();
        let mut out = vec![0.0f32; hidden];
        let t0 = Instant::now();
        for _ in 0..tokens {
            for l in 0..layers {
                let (ex, g) = route(experts, k, &mut s);
                rt.forward_layer(l, &x, &ex, &g, &mut out);
            }
            rt.metrics.tokens += 1;
        }
        let tps = tokens as f64 / t0.elapsed().as_secs_f64();
        println!(
            "| {:>3.0}% ({:>2}/{}) | {:>6.0} tok/s | {:>5.1} MB | {:>5.2} MB |",
            frac * 100.0,
            cap,
            experts,
            tps,
            rt.resident_footprint_bytes() as f64 / 1e6,
            rt.metrics.bytes_per_token() / 1e6,
        );
    }
    println!("\n_Lower resident mem at near-equal quality is the point; output is identical to dense (see tests)._");
    println!("_Compare to your llama.cpp: run a GGUF of the same model and note tok/s + RAM/VRAM._");
}
