//! Green Compress tensor tier (`--features green`).
//!
//! Bridges the sibling **Green Compress** crate into the engine as a compressed expert weight
//! format. Where the engine's own [`crate::tensor`]/[`crate::weights`] tiers are plain per-channel
//! Q8/Q4, Green Compress adds *repair* — a low-rank + sparse correction fitted to the quantization
//! residual — which recovers most of the quality quantization drops. That means, for the same
//! quality, fewer bytes (smaller RAM/VRAM working set); for the same bytes, higher fidelity.
//!
//! A [`GreenTensor`] holds the Q8 base plus an optional [`Repair`] and reconstructs to f32 on
//! demand (`reconstruct_into`), so it slots into the engine's page-in path: the cold/RAM tier
//! stores the compact Green form, and an expert is reconstructed to f32 when it enters the working
//! set (then optionally narrowed to the GPU residency cache's fp16/int8 slot). The compression
//! itself (SVD/sparse fit) is Green Compress's job; the engine only stores and applies it.

use std::collections::HashMap;
use std::fs::File;
use std::io::{self, Read, Seek, SeekFrom, Write};
use std::path::Path;
use std::sync::{Arc, Mutex};

use greencompress::q4::{dequantize_q4, quantize_q4, reconstruct as reconstruct_q4};
use greencompress::q8::{dequantize_q8, quantize_q8, reconstruct_q8};
use greencompress::repair::fit_repair;
use greencompress::types::{f16, LowRankTerm, Matrix, Q4Matrix, Q8Matrix, Repair, SparseScoreMode, SparseTerm};

use crate::paged::ExpertProvider;
use crate::weights::{ExpertWeights, Tensor, WeightStore};

/// The quantized base a Green tensor is built on. Q4 halves the base bytes vs Q8 but loses more to
/// quantization — which is exactly what repair is for (rescuing Q4 back to high fidelity).
enum Base {
    Q8(Q8Matrix),
    Q4(Q4Matrix),
}

/// One weight matrix compressed with Green Compress: a Q8 or Q4 base + optional low-rank/sparse
/// repair fitted to the quantization residual.
pub struct GreenTensor {
    base: Base,
    pub repair: Option<Repair>,
    pub rows: u32,
    pub cols: u32,
}

/// Fit repair to a residual, mirroring `greencompress` cmd_repair defaults (iters 8, seed 3,
/// low-rank-first, magnitude sparse). `rank == 0 && sparse_frac == 0` ⇒ no repair.
fn fit(residual: Matrix, rank: u32, sparse_frac: f32) -> Option<Repair> {
    if rank == 0 && sparse_frac <= 0.0 {
        return None;
    }
    Some(fit_repair(
        residual, rank, 8, 3, sparse_frac, false,
        SparseScoreMode::Magnitude, None, 0, false, 1, None,
    ))
}

impl GreenTensor {
    /// Compress row-major `[rows, cols]` on a **Q8** base. `block` = Q8 scale block (e.g. 64).
    pub fn compress(w: &[f32], rows: usize, cols: usize, block: u32, rank: u32, sparse_frac: f32) -> Self {
        assert_eq!(w.len(), rows * cols, "GreenTensor::compress: len != rows*cols");
        let m = Matrix { rows: rows as u32, cols: cols as u32, data: w.to_vec() };
        let q8 = quantize_q8(&m, block);
        let repair = fit(residual(&m, &dequantize_q8(&q8)), rank, sparse_frac);
        GreenTensor { base: Base::Q8(q8), repair, rows: rows as u32, cols: cols as u32 }
    }

    /// Compress row-major `[rows, cols]` on a **Q4** base — half the base bytes; pair with repair
    /// (`rank`/`sparse_frac` > 0) to recover the quality int4 alone loses.
    pub fn compress_q4(w: &[f32], rows: usize, cols: usize, block: u32, rank: u32, sparse_frac: f32) -> Self {
        assert_eq!(w.len(), rows * cols, "GreenTensor::compress_q4: len != rows*cols");
        let m = Matrix { rows: rows as u32, cols: cols as u32, data: w.to_vec() };
        let q4 = quantize_q4(&m, block);
        let repair = fit(residual(&m, &dequantize_q4(&q4)), rank, sparse_frac);
        GreenTensor { base: Base::Q4(q4), repair, rows: rows as u32, cols: cols as u32 }
    }

