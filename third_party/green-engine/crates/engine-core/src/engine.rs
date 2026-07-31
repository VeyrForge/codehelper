//! The Green Engine scheduler: for every (token, layer) it decides which experts are
//! resident, prefetched, or must be fetched — replaying a routing trace losslessly.

use std::collections::VecDeque;

use crate::cache::{Eviction, LayerCache};
use crate::manifest::WeightManifest;
use crate::predictor::{top_b, TransitionMatrix};
use crate::trace::Trace;

/// Which prefetch signals are active.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Prefetch {
    None,
    /// Prefetch layer L+1's experts from layer L's active set (the strong, 74%-recall signal).
    LayerAhead,
    /// Prefetch next token's predicted experts ("misprediction salvage").
    Salvage,
    /// Both of the above.
    Both,
}

#[derive(Clone, Copy, Debug)]
pub struct Config {
    /// Resident experts per layer (main cache).
    pub capacity: usize,
    /// Separate prefetch-buffer capacity per layer (0 = disabled).
    pub buffer: usize,
    pub eviction: Eviction,
    pub prefetch: Prefetch,
    /// How many predicted experts to prefetch per step.
    pub prefetch_budget: usize,
}

impl Config {
    /// Plain recency cache — the strong, simple baseline.
    pub fn lru(capacity: usize) -> Self {
        Config { capacity, buffer: 0, eviction: Eviction::Lru, prefetch: Prefetch::None, prefetch_budget: 0 }
    }
    /// The full engine: reuse-distance eviction + layer-ahead + salvage prefetch.
    pub fn full(capacity: usize, buffer: usize) -> Self {
        Config {
            capacity,
            buffer,
            eviction: Eviction::ReuseDistance,
            prefetch: Prefetch::Both,
            prefetch_budget: 8,
        }
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct Metrics {
    pub requests: u64,
    pub hits: u64,
    pub buffer_hits: u64,
    pub prefetch_loaded: u64,
    /// Total bytes pulled from the slow tier (cold misses + prefetch loads), using the
    /// per-expert compressed sizes from the Green Compress manifest.
    pub bytes_moved: u64,
    pub tokens: u64,
}

impl Metrics {
    pub fn hit_rate(&self) -> f64 {
        if self.requests == 0 { 0.0 } else { self.hits as f64 / self.requests as f64 }
    }
    /// Fraction of prefetched experts never used before eviction.
    pub fn prefetch_waste(&self) -> f64 {
        if self.prefetch_loaded == 0 { 0.0 } else { 1.0 - self.buffer_hits as f64 / self.prefetch_loaded as f64 }
    }
    /// Slow-tier bytes pulled per decoded token (the metric the engine optimizes).
    pub fn bytes_per_token(&self) -> f64 {
        if self.tokens == 0 { 0.0 } else { self.bytes_moved as f64 / self.tokens as f64 }
    }
}

struct Buffer {
    cap: usize,
    queue: VecDeque<u16>,
    present: Vec<bool>,
}

impl Buffer {
    fn new(cap: usize, experts: usize) -> Self {
        Buffer { cap, queue: VecDeque::new(), present: vec![false; experts] }
    }
    #[inline]
    fn contains(&self, e: u16) -> bool {
        self.cap > 0 && self.present[e as usize]
    }
    fn take(&mut self, e: u16) {
        if self.present[e as usize] {
            self.present[e as usize] = false;
            if let Some(pos) = self.queue.iter().position(|&x| x == e) {
                self.queue.remove(pos);
            }
        }
    }
    /// Prefetch `experts` into the buffer (skips those already resident in `main`).
    /// Returns (count loaded, bytes moved) using the manifest's per-expert compressed sizes.
    fn add(&mut self, experts: &[u16], main: &LayerCache, layer: usize, man: &WeightManifest) -> (u64, u64) {
        if self.cap == 0 {
            return (0, 0);
        }
        let (mut loaded, mut bytes) = (0u64, 0u64);
        for &e in experts {
            if main.contains(e) || self.present[e as usize] {
                continue;
            }
            if self.queue.len() >= self.cap {
                if let Some(old) = self.queue.pop_front() {
                    self.present[old as usize] = false;
                }
            }
            self.queue.push_back(e);
            self.present[e as usize] = true;
            loaded += 1;
            bytes += man.expert_bytes(layer, e);
        }
        (loaded, bytes)
    }
}

pub struct Engine {
    cfg: Config,
    experts: usize,
    layers: usize,
}

impl Engine {
    pub fn new(cfg: Config, trace: &Trace) -> Self {
        Engine { cfg, experts: trace.experts, layers: trace.layers }
    }

    /// Convenience: run with a uniform expert size (no compression manifest).
    pub fn run(&self, trace: &Trace, bytes_per_expert: u64) -> Metrics {
        let man = WeightManifest::uniform(trace.layers, trace.experts, bytes_per_expert, 16);
        self.run_with_manifest(trace, &man)
    }

    /// Run the engine over the trace, accounting bytes moved via the Green Compress manifest.
    pub fn run_with_manifest(&self, trace: &Trace, man: &WeightManifest) -> Metrics {
        let e = self.experts;
        let l_count = self.layers;
        let mut main: Vec<LayerCache> =
            (0..l_count).map(|_| LayerCache::new(self.cfg.capacity, e, self.cfg.eviction)).collect();
        let mut buf: Vec<Buffer> = (0..l_count).map(|_| Buffer::new(self.cfg.buffer, e)).collect();
        let mut trans_tok: Vec<TransitionMatrix> = (0..l_count).map(|_| TransitionMatrix::new(e)).collect();
        let mut trans_lay: Vec<TransitionMatrix> = (0..l_count).map(|_| TransitionMatrix::new(e)).collect();
        let mut scratch = vec![0.0f64; e];

        let mut m = Metrics::default();
        let mut clock: u64 = 0;
        let want_reuse = self.cfg.eviction == Eviction::ReuseDistance;
        let do_layer = matches!(self.cfg.prefetch, Prefetch::LayerAhead | Prefetch::Both);
        let do_salvage = matches!(self.cfg.prefetch, Prefetch::Salvage | Prefetch::Both);

        for t in 0..trace.tokens {
            for l in 0..l_count {
                let cur = trace.experts_at(t, l);
                let prev: &[u16] = if t > 0 { trace.experts_at(t - 1, l) } else { &[] };

                // reuse-distance score (predicted near-future use) for eviction this step
                let reuse: Option<&[f64]> = if want_reuse && t > 0 {
                    trans_tok[l].score_into(prev, &mut scratch);
                    Some(&scratch[..])
                } else {
                    None
                };

                for &expert in cur {
                    m.requests += 1;
                    clock += 1;
                    if main[l].contains(expert) {
                        m.hits += 1;
                    } else if buf[l].contains(expert) {
                        // already moved at prefetch time; just promote into the main cache
                        m.hits += 1;
                        m.buffer_hits += 1;
                        buf[l].take(expert);
                        main[l].admit(expert, clock, reuse);
                    } else {
                        // cold miss: this expert crosses the slow link now
                        m.bytes_moved += man.expert_bytes(l, expert);
                        main[l].admit(expert, clock, reuse);
                    }
                    main[l].touch(expert, clock);
                }

                // layer-ahead prefetch: warm layer l+1 from layer l's active set
                if do_layer && l + 1 < l_count && t > 0 {
                    let sum = trans_lay[l].score_into(cur, &mut scratch);
                    if sum > 0.0 {
                        let pred = top_b(&scratch, self.cfg.prefetch_budget);
                        let (n, bytes) = buf[l + 1].add(&pred, &main[l + 1], l + 1, man);
                        m.prefetch_loaded += n;
                        m.bytes_moved += bytes;
                    }
                }
                if l + 1 < l_count {
                    let next = trace.experts_at(t, l + 1);
                    let cur_owned: Vec<u16> = cur.to_vec();
                    trans_lay[l].update(&cur_owned, next);
                }
            }

            // misprediction salvage: predict next token's experts, stash for t+1
            if do_salvage {
                for l in 0..l_count {
                    let cur = trace.experts_at(t, l);
                    let sum = trans_tok[l].score_into(cur, &mut scratch);
                    if sum > 0.0 {
                        let pred = top_b(&scratch, self.cfg.prefetch_budget);
                        let (n, bytes) = buf[l].add(&pred, &main[l], l, man);
                        m.prefetch_loaded += n;
                        m.bytes_moved += bytes;
                    }
                }
            }
            // update token-transition counts (causal: prev -> cur)
            if t > 0 {
                for l in 0..l_count {
                    let prev = trace.experts_at(t - 1, l).to_vec();
                    let cur = trace.experts_at(t, l);
                    trans_tok[l].update(&prev, cur);
                }
            }
        }
        m.tokens = trace.tokens as u64;
        m
    }
}
