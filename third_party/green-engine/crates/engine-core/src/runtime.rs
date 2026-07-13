//! The executable engine: routes tokens to experts, schedules residency through the cache,
//! fetches weights from the tiered store (accounting bytes on cold misses), computes each
//! expert on the chosen backend, and combines by gate weight.
//!
//! Invariant: the engine's output equals the dense MoE output over the same experts/gates —
//! scheduling and caching change *what is in fast memory*, never the math (see tests).

use crate::backend::{ExpertBackend, Scratch};
use crate::cache::{Eviction, LayerCache};
use crate::paged::ExpertProvider;
use crate::weights::WeightStore;

#[derive(Default, Clone, Copy, Debug)]
pub struct RuntimeMetrics {
    pub expert_calls: u64,
    pub cold_misses: u64,
    pub bytes_moved: u64,
    pub tokens: u64,
}
impl RuntimeMetrics {
    pub fn bytes_per_token(&self) -> f64 {
        if self.tokens == 0 { 0.0 } else { self.bytes_moved as f64 / self.tokens as f64 }
    }
}

/// The runtime pulls weights from any [`ExpertProvider`] — an all-in-RAM `WeightStore` (default)
/// or a `PagedWeightStore` that streams a compressed cold tier from disk. Same math either way.
pub struct MoeRuntime<'a, B: ExpertBackend, P: ExpertProvider = WeightStore> {
    store: &'a P,
    backend: &'a B,
    cache: Vec<LayerCache>,
    scratch: Scratch,
    tmp: Vec<f32>,
    hidden: usize,
    layers: usize,
    experts: usize,
    capacity: usize,
    clock: u64,
    pub metrics: RuntimeMetrics,
}

impl<'a, B: ExpertBackend, P: ExpertProvider> MoeRuntime<'a, B, P> {
    pub fn new(store: &'a P, backend: &'a B, capacity: usize, eviction: Eviction) -> Self {
        let (layers, experts, hidden, inter) = (store.layers(), store.experts(), store.hidden(), store.inter());
        let cache = (0..layers).map(|_| LayerCache::new(capacity, experts, eviction)).collect();
        MoeRuntime {
            store,
            backend,
            cache,
            scratch: Scratch::new(hidden, inter),
            tmp: vec![0.0; hidden],
            hidden,
            layers,
            experts,
            capacity,
            clock: 0,
            metrics: RuntimeMetrics::default(),
        }
    }

    /// Compute one layer for one token: `out = Σ_k gate_k · expert_k(x)`, scheduling residency.
    pub fn forward_layer(&mut self, layer: usize, x: &[f32], experts: &[u16], gates: &[f32], out: &mut [f32]) {
        for v in out.iter_mut() {
            *v = 0.0;
        }
        let store = self.store; // &P: copying the reference borrows nothing of `self`
        let backend = self.backend;
        for (k, &e) in experts.iter().enumerate() {
            self.clock += 1;
            if !self.cache[layer].contains(e) {
                self.metrics.cold_misses += 1;
                self.metrics.bytes_moved += store.expert_bytes(layer, e) as u64;
                self.cache[layer].admit(e, self.clock, None);
            }
            self.cache[layer].touch(e, self.clock);

            let (scratch, tmp) = (&mut self.scratch, &mut self.tmp);
            store.with_expert(layer, e, &mut |w| backend.compute_expert(w, x, scratch, tmp));
            let g = gates[k];
            for o in 0..self.hidden {
                out[o] += g * self.tmp[o];
            }
            self.metrics.expert_calls += 1;
        }
    }

    /// Peak fast-memory footprint of resident experts (bytes), assuming a uniform expert size —
    /// the headline "uses less memory" figure vs holding all experts.
    pub fn resident_footprint_bytes(&self) -> u64 {
        self.store.expert_bytes(0, 0) as u64 * self.capacity as u64 * self.layers as u64
    }
    pub fn full_footprint_bytes(&self) -> u64 {
        self.store.expert_bytes(0, 0) as u64 * self.layers as u64 * self.experts as u64
    }
}

