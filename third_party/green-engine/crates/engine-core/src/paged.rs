//! Disk-backed, RAM-bounded expert store — the RAM↔disk analog of the GPU residency cache.
//!
//! `WeightStore` keeps every expert in RAM, so peak RAM ≈ the whole model. `PagedWeightStore`
//! keeps only a bounded, per-layer working set of experts materialized in RAM and reads cold
//! experts from disk on demand. Peak RAM = `cap_per_layer · layers · expert_bytes`, independent
//! of model size — so a machine with less RAM than the model can still run it.
//!
//! Two on-disk formats:
//!   * `F32`  — raw f32, lossless: paged output is bit-for-bit identical to the all-in-RAM store.
//!   * `Q8Ch` — per-output-channel int8 ("green compression"): ~4× smaller on disk AND in the RAM
//!     cache, at a small, measured quality cost. The cache holds the *compressed* tensor and the
//!     backend dequantizes on the fly, so a 4×-bigger model fits the same RAM/disk budget.
//!
//! Unlike a plain mmap (whose faulted pages still land in RSS and can even use *more* memory), the
//! resident set here is a hard, explicit per-layer cap — the knob that trades RAM for cold-read
//! traffic, which the engine's scheduler/predictor then minimizes.

use std::collections::HashMap;
use std::fs::File;
use std::io::{self, Read, Seek, SeekFrom, Write};
use std::path::Path;
use std::sync::{Arc, Mutex};

use crate::backend::{ExpertBackend, Scratch};
use crate::weights::{ExpertWeights, Tensor, WeightStore};

/// A source the runtime pulls expert weights from. `WeightStore` holds them all in RAM;
/// `PagedWeightStore` holds a bounded cache and pages the rest from disk. Same interface either way.
pub trait ExpertProvider {
    fn layers(&self) -> usize;
    fn experts(&self) -> usize;
    fn hidden(&self) -> usize;
    fn inter(&self) -> usize;
    fn expert_bytes(&self, layer: usize, expert: u16) -> usize;
    /// Run `f` on the expert's weights, materializing them into fast memory if needed.
    fn with_expert(&self, layer: usize, expert: u16, f: &mut dyn FnMut(&ExpertWeights));
}

impl ExpertProvider for WeightStore {
    fn layers(&self) -> usize {
        self.layers
    }
    fn experts(&self) -> usize {
        self.experts
    }
    fn hidden(&self) -> usize {
        self.hidden
    }
    fn inter(&self) -> usize {
        self.inter
    }
    fn expert_bytes(&self, layer: usize, expert: u16) -> usize {
        WeightStore::expert_bytes(self, layer, expert)
    }
    fn with_expert(&self, layer: usize, expert: u16, f: &mut dyn FnMut(&ExpertWeights)) {
        f(self.get(layer, expert));
    }
}

/// On-disk weight format — a compression spectrum from lossless to smallest.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum PagedFormat {
    /// Raw f32 — lossless.
    F32,
    /// Per-output-channel int8 — ~4× smaller, small quality cost.
    Q8Ch,
    /// Per-output-channel int4 — ~8× smaller, larger (still modest) quality cost.
    Q4Ch,
    /// Group-wise int4 (group `PAGED_GROUP`) — ~8× smaller at ~half the drift of `Q4Ch` (the
    /// best low-bit quality-per-byte).
    Q4G,
    /// Group-wise int3 (group `PAGED_GROUP`) — the smallest tier (~8-9×), quality-frontier.
    Q3G,
}

/// Fixed scale-group for the paged group-wise formats (keeps the on-disk stride fixed).
const PAGED_GROUP: usize = 64;

impl PagedFormat {
    fn code(self) -> u32 {
        match self {
            PagedFormat::F32 => 0,
            PagedFormat::Q8Ch => 1,
            PagedFormat::Q4Ch => 2,
            PagedFormat::Q4G => 3,
            PagedFormat::Q3G => 4,
        }
    }
    fn from_code(c: u32) -> io::Result<Self> {
        match c {
            0 => Ok(PagedFormat::F32),
            1 => Ok(PagedFormat::Q8Ch),
            2 => Ok(PagedFormat::Q4Ch),
            3 => Ok(PagedFormat::Q4G),
            4 => Ok(PagedFormat::Q3G),
            _ => Err(io::Error::new(io::ErrorKind::InvalidData, "unknown paged format")),
        }
    }
}

const MAGIC: &[u8; 8] = b"GEPAGE02";
const HEADER_BYTES: u64 = 8 + 4 * 5; // magic + format + 4×u32 dims

#[derive(Default, Clone, Copy, Debug)]
pub struct PagedMetrics {
    pub hits: u64,
    pub cold_reads: u64,
    pub bytes_read: u64,
    pub peak_resident_experts: usize,
}

