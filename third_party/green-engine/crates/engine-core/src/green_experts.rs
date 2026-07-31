//! `.green` expert-pack adapter + transformer FFN/MoE step.
//!
//! Bridges green-format [`TensorRole::Expert`] shard records into the existing
//! [`crate::paged::ExpertProvider`] used by [`crate::runtime::MoeRuntime`]. Dense packages
//! (Hermes / Llama) expose no expert records — callers use [`FfnMode::Dense`] and never page.
//!
//! Does **not** fork the paged store or scheduler: residency is an app-managed
//! [`crate::paged::SlotBudget`] (per-layer or global pool) over decoded [`ExpertWeights`],
//! same model as `PagedWeightStore`.

use std::collections::HashMap;
use std::fs::File;
use std::io::{self, Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use green_format::tensor::TensorRecord;

use crate::backend::{ExpertBackend, Scratch};
use crate::green_model::{ExpertHandle, ExpertProvider as GreenExpertProvider, GreenModelError};
use crate::paged::{ExpertProvider, SlotBudget, SlotPool};
use crate::predictor::{top_b, TransitionMatrix};
use crate::weights::{ExpertWeights, Tensor};

const GREENPACK_MAGIC: &[u8; 4] = b"GRNP";
const GREENPACK_VERSION: u16 = 1;
const GREENPACK_FLAGS_RAW_F32: u16 = 0;

/// Which SwiGLU matrix a shard belongs to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum ExpertPart {
    Gate,
    Up,
    Down,
}

fn classify_expert_part(name: &str) -> Option<ExpertPart> {
    let n = name.to_ascii_lowercase();
    if n.contains("ffn_gate") || n.contains(".gate.") || n.contains("_gate_") || n.ends_with(".gate")
    {
        Some(ExpertPart::Gate)
    } else if n.contains("ffn_up") || n.contains(".up.") || n.contains("_up_") || n.ends_with(".up")
    {
        Some(ExpertPart::Up)
    } else if n.contains("ffn_down")
        || n.contains(".down.")
        || n.contains("_down_")
        || n.ends_with(".down")
    {
        Some(ExpertPart::Down)
    } else {
        None
    }
}

#[derive(Clone, Debug)]
struct ShardRef {
    path: PathBuf,
    offset: u64,
    /// Element count (f32). When `None`, inferred from file length / shape.
    elems: Option<usize>,
    shape: Vec<u32>,
}

#[derive(Clone, Debug, Default)]
struct ExpertSlots {
    gate: Option<ShardRef>,
    up: Option<ShardRef>,
    down: Option<ShardRef>,
}

/// On-disk index of expert shards from a `.green` manifest (`TensorRole::Expert` / `expert` field).
#[derive(Clone, Debug)]
struct ExpertIndex {
    /// row-major: layer * n_experts + expert
    slots: Vec<ExpertSlots>,
    layers: usize,
    experts: usize,
    hidden: usize,
    inter: usize,
}

impl ExpertIndex {
    fn from_records(root: &Path, records: &[TensorRecord]) -> Result<Option<Self>, MoeError> {
        let experts_only: Vec<&TensorRecord> = records.iter().filter(|r| r.is_expert()).collect();
        if experts_only.is_empty() {
            return Ok(None);
        }

        validate_expert_records(root, &experts_only)?;

        let mut max_layer = 0usize;
        let mut max_expert = 0usize;
        let mut triples: Vec<(usize, u16, ExpertPart, ShardRef)> = Vec::new();

        for rec in &experts_only {
            let layer = rec.layer.ok_or_else(|| {
                MoeError::InvalidShard(format!("expert tensor {} missing layer", rec.name))
            })? as usize;
            let expert = rec.expert.ok_or_else(|| {
                MoeError::InvalidShard(format!("expert tensor {} missing expert id", rec.name))
            })?;
            let part = classify_expert_part(&rec.name).ok_or_else(|| {
                MoeError::InvalidShard(format!(
                    "cannot classify gate/up/down from expert name {}",
                    rec.name
                ))
            })?;
            let elems = if rec.shape.len() >= 2 {
                Some(rec.shape.iter().map(|&d| d as usize).product())
            } else {
                rec.length.map(|b| (b as usize) / 4)
            };
            let shard = ShardRef {
                path: root.join(&rec.file),
                offset: rec.offset,
                elems,
                shape: rec.shape.clone(),
            };
            max_layer = max_layer.max(layer);
            max_expert = max_expert.max(expert as usize);
            triples.push((layer, expert, part, shard));
        }

        let layers = max_layer + 1;
        let n_experts = max_expert + 1;
        let mut slots = vec![ExpertSlots::default(); layers * n_experts];
        let mut hidden = 0usize;
        let mut inter = 0usize;

        for (layer, expert, part, shard) in triples {
            let slot = &mut slots[layer * n_experts + expert as usize];
            match part {
                ExpertPart::Gate => {
                    if shard.shape.len() >= 2 {
                        hidden = shard.shape[0] as usize;
                        inter = shard.shape[1] as usize;
                    }
                    slot.gate = Some(shard);
                }
                ExpertPart::Up => slot.up = Some(shard),
                ExpertPart::Down => {
                    if hidden == 0 && shard.shape.len() >= 2 {
                        // down is [inter, hidden]
                        inter = shard.shape[0] as usize;
                        hidden = shard.shape[1] as usize;
                    }
                    slot.down = Some(shard);
                }
            }
        }

        if hidden == 0 || inter == 0 {
            // Fall back: any complete triple's gate shape / elem counts.
            for s in &slots {
                if let (Some(g), Some(u), Some(d)) = (&s.gate, &s.up, &s.down) {
                    let ge = g.elems.or_else(|| shape_elems(&g.shape));
                    let de = d.elems.or_else(|| shape_elems(&d.shape));
                    if let (Some(ge), Some(de)) = (ge, de) {
                        // ge = h*i, de = i*h → same product; need factorisation via gate shape.
                        if g.shape.len() >= 2 {
                            hidden = g.shape[0] as usize;
                            inter = g.shape[1] as usize;
                        } else if ge > 0 && de == ge {
                            // square-ish fallback: assume inter = ge / hidden when hidden divides.
                            let _ = (u, de);
                        }
                    }
                    break;
                }
            }
        }

        if hidden == 0 || inter == 0 {
            return Err(MoeError::InvalidShard(
                "could not infer hidden/inter from expert shard shapes".into(),
            ));
        }

        // Require at least one complete expert; incomplete slots stay empty and fail on acquire.
        let complete = slots
            .iter()
            .filter(|s| s.gate.is_some() && s.up.is_some() && s.down.is_some())
            .count();
        if complete == 0 {
            return Err(MoeError::InvalidShard(
                "no complete gate/up/down expert triple in package".into(),
            ));
        }

        Ok(Some(ExpertIndex {
            slots,
            layers,
            experts: n_experts,
            hidden,
            inter,
        }))
    }

