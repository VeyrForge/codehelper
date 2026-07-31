//! Cross-hardware projection: "does the engine help on devices other than this PC?"
//!
//! HONEST METHOD. The engine's win is set by one ratio: how much costlier is getting an expert
//! than computing it. That depends on the device (compute TFLOPS, offload bandwidth, VRAM) — but
//! the CACHE HIT-RATE vs resident-fraction is a property of the MODEL's routing, which we measured
//! once on the real OLMoE trace. So we hold the measured hit-rate curve fixed and plug in each
//! device's published specs. These are PROJECTIONS (spec-sheet inputs, the OLMoE routing curve
//! assumed representative of other MoEs) — run `portable_bench` on your box for measured numbers.

use engine_core::{Config, Engine, Trace};

// Projected large MoE (~48 GB in Q4), the regime where offload matters.
const HIDDEN: f64 = 4096.0;
const INTER: f64 = 1024.0;
const LAYERS: f64 = 48.0;
const N_EXPERTS: f64 = 160.0;
const TOP_K: f64 = 8.0;
const EXPERT_MACS: f64 = 3.0 * HIDDEN * INTER; // gate+up+down
const EXPERT_FLOPS: f64 = 2.0 * EXPERT_MACS;
const EXPERT_BYTES_Q4: f64 = 3.0 * HIDDEN * INTER * 0.5; // 4 bits/param
const TOTAL_EXPERT_BYTES: f64 = LAYERS * N_EXPERTS * EXPERT_BYTES_Q4;
const ACTIVE: f64 = TOP_K * LAYERS; // expert computes per token
const HOST_CPU_TFLOPS: f64 = 1.5; // many-core host CPU, recompute fallback

struct Dev {
    name: &'static str,
    tflops_f16: f64,
    offload_gbps: f64, // host->device (PCIe) or unified-memory bw
    vram_gb: f64,
    unified: bool,
}

const DEVICES: &[Dev] = &[
    Dev { name: "RTX 3060 12GB",     tflops_f16: 25.0,  offload_gbps: 26.0,  vram_gb: 12.0, unified: false },
    Dev { name: "RTX 4060Ti 16GB",   tflops_f16: 22.0,  offload_gbps: 13.0,  vram_gb: 16.0, unified: false },
    Dev { name: "RTX 4090 24GB",     tflops_f16: 150.0, offload_gbps: 26.0,  vram_gb: 24.0, unified: false },
    Dev { name: "RTX 5090 32GB",     tflops_f16: 210.0, offload_gbps: 50.0,  vram_gb: 32.0, unified: false },
    Dev { name: "AMD 7900XTX 24GB",  tflops_f16: 120.0, offload_gbps: 26.0,  vram_gb: 24.0, unified: false },
    Dev { name: "RTX A6000 48GB",    tflops_f16: 75.0,  offload_gbps: 26.0,  vram_gb: 48.0, unified: false },
    Dev { name: "A100 80GB",         tflops_f16: 156.0, offload_gbps: 26.0,  vram_gb: 80.0, unified: false },
    Dev { name: "Apple M2 Max 96GB", tflops_f16: 13.0,  offload_gbps: 400.0, vram_gb: 96.0, unified: true },
];

/// Green-engine hit-rate as a function of resident fraction, from the REAL trace.
fn hit_curve(trace: &Trace) -> Vec<(f64, f64)> {
    let mut pts = Vec::new();
    for b in [8usize, 10, 12, 16, 20, 24, 32, 40, 48, 56, 60] {
        if b > trace.experts {
            break;
        }
        let cap = if b > 4 { b - 4 } else { b };
        let hit = Engine::new(Config::full(cap, b - cap), trace).run(trace, 1).hit_rate();
        pts.push((b as f64 / trace.experts as f64, hit));
    }
    pts
}

fn interp(pts: &[(f64, f64)], x: f64) -> f64 {
    if x <= pts[0].0 {
        return pts[0].1;
    }
    if x >= pts[pts.len() - 1].0 {
        return pts[pts.len() - 1].1;
    }
    for w in pts.windows(2) {
        let (x0, y0) = w[0];
        let (x1, y1) = w[1];
        if x >= x0 && x <= x1 {
            return y0 + (y1 - y0) * (x - x0) / (x1 - x0);
        }
    }
    pts[pts.len() - 1].1
}

fn main() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/expert_trace.bin");
    let trace = Trace::load(path).expect("export_trace.py first");
    let curve = hit_curve(&trace);

    let compute_ms = EXPERT_FLOPS / 1e9; // per expert at 1 TFLOPS → ms; divide by tflops below
    let cpu_ms = compute_ms / HOST_CPU_TFLOPS;
    let xfer_ms = EXPERT_BYTES_Q4 / 1e6; // per expert at 1 GB/s → ms; divide by bw below

    println!("\nGreen Engine — cross-hardware PROJECTION (modeled from specs; not measured)\n");
    println!(
        "  projected model: ~{:.0} GB Q4 MoE ({} layers × {} experts, top-{}, {} active/token)",
        TOTAL_EXPERT_BYTES / 1e9, LAYERS as u32, N_EXPERTS as u32, TOP_K as u32, ACTIVE as u32
    );
    println!("  hit-rate curve: measured on real OLMoE trace, assumed representative.\n");
    println!(
        "{:>18} | {:>6} | {:>8} | {:>11} | {:>10} | {:>8}",
        "device", "VRAM", "resident", "llama.cpp", "Green Eng", "speedup"
    );
    println!("{}", "-".repeat(76));

    for d in DEVICES {
        let usable = d.vram_gb * 1e9 * 0.8; // leave room for KV/activations
        let frac = (usable / TOTAL_EXPERT_BYTES).min(1.0);
        if frac >= 1.0 {
            let note = if d.unified { "fits (unified mem)" } else { "fits in VRAM" };
            println!(
                "{:>18} | {:>4}GB | {:>8} | {:>11} | {:>10} | engine idle ({note})",
                d.name, d.vram_gb as u32, "100%", "—", "—"
            );
            continue;
        }
        let hit = interp(&curve, frac);
        let comp = compute_ms / d.tflops_f16;
        let miss_hybrid = (xfer_ms / d.offload_gbps).min(cpu_ms); // stream or recompute
        let green_per = hit * comp + (1.0 - hit) * miss_hybrid;
        let llama_per = frac * comp + (1.0 - frac) * cpu_ms; // static split, recompute cold on CPU
        let green = 1000.0 / (ACTIVE * green_per);
        let llama = 1000.0 / (ACTIVE * llama_per);
        println!(
            "{:>18} | {:>4}GB | {:>7.0}% | {:>8.0} t/s | {:>6.0} t/s | {:>6.2}x",
            d.name, d.vram_gb as u32, frac * 100.0, llama, green, green / llama
        );
    }
    println!("\n  Reading it: the engine helps most on consumer GPUs where the model exceeds VRAM");
    println!("  (12–32 GB). On 80 GB datacenter cards or unified-memory Macs the model fits, so the");
    println!("  engine idles — correctly. Quality identical in all cases (lossless).");
    println!("\n  For MEASURED numbers on your hardware: `cargo run --release --bin portable_bench`.");
}
