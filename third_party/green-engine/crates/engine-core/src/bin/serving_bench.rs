//! Performance deltas for the three 2026 serving techniques: chunked prefill, multi-token
//! prediction, and prefill/decode disaggregation. Cost models (labeled) — shapes are the claim.

use engine_core::serving::{decode_stall, decode_throughput_colocated, decode_throughput_disaggregated, mtp};

fn main() {
    println!("\n=== 1. Chunked prefill — bound decode stall from a long prompt ===\n");
    println!("  a {} -token prefill arrives while others are decoding (0.05 ms/prefill-tok):", 2048);
    println!("  {:>12} | {:>16} | {:>16}", "chunk size", "worst decode stall", "vs unchunked");
    let (unchunked, _) = decode_stall(2048, 2048, 0.05, 0.5);
    for chunk in [2048usize, 512, 256, 128] {
        let (_, with) = decode_stall(2048, chunk, 0.05, 0.5);
        let tag = if chunk == 2048 { "(no chunking)" } else { "" };
        println!("  {:>12} | {:>13.1} ms | {:>13.1}× less {tag}", chunk, with, unchunked / with);
    }

    println!("\n=== 2. Multi-token prediction — emit several tokens per step ===\n");
    println!("  memory-bound decode (4 ms/step, +0.1 ms/extra token):");
    println!("  {:>10} | {:>12} | {:>16}", "accept p", "tokens/step", "decode speedup");
    for &p in &[0.5, 0.7, 0.85] {
        let (acc, sp) = mtp(3, p, 4.0, 0.1); // propose 3 extra tokens
        println!("  {:>10.2} | {:>12.2} | {:>14.2}×", p, acc, sp);
    }

    println!("\n=== 3. Disaggregation — separate prefill (compute-bound) from decode (memory-bound) ===\n");
    let rate = 100.0; // decode tokens/sec at full device
    println!("  {:>20} | {:>14} | {:>10}", "prefill share of dev", "co-located", "disagg");
    for &frac in &[0.2, 0.4, 0.6] {
        let co = decode_throughput_colocated(rate, frac);
        let dis = decode_throughput_disaggregated(rate);
        println!("  {:>19.0}% | {:>9.0} tok/s | {:>6.0} tok/s  ({:.2}× decode)", frac * 100.0, co, dis, dis / co);
    }

    println!("\n  All three compose with the existing batching + prefix-cache + KV tiers.");
    println!("  Models (labeled); the shapes — chunking bounds stall, MTP ~2-3×, disagg removes interference — are robust.");
}