    fn slot(&self, layer: usize, expert: u16) -> Option<&ExpertSlots> {
        if layer >= self.layers || expert as usize >= self.experts {
            return None;
        }
        Some(&self.slots[layer * self.experts + expert as usize])
    }
}

fn shape_elems(shape: &[u32]) -> Option<usize> {
    if shape.is_empty() {
        None
    } else {
        Some(shape.iter().map(|&d| d as usize).product())
    }
}

fn validate_expert_records(root: &Path, records: &[&TensorRecord]) -> Result<(), MoeError> {
    let mut checked = std::collections::BTreeSet::new();
    for rec in records {
        let compression = rec.green_compression_type.as_deref().unwrap_or("");
        let ggml_type = rec.ggml_type.as_deref().unwrap_or("");
        if compression != "greenpack_raw_f32" || ggml_type != "F32" {
            return Err(MoeError::UnsupportedExpertPack {
                file: root.join(&rec.file),
                detail: format!(
                    "tensor {} declares unsupported expert pack type compression={compression:?} ggml_type={ggml_type:?}",
                    rec.name
                ),
            });
        }
        let path = root.join(&rec.file);
        if checked.insert(path.clone()) {
            validate_greenpack_header(&path)?;
        }
    }
    Ok(())
}

fn validate_greenpack_header(path: &Path) -> Result<(), MoeError> {
    let mut f = File::open(path).map_err(|e| MoeError::Io(format!("open {}: {e}", path.display())))?;
    let mut magic = [0u8; 4];
    f.read_exact(&mut magic)
        .map_err(|e| MoeError::Io(format!("read {}: {e}", path.display())))?;
    if &magic != GREENPACK_MAGIC {
        return Err(MoeError::UnsupportedExpertPack {
            file: path.to_path_buf(),
            detail: format!("bad magic {magic:?}"),
        });
    }
    let version = read_u16_le(&mut f, path, "version")?;
    if version != GREENPACK_VERSION {
        return Err(MoeError::UnsupportedExpertPack {
            file: path.to_path_buf(),
            detail: format!("unsupported greenpack version {version}"),
        });
    }
    let flags = read_u16_le(&mut f, path, "flags")?;
    if flags != GREENPACK_FLAGS_RAW_F32 {
        return Err(MoeError::UnsupportedExpertPack {
            file: path.to_path_buf(),
            detail: format!("unsupported greenpack flags {flags}"),
        });
    }
    let _ = read_u32_le(&mut f, path, "tensor count")?;
    Ok(())
}

fn read_u16_le(f: &mut File, path: &Path, label: &str) -> Result<u16, MoeError> {
    let mut buf = [0u8; 2];
    f.read_exact(&mut buf)
        .map_err(|e| MoeError::Io(format!("read {} {label}: {e}", path.display())))?;
    Ok(u16::from_le_bytes(buf))
}

fn read_u32_le(f: &mut File, path: &Path, label: &str) -> Result<u32, MoeError> {
    let mut buf = [0u8; 4];
    f.read_exact(&mut buf)
        .map_err(|e| MoeError::Io(format!("read {} {label}: {e}", path.display())))?;
    Ok(u32::from_le_bytes(buf))
}