    /// Reconstruct the full f32 matrix (base dequant + repair), row-major `[rows, cols]`.
    pub fn to_f32_vec(&self) -> Vec<f32> {
        match &self.base {
            Base::Q8(q8) => reconstruct_q8(q8, self.repair.as_ref()).data,
            Base::Q4(q4) => reconstruct_q4(q4, self.repair.as_ref()).data,
        }
    }

    /// Reconstruct into a caller-owned `rows*cols` buffer (the engine's page-in target). Green
    /// Compress reconstructs into a fresh `Matrix` internally, so this still allocates once per
    /// call today; kept as the seam for a future alloc-free `reconstruct` in the sibling crate.
    pub fn reconstruct_into(&self, out: &mut [f32]) {
        out.copy_from_slice(&self.to_f32_vec());
    }

    /// Compact (stored) size in bytes: base payload + scales + repair (low-rank factors + sparse
    /// triples). This is what the resident/cold tier actually pays.
    pub fn bytes(&self) -> usize {
        let base = match &self.base {
            Base::Q8(q8) => q8.packed.len() + q8.scales.len() * 2, // f16 scales
            Base::Q4(q4) => q4.packed.len() + q4.scales.len() * 4,
        };
        let rep = self.repair.as_ref().map_or(0, |r| {
            let lr: usize = r.low_rank.iter().map(|t| 4 + t.u.len() * 4 + t.v.len() * 4).sum();
            lr + r.sparse.len() * (8 + 4) // SparseTerm = u64 index + f32 value
        });
        base + rep
    }
}

/// residual = original − dequant(base), the target repair is fitted against.
fn residual(orig: &Matrix, deq: &Matrix) -> Matrix {
    let mut r = Matrix { rows: orig.rows, cols: orig.cols, data: vec![0.0; orig.data.len()] };
    for i in 0..orig.data.len() {
        r.data[i] = orig.data[i] - deq.data[i];
    }
    r
}

// --- Compact little-endian serialization for the on-disk green paged format --------------------
fn put_u32(b: &mut Vec<u8>, v: u32) { b.extend_from_slice(&v.to_le_bytes()); }
fn put_f32(b: &mut Vec<u8>, v: f32) { b.extend_from_slice(&v.to_le_bytes()); }
fn put_f32s(b: &mut Vec<u8>, v: &[f32]) { put_u32(b, v.len() as u32); for &x in v { put_f32(b, x); } }

/// Cursor over a byte slice; every getter advances past what it read.
struct Cur<'a> { b: &'a [u8], p: usize }
impl<'a> Cur<'a> {
    fn u32(&mut self) -> u32 { let v = u32::from_le_bytes(self.b[self.p..self.p + 4].try_into().unwrap()); self.p += 4; v }
    fn u64(&mut self) -> u64 { let v = u64::from_le_bytes(self.b[self.p..self.p + 8].try_into().unwrap()); self.p += 8; v }
    fn f32(&mut self) -> f32 { let v = f32::from_le_bytes(self.b[self.p..self.p + 4].try_into().unwrap()); self.p += 4; v }
    fn f32s(&mut self) -> Vec<f32> { let n = self.u32() as usize; (0..n).map(|_| self.f32()).collect() }
    fn i8s(&mut self) -> Vec<i8> { let n = self.u32() as usize; let v = self.b[self.p..self.p + n].iter().map(|&x| x as i8).collect(); self.p += n; v }
    fn u8s(&mut self) -> Vec<u8> { let n = self.u32() as usize; let v = self.b[self.p..self.p + n].to_vec(); self.p += n; v }
}

fn write_repair(b: &mut Vec<u8>, r: &Option<Repair>) {
    match r {
        None => b.push(0),
        Some(r) => {
            b.push(1);
            put_u32(b, r.rows); put_u32(b, r.cols);
            put_u32(b, r.low_rank.len() as u32);
            for t in &r.low_rank { put_f32(b, t.sigma); put_f32s(b, &t.u); put_f32s(b, &t.v); }
            put_u32(b, r.sparse.len() as u32);
            for s in &r.sparse { b.extend_from_slice(&s.index.to_le_bytes()); put_f32(b, s.value); }
        }
    }
}

