//! Hidden-state expert predictor (the 62%-recall signal) and a self-contained
//! just-in-time prefetch simulator.
//!
//! In a real engine the residual stream entering a layer is known *before* the router runs,
//! so we can predict that layer's experts from the hidden state and prefetch them just in
//! time. Here we replay captured hidden states: for (token t, layer l) we find the most
//! similar PAST hidden state (cosine) and predict its expert set. Causal — only j < t.
//!
//! Kept separate from `engine.rs` (the transition-based engine) so both predictors can be
//! compared head-to-head; shares the public `cache`, `trace`, `manifest` types.

use std::collections::VecDeque;
use std::fs;
use std::io;
use std::path::Path;

use crate::cache::{Eviction, LayerCache};
use crate::manifest::WeightManifest;
use crate::trace::Trace;

const MAGIC: u32 = 0x4E44_4948; // "HIDN"

pub struct HiddenStates {
    pub tokens: usize,
    pub layers: usize,
    pub hidden: usize,
    norm: Vec<f32>, // L2-normalized vectors, layout (t*layers + l)*hidden + h
}

impl HiddenStates {
    pub fn load<P: AsRef<Path>>(path: P) -> io::Result<Self> {
        let bytes = fs::read(path)?;
        if bytes.len() < 16 || u32::from_le_bytes(bytes[0..4].try_into().unwrap()) != MAGIC {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "bad hidden-trace header"));
        }
        let u = |o: usize| u32::from_le_bytes(bytes[o..o + 4].try_into().unwrap()) as usize;
        let (tokens, layers, hidden) = (u(4), u(8), u(12));
        let n = tokens * layers * hidden;
        if bytes.len() != 16 + n * 4 {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "hidden-trace size mismatch"));
        }
        let mut norm = vec![0.0f32; n];
        for i in 0..n {
            let o = 16 + i * 4;
            norm[i] = f32::from_le_bytes(bytes[o..o + 4].try_into().unwrap());
        }
        // L2-normalize each (t,l) vector so cosine = dot product
        for v in 0..tokens * layers {
            let s = &mut norm[v * hidden..(v + 1) * hidden];
            let mag = s.iter().map(|x| x * x).sum::<f32>().sqrt().max(1e-6);
            s.iter_mut().for_each(|x| *x /= mag);
        }
        Ok(HiddenStates { tokens, layers, hidden, norm })
    }

    #[inline]
    fn vec(&self, t: usize, l: usize) -> &[f32] {
        let base = (t * self.layers + l) * self.hidden;
        &self.norm[base..base + self.hidden]
    }

    /// Causal nearest-neighbour predicted expert set per (t, l): the experts of the most
    /// cosine-similar past token at the same layer. Returns a flat table [tokens*layers*k].
    pub fn predict_table(&self, trace: &Trace, k: usize) -> Vec<u16> {
        assert_eq!(self.tokens, trace.tokens);
        assert_eq!(self.layers, trace.layers);
        let l_count = self.layers;
        let mut table = vec![0u16; self.tokens * l_count * k];
        for l in 0..l_count {
            for t in 0..self.tokens {
                let nn = if t == 0 {
                    0
                } else {
                    let q = self.vec(t, l);
                    let mut best = 0usize;
                    let mut best_sim = f32::NEG_INFINITY;
                    for j in 0..t {
                        let cand = self.vec(j, l);
                        let mut dot = 0.0f32;
                        for h in 0..self.hidden {
                            dot += q[h] * cand[h];
                        }
                        if dot > best_sim {
                            best_sim = dot;
                            best = j;
                        }
                    }
                    best
                };
                let src = trace.experts_at(nn, l);
                let dst = &mut table[(t * l_count + l) * k..(t * l_count + l) * k + k];
                for (d, &s) in dst.iter_mut().zip(src.iter()) {
                    *d = s;
                }
            }
        }
        table
    }

    /// k-NN over-prefetch recall: for each (t, l), union the expert sets of the top-`m` cosine-
    /// nearest PAST tokens at that layer (causal), then measure recall against the true active set.
    /// Larger `m` trades a bigger prefetch budget for higher recall — the recall/bandwidth knob
    /// that lifts the single-NN ~62% toward the ~90% the research reports. Returns
    /// `(mean_recall, mean_prefetch_set_size)`.
    pub fn knn_recall(&self, trace: &Trace, m: usize) -> (f64, f64) {
        assert_eq!(self.tokens, trace.tokens);
        assert_eq!(self.layers, trace.layers);
        let l_count = self.layers;
        let (mut hit, mut tot, mut size_sum, mut steps) = (0u64, 0u64, 0u64, 0u64);
        let mut sims: Vec<(f32, usize)> = Vec::new();
        let mut pred: Vec<u16> = Vec::new();
        for l in 0..l_count {
            for t in 1..self.tokens {
                let q = self.vec(t, l);
                sims.clear();
                for j in 0..t {
                    let cand = self.vec(j, l);
                    let mut dot = 0.0f32;
                    for h in 0..self.hidden {
                        dot += q[h] * cand[h];
                    }
                    sims.push((dot, j));
                }
                let mm = m.min(sims.len());
                // partial: take the m highest-similarity past tokens
                sims.sort_by(|a, b| b.0.partial_cmp(&a.0).unwrap_or(std::cmp::Ordering::Equal));
                pred.clear();
                for &(_, j) in sims.iter().take(mm) {
                    for &e in trace.experts_at(j, l) {
                        if !pred.contains(&e) {
                            pred.push(e);
                        }
                    }
                }
                for &e in trace.experts_at(t, l) {
                    tot += 1;
                    if pred.contains(&e) {
                        hit += 1;
                    }
                }
                size_sum += pred.len() as u64;
                steps += 1;
            }
        }
        (hit as f64 / tot as f64, size_sum as f64 / steps as f64)
    }
}

