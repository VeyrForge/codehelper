//! FULL benchmark — every option compared in real units (MB per model, fidelity % vs f32), on one
//! OLMoE-class expert scaled to a 16×64 model. Answers: how do the engine's own tiers compare to
//! Green Compress (with/without repair), and to no compression at all? Build with `--features green`.
//!
//!   cargo run -p engine-core --features green --release --bin full_bench

use engine_core::backend::CpuBackend;
use engine_core::green::GreenTensor;
use engine_core::{ExpertBackend, ExpertWeights, Scratch, Tensor};

fn rng(s: &mut u64) -> f32 {
    *s ^= *s >> 12; *s ^= *s << 25; *s ^= *s >> 27;
    (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
}

fn structured(inn: usize, out: usize, seed: u64) -> Vec<f32> {
    let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
    let bias: Vec<f32> = (0..out).map(|_| rng(&mut s) * 1.5).collect();
    let mut w = vec![0.0f32; inn * out];
    for i in 0..inn {
        let mag = 0.15 + (i as f32 / inn as f32);
        for o in 0..out { w[i * out + o] = bias[o] + mag * rng(&mut s); }
    }
    for t in 0..(inn * out / 400).max(1) { w[(t * 2654435761) % (inn * out)] += 3.0; }
    w
}

fn run(gate: &[f32], up: &[f32], down: &[f32], h: usize, i: usize, xs: &[Vec<f32>]) -> Vec<Vec<f32>> {
    let e = ExpertWeights { hidden: h, inter: i, gate: Tensor::F32(gate.to_vec()), up: Tensor::F32(up.to_vec()), down: Tensor::F32(down.to_vec()) };
    let (cpu, mut sc) = (CpuBackend, Scratch::new(h, i));
    xs.iter().map(|x| { let mut y = vec![0.0f32; h]; cpu.compute_expert(&e, x, &mut sc, &mut y); y }).collect()
}

fn fidelity(refy: &[Vec<f32>], gy: &[Vec<f32>]) -> f32 {
    let (mut num, mut den) = (0.0f64, 0.0f64);
    for (a, b) in refy.iter().zip(gy) {
        for (x, y) in a.iter().zip(b) { num += ((x - y) as f64).powi(2); den += (*x as f64).powi(2); }
    }
    (100.0 * (1.0 - (num / den.max(1e-12)).sqrt())) as f32
}

fn main() {
    let (h, i) = (1024usize, 512usize); // power-of-two h for Hadamard
    let (layers, experts) = (16usize, 64usize);
    let n_experts = layers * experts;
    let (g0, u0, d0) = (structured(h, i, 1), structured(h, i, 2), structured(i, h, 3));
    let mut s = 42u64;
    let xs: Vec<Vec<f32>> = (0..8).map(|_| (0..h).map(|_| rng(&mut s)).collect()).collect();
    let refy = run(&g0, &u0, &d0, h, i, &xs);
    let f32_bytes = 3 * h * i * 4;
    let mb = |b: usize| (b * n_experts) as f64 / 1e6;

    println!("\n================ FULL BENCHMARK — real units (MB/model, fidelity % vs f32) ================");
    println!("OLMoE-class expert (gate/up {h}×{i}, down {i}×{h}); full model {layers}×{experts} = {n_experts} experts.");
    println!("Fidelity = expert-output vs f32 (100% = identical). f32 model = {:.0} MB.\n", mb(f32_bytes));

    type R = (Vec<f32>, Vec<f32>, Vec<f32>, usize);
    let sz = |t: &Tensor| t.bytes();
    let gb = |t: &GreenTensor| t.bytes();

    let rows: Vec<(&str, &str, Box<dyn Fn() -> R>)> = vec![
        ("BASELINE (no engine)", "f32 uncompressed", Box::new(|| (g0.clone(), u0.clone(), d0.clone(), f32_bytes))),
        ("Engine (dep-free)", "Q8Ch per-channel", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_q8_ch(&g0, i), Tensor::quantize_q8_ch(&u0, i), Tensor::quantize_q8_ch(&d0, h));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("Engine (dep-free)", "green-int6 g-32 (balance)", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_q6_group(&g0, 32), Tensor::quantize_q6_group(&u0, 32), Tensor::quantize_q6_group(&d0, 32));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("Engine (dep-free)", "NF4 group-32", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_nf4_group(&g0, 32), Tensor::quantize_nf4_group(&u0, 32), Tensor::quantize_nf4_group(&d0, 32));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("Engine (dep-free)", "Hadamard+NF4 g-32", Box::new(|| {
            let bytes = sz(&Tensor::quantize_nf4_group(&g0, 32)) * 3;
            (Tensor::hadamard_reconstruct(&g0, h, i, 32), Tensor::hadamard_reconstruct(&u0, h, i, 32), Tensor::hadamard_reconstruct(&d0, i, h, 32), bytes)
        })),
        ("Engine (dep-free)", "green-int3 g-64", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_q3_group(&g0, 64), Tensor::quantize_q3_group(&u0, 64), Tensor::quantize_q3_group(&d0, 64));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("Engine (dep-free)", "green-rvq (nf4+int2res)", Box::new(|| {
            let bytes = (sz(&Tensor::quantize_nf4_group(&g0, 32)) + sz(&Tensor::quantize_q2_group(&g0, 32))) * 3;
            (Tensor::residual_reconstruct(&g0, h, i, 32), Tensor::residual_reconstruct(&u0, h, i, 32), Tensor::residual_reconstruct(&d0, i, h, 32), bytes)
        })),
        ("Green Compress", "Green Q8 (no repair)", Box::new(|| {
            let a = GreenTensor::compress(&g0, h, i, 64, 0, 0.0);
            let b = GreenTensor::compress(&u0, h, i, 64, 0, 0.0);
            let c = GreenTensor::compress(&d0, i, h, 64, 0, 0.0);
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), gb(&a) + gb(&b) + gb(&c))
        })),
        ("Green Compress", "Green Q8 + repair", Box::new(|| {
            let a = GreenTensor::compress(&g0, h, i, 64, 16, 0.005);
            let b = GreenTensor::compress(&u0, h, i, 64, 16, 0.005);
            let c = GreenTensor::compress(&d0, i, h, 64, 16, 0.005);
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), gb(&a) + gb(&b) + gb(&c))
        })),
        ("Green Compress", "Green Q4 + repair", Box::new(|| {
            let a = GreenTensor::compress_q4(&g0, h, i, 64, 16, 0.01);
            let b = GreenTensor::compress_q4(&u0, h, i, 64, 16, 0.01);
            let c = GreenTensor::compress_q4(&d0, i, h, 64, 16, 0.01);
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), gb(&a) + gb(&b) + gb(&c))
        })),
    ];

    println!("  {:<20} {:<24} {:>10} {:>8} {:>10}", "category", "tier", "MB/model", "vs f32", "fidelity");
    let mut last_cat = "";
    for (cat, label, f) in &rows {
        let (ga, ua, da, bytes) = f();
        let fid = fidelity(&refy, &run(&ga, &ua, &da, h, i, &xs));
        let cat_show = if *cat == last_cat { "" } else { *cat };
        last_cat = cat;
        println!("  {:<20} {:<24} {:>7.0} MB {:>7.1}x {:>9.1}%", cat_show, label, mb(bytes), f32_bytes as f64 / bytes as f64, fid);
    }
    println!("\nWITHOUT green engine = the f32 baseline (top row). WITH green engine = every row below (compression");
    println!("+ scheduling + paging). WITHOUT green compress = dep-free engine tiers. WITH green compress = repair rows.");
    println!("Green repair adds bytes but lifts low-bit fidelity; the dep-free NF4/Hadamard tiers need no extra crate.");
}