fn read_f32_shard(shard: &ShardRef, expected_elems: usize) -> Result<Vec<f32>, MoeError> {
    let mut f = File::open(&shard.path).map_err(|e| {
        MoeError::Io(format!("open {}: {e}", shard.path.display()))
    })?;
    f.seek(SeekFrom::Start(shard.offset))
        .map_err(|e| MoeError::Io(e.to_string()))?;
    let nbytes = expected_elems * 4;
    let mut buf = vec![0u8; nbytes];
    f.read_exact(&mut buf)
        .map_err(|e| MoeError::Io(format!("read {}: {e}", shard.path.display())))?;
    let mut out = Vec::with_capacity(expected_elems);
    for chunk in buf.chunks_exact(4) {
        out.push(f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]));
    }
    Ok(out)
}

fn load_expert_weights(index: &ExpertIndex, layer: usize, expert: u16) -> Result<ExpertWeights, MoeError> {
    let slot = index.slot(layer, expert).ok_or(MoeError::ExpertOutOfRange {
        layer,
        expert,
    })?;
    let gate = slot.gate.as_ref().ok_or_else(|| {
        MoeError::InvalidShard(format!("missing gate L{layer} E{expert}"))
    })?;
    let up = slot
        .up
        .as_ref()
        .ok_or_else(|| MoeError::InvalidShard(format!("missing up L{layer} E{expert}")))?;
    let down = slot
        .down
        .as_ref()
        .ok_or_else(|| MoeError::InvalidShard(format!("missing down L{layer} E{expert}")))?;

    let (h, i) = (index.hidden, index.inter);
    let g = read_f32_shard(gate, h * i)?;
    let u = read_f32_shard(up, h * i)?;
    let d = read_f32_shard(down, i * h)?;
    Ok(ExpertWeights {
        hidden: h,
        inter: i,
        gate: Tensor::F32(g),
        up: Tensor::F32(u),
        down: Tensor::F32(d),
    })
}

#[derive(Default, Clone, Copy, Debug)]
pub struct ExpertResidencyMetrics {
    pub hits: u64,
    pub misses: u64,
    pub peak_resident: usize,
}

struct ResidentEntry {
    w: Arc<ExpertWeights>,
    clock: u64,
}

/// Routing-aware residency: reuse-distance eviction (same policy as [`crate::paged::PagedWeightStore`]).
struct ResidencyLru {
    per_layer: Vec<HashMap<u16, ResidentEntry>>,
    clock: u64,
    cap: usize,
    global_cap: usize,
    pool: SlotPool,
    peak: usize,
    trans: Vec<TransitionMatrix>,
    prev: Vec<Vec<u16>>,
    trans_lay: Vec<TransitionMatrix>,
    lay_prev: Option<(usize, Vec<u16>)>,
    scratch: Vec<f64>,
}

impl ResidencyLru {
    fn new(layers: usize, budget: SlotBudget, n_experts: usize) -> Self {
        let n_experts = n_experts.max(1);
        let cap = budget.slots_per_layer.max(1);
        let layers = layers.max(1);
        ResidencyLru {
            per_layer: (0..layers).map(|_| HashMap::new()).collect(),
            clock: 0,
            cap,
            global_cap: cap.saturating_mul(layers),
            pool: budget.pool,
            peak: 0,
            trans: (0..layers)
                .map(|_| TransitionMatrix::new(n_experts))
                .collect(),
            prev: (0..layers).map(|_| Vec::new()).collect(),
            trans_lay: (0..layers)
                .map(|_| TransitionMatrix::new(n_experts))
                .collect(),
            lay_prev: None,
            scratch: vec![0.0; n_experts],
        }
    }

    fn make_room(&mut self, layer: usize) {
        match self.pool {
            SlotPool::PerLayer => self.make_room_layer(layer),
            SlotPool::Global => self.make_room_global(),
        }
    }

    fn make_room_layer(&mut self, layer: usize) {
        if self.per_layer[layer].len() < self.cap {
            return;
        }
        if let Some(v) = self.victim_in_layer(layer) {
            self.per_layer[layer].remove(&v);
        }
    }

    fn make_room_global(&mut self) {
        if self.total() < self.global_cap {
            return;
        }
        let mut best: Option<(usize, u16)> = None;
        let mut best_key = f64::INFINITY;
        let mut best_rec = u64::MAX;
        for layer in 0..self.per_layer.len() {
            let mass = if self.prev[layer].is_empty() {
                0.0
            } else {
                self.trans[layer].score_into(&self.prev[layer], &mut self.scratch)
            };
            for (&id, e) in self.per_layer[layer].iter() {
                let key = if mass > 0.0 {
                    self.scratch.get(id as usize).copied().unwrap_or(0.0)
                } else {
                    e.clock as f64
                };
                let rec = e.clock;
                if best.is_none() || key < best_key || (key == best_key && rec < best_rec) {
                    best = Some((layer, id));
                    best_key = key;
                    best_rec = rec;
                }
            }
        }
        if let Some((layer, id)) = best {
            self.per_layer[layer].remove(&id);
        }
    }

