//! Time axis — multi-request scheduling. Two effects, both modeled with simple cost laws:
//!  (1) Decode is MEMORY-BOUND: a step streams the weights once and applies them to the whole
//!      batch, so step cost ≈ base + slope·batch with base ≫ slope ⇒ batching ≈ B× throughput.
//!  (2) CONTINUOUS batching admits a new request the instant a slot frees, vs STATIC batching that
//!      waits for the whole batch to finish ⇒ higher occupancy on variable-length requests.

/// Per-decode-step cost (ms): `base` = weight streaming (paid once/step), `slope` = per-request compute.
#[derive(Clone, Copy)]
pub struct DecodeCost {
    pub base_ms: f64,
    pub slope_ms: f64,
}

impl DecodeCost {
    pub fn step_ms(&self, batch: usize) -> f64 {
        self.base_ms + self.slope_ms * batch as f64
    }
    /// Tokens per second at a given batch size.
    pub fn throughput(&self, batch: usize) -> f64 {
        batch as f64 / self.step_ms(batch) * 1000.0
    }
    /// Speedup of `batch` vs sequential (batch 1).
    pub fn batching_speedup(&self, batch: usize) -> f64 {
        self.throughput(batch) / self.throughput(1)
    }
}

/// Makespan (in decode steps) to finish all requests under a concurrency cap, comparing static
/// batching (wait for the whole wave) vs continuous batching (refill freed slots immediately).
pub fn static_vs_continuous(gen_lens: &[usize], slots: usize) -> (u64, u64) {
    // static: process in waves of `slots`; each wave takes max(gen_len) steps.
    let mut static_steps = 0u64;
    for wave in gen_lens.chunks(slots) {
        static_steps += *wave.iter().max().unwrap_or(&0) as u64;
    }
    // continuous: greedily assign each request to the slot that frees earliest (list scheduling).
    let mut slot_end = vec![0u64; slots];
    let mut order = gen_lens.to_vec();
    order.sort_unstable_by(|a, b| b.cmp(a)); // longest-first balances best
    for g in order {
        let i = (0..slots).min_by_key(|&s| slot_end[s]).unwrap();
        slot_end[i] += g as u64;
    }
    let continuous_steps = *slot_end.iter().max().unwrap();
    (static_steps, continuous_steps)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn batching_helps_when_memory_bound() {
        let c = DecodeCost { base_ms: 4.0, slope_ms: 0.05 }; // base ≫ slope
        assert!(c.batching_speedup(32) > 10.0, "speedup {}", c.batching_speedup(32));
        // diminishing returns appear as compute starts to dominate
        assert!(c.throughput(64) > c.throughput(32));
    }

    #[test]
    fn continuous_beats_static_on_variable_lengths() {
        // mixed short/long requests waste static slots; continuous refills them
        let gens = vec![400, 50, 60, 40, 380, 45, 55, 50, 410, 48, 52, 47];
        let (s, c) = static_vs_continuous(&gens, 4);
        assert!(c < s, "continuous {c} should beat static {s}");
    }
}