fn read_repair(c: &mut Cur) -> Option<Repair> {
    if c.b[c.p] == 0 { c.p += 1; return None; }
    c.p += 1;
    let (rows, cols) = (c.u32(), c.u32());
    let nlr = c.u32() as usize;
    let low_rank = (0..nlr).map(|_| LowRankTerm { sigma: c.f32(), u: c.f32s(), v: c.f32s() }).collect();
    let nsp = c.u32() as usize;
    let sparse = (0..nsp).map(|_| SparseTerm { index: c.u64(), value: c.f32() }).collect();
    Some(Repair { rows, cols, low_rank, sparse })
}

impl GreenTensor {
    fn write_to(&self, b: &mut Vec<u8>) {
        put_u32(b, self.rows); put_u32(b, self.cols);
        match &self.base {
            Base::Q8(q) => {
                b.push(0);
                put_u32(b, q.block);
                // scales are f16 in greencompress; store as f32 for a self-contained file.
                put_u32(b, q.scales.len() as u32); for &s in &q.scales { put_f32(b, s.to_f32()); }
                put_u32(b, q.packed.len() as u32); for &x in &q.packed { b.push(x as u8); }
                put_f32s(b, &q.awq_scales); put_f32s(b, &q.row_spin);
            }
            Base::Q4(q) => {
                b.push(1);
                put_u32(b, q.block); put_f32s(b, &q.scales);
                put_u32(b, q.packed.len() as u32); b.extend_from_slice(&q.packed);
            }
        }
        write_repair(b, &self.repair);
    }

    fn read_from(c: &mut Cur) -> GreenTensor {
        let (rows, cols) = (c.u32(), c.u32());
        let tag = c.b[c.p]; c.p += 1;
        let base = if tag == 0 {
            let block = c.u32();
            let scales: Vec<f16> = c.f32s().iter().map(|&x| f16::from_f32(x)).collect();
            let packed = c.i8s();
            let awq_scales = c.f32s(); let row_spin = c.f32s();
            Base::Q8(Q8Matrix { rows, cols, block, scales, packed, awq_scales, row_spin })
        } else {
            let block = c.u32(); let scales = c.f32s(); let packed = c.u8s();
            Base::Q4(Q4Matrix { rows, cols, block, scales, packed })
        };
        let repair = read_repair(c);
        GreenTensor { base, repair, rows, cols }
    }
}

/// Which quantized base a whole store compresses its experts to.
#[derive(Clone, Copy, Debug)]
pub enum GreenBase {
    /// Q8 base — best fidelity, ~2–3× smaller than f32.
    Q8,
    /// Q4 base — smallest (~4–8× with repair); pair with repair to hold quality.
    Q4,
}

/// One expert compressed with Green Compress (gate/up/down as [`GreenTensor`]s).
pub struct GreenExpert {
    pub gate: GreenTensor,
    pub up: GreenTensor,
    pub down: GreenTensor,
}

impl GreenExpert {
    /// Compress a plain f32/quantized `ExpertWeights` into Green form.
    pub fn compress(w: &ExpertWeights, base: GreenBase, block: u32, rank: u32, sparse_frac: f32) -> Self {
        let (h, i) = (w.hidden, w.inter);
        let mk = |t: &Tensor, rows: usize, cols: usize| {
            let v = t.to_f32_vec();
            match base {
                GreenBase::Q8 => GreenTensor::compress(&v, rows, cols, block, rank, sparse_frac),
                GreenBase::Q4 => GreenTensor::compress_q4(&v, rows, cols, block, rank, sparse_frac),
            }
        };
        // gate/up are [hidden, inter]; down is [inter, hidden].
        GreenExpert { gate: mk(&w.gate, h, i), up: mk(&w.up, h, i), down: mk(&w.down, i, h) }
    }

    /// Compact stored size (bytes) of the three compressed matrices.
    pub fn bytes(&self) -> usize {
        self.gate.bytes() + self.up.bytes() + self.down.bytes()
    }