struct Entry {
    // Arc so a session can clone the handle out from under the cache lock and compute on it while
    // another session (or a prefetch) evicts the cache slot — the data lives until the last user drops.
    w: Arc<ExpertWeights>,
    clock: u64,
}

/// Per-layer bounded LRU of decoded experts held in RAM. Each layer keeps at most `cap` experts
/// resident — the same per-layer residency model as the validated engine scheduler, so the
/// hit-rate curve matches (`Config::lru(cap)`): cap 16→~57%, 24→~71%, 32→~82% on OLMoE.
struct PagedCache {
    per_layer: Vec<HashMap<u16, Entry>>,
    clock: u64,
    cap: usize,  // experts resident PER LAYER
    peak: usize, // peak total resident experts across all layers
}

impl PagedCache {
    fn new(cap: usize, layers: usize) -> Self {
        PagedCache {
            per_layer: (0..layers).map(|_| HashMap::new()).collect(),
            clock: 0,
            cap: cap.max(1),
            peak: 0,
        }
    }
    fn make_room(&mut self, layer: usize) {
        let m = &mut self.per_layer[layer];
        if m.len() < self.cap {
            return;
        }
        if let Some((&victim, _)) = m.iter().min_by_key(|(_, e)| e.clock) {
            m.remove(&victim);
        }
    }
    fn total_resident(&self) -> usize {
        self.per_layer.iter().map(|m| m.len()).sum()
    }
}

/// Expert weights stored on disk, materialized into a bounded per-layer RAM cache.
pub struct PagedWeightStore {
    file: Mutex<File>,
    fmt: PagedFormat,
    layers: usize,
    experts: usize,
    hidden: usize,
    inter: usize,
    mat: usize,    // elements per weight matrix (hidden·inter)
    stride: usize, // bytes per expert on disk == bytes per expert in the RAM cache
    cache: Mutex<PagedCache>,
    metrics: Mutex<PagedMetrics>,
}

/// Bytes one expert occupies on disk / in the cache for a given format.
fn expert_stride(fmt: PagedFormat, hidden: usize, inter: usize) -> usize {
    let mat = hidden * inter;
    match fmt {
        PagedFormat::F32 => 3 * mat * 4,
        // three matrices: gate/up have `inter` output channels, down has `hidden`; each stores
        // `out` f32 scales + `mat` int8 values (Q8Ch) or ceil(mat/2) packed nibbles (Q4Ch).
        PagedFormat::Q8Ch => 3 * mat + (2 * inter + hidden) * 4,
        PagedFormat::Q4Ch => 3 * mat.div_ceil(2) + (2 * inter + hidden) * 4,
        // group-wise: scales are per-tensor (mat/group groups per matrix, same mat for all three).
        PagedFormat::Q4G => 3 * (mat.div_ceil(2) + mat.div_ceil(PAGED_GROUP) * 4),
        PagedFormat::Q3G => 3 * ((3 * mat).div_ceil(8) + 1 + mat.div_ceil(PAGED_GROUP) * 4),
    }
}

impl PagedWeightStore {
    /// Serialize an in-RAM store to a paged file. `F32` is lossless; `Q8Ch` applies per-channel
    /// int8 "green compression" (~4× smaller on disk and in the RAM cache).
    pub fn create<P: AsRef<Path>>(path: P, store: &WeightStore, fmt: PagedFormat) -> io::Result<()> {
        let mut f = File::create(path)?;
        f.write_all(MAGIC)?;
        f.write_all(&fmt.code().to_le_bytes())?;
        for v in [store.layers, store.experts, store.hidden, store.inter] {
            f.write_all(&(v as u32).to_le_bytes())?;
        }
        let (hidden, inter) = (store.hidden, store.inter);
        let mut buf: Vec<u8> = Vec::with_capacity(expert_stride(fmt, hidden, inter));
        for layer in 0..store.layers {
            for e in 0..store.experts {
                let w = store.get(layer, e as u16);
                buf.clear();
                // gate/up have `inter` output channels; down has `hidden`.
                for (t, out) in [(&w.gate, inter), (&w.up, inter), (&w.down, hidden)] {
                    let f32v = t.to_f32_vec();
                    match fmt {
                        PagedFormat::F32 => {
                            for x in &f32v {
                                buf.extend_from_slice(&x.to_le_bytes());
                            }
                        }
                        PagedFormat::Q8Ch => {
                            if let Tensor::Q8Ch { q, scales, .. } = Tensor::quantize_q8_ch(&f32v, out) {
                                for s in &scales {
                                    buf.extend_from_slice(&s.to_le_bytes());
                                }
                                for &qi in &q {
                                    buf.push(qi as u8);
                                }
                            }
                        }
                        PagedFormat::Q4Ch => {
                            if let Tensor::Q4Ch { packed, scales, .. } = Tensor::quantize_q4_ch(&f32v, out) {
                                for s in &scales {
                                    buf.extend_from_slice(&s.to_le_bytes());
                                }
                                buf.extend_from_slice(&packed);
                            }
                        }
                        PagedFormat::Q4G => {
                            if let Tensor::Q4G { packed, scales, .. } = Tensor::quantize_q4_group(&f32v, PAGED_GROUP) {
                                for s in &scales {
                                    buf.extend_from_slice(&s.to_le_bytes());
                                }
                                buf.extend_from_slice(&packed);
                            }
                        }
                        PagedFormat::Q3G => {
                            if let Tensor::Q3G { packed, scales, .. } = Tensor::quantize_q3_group(&f32v, PAGED_GROUP) {
                                for s in &scales {
                                    buf.extend_from_slice(&s.to_le_bytes());
                                }
                                buf.extend_from_slice(&packed);
                            }
                        }
                    }
                }
                f.write_all(&buf)?;
            }
        }
        Ok(())
    }