/// Dense reference: the same weighted sum with no cache/scheduling. Used to prove losslessness.
pub fn dense_reference<B: ExpertBackend>(
    store: &WeightStore,
    backend: &B,
    layer: usize,
    x: &[f32],
    experts: &[u16],
    gates: &[f32],
) -> Vec<f32> {
    let mut scratch = Scratch::new(store.hidden, store.inter);
    let mut tmp = vec![0.0f32; store.hidden];
    let mut out = vec![0.0f32; store.hidden];
    for (k, &e) in experts.iter().enumerate() {
        backend.compute_expert(store.get(layer, e), x, &mut scratch, &mut tmp);
        for o in 0..store.hidden {
            out[o] += gates[k] * tmp[o];
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::CpuBackend;

    // deterministic helpers (no rand dep)
    fn lcg(state: &mut u64) -> f32 {
        *state ^= *state >> 12;
        *state ^= *state << 25;
        *state ^= *state >> 27;
        let u = (state.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32;
        u - 0.5
    }

    fn routing(experts: usize, k: usize, seed: &mut u64) -> (Vec<u16>, Vec<f32>) {
        let mut chosen = Vec::new();
        while chosen.len() < k {
            let e = ((lcg(seed) + 0.5) * experts as f32) as u16 % experts as u16;
            if !chosen.contains(&e) {
                chosen.push(e);
            }
        }
        let gates = vec![1.0 / k as f32; k];
        (chosen, gates)
    }

    /// The engine (with a tiny cache) must produce *exactly* the dense MoE output.
    #[test]
    fn engine_is_lossless_vs_dense() {
        let (h, inter, e, k) = (64usize, 128usize, 32usize, 8usize);
        let store = WeightStore::synthetic(2, e, h, inter, false, 7);
        let backend = CpuBackend;
        let mut rt = MoeRuntime::new(&store, &backend, 8, Eviction::Lru); // cap 8 « 32 experts
        let mut seed = 99u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        for t in 0..50 {
            let (experts, gates) = routing(e, k, &mut seed);
            let layer = t % 2;
            let mut out = vec![0.0; h];
            rt.forward_layer(layer, &x, &experts, &gates, &mut out);
            let want = dense_reference(&store, &backend, layer, &x, &experts, &gates);
            for o in 0..h {
                assert!((out[o] - want[o]).abs() < 1e-4, "lossless mismatch at {o}: {} vs {}", out[o], want[o]);
            }
        }
        assert!(rt.metrics.cold_misses > 0 && rt.metrics.expert_calls == 50 * k as u64);
    }

    /// The runtime must run end-to-end over a disk-backed `PagedWeightStore` (F32) and stay
    /// bit-for-bit lossless vs the all-in-RAM dense reference — proving the whole "model on disk,
    /// bounded RAM" path executes correctly through the real runtime, not just the benches.
    #[test]
    fn runtime_runs_paged_store_lossless() {
        use crate::paged::{PagedFormat, PagedWeightStore};
        let (h, inter, e, k) = (48usize, 96usize, 24usize, 6usize);
        let store = WeightStore::synthetic(2, e, h, inter, false, 7);
        let path = std::env::temp_dir().join(format!("ge_rt_paged_{}.bin", std::process::id()));
        PagedWeightStore::create(&path, &store, PagedFormat::F32).unwrap();
        let paged = PagedWeightStore::open(&path, 5).unwrap(); // 5 experts/layer resident « 24
        let backend = CpuBackend;
        let mut rt = MoeRuntime::new(&paged, &backend, 5, Eviction::Lru);
        let mut seed = 42u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        for t in 0..40 {
            let (experts, gates) = routing(e, k, &mut seed);
            let layer = t % 2;
            let mut out = vec![0.0; h];
            rt.forward_layer(layer, &x, &experts, &gates, &mut out);
            let want = dense_reference(&store, &backend, layer, &x, &experts, &gates);
            assert_eq!(out, want, "paged runtime must equal in-RAM dense at token {t}");
        }
        assert!(paged.metrics().cold_reads > 0, "should have paged from disk");
        let _ = std::fs::remove_file(&path);
    }

    /// The native (C++/CUDA) backend, called over FFI, must produce the same output as the
    /// Rust CPU backend — proving the device-agnostic boundary is correct end to end.
    #[cfg(feature = "gpu")]
    #[test]
    fn ffi_native_backend_matches_cpu() {
        use crate::backend::gpu::GpuBackend;
        let store = WeightStore::synthetic(1, 8, 32, 64, false, 3);
        let cpu = CpuBackend;
        let native = GpuBackend::new(0);
        let mut seed = 5u64;
        let x: Vec<f32> = (0..store.hidden).map(|_| lcg(&mut seed)).collect();
        let (experts, gates) = routing(store.experts, 4, &mut seed);
        let a = dense_reference(&store, &cpu, 0, &x, &experts, &gates);
        let b = dense_reference(&store, &native, 0, &x, &experts, &gates);
        for i in 0..a.len() {
            assert!((a[i] - b[i]).abs() < 1e-4, "native FFI backend differs at {i}: {} vs {}", a[i], b[i]);
        }
    }

    /// Q8 storage uses ~4× less memory and stays close to f32 (Green Compress-style compression).
    #[test]
    fn q8_uses_less_memory_with_small_drift() {
        let (h, inter, e, k) = (64usize, 128usize, 16usize, 8usize);
        let f32_store = WeightStore::synthetic(1, e, h, inter, false, 5);
        let q8_store = WeightStore::synthetic(1, e, h, inter, true, 5);
        assert!(q8_store.total_bytes() * 3 < f32_store.total_bytes(), "Q8 should be ~4× smaller");

        let backend = CpuBackend;
        let mut seed = 1u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        let (experts, gates) = routing(e, k, &mut seed);
        let a = dense_reference(&f32_store, &backend, 0, &x, &experts, &gates);
        let b = dense_reference(&q8_store, &backend, 0, &x, &experts, &gates);
        let num: f32 = a.iter().zip(&b).map(|(p, q)| (p - q).powi(2)).sum();
        let den: f32 = a.iter().map(|p| p * p).sum::<f32>().max(1e-9);
        let rel = (num / den).sqrt();
        assert!(rel < 0.1, "Q8 drift {rel:.4} should be small");
    }
}