    /// Serialize the three compressed matrices to a self-describing byte blob.
    fn to_bytes(&self) -> Vec<u8> {
        let mut b = Vec::new();
        self.gate.write_to(&mut b);
        self.up.write_to(&mut b);
        self.down.write_to(&mut b);
        b
    }

    /// Inverse of [`Self::to_bytes`].
    fn from_bytes(bytes: &[u8]) -> Self {
        let mut c = Cur { b: bytes, p: 0 };
        let gate = GreenTensor::read_from(&mut c);
        let up = GreenTensor::read_from(&mut c);
        let down = GreenTensor::read_from(&mut c);
        GreenExpert { gate, up, down }
    }

    /// Reconstruct to a full-precision `ExpertWeights` (F32 tensors) the backends run directly.
    fn reconstruct(&self, hidden: usize, inter: usize) -> ExpertWeights {
        ExpertWeights {
            hidden,
            inter,
            gate: Tensor::F32(self.gate.to_f32_vec()),
            up: Tensor::F32(self.up.to_f32_vec()),
            down: Tensor::F32(self.down.to_f32_vec()),
        }
    }
}

#[derive(Default, Clone, Copy, Debug)]
pub struct GreenMetrics {
    /// Decode-cache hits (expert already reconstructed and resident).
    pub decode_hits: u64,
    /// Reconstructions performed (cache misses → Green→f32 decode).
    pub decodes: u64,
    /// Peak simultaneously-resident decoded experts (bounds the f32 working set).
    pub peak_resident_experts: usize,
}

struct DecodeEntry {
    w: Arc<ExpertWeights>, // Arc: clone out from under the lock so sessions compute concurrently
    clock: u64,
}

/// Bounded per-layer LRU of *decoded* (f32) experts — the same residency model as the paged store,
/// so hot experts stay decoded (fast) while the f32 working set is hard-capped.
struct DecodeCache {
    per_layer: Vec<HashMap<u16, DecodeEntry>>,
    clock: u64,
    cap: usize,
    peak: usize,
}

