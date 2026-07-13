//! Mixed-precision allocation — best quality at a fixed RAM budget (GAMMA/HAWQ/CoopQ family).
//! Not every matrix needs the same bits: `allocate_mixed` spends the byte budget where it cuts error
//! most. This shows a mixed assignment beating the best *uniform* tier at the same total size.
//!
//!   cargo run -p engine-core --release --bin mixed_bench

use engine_core::{allocate_mixed, GreenTier};

fn rng(s: &mut u64) -> f32 {
    *s ^= *s >> 12; *s ^= *s << 25; *s ^= *s >> 27;
    (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
}

fn rel_l2(a: &[f32], b: &[f32]) -> f64 {
    let (mut n, mut d) = (0.0f64, 0.0f64);
    for (x, y) in a.iter().zip(b) { n += ((x - y) as f64).powi(2); d += (*x as f64).powi(2); }
    (n / d.max(1e-12)).sqrt()
}

// Matrices with DIFFERENT sensitivity: some smooth (quantize easily), some outlier-heavy (need bits).
fn matrix(inn: usize, out: usize, seed: u64, outlier_scale: f32) -> Vec<f32> {
    let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
    let mut w = vec![0.0f32; inn * out];
    for x in w.iter_mut() { *x = 0.3 * rng(&mut s); }
    for t in 0..(inn * out / 200).max(1) { w[(t * 2654435761) % (inn * out)] += outlier_scale; }
    w
}

fn main() {
    let (inn, out) = (256usize, 256usize);
    // 6 matrices spanning smooth → very outlier-heavy (different quantization sensitivity).
    let mats: Vec<Vec<f32>> = (0..6).map(|k| matrix(inn, out, k as u64 + 1, k as f32 * 1.2)).collect();

    // Candidate tiers (ascending size), evaluated per matrix: (bytes, error).
    let tiers = [
        GreenTier::Green3 { group: 64 },     // smallest
        GreenTier::GreenNf4 { group: 32 },
        GreenTier::Green6 { group: 32 },
        GreenTier::Green8,                    // largest (of these)
    ];
    let per_matrix: Vec<Vec<(usize, f64)>> = mats.iter().map(|w| {
        tiers.iter().map(|t| {
            let bytes = t.bytes(w.len(), out);
            let err = rel_l2(w, &t.reconstruct(w, inn, out));
            (bytes, err)
        }).collect()
    }).collect();

    let sum_err = |choice: &[usize]| -> f64 { choice.iter().enumerate().map(|(i, &c)| per_matrix[i][c].1).sum() };
    let sum_bytes = |choice: &[usize]| -> usize { choice.iter().enumerate().map(|(i, &c)| per_matrix[i][c].0).sum() };

    println!("\nMixed-precision allocation — best quality at a fixed RAM budget ({} matrices, {inn}×{out})\n", mats.len());
    println!("  {:<26} {:>10} {:>12}", "assignment", "KB total", "avg fidelity");

    // Baseline: every matrix on the same uniform tier.
    for (ti, t) in tiers.iter().enumerate() {
        let uniform = vec![ti; mats.len()];
        let kb = sum_bytes(&uniform) as f64 / 1024.0;
        let fid = 100.0 * (1.0 - sum_err(&uniform) / mats.len() as f64);
        println!("  {:<26} {:>7.1} KB {:>11.1}%", format!("uniform {}", t.name()), kb, fid);
    }

    // Mixed: give it the budget of the uniform NF4 assignment, let it reallocate.
    let budget = sum_bytes(&vec![1usize; mats.len()]); // uniform-NF4 byte budget
    let choice = allocate_mixed(&per_matrix, budget);
    let kb = sum_bytes(&choice) as f64 / 1024.0;
    let fid = 100.0 * (1.0 - sum_err(&choice) / mats.len() as f64);
    println!("  {:<26} {:>7.1} KB {:>11.1}%   <- same budget as uniform-nf4", "MIXED (allocate_mixed)", kb, fid);
    print!("  per-matrix tiers:");
    for &c in &choice { print!(" {}", tiers[c].name()); }
    println!("\n\nMixed precision spends the same bytes but assigns more bits to the outlier-heavy (sensitive)");
    println!("matrices and fewer to the smooth ones — higher average fidelity at the same RAM.");
}
