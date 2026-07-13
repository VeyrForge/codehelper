//! Residency benchmark: cycles a WORKING SET of `GE_M` distinct experts through the native GPU
//! backend, mimicking sparse MoE routing where a handful of hot experts recur across tokens.
//!
//! The GPU backend caches up to `GE_GPU_RESIDENT` experts' weights in VRAM. Run this twice to see
//! the residency win directly:
//!   GE_GPU_RESIDENT=8 GE_M=8 ...   # working set fits -> all hits -> no per-call weight upload
//!   GE_GPU_RESIDENT=1 GE_M=8 ...   # thrash -> every call re-uploads the expert (~24 MB) over PCIe
//!
//! Build with `--features gpu` and point GREEN_ENGINE_KERNELS_DIR at the CUDA kernel.

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
    let m: usize = std::env::var("GE_M").ok().and_then(|v| v.parse().ok()).unwrap_or(8);
    let iters: usize = std::env::var("GE_ITERS").ok().and_then(|v| v.parse().ok()).unwrap_or(4000);
    let resident = std::env::var("GE_GPU_RESIDENT").unwrap_or_else(|_| "8 (default)".into());
    let fp16 = std::env::var("GE_GPU_FP16").map(|v| v == "1").unwrap_or(false);
    let int8 = std::env::var("GE_GPU_INT8").map(|v| v == "1").unwrap_or(false);
    let int4 = std::env::var("GE_GPU_INT4").map(|v| v == "1").unwrap_or(false);
    let flops = 2.0 * 3.0 * hidden as f64 * inter as f64;
    // Resident VRAM per expert = 3 matrices: 4 B/w f32, 2 fp16, 1 int8, 0.5 int4 (+per-column scales).
    let expert_mib = if int4 {
        (3 * hidden * inter).div_ceil(2) as f64 / (1024.0 * 1024.0)
    } else {
        let bpw = if int8 { 1 } else if fp16 { 2 } else { 4 };
        (3 * hidden * inter * bpw) as f64 / (1024.0 * 1024.0)
    };
    let mode = if int4 { "int4-per-channel (GE_GPU_INT4=1)" } else if int8 { "int8-per-channel (GE_GPU_INT8=1)" }
        else if fp16 { "fp16 (GE_GPU_FP16=1)" } else { "f32" };

    // A working set of M distinct experts (one synthetic layer with M experts).
    let store = WeightStore::synthetic(1, m, hidden, inter, false, 1);
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
    let mut out = vec![0.0f32; hidden];

    // Correctness: GPU expert 0 must match the CPU reference regardless of caching.
    let mut yc = vec![0.0f32; hidden];
    cpu.compute_expert(store.get(0, 0), &x, &mut sc, &mut yc);
    native.compute_expert(store.get(0, 0), &x, &mut sc, &mut out);
    let diff = yc.iter().zip(&out).map(|(a, b)| (a - b).abs()).fold(0.0f32, f32::max);

    // In int8 mode the diff-vs-f32 above is the INHERENT quantization loss (large on these synthetic
    // max-entropy weights — int8's worst case). To separate that from kernel correctness, build the
    // same expert as CPU Q8Ch (the validated per-channel tier) and confirm the GPU int8 path matches
    // it — the two should agree to ~1e-6, proving the on-device quantize+dequant is exact.
    let kernel_diff = if int8 || int4 {
        use engine_core::{ExpertWeights, Tensor};
        let e0 = store.get(0, 0);
        let qz = |w: &[f32], out: usize| if int4 { Tensor::quantize_q4_ch(w, out) } else { Tensor::quantize_q8_ch(w, out) };
        let q = ExpertWeights {
            hidden,
            inter,
            gate: qz(&e0.gate.to_f32_vec(), inter),
            up: qz(&e0.up.to_f32_vec(), inter),
            down: qz(&e0.down.to_f32_vec(), hidden),
        };
        let mut yq = vec![0.0f32; hidden];
        cpu.compute_expert(&q, &x, &mut sc, &mut yq); // CPU Q8Ch/Q4Ch reference
        Some(yq.iter().zip(&out).map(|(a, b)| (a - b).abs()).fold(0.0f32, f32::max))
    } else {
        None
    };

    // Warm up: touch every expert so the cache reaches steady state (all resident if it fits).
    for e in 0..m {
        native.compute_expert(store.get(0, e as u16), &x, &mut sc, &mut out);
    }

    let t0 = Instant::now();
    for i in 0..iters {
        let e = (i % m) as u16; // cycle the working set
        native.compute_expert(store.get(0, e), &x, &mut sc, &mut out);
    }
    let ms = t0.elapsed().as_secs_f64() * 1e3 / iters as f64;

    let ws_mib = expert_mib * m as f64;
    println!("\nResidency benchmark — cycle {m} OLMoE experts (hidden {hidden}, inter {inter})");
    println!("  GE_GPU_RESIDENT = {resident}   working set = {m} experts   weights = {mode}");
    println!("  ~{expert_mib:.1} MiB/expert   resident working-set VRAM ~{ws_mib:.0} MiB\n");
    println!("  {:>10.4} ms/expert   {:>7.1} GFLOP/s   (max GPU-vs-CPU diff {:.2e})", ms, flops / (ms / 1e3) / 1e9, diff);
    if let Some(kd) = kernel_diff {
        let (bits, cpu_ref) = if int4 { (4, "Q4Ch") } else { (8, "Q8Ch") };
        println!("  int{bits} kernel vs CPU {cpu_ref} reference: max diff {kd:.2e}  (≈0 ⇒ on-device quant is exact;");
        println!("  the {diff:.2e} above is int{bits}'s inherent loss on synthetic max-entropy weights, its worst case)");
    }
    let pcie_mib = (3 * hidden * inter * 4) as f64 / (1024.0 * 1024.0); // miss H2D is always f32
    println!(
        "  If this cache holds the working set, per-call weight upload is skipped; if it thrashes,\n  \
         each call moves ~{pcie_mib:.0} MiB over PCIe. Compare GE_GPU_RESIDENT=1 vs >= {m}."
    );
}