    /// Open a paged file, materializing at most `ram_experts` experts PER LAYER in RAM.
    pub fn open<P: AsRef<Path>>(path: P, ram_experts: usize) -> io::Result<Self> {
        let mut f = File::open(path)?;
        let mut magic = [0u8; 8];
        f.read_exact(&mut magic)?;
        if &magic != MAGIC {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "bad paged-store magic"));
        }
        let mut u = [0u8; 4];
        f.read_exact(&mut u)?;
        let fmt = PagedFormat::from_code(u32::from_le_bytes(u))?;
        let mut dims = [0u32; 4];
        for d in dims.iter_mut() {
            f.read_exact(&mut u)?;
            *d = u32::from_le_bytes(u);
        }
        let (layers, experts, hidden, inter) =
            (dims[0] as usize, dims[1] as usize, dims[2] as usize, dims[3] as usize);
        Ok(PagedWeightStore {
            file: Mutex::new(f),
            fmt,
            layers,
            experts,
            hidden,
            inter,
            mat: hidden * inter,
            stride: expert_stride(fmt, hidden, inter),
            cache: Mutex::new(PagedCache::new(ram_experts, layers)),
            metrics: Mutex::new(PagedMetrics::default()),
        })
    }

    #[inline]
    fn key(&self, layer: usize, expert: u16) -> u64 {
        (layer * self.experts + expert as usize) as u64
    }

    fn read_expert(&self, layer: usize, expert: u16) -> io::Result<ExpertWeights> {
        let offset = HEADER_BYTES + self.key(layer, expert) * self.stride as u64;
        let mut bytes = vec![0u8; self.stride];
        {
            let mut f = self.file.lock().unwrap();
            f.seek(SeekFrom::Start(offset))?;
            f.read_exact(&mut bytes)?;
        }
        let mat = self.mat;
        let mut cur = 0usize;
        let mut read_tensor = |out: usize| -> Tensor {
            match self.fmt {
                PagedFormat::F32 => {
                    let mut v = Vec::with_capacity(mat);
                    for _ in 0..mat {
                        v.push(f32::from_le_bytes([bytes[cur], bytes[cur + 1], bytes[cur + 2], bytes[cur + 3]]));
                        cur += 4;
                    }
                    Tensor::F32(v)
                }
                PagedFormat::Q8Ch => {
                    let mut scales = Vec::with_capacity(out);
                    for _ in 0..out {
                        scales.push(f32::from_le_bytes([bytes[cur], bytes[cur + 1], bytes[cur + 2], bytes[cur + 3]]));
                        cur += 4;
                    }
                    let mut q = Vec::with_capacity(mat);
                    for _ in 0..mat {
                        q.push(bytes[cur] as i8);
                        cur += 1;
                    }
                    Tensor::Q8Ch { q, scales, out }
                }
                PagedFormat::Q4Ch => {
                    let mut scales = Vec::with_capacity(out);
                    for _ in 0..out {
                        scales.push(f32::from_le_bytes([bytes[cur], bytes[cur + 1], bytes[cur + 2], bytes[cur + 3]]));
                        cur += 4;
                    }
                    let nbytes = mat.div_ceil(2);
                    let packed = bytes[cur..cur + nbytes].to_vec();
                    cur += nbytes;
                    Tensor::Q4Ch { packed, scales, out, n: mat }
                }
                PagedFormat::Q4G => {
                    let ng = mat.div_ceil(PAGED_GROUP);
                    let mut scales = Vec::with_capacity(ng);
                    for _ in 0..ng {
                        scales.push(f32::from_le_bytes([bytes[cur], bytes[cur + 1], bytes[cur + 2], bytes[cur + 3]]));
                        cur += 4;
                    }
                    let nbytes = mat.div_ceil(2);
                    let packed = bytes[cur..cur + nbytes].to_vec();
                    cur += nbytes;
                    Tensor::Q4G { packed, scales, group: PAGED_GROUP, n: mat }
                }
                PagedFormat::Q3G => {
                    let ng = mat.div_ceil(PAGED_GROUP);
                    let mut scales = Vec::with_capacity(ng);
                    for _ in 0..ng {
                        scales.push(f32::from_le_bytes([bytes[cur], bytes[cur + 1], bytes[cur + 2], bytes[cur + 3]]));
                        cur += 4;
                    }
                    let nbytes = (3 * mat).div_ceil(8) + 1;
                    let packed = bytes[cur..cur + nbytes].to_vec();
                    cur += nbytes;
                    Tensor::Q3G { packed, scales, group: PAGED_GROUP, n: mat }
                }
            }
        };
        Ok(ExpertWeights {
            hidden: self.hidden,
            inter: self.inter,
            gate: read_tensor(self.inter),
            up: read_tensor(self.inter),
            down: read_tensor(self.hidden),
        })
    }

    pub fn format(&self) -> PagedFormat {
        self.fmt
    }
    pub fn metrics(&self) -> PagedMetrics {
        *self.metrics.lock().unwrap()
    }
    /// Full model size on disk (bytes).
    pub fn full_bytes(&self) -> u64 {
        self.layers as u64 * self.experts as u64 * self.stride as u64
    }
    /// Hard cap on resident RAM: `cap_per_layer · layers · expert_bytes`.
    pub fn resident_cap_bytes(&self) -> u64 {
        self.cache.lock().unwrap().cap as u64 * self.layers as u64 * self.stride as u64
    }
    pub fn expert_stride_bytes(&self) -> u64 {
        self.stride as u64
    }
}

