//! Kernel microbenchmark: times one expert FFN (OLMoE dims) through the Rust CPU backend vs
//! the linked native kernel (ref C++ / optimized C++ / ggml — whichever .so is linked), all via
//! the same `ExpertBackend` trait. Build with `--features gpu` and point GREEN_ENGINE_KERNELS_DIR
//! at the kernel you want to compare.

#[cfg(not(feature = "gpu"))]
fn main() {
    eprintln!("build with: --features gpu  (and GREEN_ENGINE_KERNELS_DIR set)");
}

#[cfg(feature = "gpu")]
fn main() {
    use engine_core::backend::gpu::GpuBackend;
    use engine_core::{CpuBackend, ExpertBackend, Scratch, WeightStore};
    use std::time::Instant;

    let (hidden, inter) = (2048usize, 1024usize); // OLMoE expert
    let flops = 2.0 * 3.0 * hidden as f64 * inter as f64;
    let store = WeightStore::synthetic(1, 1, hidden, inter, false, 1);
    let w = store.get(0, 0);
    let mut s = 1u64;
    let x: Vec<f32> = (0..hidden)
        .map(|_| {
            s ^= s >> 12;
            s ^= s << 25;
            s ^= s >> 27;
            (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
        })
        .collect();

    let cpu = CpuBackend;
    let native = GpuBackend::new(0);
    let mut sc = Scratch::new(hidden, inter);
    let (mut yc, mut yn) = (vec![0.0f32; hidden], vec![0.0f32; hidden]);

    let bench = |label: &str, b: &dyn ExpertBackend, sc: &mut Scratch, out: &mut [f32]| -> f64 {
        let n: usize = std::env::var("GE_ITERS").ok().and_then(|v| v.parse().ok()).unwrap_or(2000);
        for _ in 0..(n / 40 + 1) {
            b.compute_expert(w, &x, sc, out);
        }
        let t0 = Instant::now();
        for _ in 0..n {
            b.compute_expert(w, &x, sc, out);
        }
        let ms = t0.elapsed().as_secs_f64() * 1e3 / n as f64;
        println!("  {:<22} {:>8.4} ms/expert   {:>7.1} GFLOP/s", label, ms, flops / (ms / 1e3) / 1e9);
        ms
    };

    println!("\nKernel microbenchmark — one OLMoE expert FFN (hidden {hidden}, inter {inter})\n");
    let cpu_ms = bench("Rust CpuBackend", &cpu, &mut sc, &mut yc);
    let nat_ms = bench(&format!("native ({})", native.name()), &native, &mut sc, &mut yn);
    let diff = yc.iter().zip(&yn).map(|(a, b)| (a - b).abs()).fold(0.0f32, f32::max);
    println!("\n  speedup native/Rust: {:.2}x   max output diff: {:.2e} (correctness)", cpu_ms / nat_ms, diff);
}
