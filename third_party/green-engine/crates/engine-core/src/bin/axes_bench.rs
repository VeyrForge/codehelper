//! Performance deltas for the two new axes: persistence (prefix cache) and time (batching).
//! Pure scheduling/structural models — no model run.

use engine_core::batching::{static_vs_continuous, DecodeCost};
use engine_core::prefix::PrefixCache;

fn main() {
    // ---------- Persistence axis: prefix/KV reuse across a multi-turn, multi-conversation workload
    println!("\n=== Persistence axis — prefix/KV cache ===\n");
    let sys_len = 200; // shared system prompt tokens
    let block = 16;
    for &(convos, turns) in &[(1usize, 4usize), (1, 20), (8, 10)] {
        let mut cache = PrefixCache::new(block);
        let (mut without, mut with) = (0u64, 0u64);
        let mut next_tok = 10_000u32;
        for _c in 0..convos {
            let mut conv: Vec<u32> = (0..sys_len).collect(); // shared system prompt (ids 0..200)
            for t in 0..turns {
                // each turn appends ~70 user + ~120 assistant tokens (unique per turn/convo)
                for _ in 0..190 {
                    conv.push(next_tok);
                    next_tok += 1;
                }
                let _ = t;
                without += conv.len() as u64; // no cache: re-prefill the whole conversation
                with += cache.admit(&conv).new_tokens as u64; // cache: only the new suffix
            }
        }
        println!(
            "  {convos} convo(s) × {turns} turns: prefill WITHOUT {:>7} tok  WITH {:>6} tok  -> {:>5.1}× less",
            without, with, without as f64 / with as f64
        );
    }

    // ---------- Time axis: batching throughput + continuous vs static occupancy
    println!("\n=== Time axis — multi-request batching ===\n");
    let cost = DecodeCost { base_ms: 4.0, slope_ms: 0.05 }; // memory-bound decode (labeled model)
    println!("  decode throughput (memory-bound: base {:.0}ms/step, slope {:.2}ms/req):", cost.base_ms, cost.slope_ms);
    for b in [1usize, 4, 8, 16, 32, 64] {
        println!(
            "    batch {:>2}: {:>7.0} tok/s   ({:>4.1}× vs sequential)",
            b, cost.throughput(b), cost.batching_speedup(b)
        );
    }
    // continuous vs static on a realistic variable-length request mix
    let gens: Vec<usize> = (0..64).map(|i| if i % 5 == 0 { 400 } else { 40 + (i * 7) % 80 }).collect();
    let (s, c) = static_vs_continuous(&gens, 8);
    println!(
        "\n  64 variable-length requests, 8 slots: static {s} steps vs continuous {c} steps  -> {:.2}× faster, \
         {:.0}% less idle",
        s as f64 / c as f64,
        (1.0 - c as f64 / s as f64) * 100.0
    );
    println!("\n  (both lossless; persistence is model-agnostic, batching needs the multi-tenant loop.)");
}