impl DecodeCache {
    fn new(cap: usize, layers: usize) -> Self {
        DecodeCache { per_layer: (0..layers).map(|_| HashMap::new()).collect(), clock: 0, cap: cap.max(1), peak: 0 }
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

/// An in-RAM store of Green-compressed experts that serves the runtime through [`ExpertProvider`].
///
/// Every expert is held **compressed** (Green Q8/Q4 + repair — a fraction of f32), and decoded to
/// f32 on demand into a bounded per-layer LRU. So the resident cost is `all_experts · green_bytes`
/// (small, whole model) plus `cap · layers · f32_expert` (the hot decoded working set) — letting a
/// machine hold a model far larger than its f32 size, at Green's validated fidelity. This is the
/// RAM analog of the disk `PagedWeightStore`, with the compact source living in RAM instead of disk.
pub struct GreenWeightStore {
    experts: Vec<GreenExpert>, // layers*experts, row-major (layer*experts_per_layer + expert)
    layers: usize,
    experts_per_layer: usize,
    hidden: usize,
    inter: usize,
    compressed_bytes: usize,
    cache: Mutex<DecodeCache>,
    metrics: Mutex<GreenMetrics>,
}

impl GreenWeightStore {
    /// Compress an in-RAM `WeightStore` to Green form, keeping at most `ram_experts` decoded experts
    /// per layer. `rank`/`sparse_frac` drive repair (0/0 ⇒ plain quantization, no repair).
    pub fn from_store(
        store: &WeightStore,
        base: GreenBase,
        block: u32,
        rank: u32,
        sparse_frac: f32,
        ram_experts: usize,
    ) -> Self {
        let mut experts = Vec::with_capacity(store.layers * store.experts);
        let mut compressed_bytes = 0usize;
        for layer in 0..store.layers {
            for e in 0..store.experts {
                let ge = GreenExpert::compress(store.get(layer, e as u16), base, block, rank, sparse_frac);
                compressed_bytes += ge.bytes();
                experts.push(ge);
            }
        }
        GreenWeightStore {
            experts,
            layers: store.layers,
            experts_per_layer: store.experts,
            hidden: store.hidden,
            inter: store.inter,
            compressed_bytes,
            cache: Mutex::new(DecodeCache::new(ram_experts, store.layers)),
            metrics: Mutex::new(GreenMetrics::default()),
        }
    }

    /// Total compressed footprint of ALL experts held in RAM (bytes).
    pub fn compressed_bytes(&self) -> usize {
        self.compressed_bytes
    }
    pub fn metrics(&self) -> GreenMetrics {
        *self.metrics.lock().unwrap()
    }

    /// Decode `experts` into the cache ahead of use (demand-driven "load only what's needed").
    pub fn prefetch(&self, layer: usize, experts: &[u16]) {
        for &e in experts {
            self.with_expert(layer, e, &mut |_| {});
        }
    }
}

impl ExpertProvider for GreenWeightStore {
    fn layers(&self) -> usize {
        self.layers
    }
    fn experts(&self) -> usize {
        self.experts_per_layer
    }
    fn hidden(&self) -> usize {
        self.hidden
    }
    fn inter(&self) -> usize {
        self.inter
    }
    fn expert_bytes(&self, layer: usize, expert: u16) -> usize {
        self.experts[layer * self.experts_per_layer + expert as usize].bytes()
    }
    fn with_expert(&self, layer: usize, expert: u16, f: &mut dyn FnMut(&ExpertWeights)) {
        // Reconstruct (or reuse) under the lock, clone the Arc handle, drop the lock, then compute —
        // so multiple sessions decode/run experts concurrently.
        let w: Arc<ExpertWeights> = {
            let mut cache = self.cache.lock().unwrap();
            cache.clock += 1;
            let clock = cache.clock;
            if let Some(e) = cache.per_layer[layer].get_mut(&expert) {
                e.clock = clock;
                self.metrics.lock().unwrap().decode_hits += 1;
                e.w.clone()
            } else {
                let ge = &self.experts[layer * self.experts_per_layer + expert as usize];
                let w = Arc::new(ge.reconstruct(self.hidden, self.inter));
                cache.make_room(layer);
                cache.per_layer[layer].insert(expert, DecodeEntry { w: w.clone(), clock });
                let peak = cache.total_resident();
                cache.peak = cache.peak.max(peak);
                let mut m = self.metrics.lock().unwrap();
                m.decodes += 1;
                m.peak_resident_experts = cache.peak;
                w
            }
        };
        f(&w);
    }
}

const GREEN_MAGIC: &[u8; 8] = b"GEGREEN1";

/// Disk-backed Green store: green-compressed experts on disk with a per-expert **offset table**
/// (repair is variable-size, so the paged store's fixed-stride seek doesn't apply — the table gives
/// O(1) random access anyway). Cold experts are read + reconstructed into a bounded per-layer decode
/// LRU, exactly like [`GreenWeightStore`] but sourced from disk. Thread-safe and prefetchable, so a
/// machine can page a model far larger than its RAM from a compact green file at green fidelity.
///
/// Layout: `[magic:8][layers,experts,hidden,inter: u32×4][offsets: u64×(n+1)][expert blobs…]`.
pub struct GreenPagedStore {
    file: Mutex<File>,
    offsets: Vec<u64>, // n_experts+1 absolute file offsets (last = end of data)
    layers: usize,
    experts_per_layer: usize,
    hidden: usize,
    inter: usize,
    cache: Mutex<DecodeCache>,
    metrics: Mutex<GreenMetrics>,
}

impl GreenPagedStore {
    /// Compress `store` to a green paged file on disk (does not keep it in RAM).
    pub fn create<P: AsRef<Path>>(
        path: P, store: &WeightStore, base: GreenBase, block: u32, rank: u32, sparse_frac: f32,
    ) -> io::Result<()> {
        let n = store.layers * store.experts;
        let mut blobs: Vec<Vec<u8>> = Vec::with_capacity(n);
        for layer in 0..store.layers {
            for e in 0..store.experts {
                let ge = GreenExpert::compress(store.get(layer, e as u16), base, block, rank, sparse_frac);
                blobs.push(ge.to_bytes());
            }
        }
        let data_start = 8 + 4 * 4 + (n as u64 + 1) * 8; // magic + dims + offset table
        let mut offsets = Vec::with_capacity(n + 1);
        let mut cur = data_start;
        for blob in &blobs {
            offsets.push(cur);
            cur += blob.len() as u64;
        }
        offsets.push(cur); // sentinel = end of data

        let mut f = File::create(path)?;
        f.write_all(GREEN_MAGIC)?;
        for v in [store.layers, store.experts, store.hidden, store.inter] {
            f.write_all(&(v as u32).to_le_bytes())?;
        }
        for &o in &offsets {
            f.write_all(&o.to_le_bytes())?;
        }
        for blob in &blobs {
            f.write_all(blob)?;
        }
        Ok(())
    }

    /// Open a green paged file, keeping at most `ram_experts` decoded experts per layer in RAM.
    pub fn open<P: AsRef<Path>>(path: P, ram_experts: usize) -> io::Result<Self> {
        let mut f = File::open(path)?;
        let mut magic = [0u8; 8];
        f.read_exact(&mut magic)?;
        if &magic != GREEN_MAGIC {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "bad green-paged magic"));
        }
        let mut u = [0u8; 4];
        let mut dims = [0u32; 4];
        for d in dims.iter_mut() {
            f.read_exact(&mut u)?;
            *d = u32::from_le_bytes(u);
        }
        let (layers, experts, hidden, inter) =
            (dims[0] as usize, dims[1] as usize, dims[2] as usize, dims[3] as usize);
        let n = layers * experts;
        let mut offsets = Vec::with_capacity(n + 1);
        let mut ub = [0u8; 8];
        for _ in 0..=n {
            f.read_exact(&mut ub)?;
            offsets.push(u64::from_le_bytes(ub));
        }
        Ok(GreenPagedStore {
            file: Mutex::new(f),
            offsets,
            layers,
            experts_per_layer: experts,
            hidden,
            inter,
            cache: Mutex::new(DecodeCache::new(ram_experts, layers)),
            metrics: Mutex::new(GreenMetrics::default()),
        })
    }

