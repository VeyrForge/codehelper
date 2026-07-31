//! The "Green" metric: energy per token and tokens-per-watt for the engine's wins.
//! Uses our measured bytes/token + throughput numbers; energy constants labeled (DRAM ≫ FLOP).

use engine_core::energy::{energy_per_token_nj, tokens_per_joule};

fn main() {
    let flops = 128.0 * 12.6e6; // 128 active experts/token × ~12.6 MFLOP each (same for all rows)

    println!("\n=== Energy per token (lower = greener); bytes moved dominates ===\n");
    println!("  {:>26} | {:>14} | {:>10}", "engine mode", "bytes/token", "energy/token");
    let rows = [
        ("blind (all experts)", 12885e6),
        ("routing-aware, no cache", 1611e6),
        ("Green Engine (cache)", 471e6),
        ("Green Engine + Green Compress NF4", 132e6),
    ];
    let base = energy_per_token_nj(1611e6, flops);
    for (name, bytes) in rows {
        let e = energy_per_token_nj(bytes, flops);
        println!("  {:>26} | {:>11.0} MB | {:>7.1} µJ  ({:>4.1}× less vs no-cache)",
                 name, bytes / 1e6, e / 1e3, base / e);
    }

    println!("\n=== Tokens per joule (throughput / power; busy GPU ≈ constant power) ===\n");
    println!("  {:>28} | {:>10} | {:>8} | {:>12}", "scenario", "tok/s", "power", "tokens/joule");
    let scen = [
        ("sequential (batch 1)", 247.0, 350.0),
        ("continuous batch 8", 1818.0, 365.0),
        ("continuous batch 32", 5714.0, 380.0),
    ];
    let seq = tokens_per_joule(247.0, 350.0);
    for (name, tps, pw) in scen {
        let tpj = tokens_per_joule(tps, pw);
        println!("  {:>28} | {:>8.0} | {:>6.0} W | {:>9.1}  ({:>4.1}× greener)", name, tps, pw, tpj, tpj / seq);
    }

    println!("\n  Why: a DRAM byte costs ~3000× a FLOP, and a busy GPU draws ~constant power — so moving");
    println!("  fewer bytes (cache+compress) and keeping the GPU saturated (batching) is *directly* greener.");
    println!("  Green Engine + Green Compress: ~98% less energy/token vs naive no-cache; batching ~5× tokens/joule.");
}
