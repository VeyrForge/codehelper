//! CPU matvec throughput — the register-blocked GEMV vs a naive one, at OLMoE FFN sizes.
//! Decode is memory-bound GEMV; blocking the input dim by 4 cuts accumulator traffic and adds ILP.
//!
//!   cargo run -p engine-core --release --bin matvec_bench

use std::time::Instant;

fn naive(x: &[f32], w: &[f32], in_dim: usize, out_dim: usize, y: &mut [f32]) {
    for v in y.iter_mut() { *v = 0.0; }
    for i in 0..in_dim {
        let xi = x[i];
        let row = &w[i * out_dim..(i + 1) * out_dim];
        for o in 0..out_dim { y[o] += xi * row[o]; }
    }
}

fn main() {
    let (inn, out) = (2048usize, 1024usize); // OLMoE gate/up shape
    let mut s = 1u64;
    let mut rng = || { s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
        (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 };
    let x: Vec<f32> = (0..inn).map(|_| rng()).collect();
    let w: Vec<f32> = (0..inn * out).map(|_| rng()).collect();
    let flops = 2.0 * inn as f64 * out as f64;
    let iters = 2000;

    let mut yn = vec![0.0f32; out];
    let mut yo = vec![0.0f32; out];
    // correctness: same result within fp tolerance
    naive(&x, &w, inn, out, &mut yn);
    engine_core::tensor::matvec(&x, &w, inn, out, &mut yo);
    let maxdiff = yn.iter().zip(&yo).map(|(a, b)| (a - b).abs()).fold(0.0f32, f32::max);

    let t0 = Instant::now();
    for _ in 0..iters { naive(&x, &w, inn, out, &mut yn); }
    let tn = t0.elapsed().as_secs_f64() / iters as f64;

    let t0 = Instant::now();
    for _ in 0..iters { engine_core::tensor::matvec(&x, &w, inn, out, &mut yo); }
    let to = t0.elapsed().as_secs_f64() / iters as f64;

    println!("\nCPU matvec — OLMoE FFN shape {inn}×{out} (memory-bound GEMV)\n");
    println!("  naive              {:>7.1} GFLOP/s   {:>7.2} us/call", flops / tn / 1e9, tn * 1e6);
    println!("  register-blocked   {:>7.1} GFLOP/s   {:>7.2} us/call   ({:.2}× faster)", flops / to / 1e9, to * 1e6, tn / to);
    println!("  max diff naive vs blocked: {maxdiff:.2e} (fp reassociation only)");
}