    fn victim_in_layer(&mut self, layer: usize) -> Option<u16> {
        let mass = if self.prev[layer].is_empty() {
            0.0
        } else {
            self.trans[layer].score_into(&self.prev[layer], &mut self.scratch)
        };
        let mut best: Option<u16> = None;
        let mut best_key = f64::INFINITY;
        let mut best_rec = u64::MAX;
        for (&id, e) in self.per_layer[layer].iter() {
            let key = if mass > 0.0 {
                self.scratch.get(id as usize).copied().unwrap_or(0.0)
            } else {
                e.clock as f64
            };
            let rec = e.clock;
            if best.is_none() || key < best_key || (key == best_key && rec < best_rec) {
                best = Some(id);
                best_key = key;
                best_rec = rec;
            }
        }
        best
    }

    fn observe(&mut self, layer: usize, experts: &[u16], gates: Option<&[f32]>) -> Option<(usize, u16)> {
        if layer >= self.prev.len() {
            return None;
        }
        if !self.prev[layer].is_empty() {
            match gates {
                Some(g) => self.trans[layer].update_weighted(&self.prev[layer], experts, g),
                None => self.trans[layer].update(&self.prev[layer], experts),
            }
        }
        self.prev[layer].clear();
        self.prev[layer].extend_from_slice(experts);

        if let Some((prev_l, ref prev_ex)) = self.lay_prev {
            if prev_l + 1 == layer && prev_l < self.trans_lay.len() {
                match gates {
                    Some(g) => self.trans_lay[prev_l].update_weighted(prev_ex, experts, g),
                    None => self.trans_lay[prev_l].update(prev_ex, experts),
                }
            }
        }
        self.lay_prev = Some((layer, experts.to_vec()));

        if layer + 1 < self.per_layer.len() {
            let mass = self.trans_lay[layer].score_into(experts, &mut self.scratch);
            if mass > 0.0 {
                let pred = top_b(&self.scratch, 1);
                if let Some(&e) = pred.first() {
                    return Some((layer + 1, e));
                }
            }
        }
        None
    }

    fn total(&self) -> usize {
        self.per_layer.iter().map(|m| m.len()).sum()
    }
}

/// Disk-backed expert store for `.green` packages with a bounded per-layer RAM LRU.
pub struct PackageExpertStore {
    index: ExpertIndex,
    root: PathBuf,
    cache: Mutex<ResidencyLru>,
    metrics: Mutex<ExpertResidencyMetrics>,
}

impl PackageExpertStore {
    /// Build from green-format tensor records. Returns `Ok(None)` when the package is dense
    /// (no `TensorRole::Expert` / expert-id records) — Hermes/Llama no-op path.
    pub fn from_records(
        root: &Path,
        records: &[TensorRecord],
        ram_experts_per_layer: usize,
    ) -> Result<Option<Self>, MoeError> {
        Self::from_records_budget(root, records, SlotBudget::per_layer(ram_experts_per_layer))
    }

    /// Like [`from_records`](Self::from_records) with an explicit [`SlotBudget`] (global pool / GiB sizing).
    pub fn from_records_budget(
        root: &Path,
        records: &[TensorRecord],
        budget: SlotBudget,
    ) -> Result<Option<Self>, MoeError> {
        let Some(index) = ExpertIndex::from_records(root, records)? else {
            return Ok(None);
        };
        let layers = index.layers;
        let n_experts = index.experts;
        Ok(Some(PackageExpertStore {
            index,
            root: root.to_path_buf(),
            cache: Mutex::new(ResidencyLru::new(layers, budget, n_experts)),
            metrics: Mutex::new(ExpertResidencyMetrics::default()),
        }))
    }

    pub fn metrics(&self) -> ExpertResidencyMetrics {
        *self.metrics.lock().unwrap()
    }

    pub fn package_root(&self) -> &Path {
        &self.root
    }

    /// Warm experts into the RAM LRU (same semantics as `PagedWeightStore::prefetch`).
    pub fn prefetch_experts(&self, layer: usize, experts: &[u16]) {
        for &e in experts {
            self.with_expert(layer, e, &mut |_| {});
        }
    }

    /// Routing-aware hot-set: record routed experts (+ optional gate weights) for reuse-distance
    /// eviction on later tokens. Mirrors [`crate::paged::PagedWeightStore::observe_routing`].
    /// Also warms the top-1 predicted expert for layer L+1 when the layer-ahead model has signal.
    pub fn observe_routing(&self, layer: usize, experts: &[u16], gates: Option<&[f32]>) {
        let prefetch = {
            let mut cache = self.cache.lock().unwrap();
            cache.observe(layer, experts, gates)
        };
        if let Some((next_l, e)) = prefetch {
            self.prefetch_experts(next_l, &[e]);
        }
    }

    fn materialize(&self, layer: usize, expert: u16) -> Result<Arc<ExpertWeights>, MoeError> {
        let mut cache = self.cache.lock().unwrap();
        cache.clock += 1;
        let clock = cache.clock;
        if let Some(e) = cache.per_layer[layer].get_mut(&expert) {
            e.clock = clock;
            self.metrics.lock().unwrap().hits += 1;
            return Ok(e.w.clone());
        }
        let w = Arc::new(load_expert_weights(&self.index, layer, expert)?);
        cache.make_room(layer);
        cache.per_layer[layer].insert(expert, ResidentEntry { w: w.clone(), clock });
        let peak = cache.total();
        cache.peak = cache.peak.max(peak);
        let mut m = self.metrics.lock().unwrap();
        m.misses += 1;
        m.peak_resident = cache.peak;
        Ok(w)
    }
}