impl ExpertProvider for PagedWeightStore {
    fn layers(&self) -> usize {
        self.layers
    }
    fn experts(&self) -> usize {
        self.experts
    }
    fn hidden(&self) -> usize {
        self.hidden
    }
    fn inter(&self) -> usize {
        self.inter
    }
    fn expert_bytes(&self, _layer: usize, _expert: u16) -> usize {
        self.stride
    }
    fn with_expert(&self, layer: usize, expert: u16, f: &mut dyn FnMut(&ExpertWeights)) {
        // Resolve the expert to an Arc handle, then DROP the cache lock before computing so other
        // sessions run their experts concurrently. The disk read still happens under the lock
        // (cold reads serialize — they're I/O-bound and hidden by prefetch), but compute does not.
        let w: Arc<ExpertWeights> = {
            let mut cache = self.cache.lock().unwrap();
            cache.clock += 1;
            let clock = cache.clock;
            if let Some(e) = cache.per_layer[layer].get_mut(&expert) {
                e.clock = clock;
                self.metrics.lock().unwrap().hits += 1;
                e.w.clone()
            } else {
                let w = Arc::new(self.read_expert(layer, expert).expect("paged expert read"));
                cache.make_room(layer);
                cache.per_layer[layer].insert(expert, Entry { w: w.clone(), clock });
                let peak = cache.total_resident();
                cache.peak = cache.peak.max(peak);
                let mut m = self.metrics.lock().unwrap();
                m.cold_reads += 1;
                m.bytes_read += self.stride as u64;
                m.peak_resident_experts = cache.peak;
                w
            }
        };
        f(&w);
    }
}

/// Warm `experts` into the store's cache ahead of use (used by [`Prefetcher`] and callable directly
/// for demand-driven "load only what the next tokens need"). A no-op closure just triggers the load.
impl PagedWeightStore {
    pub fn prefetch(&self, layer: usize, experts: &[u16]) {
        for &e in experts {
            self.with_expert(layer, e, &mut |_| {});
        }
    }
}

/// Dense MoE output over any `ExpertProvider` — proves paged == in-RAM (F32) or measures drift (Q8Ch).
pub fn dense_provider<B: ExpertBackend, P: ExpertProvider>(
    provider: &P,
    backend: &B,
    layer: usize,
    x: &[f32],
    experts: &[u16],
    gates: &[f32],
) -> Vec<f32> {
    let (h, inter) = (provider.hidden(), provider.inter());
    let mut scratch = Scratch::new(h, inter);
    let mut tmp = vec![0.0f32; h];
    let mut out = vec![0.0f32; h];
    for (k, &e) in experts.iter().enumerate() {
        provider.with_expert(layer, e, &mut |w| backend.compute_expert(w, x, &mut scratch, &mut tmp));
        let g = gates[k];
        for o in 0..h {
            out[o] += g * tmp[o];
        }
    }
    out
}

