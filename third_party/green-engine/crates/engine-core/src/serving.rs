//! Serving-layer techniques (2026 SOTA, confirmed by vLLM/SGLang/TensorRT-LLM): chunked prefill,
//! multi-token prediction (MTP), and prefill/decode disaggregation. Modeled with cost laws like
//! `batching.rs` — the *shapes* are the robust claims; absolute numbers are illustrative.

// ---------------------------------------------------------------- 1. Chunked prefill
/// A long prefill on a shared device freezes concurrent decode (TTFT/inter-token latency spike).
/// Splitting it into chunks interleaves decode between chunks, bounding the stall.
/// Returns (stall_without_chunking_ms, stall_with_chunking_ms) — the worst a decode token waits.
pub fn decode_stall(prefill_len: usize, chunk: usize, prefill_ms_per_tok: f64, decode_step_ms: f64) -> (f64, f64) {
    let without = prefill_len as f64 * prefill_ms_per_tok; // whole prefill blocks decode
    let with = chunk.min(prefill_len) as f64 * prefill_ms_per_tok + decode_step_ms; // one chunk, then a decode step
    (without, with)
}

// ---------------------------------------------------------------- 2. Multi-token prediction
/// Propose `k` extra tokens per step; accept greedily with per-token probability `accept_p`
/// (stop at first reject). Decode is memory-bound so the step cost barely grows with k.
/// Returns (expected_tokens_per_step, throughput_speedup_vs_1).
pub fn mtp(k: usize, accept_p: f64, step_ms: f64, extra_ms_per_tok: f64) -> (f64, f64) {
    // accepted = 1 + p + p^2 + ... + p^k  (base token always emitted)
    let mut accepted = 0.0;
    let mut pk = 1.0;
    for _ in 0..=k {
        accepted += pk;
        pk *= accept_p;
    }
    let step_cost = step_ms + extra_ms_per_tok * k as f64;
    let tps = accepted / step_cost;
    let base_tps = 1.0 / step_ms;
    (accepted, tps / base_tps)
}

// ---------------------------------------------------------------- 3. Disaggregation
/// Prefill is compute-bound, decode memory-bound; co-located they interfere (prefill steals device
/// time from decode). `prefill_fraction` = share of device time spent on prefill.
pub fn decode_throughput_colocated(decode_rate: f64, prefill_fraction: f64) -> f64 {
    decode_rate * (1.0 - prefill_fraction)
}
/// Disaggregated: decode runs uninterrupted on its own worker at the full rate.
pub fn decode_throughput_disaggregated(decode_rate: f64) -> f64 {
    decode_rate
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn chunking_bounds_decode_stall() {
        let (without, with) = decode_stall(2048, 256, 0.05, 0.5); // 2k-token prefill, 256 chunks
        assert!(with < without / 4.0, "chunked {with} should be ≪ unchunked {without}");
    }

    #[test]
    fn mtp_speeds_up_decode() {
        let (acc, speedup) = mtp(3, 0.7, 4.0, 0.1); // 3 extra tokens, 70% accept, memory-bound
        assert!(acc > 2.0, "expected >2 tokens/step, got {acc}");
        assert!(speedup > 1.8, "expected >1.8× decode, got {speedup}");
    }

    #[test]
    fn disaggregation_recovers_decode_throughput() {
        let co = decode_throughput_colocated(100.0, 0.4); // 40% of time stolen by prefill
        let dis = decode_throughput_disaggregated(100.0);
        assert!(dis > co, "disaggregated {dis} should beat co-located {co}");
        assert!((dis / co - 1.0 / 0.6).abs() < 1e-6);
    }
}
