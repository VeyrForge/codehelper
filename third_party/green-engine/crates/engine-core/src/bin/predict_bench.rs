//! Predictive-prefetch accuracy on the REAL OLMoE routing trace.
//!
//! Prefetch hides an expert's load latency only if we know *which* expert to warm before it's
//! needed. This measures how well the engine's first-order `TransitionMatrix` predicts the next
//! access, i.e. how much of the load latency predictive prefetch can overlap with compute:
//!   * layer-ahead:      experts at layer L  → experts at layer L+1 (cross-layer, same token)
//!   * token-transition: experts at token t-1 → experts at token t (per layer)
//! Recall@k = fraction of the actually-used next experts that were in the top-k prediction.
//! Baselines: a naive "persistence" predictor (reuse the current set) and a static top-k frequency
//! predictor — the predictor must beat both to be worth prefetching with.
//!
//!   cargo run -p engine-core --release --bin predict_bench

use engine_core::predictor::{top_b, TransitionMatrix};
use engine_core::Trace;

fn main() {
    let trace_path = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/expert_trace.bin");
    let trace = match Trace::load(trace_path) {
        Ok(t) => t,
        Err(_) => {
            eprintln!("no testdata/expert_trace.bin");
            return;
        }
    };
    let (tokens, layers, experts, k) = (trace.tokens, trace.layers, trace.experts, trace.top_k);

    // Per-boundary layer-ahead matrices, per-layer token-transition matrices.
    let mut la: Vec<TransitionMatrix> = (0..layers).map(|_| TransitionMatrix::new(experts)).collect();
    let mut tt: Vec<TransitionMatrix> = (0..layers).map(|_| TransitionMatrix::new(experts)).collect();
    let mut scores = vec![0.0f64; experts];
    let mut prev_tok: Vec<Vec<u16>> = vec![Vec::new(); layers];

    let (mut la_hit, mut la_tot) = (0usize, 0usize);
    let (mut persist_hit, mut tt_hit, mut tt_tot) = (0usize, 0usize, 0usize);

    // Global expert frequency (for the static-frequency baseline, evaluated causally-ish below).
    let mut freq = vec![0f64; experts];

    for t in 0..tokens {
        for l in 0..layers {
            let cur = trace.experts_at(t, l);

            // --- layer-ahead: predict layer l+1 from layer l (causal: predict, score, then update)
            if l + 1 < layers {
                let nxt = trace.experts_at(t, l + 1);
                let mass = la[l].score_into(cur, &mut scores);
                let pred = if mass > 0.0 { top_b(&scores, k) } else { Vec::new() };
                la_tot += nxt.len();
                la_hit += nxt.iter().filter(|e| pred.contains(e)).count();
                // persistence baseline: guess next == current set
                persist_hit += nxt.iter().filter(|e| cur.contains(e)).count();
                la[l].update(cur, nxt);
            }

            // --- token-transition: predict token t from t-1 within this layer
            if !prev_tok[l].is_empty() {
                let mass = tt[l].score_into(&prev_tok[l], &mut scores);
                let pred = if mass > 0.0 { top_b(&scores, k) } else { Vec::new() };
                tt_tot += cur.len();
                tt_hit += cur.iter().filter(|e| pred.contains(e)).count();
                tt[l].update(&prev_tok[l], cur);
            }
            prev_tok[l] = cur.to_vec();
            for &e in cur {
                freq[e as usize] += 1.0;
            }
        }
    }

    // Static top-k-frequency baseline: how many accesses the globally hottest k experts cover.
    let hot = top_b(&freq, k);
    let (mut freq_hit, mut freq_tot) = (0usize, 0usize);
    for t in 0..tokens {
        for l in 0..layers {
            let cur = trace.experts_at(t, l);
            freq_tot += cur.len();
            freq_hit += cur.iter().filter(|e| hot.contains(e)).count();
        }
    }

    let pct = |h: usize, n: usize| if n > 0 { 100.0 * h as f64 / n as f64 } else { 0.0 };
    println!("\nPredictive-prefetch accuracy — real OLMoE trace ({tokens} tokens, {layers} layers, {experts} experts, top-{k})\n");
    println!("  {:<34} {:>10}", "predictor (recall@k = coverage)", "recall");
    println!("  {:<34} {:>9.1}%", "layer-ahead  TransitionMatrix", pct(la_hit, la_tot));
    println!("  {:<34} {:>9.1}%   (naive baseline)", "layer-ahead  persistence (reuse cur)", pct(persist_hit, la_tot));
    println!("  {:<34} {:>9.1}%", "token-trans  TransitionMatrix", pct(tt_hit, tt_tot));
    println!("  {:<34} {:>9.1}%   (naive baseline)", "static top-k frequency", pct(freq_hit, freq_tot));
    println!("\nRecall@k = the fraction of next-step experts correctly predicted = the share of expert");
    println!("load latency predictive prefetch can overlap with compute. Higher beats the baselines.");
}
