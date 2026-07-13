//! Online expert predictor — a first-order transition (co-occurrence) model, the
//! "routing branch-prediction" component. Used two ways:
//!   * token transition (per layer): experts at t-1 -> experts at t
//!   * layer-ahead (per layer):      experts at layer L -> experts at layer L+1
//!
//! All updates are causal: the engine predicts before it updates, so there is no leakage.
//! The hidden-state predictor (62% recall, see docs/benchmark-vs-llamacpp.md) plugs in here
//! later behind the same `score`/`top_b` interface.

pub struct TransitionMatrix {
    experts: usize,
    counts: Vec<f64>, // experts * experts, row i = "given i was active"
}

impl TransitionMatrix {
    pub fn new(experts: usize) -> Self {
        TransitionMatrix { experts, counts: vec![0.0; experts * experts] }
    }

    /// `out[j] = Σ_{i ∈ prev} counts[i][j]`. Returns the total mass (0.0 ⇒ no signal yet).
    pub fn score_into(&self, prev: &[u16], out: &mut [f64]) -> f64 {
        out.iter_mut().for_each(|v| *v = 0.0);
        for &i in prev {
            let row = (i as usize) * self.experts;
            for j in 0..self.experts {
                out[j] += self.counts[row + j];
            }
        }
        out.iter().sum()
    }

    /// Record that the set `cur` followed the set `prev`.
    pub fn update(&mut self, prev: &[u16], cur: &[u16]) {
        for &i in prev {
            let row = (i as usize) * self.experts;
            for &j in cur {
                self.counts[row + j as usize] += 1.0;
            }
        }
    }
}

/// Layer-ahead predictor: one transition model per layer boundary. `predict(l, cur)` returns the
/// top-`k` experts likely at layer `l+1` given layer `l`'s active experts — exactly the set to hand
/// [`crate::paged::Prefetcher::request`] so their weights are warm before layer `l+1` computes.
/// `observe` updates causally (predict *before* observing, so there is no leakage).
///
/// On the real OLMoE trace this covers ~53% of next-layer experts vs ~12% for a naive "reuse the
/// current set" guess — i.e. predictive prefetch can overlap ~4× more load latency (see
/// `bin/predict_bench`). The hidden-state predictor plugs in behind the same interface later.
pub struct LayerAheadPredictor {
    mats: Vec<TransitionMatrix>,
    scratch: Vec<f64>,
    k: usize,
}

impl LayerAheadPredictor {
    pub fn new(layers: usize, experts: usize, k: usize) -> Self {
        LayerAheadPredictor {
            mats: (0..layers.max(1)).map(|_| TransitionMatrix::new(experts)).collect(),
            scratch: vec![0.0; experts],
            k,
        }
    }

    /// Top-`k` experts predicted at layer `l+1` given layer `l`'s experts (empty until trained).
    pub fn predict(&mut self, layer: usize, cur: &[u16]) -> Vec<u16> {
        let mass = self.mats[layer].score_into(cur, &mut self.scratch);
        if mass > 0.0 {
            top_b(&self.scratch, self.k)
        } else {
            Vec::new()
        }
    }

    /// Record that `next` (layer l+1) followed `cur` (layer l).
    pub fn observe(&mut self, layer: usize, cur: &[u16], next: &[u16]) {
        self.mats[layer].update(cur, next);
    }
}

/// Indices of the top `b` scores, ordered to match NumPy's `argsort(s)[::-1][:b]`
/// (descending score, ties broken by descending index). Used so the Rust engine
/// reproduces the validated Python predictor exactly.
pub fn top_b(scores: &[f64], b: usize) -> Vec<u16> {
    let mut idx: Vec<u16> = (0..scores.len() as u16).collect();
    idx.sort_by(|&a, &c| {
        let (sa, sc) = (scores[a as usize], scores[c as usize]);
        sc.partial_cmp(&sa)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(c.cmp(&a))
    });
    idx.truncate(b);
    idx
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn layer_ahead_learns_a_repeating_transition() {
        // Layer 0 experts {0,1} always precede layer-1 experts {5,6}; the predictor should learn it.
        let mut p = LayerAheadPredictor::new(2, 8, 2);
        let (cur, next) = (vec![0u16, 1], vec![5u16, 6]);
        // untrained → no prediction
        assert!(p.predict(0, &cur).is_empty());
        for _ in 0..5 {
            p.observe(0, &cur, &next);
        }
        let pred = p.predict(0, &cur);
        assert!(next.iter().all(|e| pred.contains(e)), "should recall the learned next set, got {pred:?}");
    }
}