/// Online transition predicted table: predict (t,l)'s set from the previous token via a
/// causal co-occurrence model. The non-hidden baseline for the same JIT-prefetch harness.
pub fn transition_table(trace: &Trace, k: usize) -> Vec<u16> {
    let e = trace.experts;
    let l_count = trace.layers;
    let mut table = vec![0u16; trace.tokens * l_count * k];
    let mut trans = vec![vec![0u32; e * e]; l_count];
    let mut score = vec![0u32; e];
    for t in 0..trace.tokens {
        for l in 0..l_count {
            let dst = &mut table[(t * l_count + l) * k..(t * l_count + l) * k + k];
            if t == 0 {
                for (d, &s) in dst.iter_mut().zip(trace.experts_at(0, l).iter()) {
                    *d = s;
                }
            } else {
                let prev = trace.experts_at(t - 1, l);
                score.iter_mut().for_each(|v| *v = 0);
                for &i in prev {
                    let row = &trans[l][i as usize * e..(i as usize + 1) * e];
                    for j in 0..e {
                        score[j] += row[j];
                    }
                }
                // top-k by (score desc, index desc) to match the engine's predictor
                let mut idx: Vec<u16> = (0..e as u16).collect();
                idx.sort_by(|&a, &b| score[b as usize].cmp(&score[a as usize]).then(b.cmp(&a)));
                for (d, &s) in dst.iter_mut().zip(idx.iter()) {
                    *d = s;
                }
            }
        }
        if t > 0 {
            for l in 0..l_count {
                let prev = trace.experts_at(t - 1, l);
                let cur = trace.experts_at(t, l);
                for &i in prev {
                    for &j in cur {
                        trans[l][i as usize * e + j as usize] += 1;
                    }
                }
            }
        }
    }
    table
}

/// Mean recall of a predicted table vs the true active sets (how good the predictor is).
pub fn table_recall(trace: &Trace, table: &[u16], k: usize) -> f64 {
    let l_count = trace.layers;
    let mut hit = 0u64;
    let mut tot = 0u64;
    for t in 1..trace.tokens {
        for l in 0..l_count {
            let pred = &table[(t * l_count + l) * k..(t * l_count + l) * k + k];
            for &e in trace.experts_at(t, l) {
                tot += 1;
                if pred.contains(&e) {
                    hit += 1;
                }
            }
        }
    }
    hit as f64 / tot as f64
}