/// Background expert prefetcher — overlaps disk/decode I/O with compute.
///
/// The research-consistent bottleneck in expert offload is that loading an expert costs far more
/// than computing it; the fix is to fetch the experts the *next* tokens/layers will need while the
/// current ones compute. `Prefetcher` owns a worker thread and an `Arc` to any thread-safe
/// `ExpertProvider`; `request` hands it a predicted expert set (non-blocking), and the worker warms
/// them into the store's bounded cache so the compute thread finds them resident (a cache hit).
///
/// This is the "load only what it needs" path: the scheduler/predictor decides which experts to
/// request, so cold experts are never fetched and the working set stays minimal.
pub struct Prefetcher {
    tx: Option<std::sync::mpsc::Sender<(usize, Vec<u16>)>>,
    handle: Option<std::thread::JoinHandle<()>>,
}

impl Prefetcher {
    /// Spawn a worker that warms experts into `store`'s cache off the compute thread.
    pub fn new<P: ExpertProvider + Send + Sync + 'static>(store: Arc<P>) -> Self {
        let (tx, rx) = std::sync::mpsc::channel::<(usize, Vec<u16>)>();
        let handle = std::thread::spawn(move || {
            while let Ok((layer, experts)) = rx.recv() {
                for e in experts {
                    store.with_expert(layer, e, &mut |_| {}); // warm only — no compute
                }
            }
        });
        Prefetcher { tx: Some(tx), handle: Some(handle) }
    }

    /// Queue a predicted expert set to warm (non-blocking). Dropped silently if the worker is gone.
    pub fn request(&self, layer: usize, experts: Vec<u16>) {
        if let Some(tx) = &self.tx {
            let _ = tx.send((layer, experts));
        }
    }
}

impl Drop for Prefetcher {
    fn drop(&mut self) {
        self.tx.take(); // close the channel so the worker's recv() returns Err and it exits
        if let Some(h) = self.handle.take() {
            let _ = h.join(); // ensure all queued prefetches complete before we return
        }
    }
}

/// The live predictive-prefetch loop: composes the [`crate::predictor::LayerAheadPredictor`] with a
/// [`Prefetcher`] over a shared store. As each layer's experts become known, it predicts the next
/// layer's experts and warms them off-thread — turning the predictor's ~60-83% recall into overlapped
/// load latency. Call `on_layer(l, cur)` while computing layer `l` (fires the prefetch for `l+1`),
/// then `observe(l, cur, next)` once `l+1` is known (causal training, no leakage).
pub struct PredictivePrefetcher {
    predictor: crate::predictor::LayerAheadPredictor,
    prefetcher: Prefetcher,
    layers: usize,
}

