//! Energy / tokens-per-watt — the "Green" metric the project is named for, and the one we never
//! measured. Hardware fact: moving a byte from DRAM/HBM costs ~100-1000× the energy of a FLOP, and a
//! busy GPU draws ~constant power regardless of utilization. So **bytes moved ≈ energy**, and the
//! engine's levers (cache, prefix reuse, batching) cut energy/token by cutting bytes moved and work.
//! Constants are labeled, order-of-magnitude representative; the ratios are the robust claim.

/// Energy moving `bytes` from DRAM + doing `flops`, in nanojoules.
pub fn energy_nj(bytes: f64, flops: f64) -> f64 {
    const DRAM_NJ_PER_BYTE: f64 = 1.5; // HBM/DRAM access, effective incl. overhead
    const COMPUTE_NJ_PER_FLOP: f64 = 0.0005; // ~0.5 pJ/FLOP — ~3000× cheaper than a DRAM byte
    bytes * DRAM_NJ_PER_BYTE + flops * COMPUTE_NJ_PER_FLOP
}

/// Energy per decoded token (nJ) given slow-tier bytes moved and active FLOPs that token.
pub fn energy_per_token_nj(bytes_moved: f64, flops: f64) -> f64 {
    energy_nj(bytes_moved, flops)
}

/// Tokens per joule at a given throughput (tok/s) and board power (W). Power is ~constant when busy,
/// so higher throughput (e.g. from batching) ≈ proportionally more tokens per joule.
pub fn tokens_per_joule(throughput_tok_s: f64, power_w: f64) -> f64 {
    throughput_tok_s / power_w
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn data_movement_dominates_energy() {
        // one OLMoE expert: ~12.6e6 FLOPs vs moving its 3.5 MB
        let move_only = energy_nj(3.5e6, 0.0);
        let compute_only = energy_nj(0.0, 12.6e6);
        assert!(move_only > compute_only * 100.0, "DRAM byte ≫ FLOP: {move_only} vs {compute_only}");
    }

    #[test]
    fn engine_cuts_energy_per_token() {
        // routing-aware (1611 MB/token) vs green engine (471 MB/token), same FLOPs
        let flops = 128.0 * 12.6e6;
        let baseline = energy_per_token_nj(1611e6, flops);
        let engine = energy_per_token_nj(471e6, flops);
        assert!(engine < baseline * 0.4, "engine {engine} should be ≪ baseline {baseline}");
    }

    #[test]
    fn batching_improves_tokens_per_joule() {
        // ~constant power; batching lifts throughput -> proportional tokens/joule
        let seq = tokens_per_joule(247.0, 350.0);
        let batched = tokens_per_joule(5714.0, 380.0); // slightly more power at higher batch
        assert!(batched > seq * 5.0, "batched {batched} ≫ sequential {seq}");
    }
}