impl ExpertProvider for PackageExpertStore {
    fn layers(&self) -> usize {
        self.index.layers
    }
    fn experts(&self) -> usize {
        self.index.experts
    }
    fn hidden(&self) -> usize {
        self.index.hidden
    }
    fn inter(&self) -> usize {
        self.index.inter
    }
    fn expert_bytes(&self, layer: usize, expert: u16) -> usize {
        let _ = (layer, expert);
        let (h, i) = (self.index.hidden, self.index.inter);
        (h * i * 2 + i * h) * 4
    }
    fn with_expert(&self, layer: usize, expert: u16, f: &mut dyn FnMut(&ExpertWeights)) {
        let w = self
            .materialize(layer, expert)
            .expect("expert shard load");
        f(&w);
    }
    fn observe_routing(&self, layer: usize, experts: &[u16], gates: Option<&[f32]>) {
        PackageExpertStore::observe_routing(self, layer, experts, gates);
    }
}

/// Package-facing [`GreenExpertProvider`] adapter (prefetch / acquire / evict) over the same LRU.
impl GreenExpertProvider for PackageExpertStore {
    fn prefetch(&self, layer: usize, experts: &[u16]) {
        self.prefetch_experts(layer, experts);
    }

    fn acquire(&self, layer: usize, expert: u16) -> Result<ExpertHandle, GreenModelError> {
        if layer >= self.index.layers || expert as usize >= self.index.experts {
            return Err(GreenModelError::ExpertUnavailable { layer, expert });
        }
        self.materialize(layer, expert).map_err(|e| {
            GreenModelError::Io(e.to_string())
        })?;
        Ok(ExpertHandle { layer, expert })
    }

    fn evict(&self, layer: usize, expert: u16) {
        let mut cache = self.cache.lock().unwrap();
        if layer < cache.per_layer.len() {
            cache.per_layer[layer].remove(&expert);
        }
    }
}

/// Softmax top-k routing over router logits → (expert_id, gate_weight) pairs.
#[derive(Clone, Debug, Default)]
pub struct RouteSelection {
    pub experts: Vec<u16>,
    pub gates: Vec<f32>,
}

/// Softmax → keep top `k` experts; renormalize gates to sum to 1.
pub fn route_topk(logits: &[f32], top_k: usize) -> RouteSelection {
    let k = top_k.min(logits.len()).max(1);
    if logits.is_empty() {
        return RouteSelection::default();
    }
    let max = logits.iter().copied().fold(f32::NEG_INFINITY, f32::max);
    let mut scored: Vec<(usize, f32)> = logits
        .iter()
        .enumerate()
        .map(|(i, &v)| (i, (v - max).exp()))
        .collect();
    scored.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));
    scored.truncate(k);
    let sum: f32 = scored.iter().map(|(_, w)| w).sum();
    let mut experts = Vec::with_capacity(k);
    let mut gates = Vec::with_capacity(k);
    for (i, w) in scored {
        experts.push(i as u16);
        gates.push(if sum > 0.0 { w / sum } else { 0.0 });
    }
    RouteSelection { experts, gates }
}

/// How the transformer FFN block should run for this layer.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FfnMode {
    /// Standard dense SwiGLU (Llama / Hermes). Ignores expert packs.
    Dense,
    /// MoE: top-k route then gated expert FFN sum.
    Moe { top_k: usize },
}

#[derive(Debug)]
pub enum MoeError {
    Io(String),
    InvalidShard(String),
    ExpertOutOfRange { layer: usize, expert: u16 },
    UnsupportedExpertPack { file: PathBuf, detail: String },
    DenseWeightsMissing,
    MoEProviderMissing,
    RouterMissing,
    DimMismatch { expected: usize, got: usize },
}

impl std::fmt::Display for MoeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            MoeError::Io(e) => write!(f, "{e}"),
            MoeError::InvalidShard(e) => write!(f, "invalid expert shard: {e}"),
            MoeError::ExpertOutOfRange { layer, expert } => {
                write!(f, "expert L{layer} E{expert} out of range")
            }
            MoeError::UnsupportedExpertPack { file, detail } => {
                write!(f, "unsupported expert pack {}: {detail}", file.display())
            }
            MoeError::DenseWeightsMissing => write!(f, "dense FFN weights required for FfnMode::Dense"),
            MoeError::MoEProviderMissing => write!(f, "MoE ExpertProvider required for FfnMode::Moe"),
            MoeError::RouterMissing => write!(f, "router logits required for FfnMode::Moe"),
            MoeError::DimMismatch { expected, got } => {
                write!(f, "activation dim {got} != expected {expected}")
            }
        }
    }
}

impl std::error::Error for MoeError {}

impl From<io::Error> for MoeError {
    fn from(value: io::Error) -> Self {
        MoeError::Io(value.to_string())
    }
}