#[derive(Default, Clone, Copy)]
pub struct JitStats {
    pub requests: u64,
    pub hits: u64,
    pub bytes_moved: u64,
    pub tokens: u64,
}
impl JitStats {
    pub fn hit_rate(&self) -> f64 {
        if self.requests == 0 { 0.0 } else { self.hits as f64 / self.requests as f64 }
    }
    pub fn bytes_per_token(&self) -> f64 {
        if self.tokens == 0 { 0.0 } else { self.bytes_moved as f64 / self.tokens as f64 }
    }
}

struct Buf {
    cap: usize,
    q: VecDeque<u16>,
    present: Vec<bool>,
}
impl Buf {
    fn new(cap: usize, e: usize) -> Self {
        Buf { cap, q: VecDeque::new(), present: vec![false; e] }
    }
    fn contains(&self, e: u16) -> bool {
        self.cap > 0 && self.present[e as usize]
    }
    fn take(&mut self, e: u16) {
        if self.present[e as usize] {
            self.present[e as usize] = false;
            if let Some(p) = self.q.iter().position(|&x| x == e) {
                self.q.remove(p);
            }
        }
    }
    fn add(&mut self, set: &[u16], main: &LayerCache, layer: usize, man: &WeightManifest) -> u64 {
        if self.cap == 0 {
            return 0;
        }
        let mut bytes = 0;
        for &e in set {
            if main.contains(e) || self.present[e as usize] {
                continue;
            }
            if self.q.len() >= self.cap {
                if let Some(old) = self.q.pop_front() {
                    self.present[old as usize] = false;
                }
            }
            self.q.push_back(e);
            self.present[e as usize] = true;
            bytes += man.expert_bytes(layer, e);
        }
        bytes
    }
}

/// Just-in-time prefetch simulator: before serving each (t,l), prefetch the predicted set
/// into a side buffer; misses cross the slow link. `predicted_reuse` keys eviction on the
/// predicted set (approximate Belady) when true; otherwise plain LRU.
pub fn simulate_jit(
    trace: &Trace,
    man: &WeightManifest,
    table: &[u16],
    k: usize,
    main_cap: usize,
    buf_cap: usize,
    predicted_reuse: bool,
) -> JitStats {
    let e = trace.experts;
    let l_count = trace.layers;
    let evict = if predicted_reuse { Eviction::ReuseDistance } else { Eviction::Lru };
    let mut main: Vec<LayerCache> =
        (0..l_count).map(|_| LayerCache::new(main_cap, e, evict)).collect();
    let mut buf: Vec<Buf> = (0..l_count).map(|_| Buf::new(buf_cap, e)).collect();
    let mut score = vec![0.0f64; e];
    let mut s = JitStats::default();
    let mut clock = 0u64;

    for t in 0..trace.tokens {
        for l in 0..l_count {
            let pred = &table[(t * l_count + l) * k..(t * l_count + l) * k + k];
            // eviction score: predicted experts are "keep" (high), others evictable
            let reuse: Option<&[f64]> = if predicted_reuse {
                score.iter_mut().for_each(|v| *v = 0.0);
                for &p in pred {
                    score[p as usize] = 1.0;
                }
                Some(&score[..])
            } else {
                None
            };
            // just-in-time prefetch
            s.bytes_moved += buf[l].add(pred, &main[l], l, man);
            // serve
            for &expert in trace.experts_at(t, l) {
                s.requests += 1;
                clock += 1;
                if main[l].contains(expert) {
                    s.hits += 1;
                } else if buf[l].contains(expert) {
                    s.hits += 1;
                    buf[l].take(expert);
                    main[l].admit(expert, clock, reuse);
                } else {
                    s.bytes_moved += man.expert_bytes(l, expert);
                    main[l].admit(expert, clock, reuse);
                }
                main[l].touch(expert, clock);
            }
        }
    }
    s.tokens = trace.tokens as u64;
    s
}
