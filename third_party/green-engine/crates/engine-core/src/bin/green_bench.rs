//! Green Compress vs the engine's own quant tiers — is `--features green` *truly* better?
//!
//! Compresses a synthetic-but-STRUCTURED expert (low-rank signal + sparse outliers + small noise,
//! the regime real transformer weights live in) four ways and reports, per method:
//!   * stored bytes (RAM/VRAM footprint of the resident form)
//!   * reconstruction fidelity vs f32 (rel-L2 drift; lower = better quality)
//!   * end-to-end expert-output error through the real CpuBackend (max abs + rel-L2)
//!   * compress + reconstruct wall time (does it cost speed?)
//!
//! Methods: engine Q8Ch, engine Q4Ch (per-channel, no repair) vs Green Q8-only and Green_optimal
//! (Q8 + low-rank/sparse repair). Run: `cargo run -p engine-core --features green --release --bin green_bench`

use engine_core::green::GreenTensor;
use engine_core::{CpuBackend, ExpertBackend, ExpertWeights, Scratch, Tensor};
use std::time::Instant;

fn lcg(seed: u64) -> impl FnMut() -> f32 {
    let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
    move || {
        s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
        (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
    }
}

/// [rows, cols] row-major: rank-`r` signal + small noise + a few large outliers.
fn structured(rows: usize, cols: usize, seed: u64) -> Vec<f32> {
    let mut rng = lcg(seed);
    let r = 4;
    let u: Vec<f32> = (0..rows * r).map(|_| rng()).collect();
    let v: Vec<f32> = (0..cols * r).map(|_| rng()).collect();
    let mut w = vec![0.0f32; rows * cols];
    for i in 0..rows {
        for j in 0..cols {
            let mut acc = 0.0;
            for k in 0..r {
                acc += u[i * r + k] * v[j * r + k];
            }
            w[i * cols + j] = acc + 0.02 * rng();
        }
    }
    for t in 0..(rows * cols / 500).max(1) {
        w[(t.wrapping_mul(2654435761)) % (rows * cols)] += 0.6; // realistic outlier (~few sigma)
    }
    w
}

fn rel_l2(a: &[f32], b: &[f32]) -> f32 {
    let (mut num, mut den) = (0.0f64, 0.0f64);
    for (x, y) in a.iter().zip(b) {
        num += ((x - y) as f64).powi(2);
        den += (*x as f64).powi(2);
    }
    (num / den.max(1e-12)).sqrt() as f32
}

fn main() {
    let (hidden, inter) = (512usize, 256usize); // light expert; kept small per thermal budget
    // f32 reference expert.
    let gate = structured(hidden, inter, 1);
    let up = structured(hidden, inter, 2);
    let down = structured(inter, hidden, 3);
    let f32_expert = ExpertWeights {
        hidden, inter,
        gate: Tensor::F32(gate.clone()),
        up: Tensor::F32(up.clone()),
        down: Tensor::F32(down.clone()),
    };

    let mut rx = lcg(42);
    let x: Vec<f32> = (0..hidden).map(|_| rx()).collect();
    let cpu = CpuBackend;
    let mut sc = Scratch::new(hidden, inter);
    let mut y_ref = vec![0.0f32; hidden];
    cpu.compute_expert(&f32_expert, &x, &mut sc, &mut y_ref);

    let f32_bytes = (gate.len() + up.len() + down.len()) * 4;
    println!("\nGreen Compress vs engine quant tiers — structured expert (hidden {hidden}, inter {inter})");
    println!("f32 reference: {:.1} KiB\n", f32_bytes as f64 / 1024.0);
    println!("  {:<16} {:>9} {:>8} {:>12} {:>11} {:>10} {:>10}",
             "method", "KiB", "vs f32", "weight-drift", "out-relL2", "out-max", "recon-us");

    // Per-method: (label, three compressed tensors as f32 vecs + stored bytes + reconstruct time).
    // Engine tiers reconstruct via Tensor::to_f32_vec; Green via GreenTensor::to_f32_vec.
    let run = |label: &str,
               g: &dyn Fn() -> (Vec<f32>, Vec<f32>, Vec<f32>, usize)| {
        let t0 = Instant::now();
        let (dg, du, dd, bytes) = g();
        let recon_us = t0.elapsed().as_secs_f64() * 1e6;
        // fidelity of the reconstructed weights vs original
        let wd = (rel_l2(&gate, &dg) + rel_l2(&up, &du) + rel_l2(&down, &dd)) / 3.0;
        // end-to-end through the real backend
        let e = ExpertWeights {
            hidden, inter,
            gate: Tensor::F32(dg), up: Tensor::F32(du), down: Tensor::F32(dd),
        };
        let mut y = vec![0.0f32; hidden];
        let mut sc2 = Scratch::new(hidden, inter);
        cpu.compute_expert(&e, &x, &mut sc2, &mut y);
        let out_rel = rel_l2(&y_ref, &y);
        let out_max = y_ref.iter().zip(&y).map(|(a, b)| (a - b).abs()).fold(0.0f32, f32::max);
        println!("  {:<16} {:>9.1} {:>7.2}x {:>12.2e} {:>11.2e} {:>10.2e} {:>10.0}",
                 label, bytes as f64 / 1024.0, f32_bytes as f64 / bytes as f64,
                 wd, out_rel, out_max, recon_us);
    };

    // engine Q8Ch (per-output-channel int8) — out = inter for gate/up, hidden for down.
    run("engine Q8Ch", &|| {
        let g = Tensor::quantize_q8_ch(&gate, inter);
        let u = Tensor::quantize_q8_ch(&up, inter);
        let d = Tensor::quantize_q8_ch(&down, hidden);
        (g.to_f32_vec(), u.to_f32_vec(), d.to_f32_vec(), g.bytes() + u.bytes() + d.bytes())
    });
    // engine Q4Ch (per-output-channel int4)
    run("engine Q4Ch", &|| {
        let g = Tensor::quantize_q4_ch(&gate, inter);
        let u = Tensor::quantize_q4_ch(&up, inter);
        let d = Tensor::quantize_q4_ch(&down, hidden);
        (g.to_f32_vec(), u.to_f32_vec(), d.to_f32_vec(), g.bytes() + u.bytes() + d.bytes())
    });
    // Green Q8 only (no repair) — apples-to-apples vs engine Q8Ch (block-scaled instead of per-channel)
    run("green Q8", &|| {
        let g = GreenTensor::compress(&gate, hidden, inter, 64, 0, 0.0);
        let u = GreenTensor::compress(&up, hidden, inter, 64, 0, 0.0);
        let d = GreenTensor::compress(&down, inter, hidden, 64, 0, 0.0);
        (g.to_f32_vec(), u.to_f32_vec(), d.to_f32_vec(), g.bytes() + u.bytes() + d.bytes())
    });
    // Green_optimal: Q8 + rank-16 low-rank + 0.5% sparse repair
    run("green_q8+repair", &|| {
        let g = GreenTensor::compress(&gate, hidden, inter, 64, 16, 0.005);
        let u = GreenTensor::compress(&up, hidden, inter, 64, 16, 0.005);
        let d = GreenTensor::compress(&down, inter, hidden, 64, 16, 0.005);
        (g.to_f32_vec(), u.to_f32_vec(), d.to_f32_vec(), g.bytes() + u.bytes() + d.bytes())
    });
    // Green Q4 base only — small but lossy (the row engine Q4Ch also fails on)
    run("green Q4", &|| {
        let g = GreenTensor::compress_q4(&gate, hidden, inter, 64, 0, 0.0);
        let u = GreenTensor::compress_q4(&up, hidden, inter, 64, 0, 0.0);
        let d = GreenTensor::compress_q4(&down, inter, hidden, 64, 0, 0.0);
        (g.to_f32_vec(), u.to_f32_vec(), d.to_f32_vec(), g.bytes() + u.bytes() + d.bytes())
    });
    // Green_optimal on Q4: repair RESCUES int4 — the smaller-without-quality-loss sweet spot
    run("green_q4+repair", &|| {
        let g = GreenTensor::compress_q4(&gate, hidden, inter, 64, 16, 0.01);
        let u = GreenTensor::compress_q4(&up, hidden, inter, 64, 16, 0.01);
        let d = GreenTensor::compress_q4(&down, inter, hidden, 64, 16, 0.01);
        (g.to_f32_vec(), u.to_f32_vec(), d.to_f32_vec(), g.bytes() + u.bytes() + d.bytes())
    });

    println!("\nLower weight-drift / out-relL2 = higher quality. `vs f32` = compression ratio.");
    println!("Repair (green_optimal) should beat plain Q8 on fidelity at a comparable size —");
    println!("the RAM/VRAM the resident working set costs drops without giving up quality.");

    // --- Green paged-from-disk + async prefetch + multi-session demo ---------------------------
    use engine_core::green::{GreenBase, GreenPagedStore};
    use engine_core::paged::{dense_provider, Prefetcher};
    use std::sync::Arc;

    let (layers, experts, dh, di, k) = (4usize, 16usize, 256usize, 128usize, 6usize);
    let store = engine_core::WeightStore::synthetic(layers, experts, dh, di, false, 5);
    let path = std::env::temp_dir().join(format!("ge_green_demo_{}.bin", std::process::id()));
    GreenPagedStore::create(&path, &store, GreenBase::Q8, 64, 0, 0.0).unwrap();
    let disk = Arc::new(GreenPagedStore::open(&path, 6).unwrap()); // 6 decoded/layer « 16
    let f32_model = (layers * experts * 3 * dh * di * 4) as f64;
    println!("\nGreen paged from disk — model {} experts × {} layers ({}×{}):", experts, layers, dh, di);
    println!("  on-disk {:.1} KiB vs f32 {:.1} KiB  ({:.1}× smaller); decoded RAM cap 6/layer",
             disk.disk_bytes() as f64 / 1024.0, f32_model / 1024.0, f32_model / disk.disk_bytes() as f64);

    // Async prefetch: warm the experts the next step needs while nothing else runs.
    let pf = Prefetcher::new(Arc::clone(&disk));
    pf.request(0, (0..experts as u16).collect());
    drop(pf); // join → warmed
    println!("  after prefetch(layer 0, all): {} decoded (cold reads), 0 during compute expected",
             disk.metrics().decodes);

    // Multi-session: 3 sessions share ONE Arc<store> concurrently, each its own input.
    let mut xseed = 1u64;
    let handles: Vec<_> = (0..3).map(|s| {
        let disk = Arc::clone(&disk);
        xseed = xseed.wrapping_add(0x9E37);
        let seed0 = xseed;
        std::thread::spawn(move || {
            let cpu = CpuBackend;
            let mut sd = seed0;
            let x: Vec<f32> = (0..dh).map(|_| { sd ^= sd >> 12; sd ^= sd << 25; sd ^= sd >> 27;
                (sd.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 }).collect();
            let chosen: Vec<u16> = (0..k as u16).collect();
            let gates = vec![1.0f32 / k as f32; k];
            let mut acc = 0.0f32;
            for layer in 0..layers {
                let o = dense_provider(&*disk, &cpu, layer, &x, &chosen, &gates);
                acc += o.iter().sum::<f32>();
            }
            (s, acc)
        })
    }).collect();
    for h in handles {
        let (s, acc) = h.join().unwrap();
        println!("  session {s}: ran {layers} layers over shared disk store, out-sum {acc:.3}");
    }
    let m = disk.metrics();
    println!("  shared store: {} decodes, {} decode-hits, peak resident {} (cap {})",
             m.decodes, m.decode_hits, m.peak_resident_experts, 6 * layers);
    let _ = std::fs::remove_file(&path);
}
