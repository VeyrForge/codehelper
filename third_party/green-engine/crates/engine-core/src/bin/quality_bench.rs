//! Quality-vs-size sweep across the engine's DEP-FREE weight tiers (runs on any old CPU, no GPU,
//! no green-compress dep). Priorities: quality and smallest memory first. Reports, per tier, the
//! rel-L2 drift vs f32 and bytes/weight, so you can pick the best quality-per-byte point.
//!
//!   cargo run -p engine-core --release --bin quality_bench

use engine_core::Tensor;

fn rel_l2(a: &[f32], b: &[f32]) -> f32 {
    let (mut n, mut d) = (0.0f64, 0.0f64);
    for (x, y) in a.iter().zip(b) { n += ((x - y) as f64).powi(2); d += (*x as f64).powi(2); }
    (n / d.max(1e-12)).sqrt() as f32
}

// Structured [in,out] weights: per-column bias + magnitude varying down the column + outliers —
// the regime where finer (group) scales beat one-per-column scales, like real transformer weights.
fn structured(inn: usize, out: usize, seed: u64) -> Vec<f32> {
    let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
    let mut rng = || { s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
        (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 };
    let bias: Vec<f32> = (0..out).map(|_| rng() * 1.5).collect();
    let mut w = vec![0.0f32; inn * out];
    for i in 0..inn {
        let mag = 0.15 + (i as f32 / inn as f32); // magnitude grows down each column
        for o in 0..out { w[i * out + o] = bias[o] + mag * rng(); }
    }
    for t in 0..(inn * out / 400).max(1) { w[(t * 2654435761) % (inn * out)] += 3.0; }
    w
}

fn main() {
    let (inn, out) = (1024usize, 512usize); // one weight matrix
    let w = structured(inn, out, 7);
    let n = w.len();
    let bpw = |t: &Tensor| t.bytes() as f64 / n as f64;

    // (label, tensor)
    let tiers: Vec<(String, Tensor)> = vec![
        ("Q8 per-tensor".into(), Tensor::quantize_q8(&w)),
        ("Q8Ch per-channel".into(), Tensor::quantize_q8_ch(&w, out)),
        ("Q4Ch per-channel".into(), Tensor::quantize_q4_ch(&w, out)),
        ("Q4G group=128".into(), Tensor::quantize_q4_group(&w, 128)),
        ("Q4G group=64".into(), Tensor::quantize_q4_group(&w, 64)),
        ("Q4G group=32".into(), Tensor::quantize_q4_group(&w, 32)),
        ("Q4G+MSEclip g=64".into(), Tensor::quantize_q4_group_clip(&w, 64)),
        ("Q4G+MSEclip g=32".into(), Tensor::quantize_q4_group_clip(&w, 32)),
        ("NF4 group=64".into(), Tensor::quantize_nf4_group(&w, 64)),
        ("NF4 group=32".into(), Tensor::quantize_nf4_group(&w, 32)),
        ("Q3G group=64".into(), Tensor::quantize_q3_group(&w, 64)),
        ("Q3G group=32".into(), Tensor::quantize_q3_group(&w, 32)),
    ];

    let kb = |t: &Tensor| t.bytes() as f64 / 1024.0;
    let f32_kb = n as f64 * 4.0 / 1024.0;
    println!("\nQuality vs size — dep-free weight tiers ({inn}×{out} weight matrix)\n");
    println!("  f32 reference: {f32_kb:.0} KB, fidelity 100.0%\n");
    println!("  {:<22} {:>9} {:>10} {:>12}", "tier", "KB", "vs f32", "fidelity");
    for (label, t) in &tiers {
        let fidelity = 100.0 * (1.0 - rel_l2(&w, &t.to_f32_vec()));
        println!("  {:<22} {:>7.0} KB {:>8.1}x {:>11.1}%", label, kb(t), 4.0 / bpw(t), fidelity);
    }
    println!("\nHigher fidelity % = closer to the original (100% = identical). Group-wise int4 (Q4G) gives");
    println!("the best low-bit quality-per-byte; Q3G is smallest. Dependency-free (old CPUs, no GPU).");
}
