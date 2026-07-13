//! Per-layer expert cache. Experts per layer are few (≤ a few hundred), so we index
//! state by expert id with flat arrays and scan on eviction — simple and fast.

/// Eviction policy for the resident expert set.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Eviction {
    /// Evict least-recently-used. Best simple policy for load-balanced MoE (measured).
    Lru,
    /// Evict least-frequently-used.
    Lfu,
    /// Approximate Belady: evict the expert least likely to be used soon, scored by the
    /// transition predictor; ties broken by recency.
    ReuseDistance,
}

pub struct LayerCache {
    pub cap: usize,
    policy: Eviction,
    present: Vec<bool>,
    recency: Vec<u64>,
    freq: Vec<u64>,
    size: usize,
}

impl LayerCache {
    pub fn new(cap: usize, experts: usize, policy: Eviction) -> Self {
        LayerCache {
            cap,
            policy,
            present: vec![false; experts],
            recency: vec![0; experts],
            freq: vec![0; experts],
            size: 0,
        }
    }

    #[inline]
    pub fn contains(&self, e: u16) -> bool {
        self.present[e as usize]
    }

    #[inline]
    pub fn touch(&mut self, e: u16, clock: u64) {
        self.recency[e as usize] = clock;
        self.freq[e as usize] += 1;
    }

    pub fn remove(&mut self, e: u16) {
        if self.present[e as usize] {
            self.present[e as usize] = false;
            self.size -= 1;
        }
    }

    /// Make `e` resident, evicting one expert per `policy` if at capacity.
    /// `reuse_score[x]` = predicted near-future use of expert x (only read for ReuseDistance).
    pub fn admit(&mut self, e: u16, clock: u64, reuse_score: Option<&[f64]>) {
        if self.present[e as usize] {
            return;
        }
        if self.size >= self.cap {
            if let Some(v) = self.victim(reuse_score) {
                self.present[v] = false;
                self.size -= 1;
            }
        }
        self.present[e as usize] = true;
        self.recency[e as usize] = clock;
        self.size += 1;
    }

    fn victim(&self, reuse_score: Option<&[f64]>) -> Option<usize> {
        let mut best: Option<usize> = None;
        let mut best_key = f64::INFINITY;
        let mut best_rec = u64::MAX;
        for x in 0..self.present.len() {
            if !self.present[x] {
                continue;
            }
            let (key, rec) = match self.policy {
                Eviction::Lru => (self.recency[x] as f64, self.recency[x]),
                Eviction::Lfu => (self.freq[x] as f64, self.recency[x]),
                Eviction::ReuseDistance => {
                    (reuse_score.map_or(0.0, |s| s[x]), self.recency[x])
                }
            };
            // min by (key, recency); ties → lowest index (first seen)
            if best.is_none() || key < best_key || (key == best_key && rec < best_rec) {
                best = Some(x);
                best_key = key;
                best_rec = rec;
            }
        }
        best
    }
}