    fn read_reconstructed(&self, key: usize) -> ExpertWeights {
        let (start, end) = (self.offsets[key], self.offsets[key + 1]);
        let mut bytes = vec![0u8; (end - start) as usize];
        {
            let mut f = self.file.lock().unwrap();
            f.seek(SeekFrom::Start(start)).expect("green paged seek");
            f.read_exact(&mut bytes).expect("green paged read");
        }
        GreenExpert::from_bytes(&bytes).reconstruct(self.hidden, self.inter)
    }

    /// Total on-disk size of all compressed experts (bytes).
    pub fn disk_bytes(&self) -> u64 {
        *self.offsets.last().unwrap() - self.offsets[0]
    }
    pub fn metrics(&self) -> GreenMetrics {
        *self.metrics.lock().unwrap()
    }
    /// Warm `experts` into the decode cache ahead of use (demand-driven page-in).
    pub fn prefetch(&self, layer: usize, experts: &[u16]) {
        for &e in experts {
            self.with_expert(layer, e, &mut |_| {});
        }
    }
}

impl ExpertProvider for GreenPagedStore {
    fn layers(&self) -> usize { self.layers }
    fn experts(&self) -> usize { self.experts_per_layer }
    fn hidden(&self) -> usize { self.hidden }
    fn inter(&self) -> usize { self.inter }
    fn expert_bytes(&self, layer: usize, expert: u16) -> usize {
        let key = layer * self.experts_per_layer + expert as usize;
        (self.offsets[key + 1] - self.offsets[key]) as usize
    }
    fn with_expert(&self, layer: usize, expert: u16, f: &mut dyn FnMut(&ExpertWeights)) {
        let w: Arc<ExpertWeights> = {
            let mut cache = self.cache.lock().unwrap();
            cache.clock += 1;
            let clock = cache.clock;
            if let Some(e) = cache.per_layer[layer].get_mut(&expert) {
                e.clock = clock;
                self.metrics.lock().unwrap().decode_hits += 1;
                e.w.clone()
            } else {
                let key = layer * self.experts_per_layer + expert as usize;
                let w = Arc::new(self.read_reconstructed(key));
                cache.make_room(layer);
                cache.per_layer[layer].insert(expert, DecodeEntry { w: w.clone(), clock });
                let peak = cache.total_resident();
                cache.peak = cache.peak.max(peak);
                let mut m = self.metrics.lock().unwrap();
                m.decodes += 1;
                m.peak_resident_experts = cache.peak;
                w
            }
        };
        f(&w);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Build a *structured* matrix (low-rank signal + sparse outliers + small noise) — the regime
    /// real transformer weights live in, and where repair earns its keep. Deterministic (no rng dep).
    fn structured(rows: usize, cols: usize, seed: u64) -> Vec<f32> {
        let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
        let mut rng = || {
            s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
            (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
        };
        // rank-4 signal
        let r = 4;
        let u: Vec<f32> = (0..rows * r).map(|_| rng()).collect();
        let v: Vec<f32> = (0..cols * r).map(|_| rng()).collect();
        let mut w = vec![0.0f32; rows * cols];
        for i in 0..rows {
            for j in 0..cols {
                let mut acc = 0.0;
                for k in 0..r {
                    acc += u[i * r + k] * v[j * r + k];
                }
                w[i * cols + j] = acc + 0.02 * rng(); // + small noise
            }
        }
        // a few large outliers
        for t in 0..(rows * cols / 500).max(1) {
            let idx = ((t * 2654435761) % (rows * cols)) as usize;
            w[idx] += 3.0;
        }
        w
    }

    fn rel_l2(a: &[f32], b: &[f32]) -> f32 {
        let mut num = 0.0f64;
        let mut den = 0.0f64;
        for (x, y) in a.iter().zip(b) {
            num += ((x - y) as f64).powi(2);
            den += (*x as f64).powi(2);
        }
        (num / den.max(1e-12)).sqrt() as f32
    }

    #[test]
    fn green_repair_beats_plain_q8_on_structured_weights() {
        let (rows, cols) = (128, 128);
        let w = structured(rows, cols, 7);

        let plain = GreenTensor::compress(&w, rows, cols, 64, 0, 0.0); // Q8 only
        let green = GreenTensor::compress(&w, rows, cols, 64, 16, 0.005); // Q8 + repair

        let d_plain = rel_l2(&w, &plain.to_f32_vec());
        let d_green = rel_l2(&w, &green.to_f32_vec());

        // Repair must strictly improve fidelity, and land in high-quality territory.
        assert!(d_green < d_plain, "repair should cut drift: green {d_green} vs plain {d_plain}");
        assert!(d_green < 0.02, "green drift {d_green} should be < 2%");
        // And it should still be a real compression win vs f32 (4 bytes/weight).
        assert!(green.bytes() < w.len() * 4, "green must be smaller than f32");
    }

    #[test]
    fn green_store_runs_through_provider_bounded_and_faithful() {
        use crate::backend::CpuBackend;
        use crate::paged::dense_provider;
        use crate::weights::WeightStore;

        let (layers, experts, h, inter, k) = (2usize, 16usize, 48usize, 96usize, 6usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 5);
        let ram = 4usize; // decoded working set per layer « 16 experts/layer
        let green = GreenWeightStore::from_store(&store, GreenBase::Q8, 64, 0, 0.0, ram);

        // Whole-model compressed footprint must be a real fraction of the f32 model.
        let f32_bytes = layers * experts * 3 * h * inter * 4;
        assert!(green.compressed_bytes() * 2 < f32_bytes,
                "green {} should be « f32 {f32_bytes}", green.compressed_bytes());

        let backend = CpuBackend;
        let mut seed = 9u64;
        let x: Vec<f32> = (0..h).map(|_| { seed ^= seed >> 12; seed ^= seed << 25; seed ^= seed >> 27;
            (seed.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 }).collect();

        let mut worst = 0.0f32;
        for t in 0..40 {
            // deterministic routing
            let mut chosen = Vec::new();
            let mut s = seed.wrapping_add(t as u64 * 0x9E37);
            while chosen.len() < k {
                s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
                let e = ((s >> 33) as usize % experts) as u16;
                if !chosen.contains(&e) { chosen.push(e); }
            }
            let gates = vec![1.0f32 / k as f32; k];
            let layer = t % layers;
            let want = dense_provider(&store, &backend, layer, &x, &chosen, &gates);
            let got = dense_provider(&green, &backend, layer, &x, &chosen, &gates);
            let num: f32 = want.iter().zip(&got).map(|(a, b)| (a - b).powi(2)).sum();
            let den: f32 = want.iter().map(|a| a * a).sum::<f32>().max(1e-9);
            worst = worst.max((num / den).sqrt());
        }
        assert!(worst < 0.05, "green store output drift {worst} should be small");

        let m = green.metrics();
        assert!(m.peak_resident_experts <= ram * layers, "decoded resident {} > cap {}", m.peak_resident_experts, ram * layers);
        assert!(m.decode_hits > 0, "hot experts should hit the decode cache (reuse), got {}", m.decode_hits);
    }

    #[test]
    fn green_paged_disk_roundtrips_and_is_small() {
        use crate::backend::CpuBackend;
        use crate::paged::dense_provider;
        use crate::weights::WeightStore;

        let (layers, experts, h, inter, k) = (2usize, 12usize, 48usize, 96usize, 6usize);
        let store = WeightStore::synthetic(layers, experts, h, inter, false, 5);
        let path = std::env::temp_dir().join(format!("ge_green_paged_{}.bin", std::process::id()));

        // Q4 base + repair on disk (the compact tier).
        GreenPagedStore::create(&path, &store, GreenBase::Q4, 64, 8, 0.01).unwrap();
        let ram = 3usize; // decoded working set per layer « 12
        let disk = GreenPagedStore::open(&path, ram).unwrap();

        // On-disk model is a real fraction of f32.
        let f32_bytes = (layers * experts * 3 * h * inter * 4) as u64;
        assert!(disk.disk_bytes() * 2 < f32_bytes, "green disk {} should be « f32 {f32_bytes}", disk.disk_bytes());

        // In-RAM green reference with the SAME params — disk decode must reproduce it EXACTLY
        // (isolates disk I/O + serialization correctness from green's inherent quant loss).
        let mem = GreenWeightStore::from_store(&store, GreenBase::Q4, 64, 8, 0.01, ram);

        let backend = CpuBackend;
        let mut seed = 7u64;
        let x: Vec<f32> = (0..h).map(|_| { seed ^= seed >> 12; seed ^= seed << 25; seed ^= seed >> 27;
            (seed.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 }).collect();

        for layer in 0..layers {
            let chosen: Vec<u16> = (0..k as u16).collect();
            let gates = vec![1.0f32 / k as f32; k];
            let want = dense_provider(&mem, &backend, layer, &x, &chosen, &gates);
            let got = dense_provider(&disk, &backend, layer, &x, &chosen, &gates);
            assert_eq!(want, got, "green paged disk must equal in-RAM green exactly (layer {layer})");
        }
        let m = disk.metrics();
        assert!(m.peak_resident_experts <= ram * layers, "resident {} > cap", m.peak_resident_experts);
        assert!(m.decodes > 0, "should have paged from disk");

        // Prefetch also works over the disk store.
        disk.prefetch(0, &[0, 1, 2]);
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn repair_rescues_q4_base() {
        let (rows, cols) = (128, 128);
        let w = structured(rows, cols, 11);

        let q4_plain = GreenTensor::compress_q4(&w, rows, cols, 64, 0, 0.0); // Q4, no repair
        let q4_rep = GreenTensor::compress_q4(&w, rows, cols, 64, 16, 0.01); // Q4 + repair

        let d_plain = rel_l2(&w, &q4_plain.to_f32_vec());
        let d_rep = rel_l2(&w, &q4_rep.to_f32_vec());

        // Repair must dramatically cut Q4's drift, and the repaired form stays well under f32 size.
        assert!(d_rep < d_plain * 0.5, "repair should at least halve Q4 drift: {d_rep} vs {d_plain}");
        assert!(q4_rep.bytes() < w.len() * 4, "Q4+repair must beat f32 size");
    }
}