/// CPU SwiGLU apply for one dense FFN (or a single expert).
pub fn apply_expert_ffn(
    backend: &impl ExpertBackend,
    w: &ExpertWeights,
    x: &[f32],
    scratch: &mut Scratch,
    out: &mut [f32],
) {
    backend.compute_expert(w, x, scratch, out);
}

/// Gated MoE sum over an [`ExpertProvider`] (in-RAM, paged, or `.green` package store).
pub fn apply_moe_ffn<B: ExpertBackend, P: ExpertProvider>(
    provider: &P,
    backend: &B,
    layer: usize,
    x: &[f32],
    experts: &[u16],
    gates: &[f32],
    scratch: &mut Scratch,
    out: &mut [f32],
) {
    let h = provider.hidden();
    debug_assert_eq!(out.len(), h);
    for v in out.iter_mut() {
        *v = 0.0;
    }
    let mut tmp = vec![0.0f32; h];
    for (k, &e) in experts.iter().enumerate() {
        provider.with_expert(layer, e, &mut |w| backend.compute_expert(w, x, scratch, &mut tmp));
        let g = gates.get(k).copied().unwrap_or(0.0);
        for o in 0..h {
            out[o] += g * tmp[o];
        }
    }
}

/// Transformer FFN / MoE step — the seam the forward graph should call after attention.
///
/// * [`FfnMode::Dense`]: applies `dense_ffn` SwiGLU; `experts` / router ignored (Hermes/Llama).
/// * [`FfnMode::Moe`]: top-k routes `router_logits`, pages experts via `experts`, gated sum.
///
/// Returns the route selection used (`experts`/`gates` empty for dense).
pub fn ffn_moe_step<B: ExpertBackend, P: ExpertProvider>(
    mode: FfnMode,
    backend: &B,
    dense_ffn: Option<&ExpertWeights>,
    experts: Option<&P>,
    layer: usize,
    x: &[f32],
    router_logits: Option<&[f32]>,
    scratch: &mut Scratch,
    out: &mut [f32],
) -> Result<RouteSelection, MoeError> {
    match mode {
        FfnMode::Dense => {
            let w = dense_ffn.ok_or(MoeError::DenseWeightsMissing)?;
            if x.len() != w.hidden {
                return Err(MoeError::DimMismatch {
                    expected: w.hidden,
                    got: x.len(),
                });
            }
            if out.len() != w.hidden {
                return Err(MoeError::DimMismatch {
                    expected: w.hidden,
                    got: out.len(),
                });
            }
            apply_expert_ffn(backend, w, x, scratch, out);
            Ok(RouteSelection::default())
        }
        FfnMode::Moe { top_k } => {
            let provider = experts.ok_or(MoeError::MoEProviderMissing)?;
            let logits = router_logits.ok_or(MoeError::RouterMissing)?;
            if x.len() != provider.hidden() {
                return Err(MoeError::DimMismatch {
                    expected: provider.hidden(),
                    got: x.len(),
                });
            }
            if out.len() != provider.hidden() {
                return Err(MoeError::DimMismatch {
                    expected: provider.hidden(),
                    got: out.len(),
                });
            }
            let route = route_topk(logits, top_k);
            // `with_expert` inside apply_moe_ffn fills the provider's residency cache.
            apply_moe_ffn(
                provider,
                backend,
                layer,
                x,
                &route.experts,
                &route.gates,
                scratch,
                out,
            );
            provider.observe_routing(layer, &route.experts, Some(&route.gates));
            Ok(route)
        }
    }
}

/// Convenience: dense-or-MoE dispatch when the package may or may not have experts.
///
/// If `package_experts` is `None`, always runs dense (requires `dense_ffn`).
pub fn ffn_step_auto<B: ExpertBackend>(
    backend: &B,
    dense_ffn: Option<&ExpertWeights>,
    package_experts: Option<&PackageExpertStore>,
    layer: usize,
    x: &[f32],
    router_logits: Option<&[f32]>,
    top_k: usize,
    scratch: &mut Scratch,
    out: &mut [f32],
) -> Result<RouteSelection, MoeError> {
    match package_experts {
        None => ffn_moe_step(
            FfnMode::Dense,
            backend,
            dense_ffn,
            None::<&PackageExpertStore>,
            layer,
            x,
            None,
            scratch,
            out,
        ),
        Some(store) => ffn_moe_step(
            FfnMode::Moe { top_k },
            backend,
            dense_ffn,
            Some(store),
            layer,
            x,
            router_logits,
            scratch,
            out,
        ),
    }
}

