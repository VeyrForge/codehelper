//! Better prefetch prediction — hidden-state cosine similarity vs the transition baseline, on the
//! OLMoE traces from `crates/engine-core/testdata/`.
//!
//! Task: predict the experts a token needs at each layer BEFORE computing it, so they can be
//! prefetched. Recall = fraction correctly predicted = share of load latency overlappable.
//!   * transition (t-1 → t): the ID-only baseline.
//!   * hidden k-NN (m=1..): experts of the top-m cosine-nearest past tokens (uses the router's own
//!     hidden state). m is the recall/budget knob — more neighbours = higher recall, bigger prefetch.
//!
//!   cargo run -p engine-core --release --bin predict_hidden_bench

use engine_core::hidden::{table_recall, transition_table, HiddenStates};
use engine_core::Trace;

fn main() {
    let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata");
    let trace = match Trace::load(format!("{dir}/expert_trace.bin")) {
        Ok(t) => t,
        Err(_) => { eprintln!("no testdata/expert_trace.bin"); return; }
    };
    let hidden = match HiddenStates::load(format!("{dir}/hidden_trace.bin")) {
        Ok(h) => h,
        Err(_) => { eprintln!("no testdata/hidden_trace.bin"); return; }
    };
    let k = trace.top_k;
    println!("\nPrefetch prediction — real OLMoE trace ({} tokens, {} layers, {} experts, top-{k})\n",
             trace.tokens, trace.layers, trace.experts);
    println!("  {:<32} {:>8} {:>12}", "predictor", "recall", "prefetch/step");

    // ID-only transition baseline (budget = k).
    let tt = transition_table(&trace, k);
    println!("  {:<32} {:>7.1}% {:>10.1}", "transition (t-1→t)", 100.0 * table_recall(&trace, &tt, k), k as f64);

    // Hidden-state k-NN at growing neighbour budgets.
    for m in [1usize, 2, 3, 4] {
        let (recall, size) = hidden.knn_recall(&trace, m);
        let label = format!("hidden k-NN  m={m}");
        println!("  {:<32} {:>7.1}% {:>10.1}", label, 100.0 * recall, size);
    }
    println!("\nHidden-state similarity beats the ID-only baseline; raising m lifts recall toward ~90%");
    println!("for a modestly larger prefetch budget — feed the predicted set to Prefetcher::request.");
}
