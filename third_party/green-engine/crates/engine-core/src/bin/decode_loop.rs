//! INTEGRATION SKELETON — assembles the validated tiers into one decode loop that actually runs
//! and emits tokens: prefix cache (persistence) → per layer { MoE route + scheduled compute
//! (residency cache) + KV append/recall } → next token. Synthetic weights (so this is the *wiring*,
//! not a quality run); swapping in real weights + ggml kernels + real attention is the remaining work.

use std::time::Instant;

use engine_core::backend::{CpuBackend, ExpertBackend, Scratch};
use engine_core::cache::{Eviction, LayerCache};
use engine_core::kv::{KvStore, RamKvStore, F16};
use engine_core::prefix::PrefixCache;
use engine_core::weights::WeightStore;

fn main() {
    let (layers, experts, hidden, inter, top_k) = (8usize, 64usize, 256usize, 512usize, 8usize);
    let store = WeightStore::synthetic(layers, experts, hidden, inter, false, 1);
    let backend = CpuBackend;
    let mut scratch = Scratch::new(hidden, inter);

    // engine tiers
    let cache_cap = 24; // resident experts/layer
    let mut expert_cache: Vec<LayerCache> =
        (0..layers).map(|_| LayerCache::new(cache_cap, experts, Eviction::Lru)).collect();
    let mut prefix = PrefixCache::new(16);
    // KV cache: dim = n_kv_heads * head_dim (synthetic stub width).
    let kv_dim = 32usize;
    let mut kv = RamKvStore::new(layers, kv_dim);

    // "prompt" (prefill via prefix cache) then decode
    let prompt: Vec<u32> = (0..96).collect();
    let reuse = prefix.admit(&prompt);
    let prompt2 = prompt.clone(); // a second turn reuses the prefix
    let reuse2 = prefix.admit(&{
        let mut v = prompt2;
        v.extend(500..540);
        v
    });

    let mut x = vec![0.05f32; hidden];
    let mut tmp = vec![0.0f32; hidden];
    let n_decode = 64;
    let (mut req, mut hit, mut clock) = (0u64, 0u64, 0u64);
    let mut tokens = Vec::new();

    let t0 = Instant::now();
    for _step in 0..n_decode {
        let t = kv.seq_len();
        for l in 0..layers {
            // route: top_k experts by a cheap score of x (synthetic gate)
            let mut score: Vec<(f32, u16)> = (0..experts as u16)
                .map(|e| (x[(e as usize) % hidden] * ((e as f32) + 1.0).sin(), e))
                .collect();
            score.sort_by(|a, b| b.0.partial_cmp(&a.0).unwrap());
            // schedule residency + compute each expert, accumulate FFN (MoE), add residual
            for &(_, e) in score.iter().take(top_k) {
                req += 1;
                clock += 1;
                if expert_cache[l].contains(e) {
                    hit += 1;
                } else {
                    expert_cache[l].admit(e, clock, None);
                }
                expert_cache[l].touch(e, clock);
                backend.compute_expert(store.get(l, e), &x, &mut scratch, &mut tmp);
                for o in 0..hidden {
                    x[o] += tmp[o] / top_k as f32; // residual + weighted expert sum
                }
            }
            // KV append + recall (attention math is a separate agent; attend() is a stub).
            let mut key = vec![0u16; kv_dim];
            let mut value = vec![0u16; kv_dim];
            for i in 0..kv_dim {
                key[i] = (x[i % hidden].to_bits() as F16).wrapping_add((l * kv_dim + i) as u16);
                value[i] = (tmp[i % hidden].to_bits() as F16).wrapping_add(t as u16);
            }
            kv.append(l, t, &key, &value);
            let past = kv.recall(l, 0..kv.seq_len());
            let q = key; // synthetic query == current key
            let _ = kv.attend(l, &q, 0..past.tokens);
        }
        // next token = argmax of x (synthetic head); feed a hash back to keep it moving
        let next = x.iter().enumerate().max_by(|a, b| a.1.partial_cmp(b.1).unwrap()).unwrap().0;
        tokens.push(next as u32);
        x[next % hidden] *= 0.5; // perturb so it doesn't fixpoint
    }
    let secs = t0.elapsed().as_secs_f64();

    println!("\n=== Green Engine — integrated decode loop (skeleton) ===\n");
    println!("  model: {layers} layers, {experts} experts, hidden {hidden}, top-{top_k} (synthetic)");
    println!(
        "  prefill via prefix cache: turn1 reused {}/{} tok, turn2 reused {}/{} tok",
        reuse.reused_tokens,
        reuse.reused_tokens + reuse.new_tokens,
        reuse2.reused_tokens,
        reuse2.reused_tokens + reuse2.new_tokens
    );
    println!(
        "  decoded {} tokens in {:.3}s ({:.0} tok/s synthetic)",
        tokens.len(),
        secs,
        n_decode as f64 / secs
    );
    println!(
        "  expert cache hit rate: {:.1}% (cap {cache_cap}/{experts})",
        hit as f64 / req as f64 * 100.0
    );
    println!(
        "  KV cache: {} layers × {} tokens × dim {} (RamKvStore append/recall)",
        kv.layers(),
        kv.seq_len(),
        kv.dim()
    );
    println!("\n  All four tiers ran in one loop: persistence + width + memory + compute.");
}
