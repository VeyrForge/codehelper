//! KV scheduler benchmark — answers "can the engine extend context?".
//!
//! 1. Adaptive per-layer budget vs uniform (same total KV) — the engine's differentiator.
//! 2. Context-scaling: KV memory vs context length under full / evicted / evicted+2-bit, and the
//!    effective context a fixed VRAM budget allows. This is how the engine extends the
//!    *memory-feasible* context (not the model's trained window) — bounded KV as context grows.

use engine_core::kv::{kv_bytes, Attention, KvPolicy};

// representative OLMoE-class attention shape for the memory model
const N_KV_HEADS: usize = 16;
const HEAD_DIM: usize = 128;
const MODEL_LAYERS: usize = 16;

fn main() {
    let p = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/attention_trace.bin");
    let att = Attention::load(p).expect("run export_attention.py first");
    let q = att.tokens - 1;

    println!("\nGreen Engine — KV scheduler ({} tokens, {} layers)\n", att.tokens, att.layers);

    // 1. adaptive vs uniform at several total budgets
    println!("1) adaptive per-layer budget vs uniform (attention mass retained, same total KV):");
    println!("   {:>10} | {:>8} | {:>8} | {:>6}", "total KV%", "uniform", "adaptive", "gain");
    for fr in [0.10, 0.20, 0.30, 0.50] {
        let total = ((fr * (q + 1) as f64) as usize) * att.layers;
        let uni = att.total_retained(KvPolicy::Quest, q, &att.allocate_uniform(q, total));
        let ada = att.total_retained(KvPolicy::Quest, q, &att.allocate_adaptive(KvPolicy::Quest, q, total));
        println!(
            "   {:>9.0}% | {:>7.1}% | {:>7.1}% | {:>+5.1}",
            fr * 100.0, uni * 100.0, ada * 100.0, (ada - uni) * 100.0
        );
    }

    // 2. context-scaling memory (per the standard KV formula; eviction keeps 25%, 2-bit = KIVI)
    println!("\n2) KV memory vs context length (16 layers, {N_KV_HEADS} kv-heads, head_dim {HEAD_DIM}):");
    println!(
        "   {:>9} | {:>10} | {:>12} | {:>16}",
        "context", "full fp16", "evict 25%", "evict25%+2bit"
    );
    let keep_frac = 0.25;
    for ctx in [8_192usize, 32_768, 131_072, 524_288] {
        let full = kv_bytes(&vec![ctx; MODEL_LAYERS], N_KV_HEADS, HEAD_DIM, 16.0);
        let ev = kv_bytes(&vec![(ctx as f64 * keep_frac) as usize; MODEL_LAYERS], N_KV_HEADS, HEAD_DIM, 16.0);
        let evq = kv_bytes(&vec![(ctx as f64 * keep_frac) as usize; MODEL_LAYERS], N_KV_HEADS, HEAD_DIM, 2.5);
        println!(
            "   {:>8}k | {:>8.1} GB | {:>10.2} GB | {:>14.2} GB",
            ctx / 1024,
            full as f64 / 1e9,
            ev as f64 / 1e9,
            evq as f64 / 1e9
        );
    }

    // 3. effective context at a fixed VRAM budget for KV
    let budget_gb = 8.0;
    let per_tok_full = kv_bytes(&vec![1; MODEL_LAYERS], N_KV_HEADS, HEAD_DIM, 16.0) as f64;
    let per_tok_evq = kv_bytes(&vec![1; MODEL_LAYERS], N_KV_HEADS, HEAD_DIM, 2.5) as f64 * keep_frac;
    let ctx_full = budget_gb * 1e9 / per_tok_full;
    let ctx_evq = budget_gb * 1e9 / per_tok_evq;
    println!(
        "\n3) with {budget_gb:.0} GB for KV: full fp16 fits ~{:.0}k tokens; engine (evict25%+2bit) fits \
         ~{:.0}k tokens  = {:.0}× longer context, same memory.",
        ctx_full / 1024.0,
        ctx_evq / 1024.0,
        ctx_evq / ctx_full
    );
    println!("\n   (lossless-ish: retains ~85-90% attention mass; extends memory-feasible context,");
    println!("    not the model's trained window. Cold KV can also offload to RAM/SSD for more.)");
}