impl PredictivePrefetcher {
    pub fn new<P: ExpertProvider + Send + Sync + 'static>(store: Arc<P>, layers: usize, experts: usize, k: usize) -> Self {
        PredictivePrefetcher {
            predictor: crate::predictor::LayerAheadPredictor::new(layers, experts, k),
            prefetcher: Prefetcher::new(store),
            layers,
        }
    }

    /// While computing layer `l`, predict and warm layer `l+1`'s experts (non-blocking).
    pub fn on_layer(&mut self, l: usize, cur: &[u16]) {
        if l + 1 < self.layers {
            let pred = self.predictor.predict(l, cur);
            if !pred.is_empty() {
                self.prefetcher.request(l + 1, pred);
            }
        }
    }

    /// Train the predictor once the next layer's experts are known (call after `on_layer`).
    pub fn observe(&mut self, l: usize, cur: &[u16], next: &[u16]) {
        self.predictor.observe(l, cur, next);
    }

    /// The predicted expert set for layer `l+1` given layer `l` (what `on_layer` would prefetch).
    pub fn predict(&mut self, l: usize, cur: &[u16]) -> Vec<u16> {
        self.predictor.predict(l, cur)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::CpuBackend;

    fn lcg(state: &mut u64) -> f32 {
        *state ^= *state >> 12;
        *state ^= *state << 25;
        *state ^= *state >> 27;
        (state.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
    }

    fn routing(experts: usize, k: usize, seed: &mut u64) -> (Vec<u16>, Vec<f32>) {
        let mut chosen = Vec::new();
        while chosen.len() < k {
            let e = ((lcg(seed) + 0.5) * experts as f32) as u16 % experts as u16;
            if !chosen.contains(&e) {
                chosen.push(e);
            }
        }
        (chosen, vec![1.0f32 / k as f32; k])
    }

    /// F32 paged store: output must be *bit-for-bit* identical to all-in-RAM, RAM stays bounded.
    #[test]
    fn paged_f32_is_lossless_and_ram_bounded() {
        let (layers, experts, h, inter, k) = (2usize, 24usize, 48usize, 96usize, 6usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 11);
        let path = std::env::temp_dir().join(format!("ge_paged_f32_{}.bin", std::process::id()));
        PagedWeightStore::create(&path, &store, PagedFormat::F32).unwrap();
        let ram = 5usize; // per-layer cap « 24 experts/layer
        let paged = PagedWeightStore::open(&path, ram).unwrap();

        let backend = CpuBackend;
        let mut seed = 3u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        for t in 0..60 {
            let (chosen, gates) = routing(experts, k, &mut seed);
            let layer = t % layers;
            let want = dense_provider(&store, &backend, layer, &x, &chosen, &gates);
            let got = dense_provider(&paged, &backend, layer, &x, &chosen, &gates);
            assert_eq!(want, got, "F32 paged must be bit-identical to in-RAM at token {t}");
        }
        let m = paged.metrics();
        assert!(m.peak_resident_experts <= ram * layers, "resident {} > cap {}", m.peak_resident_experts, ram * layers);
        assert!(m.cold_reads > 0, "should have paged from disk");
        let _ = std::fs::remove_file(&path);
    }

    /// Group-wise paged formats (Q4G/Q3G): disk decode must reproduce the same in-RAM group quant
    /// EXACTLY (isolates disk I/O + packing from quant loss), be smaller than f32, and stay bounded.
    #[test]
    fn paged_group_formats_roundtrip_exact() {
        let (layers, experts, h, inter, k) = (1usize, 16usize, 64usize, 128usize, 8usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 5);
        let backend = CpuBackend;
        let mut seed = 7u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        let (chosen, gates) = routing(experts, k, &mut seed);
        let f32_bytes = layers as u64 * experts as u64 * (3 * h * inter * 4) as u64;

        for fmt in [PagedFormat::Q4G, PagedFormat::Q3G] {
            let path = std::env::temp_dir().join(format!("ge_paged_grp_{:?}_{}.bin", fmt, std::process::id()));
            PagedWeightStore::create(&path, &store, fmt).unwrap();
            let paged = PagedWeightStore::open(&path, 4).unwrap();
            assert!(paged.full_bytes() * 6 < f32_bytes, "{fmt:?} should be ~8× < f32");

            // Reference: the SAME group quant applied in RAM (group = PAGED_GROUP).
            let mut scratch = Scratch::new(h, inter);
            let mut tmp = vec![0.0f32; h];
            let mut ref_out = vec![0.0f32; h];
            for (kk, &e) in chosen.iter().enumerate() {
                let w = store.get(0, e);
                let mk = |v: Vec<f32>| if fmt == PagedFormat::Q4G { Tensor::quantize_q4_group(&v, super::PAGED_GROUP) } else { Tensor::quantize_q3_group(&v, super::PAGED_GROUP) };
                let q = ExpertWeights {
                    hidden: h, inter,
                    gate: mk(w.gate.to_f32_vec()), up: mk(w.up.to_f32_vec()), down: mk(w.down.to_f32_vec()),
                };
                backend.compute_expert(&q, &x, &mut scratch, &mut tmp);
                for o in 0..h { ref_out[o] += gates[kk] * tmp[o]; }
            }
            let got = dense_provider(&paged, &backend, 0, &x, &chosen, &gates);
            assert_eq!(ref_out, got, "{fmt:?} paged must equal in-RAM group quant exactly");
            let _ = std::fs::remove_file(&path);
        }
    }

    /// Multi-session: one Arc-shared paged store driven concurrently by N threads (sessions), each
    /// with its own activations/state, must match the single-threaded result — weights are read-only
    /// and the cache is internally synchronized, so sessions share RAM without corrupting each other.
    #[test]
    fn paged_store_is_shareable_across_sessions() {
        use std::sync::Arc;
        let (layers, experts, h, inter, k) = (2usize, 24usize, 48usize, 96usize, 6usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 11);
        let path = std::env::temp_dir().join(format!("ge_paged_ms_{}.bin", std::process::id()));
        PagedWeightStore::create(&path, &store, PagedFormat::F32).unwrap();
        let paged = Arc::new(PagedWeightStore::open(&path, 6).unwrap());

        // Reference outputs (single-threaded) for a set of distinct per-session inputs.
        let backend = CpuBackend;
        let sessions = 4;
        let mut want = Vec::new();
        let mut inputs = Vec::new();
        for sfrom in 0..sessions {
            let mut seed = 100 + sfrom as u64;
            let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
            let (chosen, gates) = routing(experts, k, &mut seed);
            want.push(dense_provider(&*paged, &backend, sfrom % layers, &x, &chosen, &gates));
            inputs.push((sfrom % layers, x, chosen, gates));
        }

        // Now run all sessions concurrently against the SAME shared store.
        let mut handles = Vec::new();
        for (s, (layer, x, chosen, gates)) in inputs.into_iter().enumerate() {
            let paged = Arc::clone(&paged);
            handles.push(std::thread::spawn(move || {
                let backend = CpuBackend;
                let mut out = Vec::new();
                for _ in 0..10 {
                    out = dense_provider(&*paged, &backend, layer, &x, &chosen, &gates);
                }
                (s, out)
            }));
        }
        for hd in handles {
            let (s, out) = hd.join().unwrap();
            assert_eq!(out, want[s], "session {s} concurrent output must equal single-threaded");
        }
        let _ = std::fs::remove_file(&path);
    }

    /// Live predictive-prefetch loop: the predictor drives the prefetcher across layers. After a
    /// warm-up it predicts most of the next layer's experts, so they're resident when computed.
    #[test]
    fn predictive_prefetch_loop_warms_predicted_experts() {
        use std::sync::Arc;
        let (layers, experts, h, inter, k) = (4usize, 12usize, 32usize, 64usize, 6usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 3);
        let path = std::env::temp_dir().join(format!("ge_predpf_{}.bin", std::process::id()));
        PagedWeightStore::create(&path, &store, PagedFormat::F32).unwrap();
        let paged = Arc::new(PagedWeightStore::open(&path, experts).unwrap());
        let mut pp = PredictivePrefetcher::new(Arc::clone(&paged), layers, experts, k);

        // A repeating routing pattern per layer the predictor can learn.
        let pat: Vec<Vec<u16>> = (0..layers).map(|l| (0..k as u16).map(|j| ((l * 2 + j as usize) % experts) as u16).collect()).collect();
        // Train several passes.
        for _ in 0..8 {
            for l in 0..layers {
                pp.on_layer(l, &pat[l]);
                if l + 1 < layers { pp.observe(l, &pat[l], &pat[l + 1]); }
            }
        }
        // Now a fresh pass: prefetch layer l+1 before "computing" it; the predicted set should match.
        let mut correct = 0;
        for l in 0..layers - 1 {
            let pred = pp.predict(l, &pat[l]);
            correct += pat[l + 1].iter().filter(|e| pred.contains(e)).count();
        }
        let total: usize = (0..layers - 1).map(|l| pat[l + 1].len()).sum();
        assert!(correct * 2 >= total, "predictive prefetch should cover most next-layer experts ({correct}/{total})");
        let _ = std::fs::remove_file(&path);
    }

    /// Async prefetch: warming a layer's experts off-thread makes the subsequent compute find them
    /// resident (no cold read during compute), overlapping I/O with the previous layer's work.
    #[test]
    fn prefetch_warms_cache_before_compute() {
        use std::sync::Arc;
        let (layers, experts, h, inter, k) = (1usize, 16usize, 48usize, 96usize, 6usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 7);
        let path = std::env::temp_dir().join(format!("ge_paged_pf_{}.bin", std::process::id()));
        PagedWeightStore::create(&path, &store, PagedFormat::F32).unwrap();
        let paged = Arc::new(PagedWeightStore::open(&path, experts).unwrap()); // cap holds the set

        let mut seed = 5u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        let (chosen, gates) = routing(experts, k, &mut seed);

        // Prefetch the experts we're about to use, then join the worker so warming completes.
        let pf = Prefetcher::new(Arc::clone(&paged));
        pf.request(0, chosen.clone());
        drop(pf); // Drop joins the worker → all prefetches done

        let cold_after_prefetch = paged.metrics().cold_reads;
        assert_eq!(cold_after_prefetch, chosen.len() as u64, "prefetch should have loaded exactly the requested experts");

        // Compute now: every expert is already resident → these are hits, no new cold reads.
        let backend = CpuBackend;
        let _ = dense_provider(&*paged, &backend, 0, &x, &chosen, &gates);
        assert_eq!(paged.metrics().cold_reads, cold_after_prefetch, "compute after prefetch must not cold-read");
        assert!(paged.metrics().hits >= chosen.len() as u64, "prefetched experts should hit on compute");
        let _ = std::fs::remove_file(&path);
    }

    /// Q8Ch paged store: ~4× smaller on disk AND in RAM, with small drift and lower error than
    /// per-tensor Q8 (per-channel scales preserve the distribution).
    #[test]
    fn paged_q8ch_is_small_and_high_fidelity() {
        let (layers, experts, h, inter, k) = (1usize, 16usize, 64usize, 128usize, 8usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 5);
        let path = std::env::temp_dir().join(format!("ge_paged_q8_{}.bin", std::process::id()));
        PagedWeightStore::create(&path, &store, PagedFormat::Q8Ch).unwrap();
        let paged = PagedWeightStore::open(&path, 4).unwrap();

        // ~4× smaller than the f32 model.
        let f32_bytes = layers as u64 * experts as u64 * (3 * h * inter * 4) as u64;
        assert!(paged.full_bytes() * 3 < f32_bytes, "Q8Ch {} should be ~4× < f32 {f32_bytes}", paged.full_bytes());

        let backend = CpuBackend;
        let mut seed = 7u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        let (chosen, gates) = routing(experts, k, &mut seed);
        let want = dense_provider(&store, &backend, 0, &x, &chosen, &gates);
        let got = dense_provider(&paged, &backend, 0, &x, &chosen, &gates);

        let num: f32 = want.iter().zip(&got).map(|(a, b)| (a - b).powi(2)).sum();
        let den: f32 = want.iter().map(|a| a * a).sum::<f32>().max(1e-9);
        let rel = (num / den).sqrt();
        // Per-channel int8: high fidelity (well under the per-tensor Q8 bound of 0.1).
        assert!(rel < 0.02, "per-channel Q8 drift {rel:.4} should be small");

        // Compare: per-channel must beat per-tensor Q8 at equal bits.
        let q8_store = WeightStore::synthetic(layers, experts, h, inter, true, 5); // per-tensor Q8
        let got_pt = dense_provider(&q8_store, &backend, 0, &x, &chosen, &gates);
        let num_pt: f32 = want.iter().zip(&got_pt).map(|(a, b)| (a - b).powi(2)).sum();
        let rel_pt = (num_pt / den).sqrt();
        assert!(rel <= rel_pt, "per-channel {rel:.4} should be ≤ per-tensor {rel_pt:.4}");
        let _ = std::fs::remove_file(&path);
    }

    /// Q4Ch paged store: ~8× smaller than f32 (0.5 byte/weight). The disk+packing roundtrip is
    /// EXACT (paged == the same int4 quantization computed in RAM); the *drift vs f32* is data-
    /// dependent int4 error — larger than Q8Ch, and worst-case here because synthetic weights are
    /// uniform max-entropy noise (real structured LLM weights quantize much better).
    #[test]
    fn paged_q4ch_roundtrip_exact_and_tiny() {
        use crate::weights::ExpertWeights;
        let (layers, experts, h, inter, k) = (1usize, 16usize, 64usize, 128usize, 8usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 5);
        let path = std::env::temp_dir().join(format!("ge_paged_q4_{}.bin", std::process::id()));
        PagedWeightStore::create(&path, &store, PagedFormat::Q4Ch).unwrap();
        let paged = PagedWeightStore::open(&path, 4).unwrap();

        // ~8× smaller than the f32 model (int4 = 0.5 byte/weight + small per-channel scale overhead).
        let f32_bytes = layers as u64 * experts as u64 * (3 * h * inter * 4) as u64;
        assert!(paged.full_bytes() * 6 < f32_bytes, "Q4Ch {} should be ~8× < f32 {f32_bytes}", paged.full_bytes());

        let backend = CpuBackend;
        let mut seed = 7u64;
        let x: Vec<f32> = (0..h).map(|_| lcg(&mut seed)).collect();
        let (chosen, gates) = routing(experts, k, &mut seed);

        // Reference: the SAME per-channel int4 applied in RAM. Paging must reproduce it exactly —
        // this isolates disk-I/O + nibble packing correctness from int4's inherent quality loss.
        let mut scratch = Scratch::new(h, inter);
        let mut tmp = vec![0.0f32; h];
        let mut ref_out = vec![0.0f32; h];
        for (kk, &e) in chosen.iter().enumerate() {
            let w = store.get(0, e);
            let q = ExpertWeights {
                hidden: h,
                inter,
                gate: Tensor::quantize_q4_ch(&w.gate.to_f32_vec(), inter),
                up: Tensor::quantize_q4_ch(&w.up.to_f32_vec(), inter),
                down: Tensor::quantize_q4_ch(&w.down.to_f32_vec(), h),
            };
            backend.compute_expert(&q, &x, &mut scratch, &mut tmp);
            for o in 0..h {
                ref_out[o] += gates[kk] * tmp[o];
            }
        }
        let got = dense_provider(&paged, &backend, 0, &x, &chosen, &gates);
        assert_eq!(ref_out, got, "paged Q4Ch must equal in-RAM Q4Ch exactly (packing/roundtrip)");

        // Drift vs f32 is bounded (honest: synthetic uniform is int4's worst case).
        let want = dense_provider(&store, &backend, 0, &x, &chosen, &gates);
        let num: f32 = want.iter().zip(&got).map(|(a, b)| (a - b).powi(2)).sum();
        let den: f32 = want.iter().map(|a| a * a).sum::<f32>().max(1e-9);
        let rel = (num / den).sqrt();
        assert!(rel < 0.2, "int4 drift {rel:.4} bounded");
        let _ = std::fs::remove_file(&path);
    }
}
