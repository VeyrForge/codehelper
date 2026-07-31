//! Persistence tier — prefix/KV cache (RadixAttention / LMCache style). Reuse KV for any token
//! prefix already seen (same turn history or same shared system prompt), prefilling only the new
//! suffix. Lossless, model-agnostic, workload-driven. Composes with `kv.rs` (evict/quantize within
//! a reused prefix) and offload (cold prefixes → RAM/SSD).
//!
//! Block-based: KV is cached per block of `block` tokens; a block's key is the rolling hash of the
//! whole prefix up to it, so two sequences share a block only if their entire prefix matches.

use std::collections::HashSet;

pub struct PrefixCache {
    block: usize,
    blocks: HashSet<u64>,
}

#[derive(Clone, Copy, Debug, Default)]
pub struct Reuse {
    pub reused_tokens: usize,
    pub new_tokens: usize,
}

impl PrefixCache {
    pub fn new(block_size: usize) -> Self {
        PrefixCache { block: block_size.max(1), blocks: HashSet::new() }
    }

    /// Prefill `tokens`: reuse the longest cached block-prefix, insert the new blocks, and report
    /// how many tokens were reused vs recomputed.
    pub fn admit(&mut self, tokens: &[u32]) -> Reuse {
        let mut h: u64 = 0xcbf29ce484222325; // FNV-1a seed, carried across blocks = prefix hash
        let mut reused = 0usize;
        let mut diverged = false;
        let n_blocks = tokens.len().div_ceil(self.block);
        for b in 0..n_blocks {
            let start = b * self.block;
            let end = (start + self.block).min(tokens.len());
            for &t in &tokens[start..end] {
                h ^= t as u64;
                h = h.wrapping_mul(0x100000001b3);
            }
            if !diverged && self.blocks.contains(&h) {
                reused += end - start;
            } else {
                diverged = true; // once a block is new, every later block is new (prefix hash)
                self.blocks.insert(h);
            }
        }
        Reuse { reused_tokens: reused, new_tokens: tokens.len() - reused }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Re-prefilling a grown conversation reuses the previous turn's tokens (the persistence win).
    #[test]
    fn reuses_growing_conversation() {
        let mut c = PrefixCache::new(16);
        let turn1: Vec<u32> = (0..100).collect();
        let turn2: Vec<u32> = (0..180).collect(); // turn1 + 80 new tokens
        let r1 = c.admit(&turn1);
        let r2 = c.admit(&turn2);
        assert_eq!(r1.reused_tokens, 0);
        // turn2 reuses turn1's whole blocks (96 of 100), only the suffix is new
        assert!(r2.reused_tokens >= 96, "reused {} of 180", r2.reused_tokens);
        assert!(r2.new_tokens <= 84);
    }

    /// Two conversations sharing a system prompt reuse it (cross-request sharing).
    #[test]
    fn shares_system_prompt_across_conversations() {
        let mut c = PrefixCache::new(16);
        let sys: Vec<u32> = (0..64).collect();
        let mut a = sys.clone();
        a.extend(1000..1050);
        let mut b = sys.clone();
        b.extend(2000..2050);
        c.admit(&a);
        let rb = c.admit(&b); // should reuse the 64-token shared system prompt
        assert!(rb.reused_tokens >= 64, "reused {} (expected ≥64 shared)", rb.reused_tokens);
    }
}
