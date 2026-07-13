//! Quality/size frontier in REAL units — size in KB/MB, quality as fidelity % (100% = identical to
//! the full-precision model). Measures end-to-end EXPERT OUTPUT fidelity (y = expert(x)) over
//! realistic activations, per weight tier, and scales the size to a whole OLMoE-class model.
//!
//!   cargo run -p engine-core --release --bin frontier_bench

use engine_core::backend::CpuBackend;
use engine_core::{ExpertBackend, ExpertWeights, Scratch, Tensor};

fn rng(s: &mut u64) -> f32 {
    *s ^= *s >> 12; *s ^= *s << 25; *s ^= *s >> 27;
    (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
}

// Structured [in,out]: per-column bias + magnitude varying down the column + a few outliers.
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

fn out_fidelity(refy: &[Vec<f32>], gy: &[Vec<f32>]) -> f32 {
    let (mut num, mut den) = (0.0f64, 0.0f64);
    for (a, b) in refy.iter().zip(gy) {
        for (x, y) in a.iter().zip(b) { num += ((x - y) as f64).powi(2); den += (*x as f64).powi(2); }
    }
    (100.0 * (1.0 - (num / den.max(1e-12)).sqrt())) as f32
}

fn run_expert(gate: &[f32], up: &[f32], down: &[f32], h: usize, i: usize, xs: &[Vec<f32>]) -> Vec<Vec<f32>> {
    let e = ExpertWeights { hidden: h, inter: i, gate: Tensor::F32(gate.to_vec()), up: Tensor::F32(up.to_vec()), down: Tensor::F32(down.to_vec()) };
    let cpu = CpuBackend;
    let mut sc = Scratch::new(h, i);
    xs.iter().map(|x| { let mut y = vec![0.0f32; h]; cpu.compute_expert(&e, x, &mut sc, &mut y); y }).collect()
}

fn main() {
    let (h, i) = (1024usize, 512usize); // one OLMoE-class expert (gate/up [h,i], down [i,h])
    let (model_layers, model_experts) = (16usize, 64usize);
    let g0 = structured(h, i, 1);
    let u0 = structured(h, i, 2);
    let d0 = structured(i, h, 3);

    // realistic activations: a per-input-channel importance profile, test vectors drawn from it.
    let mut s = 42u64;
    let act_mag: Vec<f32> = (0..h).map(|c| if c % 32 == 0 { 6.0 } else { 0.4 + rng(&mut s).abs() }).collect();
    let xs: Vec<Vec<f32>> = (0..8).map(|_| (0..h).map(|c| act_mag[c] * rng(&mut s)).collect()).collect();
    let refy = run_expert(&g0, &u0, &d0, h, i, &xs);

    let expert_f32 = 3 * h * i * 4;
    let model_experts_total = model_layers * model_experts;
    println!("\n=== Quality / size frontier (real units) — OLMoE-class expert (gate/up {h}×{i}, down {i}×{h}) ===");
    println!("Full model = {model_layers} layers × {model_experts} experts. Quality = expert-output fidelity vs f32 (100% = identical).\n");
    println!("  {:<22} {:>12} {:>13} {:>10}", "tier", "KB/expert", "MB/model", "fidelity");
    println!("  {:<22} {:>10.1} KB {:>10.0} MB {:>9.1}%", "f32 (reference)", expert_f32 as f64 / 1024.0, (expert_f32 * model_experts_total) as f64 / 1e6, 100.0);

    // each closure returns (reconstructed gate/up/down, total bytes/expert)
    let sz = |t: &Tensor| t.bytes();
    type Recon = (Vec<f32>, Vec<f32>, Vec<f32>, usize);
    let tiers: Vec<(&str, Box<dyn Fn() -> Recon>)> = vec![
        ("Q8Ch per-channel", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_q8_ch(&g0, i), Tensor::quantize_q8_ch(&u0, i), Tensor::quantize_q8_ch(&d0, h));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("Q4G group-32", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_q4_group(&g0, 32), Tensor::quantize_q4_group(&u0, 32), Tensor::quantize_q4_group(&d0, 32));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("Q4G+MSEclip g-32", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_q4_group_clip(&g0, 32), Tensor::quantize_q4_group_clip(&u0, 32), Tensor::quantize_q4_group_clip(&d0, 32));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("NF4 group-32", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_nf4_group(&g0, 32), Tensor::quantize_nf4_group(&u0, 32), Tensor::quantize_nf4_group(&d0, 32));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
        ("AWQ int4 (calibrated)", Box::new(|| {
            let a = Tensor::awq_reconstruct(&g0, h, i, &act_mag, 32);
            let b = Tensor::awq_reconstruct(&u0, h, i, &act_mag, 32);
            let c = Tensor::awq_reconstruct(&d0, i, h, &vec![1.0; i], 32); // down input = inter (no act profile)
            let bytes = sz(&Tensor::quantize_q4_group(&g0, 32)) * 2 + sz(&Tensor::quantize_q4_group(&d0, 32)) + (h + h + i) * 4;
            (a, b, c, bytes)
        })),
        ("int4 + 1% outliers", Box::new(|| {
            let no = h * i / 100;
            let a = Tensor::outlier_reconstruct(&g0, 32, no);
            let b = Tensor::outlier_reconstruct(&u0, 32, no);
            let c = Tensor::outlier_reconstruct(&d0, 32, no);
            let bytes = (sz(&Tensor::quantize_q4_group(&g0, 32)) + no * 8) * 2 + sz(&Tensor::quantize_q4_group(&d0, 32)) + (i * h / 100) * 8;
            (a, b, c, bytes)
        })),
        ("Q3G group-64 (smallest)", Box::new(|| {
            let (a, b, c) = (Tensor::quantize_q3_group(&g0, 64), Tensor::quantize_q3_group(&u0, 64), Tensor::quantize_q3_group(&d0, 64));
            (a.to_f32_vec(), b.to_f32_vec(), c.to_f32_vec(), sz(&a) + sz(&b) + sz(&c))
        })),
    ];

    for (label, f) in &tiers {
        let (ga, ua, da, bytes) = f();
        let gy = run_expert(&ga, &ua, &da, h, i, &xs);
        let fid = out_fidelity(&refy, &gy);
        println!("  {:<22} {:>10.1} KB {:>10.0} MB {:>9.1}%   ({:.1}× smaller)",
                 label, bytes as f64 / 1024.0, (bytes * model_experts_total) as f64 / 1e6, fid, expert_f32 as f64 / bytes as f64);
    }
    println!("\nKB/expert = one expert compressed; MB/model = all {model_experts_total} experts. Higher fidelity % = closer");
    println!("to the original model. AWQ uses calibrated activations; outliers keep the 1% worst weights in f32.");
}