/// Write a tiny raw-f32 expert pack under `dir` for tests (3 experts × 1 layer).
///
/// Uses the same `experts-000.greenpack` layout as Green Compress `pack-model`
/// (GRNP header + concatenated f32 payloads + manifest offsets).
#[cfg(test)]
pub fn write_synthetic_expert_pack(
    dir: &Path,
    hidden: usize,
    inter: usize,
    n_experts: usize,
) -> Result<Vec<TensorRecord>, MoeError> {
    use std::fs;
    use std::io::Write;
    fs::create_dir_all(dir).map_err(|e| MoeError::Io(e.to_string()))?;
    let file = "experts-000.greenpack";
    let path = dir.join(file);
    let mut f = File::create(&path).map_err(|e| MoeError::Io(e.to_string()))?;
    // GRNP + ver_u16=1 + flags_u16=0 + n_tensors_u32
    let n_tensors = (n_experts * 3) as u32;
    f.write_all(b"GRNP").map_err(|e| MoeError::Io(e.to_string()))?;
    f.write_all(&1u16.to_le_bytes())
        .map_err(|e| MoeError::Io(e.to_string()))?;
    f.write_all(&0u16.to_le_bytes())
        .map_err(|e| MoeError::Io(e.to_string()))?;
    f.write_all(&n_tensors.to_le_bytes())
        .map_err(|e| MoeError::Io(e.to_string()))?;

    let mut records = Vec::new();
    let mut seed = 7u64;
    let mut next = || {
        seed ^= seed >> 12;
        seed ^= seed << 25;
        seed ^= seed >> 27;
        let u = (seed.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32;
        (u - 0.5) * 0.2
    };
    for e in 0..n_experts {
        for (rows, cols, tag) in [
            (hidden, inter, "ffn_gate_exps"),
            (hidden, inter, "ffn_up_exps"),
            (inter, hidden, "ffn_down_exps"),
        ] {
            let name = format!("blk.0.{tag}.{e}.weight");
            let offset = f
                .stream_position()
                .map_err(|e| MoeError::Io(e.to_string()))?;
            let n = rows * cols;
            for _ in 0..n {
                f.write_all(&next().to_le_bytes())
                    .map_err(|e| MoeError::Io(e.to_string()))?;
            }
            records.push(TensorRecord {
                name,
                role: Some(green_format::tensor::TensorRole::Expert),
                layer: Some(0),
                expert: Some(e as u16),
                shape: vec![rows as u32, cols as u32],
                file: file.to_string(),
                offset,
                length: Some((n * 4) as u64),
                checksum: None,
                method: None,
                ggml_type: Some("F32".into()),
                source_gguf_type: None,
                green_compression_type: Some("greenpack_raw_f32".into()),
                compressed_size: Some((n * 4) as u64),
            });
        }
    }
    Ok(records)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::CpuBackend;
    use crate::paged::dense_provider;
    use crate::runtime::MoeRuntime;
    use crate::cache::Eviction;
    use crate::weights::WeightStore;
    use tempfile::TempDir;

    #[test]
    fn dense_package_yields_no_expert_store() {
        let recs = vec![TensorRecord {
            name: "blk.0.ffn_up.weight".into(),
            role: Some(green_format::tensor::TensorRole::Dense),
            layer: Some(0),
            expert: None,
            shape: vec![8, 16],
            file: "dense.gguf".into(),
            offset: 0,
            length: None,
            checksum: None,
            method: None,
            ggml_type: None,
            source_gguf_type: None,
            green_compression_type: None,
            compressed_size: None,
        }];
        let dir = TempDir::new().unwrap();
        let store = PackageExpertStore::from_records(dir.path(), &recs, 4).unwrap();
        assert!(store.is_none(), "Hermes/Llama dense must no-op");
    }

    #[test]
    fn route_topk_renormalizes() {
        let r = route_topk(&[1.0, 3.0, 0.5, 2.0], 2);
        assert_eq!(r.experts, vec![1, 3]);
        let s: f32 = r.gates.iter().sum();
        assert!((s - 1.0).abs() < 1e-5);
    }

    #[test]
    fn package_experts_match_weight_store_moe() {
        let dir = TempDir::new().unwrap();
        let (h, inter, e, k) = (16usize, 32usize, 4usize, 2usize);
        let recs = write_synthetic_expert_pack(dir.path(), h, inter, e).unwrap();
        let pkg = PackageExpertStore::from_records(dir.path(), &recs, 2)
            .unwrap()
            .expect("moe pack");
        assert_eq!(pkg.layers(), 1);
        assert_eq!(pkg.experts(), e);
        assert_eq!(pkg.hidden(), h);

        let backend = CpuBackend;
        let mut seed = 11u64;
        let mut lcg = || {
            seed ^= seed >> 12;
            seed ^= seed << 25;
            seed ^= seed >> 27;
            (seed.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
        };
        let x: Vec<f32> = (0..h).map(|_| lcg()).collect();
        let logits: Vec<f32> = (0..e).map(|_| lcg()).collect();
        let route = route_topk(&logits, k);

        let via_dense = dense_provider(&pkg, &backend, 0, &x, &route.experts, &route.gates);
        let mut scratch = Scratch::new(h, inter);
        let mut out = vec![0.0f32; h];
        let sel = ffn_moe_step(
            FfnMode::Moe { top_k: k },
            &backend,
            None,
            Some(&pkg),
            0,
            &x,
            Some(&logits),
            &mut scratch,
            &mut out,
        )
        .unwrap();
        assert_eq!(sel.experts, route.experts);
        for i in 0..h {
            assert!(
                (out[i] - via_dense[i]).abs() < 1e-5,
                "ffn_moe_step vs dense_provider at {i}"
            );
        }
        assert!(pkg.metrics().misses >= 1);
    }

    #[test]
    fn ffn_step_auto_moe_matches_dense_provider() {
        let dir = TempDir::new().unwrap();
        let (h, inter, e, k) = (16usize, 32usize, 4usize, 2usize);
        let recs = write_synthetic_expert_pack(dir.path(), h, inter, e).unwrap();
        // Confirm greenpack magic.
        let mut hdr = [0u8; 4];
        {
            use std::io::Read;
            let mut f = File::open(dir.path().join("experts-000.greenpack")).unwrap();
            f.read_exact(&mut hdr).unwrap();
        }
        assert_eq!(&hdr, b"GRNP");
        let pkg = PackageExpertStore::from_records(dir.path(), &recs, 2)
            .unwrap()
            .unwrap();
        let backend = CpuBackend;
        let x = vec![0.07f32; h];
        let logits = vec![0.2f32, 1.5, -0.3, 0.9];
        let route = route_topk(&logits, k);
        let via_dense = dense_provider(&pkg, &backend, 0, &x, &route.experts, &route.gates);
        let mut scratch = Scratch::new(h, inter);
        let mut out = vec![0.0f32; h];
        let sel = ffn_step_auto(
            &backend,
            None,
            Some(&pkg),
            0,
            &x,
            Some(&logits),
            k,
            &mut scratch,
            &mut out,
        )
        .unwrap();
        assert_eq!(sel.experts, route.experts);
        assert_eq!(out, via_dense);
    }

    #[test]
    fn rejects_non_raw_f32_expert_pack_contract() {
        let dir = TempDir::new().unwrap();
        let (h, inter, e) = (16usize, 32usize, 2usize);
        let mut recs = write_synthetic_expert_pack(dir.path(), h, inter, e).unwrap();
        recs[0].green_compression_type = Some("greenpack_q4_0".into());
        recs[0].ggml_type = Some("Q4_0".into());
        let err = match PackageExpertStore::from_records(dir.path(), &recs, 2) {
            Err(err) => err,
            Ok(_) => panic!("expected unsupported expert pack"),
        };
        assert!(matches!(err, MoeError::UnsupportedExpertPack { .. }));
        assert!(err.to_string().contains("unsupported expert pack"));
    }

    #[test]
    fn rejects_greenpack_with_unknown_flags() {
        let dir = TempDir::new().unwrap();
        let (h, inter, e) = (16usize, 32usize, 2usize);
        let recs = write_synthetic_expert_pack(dir.path(), h, inter, e).unwrap();
        let path = dir.path().join("experts-000.greenpack");
        {
            use std::io::{Seek, SeekFrom, Write};
            let mut f = File::options().write(true).open(&path).unwrap();
            f.seek(SeekFrom::Start(6)).unwrap();
            f.write_all(&1u16.to_le_bytes()).unwrap();
        }
        let err = match PackageExpertStore::from_records(dir.path(), &recs, 2) {
            Err(err) => err,
            Ok(_) => panic!("expected unsupported expert pack"),
        };
        assert!(matches!(err, MoeError::UnsupportedExpertPack { .. }));
        assert!(err.to_string().contains("unsupported greenpack flags 1"));
    }

    #[test]
    fn dense_ffn_step_no_experts() {
        let store = WeightStore::synthetic(1, 1, 8, 16, false, 3);
        let w = store.get(0, 0);
        let backend = CpuBackend;
        let x = vec![0.1f32; 8];
        let mut scratch = Scratch::new(8, 16);
        let mut out = vec![0.0f32; 8];
        let route = ffn_step_auto(
            &backend,
            Some(w),
            None,
            0,
            &x,
            None,
            2,
            &mut scratch,
            &mut out,
        )
        .unwrap();
        assert!(route.experts.is_empty());
        let mut ref_out = vec![0.0f32; 8];
        apply_expert_ffn(&backend, w, &x, &mut scratch, &mut ref_out);
        assert_eq!(out, ref_out);
    }

    #[test]
    fn stub_green_provider_is_dense_noop() {
        use crate::green_model::StubExpertProvider;
        let s = StubExpertProvider;
        s.prefetch(0, &[0, 1]);
        let h = s.acquire(0, 0).expect("dense stub acquire must succeed");
        assert_eq!(h.layer, 0);
        s.evict(0, 0);
    }

    #[test]
    fn moe_runtime_over_package_store() {
        let dir = TempDir::new().unwrap();
        let (h, inter, e) = (12usize, 24usize, 6usize);
        let recs = write_synthetic_expert_pack(dir.path(), h, inter, e).unwrap();
        let pkg = PackageExpertStore::from_records(dir.path(), &recs, 3)
            .unwrap()
            .unwrap();
        let backend = CpuBackend;
        let mut rt = MoeRuntime::new(&pkg, &backend, 3, Eviction::Lru);
        let x = vec![0.05f32; h];
        let experts = [0u16, 2];
        let gates = [0.6f32, 0.4];
        let mut out = vec![0.0; h];
        rt.forward_layer(0, &x, &experts, &gates, &mut out);
        let want = dense_provider(&pkg, &backend, 0, &x, &experts, &gates);
        assert_eq!(out, want);
    }
}
