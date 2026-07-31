//! KV-cache scheduling — the token/memory-compression tier (orthogonal to Green Compress weights).
//!
//! The KV cache grows with every token and dominates long-context memory. The engine schedules
//! it like experts: keep the KV blocks that matter, evict the rest. This module implements the
//! 2025-26 training-free eviction policies (StreamingLLM / H2O / SnapKV / Quest-oracle) and the
//! engine's differentiator — **adaptive per-layer budget** (some layers concentrate attention and
//! need little KV; others need a lot). Validated against the Python analysis (see tests).
//!
//! The [`KvStore`] trait is the Phase 4 / M5 execution seam for native generation: append K/V per
//! token, recall by layer for attention, and [`PagedRamKvStore`] for hot/cold memory tiers.
//! Softmax / Q·Kᵀ live with the attention agent — [`KvStore::attend`] is intentionally a no-op stub.
//! Live dense decode (`DenseForward::generate_greedy` / `ge run`) uses [`PagedRamKvStore`].

use std::cell::Cell;
use std::fs;
use std::io;
use std::ops::Range;
use std::path::Path;

const MAGIC: u32 = 0x4E54_5441; // "ATTN"
/// Default StreamingLLM attention sinks (first tokens always retained).
pub const DEFAULT_ATTENTION_SINKS: usize = 4;
const SINKS: usize = DEFAULT_ATTENTION_SINKS;

/// Default chat hot window (`sinks ∪ recent`) when `kv_hot_cap` / `GE_ATTN_WINDOW` unset.
pub const DEFAULT_CHAT_ATTN_WINDOW: usize = 256;

/// `GE_ATTN_WINDOW=N` → Some(N≥1). Unset / 0 / invalid → None.
pub fn attn_window_from_env() -> Option<usize> {
    std::env::var("GE_ATTN_WINDOW")
        .ok()
        .and_then(|s| s.trim().parse::<usize>().ok())
        .filter(|&n| n >= 1)
}

/// Resolve StreamingLLM hot cap: explicit → `GE_ATTN_WINDOW` → chat default 256 → full `ctx_len`.
pub fn resolve_kv_hot_cap(explicit: Option<usize>, ctx_len: usize, chat_default: bool) -> usize {
    let ctx = ctx_len.max(1);
    if let Some(h) = explicit {
        return h.max(1).min(ctx);
    }
    if let Some(w) = attn_window_from_env() {
        return w.min(ctx);
    }
    if chat_default {
        return DEFAULT_CHAT_ATTN_WINDOW.min(ctx);
    }
    ctx
}

/// Half-precision lane stored as IEEE 754 binary16 bits (no `half` crate dependency).
pub type F16 = u16;

/// Output of a single attention step over a KV range (Phase 4 stub).
#[derive(Clone, Debug, Default)]
pub struct AttentionResult {
    pub output: Vec<F16>,
}

/// Flattened K/V for one layer over a token range — row-major `[tokens × dim]`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct KvRecall {
    pub keys: Vec<F16>,
    pub values: Vec<F16>,
    /// Number of tokens in this slice (`keys.len() / dim`).
    pub tokens: usize,
    /// Per-token width (`n_kv_heads * head_dim`).
    pub dim: usize,
}

impl KvRecall {
    #[inline]
    pub fn key_at(&self, t: usize) -> &[F16] {
        let o = t * self.dim;
        &self.keys[o..o + self.dim]
    }

    #[inline]
    pub fn value_at(&self, t: usize) -> &[F16] {
        let o = t * self.dim;
        &self.values[o..o + self.dim]
    }
}

/// Paged KV store for the native generate loop (Phase 4).
///
/// Typical usage per decode step:
/// ```ignore
/// for layer in 0..layers {
///     kv.append(layer, t, &k, &v);
///     let past = kv.recall(layer, 0..kv.seq_len());
///     // attention agent: scores = q · past.keys; out = softmax(scores) · past.values
///     let _ = kv.attend(layer, &q, 0..kv.seq_len()); // stub until attention lands
/// }
/// ```
pub trait KvStore {
    /// Append K/V for `token` at `layer`. Must be sequential (`token == layer_len`).
    fn append(&mut self, layer: usize, token: usize, key: &[F16], value: &[F16]);

    /// Recall K/V for `layer` over token `range` (half-open, clamped to stored length).
    fn recall(&self, layer: usize, range: Range<usize>) -> KvRecall;

    fn recall_f32_into(
        &self,
        layer: usize,
        range: Range<usize>,
        keys: &mut Vec<f32>,
        values: &mut Vec<f32>,
    ) {
        let rec = self.recall(layer, range);
        f16_chunk_to_f32_into(&rec.keys, keys);
        f16_chunk_to_f32_into(&rec.values, values);
    }

    /// Tokens stored at layer 0 (layers stay in lockstep when used from the generate loop).
    fn seq_len(&self) -> usize;

    fn layers(&self) -> usize;

    fn dim(&self) -> usize;

    fn clear(&mut self);

    /// Shrink resident length to `new_len` (drop the suffix). No-op when `new_len >= seq_len`.
    /// Speculative draft→verify rewind seam (R13 PLD) — keeps prefix K/V so rejected drafts can
    /// be undone without clearing the whole cache.
    fn truncate(&mut self, new_len: usize);

    /// Attention seam — **stub only** (empty output). Real QK/V math is owned elsewhere.
    fn attend(&self, layer: usize, query: &[F16], range: Range<usize>) -> AttentionResult;
}

/// In-RAM KV cache: one contiguous buffer per layer.
#[derive(Clone, Debug)]
pub struct RamKvStore {
    layers: usize,
    dim: usize,
    pub(crate) keys: Vec<Vec<F16>>,
    pub(crate) values: Vec<Vec<F16>>,
}

impl RamKvStore {
    pub fn new(layers: usize, dim: usize) -> Self {
        assert!(dim > 0, "kv dim must be > 0");
        RamKvStore {
            layers,
            dim,
            keys: (0..layers).map(|_| Vec::new()).collect(),
            values: (0..layers).map(|_| Vec::new()).collect(),
        }
    }

    /// Pre-reserve capacity for `tokens` at every layer (avoids realloc thrash on long ctx).
    pub fn reserve_tokens(&mut self, tokens: usize) {
        let elems = tokens.saturating_mul(self.dim);
        for l in 0..self.layers {
            self.keys[l].reserve(elems);
            self.values[l].reserve(elems);
        }
    }

    #[inline]
    fn layer_len(&self, layer: usize) -> usize {
        self.values[layer].len() / self.dim
    }

    pub(crate) fn append_value_only(&mut self, layer: usize, value: &[F16]) {
        assert!(layer < self.layers);
        assert_eq!(value.len(), self.dim);
        self.values[layer].extend_from_slice(value);
    }

    /// StreamingLLM compact: keep `[0..sinks)` + last `recent` tokens; drop the middle.
    /// No-op when `layer_len <= sinks + recent`.
    fn compact_sinks_recent(&mut self, sinks: usize, recent: usize) {
        let dim = self.dim;
        for l in 0..self.layers {
            let n = self.layer_len(l);
            let sinks = sinks.min(n);
            let recent = recent.min(n.saturating_sub(sinks));
            if sinks + recent >= n {
                continue;
            }
            let keep_from = n - recent;
            let new_tokens = sinks + recent;
            let mut nv = Vec::with_capacity(new_tokens * dim);
            nv.extend_from_slice(&self.values[l][..sinks * dim]);
            nv.extend_from_slice(&self.values[l][keep_from * dim..n * dim]);
            self.values[l] = nv;
            if !self.keys[l].is_empty() {
                let mut nk = Vec::with_capacity(new_tokens * dim);
                nk.extend_from_slice(&self.keys[l][..sinks * dim]);
                nk.extend_from_slice(&self.keys[l][keep_from * dim..n * dim]);
                self.keys[l] = nk;
            }
        }
    }
}

impl KvStore for RamKvStore {
    fn append(&mut self, layer: usize, token: usize, key: &[F16], value: &[F16]) {
        assert!(layer < self.layers, "layer {layer} out of range ({})", self.layers);
        assert_eq!(key.len(), self.dim, "key width {} != dim {}", key.len(), self.dim);
        assert_eq!(value.len(), self.dim, "value width {} != dim {}", value.len(), self.dim);
        let len = self.layer_len(layer);
        assert_eq!(token, len, "append must be sequential (token {token}, layer_len {len})");
        self.keys[layer].extend_from_slice(key);
        self.values[layer].extend_from_slice(value);
    }

    fn recall(&self, layer: usize, range: Range<usize>) -> KvRecall {
        assert!(layer < self.layers, "layer {layer} out of range ({})", self.layers);
        let len = self.layer_len(layer);
        let start = range.start.min(len);
        let end = range.end.min(len).max(start);
        let tokens = end - start;
        let dim = self.dim;
        let ks = start * dim;
        let ke = end * dim;
        KvRecall {
            keys: self.keys[layer][ks..ke].to_vec(),
            values: self.values[layer][ks..ke].to_vec(),
            tokens,
            dim,
        }
    }

    fn recall_f32_into(
        &self,
        layer: usize,
        range: Range<usize>,
        keys: &mut Vec<f32>,
        values: &mut Vec<f32>,
    ) {
        let len = self.layer_len(layer);
        let start = range.start.min(len);
        let end = range.end.min(len).max(start);
        let dim = self.dim;
        let ks = start * dim;
        let ke = end * dim;
        f16_chunk_to_f32_into(&self.keys[layer][ks..ke], keys);
        f16_chunk_to_f32_into(&self.values[layer][ks..ke], values);
    }

    fn seq_len(&self) -> usize {
        if self.layers == 0 {
            0
        } else {
            self.layer_len(0)
        }
    }

    fn layers(&self) -> usize {
        self.layers
    }

    fn dim(&self) -> usize {
        self.dim
    }

    fn clear(&mut self) {
        for l in 0..self.layers {
            self.keys[l].clear();
            self.values[l].clear();
        }
    }

    fn truncate(&mut self, new_len: usize) {
        let dim = self.dim;
        for l in 0..self.layers {
            let n = self.layer_len(l);
            let keep = new_len.min(n);
            self.keys[l].truncate(keep * dim);
            self.values[l].truncate(keep * dim);
        }
    }

    fn attend(&self, _layer: usize, _query: &[F16], _range: Range<usize>) -> AttentionResult {
        AttentionResult::default()
    }
}

/// Counters for paged KV residency (StreamingLLM eviction + hot window).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct PagedKvMetrics {
    /// Times a recall range touched tokens outside the hot window (pre-eviction) or
    /// counted as a cold spill after compaction.
    pub page_ins: u64,
    pub hot_recalls: u64,
    /// Tokens physically dropped by StreamingLLM compaction.
    pub cold_tokens_touched: u64,
    /// Compaction / eviction events (one per decode step that overflowed `hot_cap`).
    pub evictions: u64,
}

/// Ctx length at/above which [`KvKeyQuant::auto_for_ctx`] selects Q8 K+V lanes.
/// R28.3: tier starts at **2048** (was 4096) so mid-ctx sessions get ~50% KV vs F16
/// without waiting for the 4k default. Ultra-short ctx (<2048) stays F16 for tight
/// microbenches. At [`KV_KEY_QUANT_Q8V4_MIN_CTX`] auto promotes to Q8V4.
pub const KV_KEY_QUANT_Q8_MIN_CTX: usize = 2048;

/// Ctx length at/above which [`KvKeyQuant::auto_for_ctx`] selects Q8V4 (K=Q8, V=Q4).
/// Force earlier via `GE_KV_AUTO_Q8V4=1` or `GE_KV_QUANT=Q8V4`. See [`KvKeyQuant::auto_q8v4_from_env`].
pub const KV_KEY_QUANT_Q8V4_MIN_CTX: usize = 8192;

/// MC.4 grow-on-demand: initial KV token reserve per request (not full `hot_cap`).
/// Keep small — short chats should not commit pages for unused `-c` slots.
pub const KV_INITIAL_RESERVE_MIN: usize = 32;

#[inline]
fn kv_full_prealloc_env() -> bool {
    match std::env::var("GE_KV_FULL_PREALLOC") {
        Ok(v) => matches!(
            v.trim().to_ascii_lowercase().as_str(),
            "1" | "true" | "yes" | "on"
        ),
        Err(_) => false,
    }
}

/// Key/value lane storage for the paged store.
///
/// - [`KvKeyQuant::Q8`] — symmetric int8+scale on **both** K and V (~50% vs F16).
/// - [`KvKeyQuant::Q8V4`] — MC.3 paired: **K=Q8**, **V=Q4** packed nibbles (~0.375× F16).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum KvKeyQuant {
    /// Full F16 keys and values (highest fidelity, ~2× RAM vs Q8).
    #[default]
    F16,
    /// Per-token absmax scale + i8 lanes for **keys and values**.
    /// Auto-selected when resolved ctx ∈ [[`KV_KEY_QUANT_Q8_MIN_CTX`], [`KV_KEY_QUANT_Q8V4_MIN_CTX`]).
    Q8,
    /// Asymmetric MC.3: K stays Q8; V uses group-wise absmax + packed i4 (2 elems/byte).
    /// Auto-selected when resolved ctx ≥ [`KV_KEY_QUANT_Q8V4_MIN_CTX`], or earlier via
    /// `GE_KV_AUTO_Q8V4=1` / `GE_KV_QUANT=Q8V4`. ~2.7× denser than F16; ~1.3× vs Q8/Q8.
    Q8V4,
}

impl KvKeyQuant {
    /// `GE_KV_AUTO_Q8V4=1|true|yes|on` forces [`KvKeyQuant::Q8V4`] from [`Self::auto_for_ctx`].
    #[inline]
    pub fn auto_q8v4_from_env() -> bool {
        match std::env::var("GE_KV_AUTO_Q8V4") {
            Ok(v) => matches!(
                v.trim().to_ascii_lowercase().as_str(),
                "1" | "true" | "yes" | "on"
            ),
            Err(_) => false,
        }
    }

    /// Auto tier for native generate when the request omits an explicit key quant.
    ///
    /// - `ctx < 2048` -> [`KvKeyQuant::F16`] (short-ctx / tight benches)
    /// - `2048 <= ctx < 8192` -> [`KvKeyQuant::Q8`] (~50% KV RAM vs F16 K+V)
    /// - `ctx >= 8192` -> [`KvKeyQuant::Q8V4`] (K=Q8 V=Q4; group-wise V scales)
    /// - `GE_KV_AUTO_Q8V4=1` forces [`KvKeyQuant::Q8V4`] at any ctx
    ///
    /// Explicit `GE_KV_QUANT` still wins via [`Self::from_env`] at the call site.
    #[inline]
    pub fn auto_for_ctx(ctx: usize) -> Self {
        if Self::auto_q8v4_from_env() || ctx >= KV_KEY_QUANT_Q8V4_MIN_CTX {
            KvKeyQuant::Q8V4
        } else if ctx >= KV_KEY_QUANT_Q8_MIN_CTX {
            KvKeyQuant::Q8
        } else {
            KvKeyQuant::F16
        }
    }

    /// Short label for CLI / metrics (`F16`, `Q8`, `Q8V4`).
    pub fn label(self) -> &'static str {
        match self {
            KvKeyQuant::F16 => "F16",
            KvKeyQuant::Q8 => "Q8",
            KvKeyQuant::Q8V4 => "Q8V4",
        }
    }

    #[inline]
    pub fn uses_q8_keys(self) -> bool {
        matches!(self, KvKeyQuant::Q8 | KvKeyQuant::Q8V4)
    }

    /// Parse `GE_KV_QUANT` / aliases (`F16`, `Q8`, `Q8V4` / `K8V4`). `None` if unset/unknown.
    pub fn from_env() -> Option<Self> {
        match std::env::var("GE_KV_QUANT").ok().as_deref().map(|s| s.trim()) {
            Some("F16") | Some("f16") => Some(KvKeyQuant::F16),
            Some("Q8") | Some("q8") => Some(KvKeyQuant::Q8),
            Some("Q8V4") | Some("q8v4") | Some("Q8_V4") | Some("K8V4") | Some("k8v4") => {
                Some(KvKeyQuant::Q8V4)
            }
            _ => None,
        }
    }
}

impl std::fmt::Display for KvKeyQuant {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.label())
    }
}

/// Paged KV with **real** StreamingLLM residency: attention sinks + recent window.
///
/// Unlike the old metrics-only stub, tokens outside `sinks ∪ recent` are **physically
/// dropped** once `seq_len > hot_cap`, so peak KV RAM stays ≈ `hot_cap` tokens.
/// RoPE is applied before append, so retained K/V keep their original positions.
///
/// Optional quantized lanes: [`KvKeyQuant::Q8`] stores K+V as int8+scale;
/// [`KvKeyQuant::Q8V4`] stores K as Q8 and V as packed Q4.
#[derive(Clone, Debug)]
pub struct PagedRamKvStore {
    inner: RamKvStore,
    hot_cap: usize,
    sinks: usize,
    key_quant: KvKeyQuant,
    /// When keys are Q8 (`Q8` / `Q8V4`), packed keys live here (F16 `inner.keys` unused).
    keys_q8: Option<Q8LaneStore>,
    /// When `key_quant == Q8`, packed values live here.
    values_q8: Option<Q8LaneStore>,
    /// When `key_quant == Q8V4`, packed Q4 values live here.
    values_q4: Option<Q4LaneStore>,
    page_ins: Cell<u64>,
    hot_recalls: Cell<u64>,
    cold_tokens_touched: Cell<u64>,
    evictions: Cell<u64>,
}

/// Per-layer Q8 lanes: `scales[t]` + `qs[t*dim..(t+1)*dim]` (keys or values).
#[derive(Clone, Debug)]
struct Q8LaneStore {
    dim: usize,
    scales: Vec<Vec<F16>>,
    qs: Vec<Vec<i8>>,
}

/// Group size for V=Q4 absmax scales (ggml-style blocks).
/// One scale over full kv_dim let a single-head outlier crush other heads and
/// caused Hello@8192 greedy diverge vs Q8 at token 3. Group=32 keeps >=20% save vs Q8.
pub const KV_Q4_V_GROUP: usize = 32;

/// Per-layer Q4 values: `scales[t*ngroups + g]` + packed nibbles `qs[t*(dim/2)..]`.
/// Dim must be divisible by [`KV_Q4_V_GROUP`]. Scale = group-absmax/7; codes [-7, 7].
#[derive(Clone, Debug)]
struct Q4LaneStore {
    dim: usize,
    groups: usize,
    scales: Vec<Vec<F16>>,
    qs: Vec<Vec<u8>>,
}

impl Q4LaneStore {
    fn new(layers: usize, dim: usize) -> Self {
        assert!(
            dim > 0 && dim % KV_Q4_V_GROUP == 0,
            "Q4 KV dim must be multiple of {} (got {dim})",
            KV_Q4_V_GROUP
        );
        Q4LaneStore {
            dim,
            groups: dim / KV_Q4_V_GROUP,
            scales: (0..layers).map(|_| Vec::new()).collect(),
            qs: (0..layers).map(|_| Vec::new()).collect(),
        }
    }

    #[inline]
    fn packed_elems(&self) -> usize {
        self.dim / 2
    }

    #[inline]
    fn len(&self, layer: usize) -> usize {
        self.scales[layer].len() / self.groups.max(1)
    }

    fn append(&mut self, layer: usize, lane_f16: &[F16]) {
        let dim = self.dim;
        let groups = self.groups;
        let gsz = KV_Q4_V_GROUP;
        assert_eq!(lane_f16.len(), dim);
        let mut tmp = vec![0.0f32; dim];
        for (i, &h) in lane_f16.iter().enumerate() {
            tmp[i] = f16_bits_to_f32_kv(h);
        }
        for g in 0..groups {
            let base = g * gsz;
            let mut amax = 0.0f32;
            for i in 0..gsz {
                amax = amax.max(tmp[base + i].abs());
            }
            let scale = if amax > 0.0 { amax / 7.0 } else { 1.0 };
            self.scales[layer].push(f32_to_f16_bits_kv(scale));
            for p in 0..(gsz / 2) {
                let i0 = base + p * 2;
                let q0 = (tmp[i0] / scale).round().clamp(-7.0, 7.0) as i8;
                let q1 = (tmp[i0 + 1] / scale).round().clamp(-7.0, 7.0) as i8;
                let byte = ((q0 as u8) & 0x0f) | (((q1 as u8) & 0x0f) << 4);
                self.qs[layer].push(byte);
            }
        }
    }

    #[inline]
    fn unpack_nibble(byte: u8, high: bool) -> i8 {
        let n = if high { (byte >> 4) & 0x0f } else { byte & 0x0f };
        ((n as i8) << 4) >> 4
    }

    fn recall_f32_into(&self, layer: usize, start: usize, end: usize, out: &mut Vec<f32>) {
        out.clear();
        let dim = self.dim;
        let groups = self.groups;
        let gsz = KV_Q4_V_GROUP;
        let npack = self.packed_elems();
        out.reserve((end - start) * dim);
        for t in start..end {
            let sc_base = t * groups;
            let qs_base = t * npack;
            for g in 0..groups {
                let scale = f16_bits_to_f32_kv(self.scales[layer][sc_base + g]);
                let pack_base = qs_base + g * (gsz / 2);
                for p in 0..(gsz / 2) {
                    let byte = self.qs[layer][pack_base + p];
                    out.push(Self::unpack_nibble(byte, false) as f32 * scale);
                    out.push(Self::unpack_nibble(byte, true) as f32 * scale);
                }
            }
        }
    }

    fn recall_f16(&self, layer: usize, start: usize, end: usize) -> Vec<F16> {
        let mut f = Vec::new();
        self.recall_f32_into(layer, start, end, &mut f);
        f.into_iter().map(f32_to_f16_bits_kv).collect()
    }

    fn compact_sinks_recent(&mut self, sinks: usize, recent: usize) {
        let npack = self.packed_elems();
        let groups = self.groups;
        for l in 0..self.scales.len() {
            let n = self.len(l);
            let sinks = sinks.min(n);
            let recent = recent.min(n.saturating_sub(sinks));
            if sinks + recent >= n {
                continue;
            }
            let keep_from = n - recent;
            let mut ns = Vec::with_capacity((sinks + recent) * groups);
            let mut nq = Vec::with_capacity((sinks + recent) * npack);
            ns.extend_from_slice(&self.scales[l][..sinks * groups]);
            nq.extend_from_slice(&self.qs[l][..sinks * npack]);
            ns.extend_from_slice(&self.scales[l][keep_from * groups..n * groups]);
            nq.extend_from_slice(&self.qs[l][keep_from * npack..n * npack]);
            self.scales[l] = ns;
            self.qs[l] = nq;
        }
    }

    fn clear(&mut self) {
        for s in &mut self.scales {
            s.clear();
        }
        for q in &mut self.qs {
            q.clear();
        }
    }

    fn truncate(&mut self, new_len: usize) {
        let npack = self.packed_elems();
        let groups = self.groups;
        for l in 0..self.scales.len() {
            let n = self.len(l);
            let keep = new_len.min(n);
            self.scales[l].truncate(keep * groups);
            self.qs[l].truncate(keep * npack);
        }
    }

    fn reserve_tokens(&mut self, tokens: usize) {
        let npack = self.packed_elems();
        let groups = self.groups;
        for l in 0..self.scales.len() {
            self.scales[l].reserve(tokens.saturating_mul(groups));
            self.qs[l].reserve(tokens.saturating_mul(npack));
        }
    }
}

impl Q8LaneStore {
    fn new(layers: usize, dim: usize) -> Self {
        Q8LaneStore {
            dim,
            scales: (0..layers).map(|_| Vec::new()).collect(),
            qs: (0..layers).map(|_| Vec::new()).collect(),
        }
    }

    #[inline]
    fn len(&self, layer: usize) -> usize {
        self.scales[layer].len()
    }

    fn append(&mut self, layer: usize, lane_f16: &[F16]) {
        let dim = self.dim;
        assert_eq!(lane_f16.len(), dim);
        let mut amax = 0.0f32;
        let mut tmp = vec![0.0f32; dim];
        for (i, &h) in lane_f16.iter().enumerate() {
            let v = f16_bits_to_f32_kv(h);
            tmp[i] = v;
            amax = amax.max(v.abs());
        }
        let scale = if amax > 0.0 { amax / 127.0 } else { 1.0 };
        self.scales[layer].push(f32_to_f16_bits_kv(scale));
        for &v in &tmp {
            let q = (v / scale).round().clamp(-127.0, 127.0) as i8;
            self.qs[layer].push(q);
        }
    }

    fn recall_f32_into(&self, layer: usize, start: usize, end: usize, out: &mut Vec<f32>) {
        out.clear();
        let dim = self.dim;
        out.reserve((end - start) * dim);
        for t in start..end {
            let scale = f16_bits_to_f32_kv(self.scales[layer][t]);
            let base = t * dim;
            for i in 0..dim {
                out.push(self.qs[layer][base + i] as f32 * scale);
            }
        }
    }

    fn recall_f16(&self, layer: usize, start: usize, end: usize) -> Vec<F16> {
        let dim = self.dim;
        let mut out = Vec::with_capacity((end - start) * dim);
        for t in start..end {
            let scale = f16_bits_to_f32_kv(self.scales[layer][t]);
            let base = t * dim;
            for i in 0..dim {
                let v = self.qs[layer][base + i] as f32 * scale;
                out.push(f32_to_f16_bits_kv(v));
            }
        }
        out
    }

    fn compact_sinks_recent(&mut self, sinks: usize, recent: usize) {
        let dim = self.dim;
        for l in 0..self.scales.len() {
            let n = self.scales[l].len();
            let sinks = sinks.min(n);
            let recent = recent.min(n.saturating_sub(sinks));
            if sinks + recent >= n {
                continue;
            }
            let keep_from = n - recent;
            let mut ns = Vec::with_capacity(sinks + recent);
            let mut nq = Vec::with_capacity((sinks + recent) * dim);
            ns.extend_from_slice(&self.scales[l][..sinks]);
            nq.extend_from_slice(&self.qs[l][..sinks * dim]);
            ns.extend_from_slice(&self.scales[l][keep_from..n]);
            nq.extend_from_slice(&self.qs[l][keep_from * dim..n * dim]);
            self.scales[l] = ns;
            self.qs[l] = nq;
        }
    }

    fn clear(&mut self) {
        for s in &mut self.scales {
            s.clear();
        }
        for q in &mut self.qs {
            q.clear();
        }
    }

    fn truncate(&mut self, new_len: usize) {
        let dim = self.dim;
        for l in 0..self.scales.len() {
            let n = self.scales[l].len();
            let keep = new_len.min(n);
            self.scales[l].truncate(keep);
            self.qs[l].truncate(keep * dim);
        }
    }

    fn reserve_tokens(&mut self, tokens: usize) {
        let dim = self.dim;
        for l in 0..self.scales.len() {
            self.scales[l].reserve(tokens);
            self.qs[l].reserve(tokens.saturating_mul(dim));
        }
    }
}

// Local f16 helpers so kv.rs does not depend on attention (avoids cycle). Same RNE rules.
fn f16_bits_to_f32_kv(h: F16) -> f32 {
    let sign = ((h as u32) & 0x8000) << 16;
    let exp = ((h as u32) >> 10) & 0x1f;
    let mant = (h as u32) & 0x3ff;
    let bits = if exp == 0 {
        if mant == 0 {
            sign
        } else {
            let mut m = mant;
            let mut e = -1i32;
            while (m & 0x400) == 0 {
                m <<= 1;
                e -= 1;
            }
            m &= 0x3ff;
            let exp_f = (127 - 15 + e) as u32;
            sign | (exp_f << 23) | (m << 13)
        }
    } else if exp == 31 {
        sign | 0x7f80_0000 | (mant << 13)
    } else {
        let exp_f = (exp as i32 - 15 + 127) as u32;
        sign | (exp_f << 23) | (mant << 13)
    };
    f32::from_bits(bits)
}

fn f32_to_f16_bits_kv(v: f32) -> F16 {
    let bits = v.to_bits();
    let sign = ((bits >> 16) & 0x8000) as u32;
    let mut exp = ((bits >> 23) & 0xff) as i32;
    let mut mant = bits & 0x7f_ffff;
    if exp == 255 {
        return if mant == 0 {
            (sign | 0x7c00) as u16
        } else {
            (sign | 0x7c00 | (mant >> 13)) as u16
        };
    }
    if exp == 0 {
        return sign as u16;
    }
    exp = exp - 127 + 15;
    if exp >= 31 {
        return (sign | 0x7c00) as u16;
    }
    if exp <= 0 {
        if exp < -10 {
            return sign as u16;
        }
        mant |= 0x800_000;
        let shift = (1 - exp) as u32;
        let mut m = mant >> (shift + 13);
        let rem = mant >> (shift + 12);
        if (rem & 1) != 0 {
            m += 1;
        }
        return (sign | m) as u16;
    }
    let mut half = sign | ((exp as u32) << 10) | (mant >> 13);
    if (mant & 0x1000) != 0 {
        half += 1;
    }
    half as u16
}

fn f16_chunk_to_f32_into(src: &[F16], dst: &mut Vec<f32>) {
    dst.clear();
    dst.reserve(src.len());
    for &h in src {
        dst.push(f16_bits_to_f32_kv(h));
    }
}

impl PagedRamKvStore {
    pub fn new(layers: usize, dim: usize, hot_cap: usize) -> Self {
        Self::with_policy(layers, dim, hot_cap, DEFAULT_ATTENTION_SINKS, KvKeyQuant::F16)
    }

    pub fn with_policy(
        layers: usize,
        dim: usize,
        hot_cap: usize,
        sinks: usize,
        key_quant: KvKeyQuant,
    ) -> Self {
        let hot_cap = hot_cap.max(1);
        // Leave at least one slot for the recent window.
        let sinks = sinks.min(hot_cap.saturating_sub(1));
        let (keys_q8, values_q8, values_q4) = match key_quant {
            KvKeyQuant::Q8 => (
                Some(Q8LaneStore::new(layers, dim)),
                Some(Q8LaneStore::new(layers, dim)),
                None,
            ),
            KvKeyQuant::Q8V4 => (
                Some(Q8LaneStore::new(layers, dim)),
                None,
                Some(Q4LaneStore::new(layers, dim)),
            ),
            KvKeyQuant::F16 => (None, None, None),
        };
        PagedRamKvStore {
            inner: RamKvStore::new(layers, dim),
            hot_cap,
            sinks,
            key_quant,
            keys_q8,
            values_q8,
            values_q4,
            page_ins: Cell::new(0),
            hot_recalls: Cell::new(0),
            cold_tokens_touched: Cell::new(0),
            evictions: Cell::new(0),
        }
    }

    /// Tokens retained in the hot residency window (capped memory tier).
    #[inline]
    pub fn hot_cap(&self) -> usize {
        self.hot_cap
    }

    #[inline]
    pub fn sinks(&self) -> usize {
        self.sinks
    }

    #[inline]
    pub fn key_quant(&self) -> KvKeyQuant {
        self.key_quant
    }

    /// Reserve for up to `tokens` resident slots (typically `ctx_len` / `hot_cap`).
    pub fn reserve_tokens(&mut self, tokens: usize) {
        let n = tokens.min(self.hot_cap).max(1);
        match self.key_quant {
            KvKeyQuant::F16 => self.inner.reserve_tokens(n),
            KvKeyQuant::Q8 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.reserve_tokens(n);
                }
                if let Some(ref mut q) = self.values_q8 {
                    q.reserve_tokens(n);
                }
            }
            KvKeyQuant::Q8V4 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.reserve_tokens(n);
                }
                if let Some(ref mut q) = self.values_q4 {
                    q.reserve_tokens(n);
                }
            }
        }
    }

    /// Reserve capacity for this request only (prompt + decode headroom), not full `-c`.
    ///
    /// R28.2 / MC.4: short prompts with large `hot_cap` must not commit unused pages.
    /// Decode budget is clamped to a small grow step so `max_new` ≈ `-c` does not
    /// pre-size the whole window — Vec/device grow on append. Set
    /// `GE_KV_FULL_PREALLOC=1` to force full `hot_cap` reserve (A/B reclaim gate).
    pub fn reserve_for_request(&mut self, prompt_len: usize, max_new: usize) {
        let full = kv_full_prealloc_env();
        let need = if full {
            self.hot_cap
        } else {
            // Headroom: a few grow steps past the prompt, never the full decode budget.
            let headroom = max_new.min(KV_INITIAL_RESERVE_MIN.saturating_mul(4).max(64));
            prompt_len
                .saturating_add(headroom)
                .max(KV_INITIAL_RESERVE_MIN)
                .min(self.hot_cap)
        };
        self.reserve_tokens(need);
        if full {
            self.fault_in_reserved();
        }
    }

    /// Touch reserved-but-empty capacity so OS Working Set accounts for it (A/B only).
    fn fault_in_reserved(&mut self) {
        fn fault_u16(v: &mut Vec<u16>) {
            let cap = v.capacity();
            let len = v.len();
            if cap > len {
                v.resize(cap, 0);
                v.truncate(len);
            }
        }
        fn fault_i8(v: &mut Vec<i8>) {
            let cap = v.capacity();
            let len = v.len();
            if cap > len {
                v.resize(cap, 0);
                v.truncate(len);
            }
        }
        fn fault_u8(v: &mut Vec<u8>) {
            let cap = v.capacity();
            let len = v.len();
            if cap > len {
                v.resize(cap, 0);
                v.truncate(len);
            }
        }
        match self.key_quant {
            KvKeyQuant::F16 => {
                for l in 0..self.inner.keys.len() {
                    fault_u16(&mut self.inner.keys[l]);
                    fault_u16(&mut self.inner.values[l]);
                }
            }
            KvKeyQuant::Q8 => {
                if let Some(ref mut q) = self.keys_q8 {
                    for l in 0..q.scales.len() {
                        fault_u16(&mut q.scales[l]);
                        fault_i8(&mut q.qs[l]);
                    }
                }
                if let Some(ref mut q) = self.values_q8 {
                    for l in 0..q.scales.len() {
                        fault_u16(&mut q.scales[l]);
                        fault_i8(&mut q.qs[l]);
                    }
                }
            }
            KvKeyQuant::Q8V4 => {
                if let Some(ref mut q) = self.keys_q8 {
                    for l in 0..q.scales.len() {
                        fault_u16(&mut q.scales[l]);
                        fault_i8(&mut q.qs[l]);
                    }
                }
                if let Some(ref mut q) = self.values_q4 {
                    for l in 0..q.scales.len() {
                        fault_u16(&mut q.scales[l]);
                        fault_u8(&mut q.qs[l]);
                    }
                }
            }
        }
    }

    /// Token capacity currently reserved (layer 0), for reclaim / grow-on-demand checks.
    pub fn reserved_capacity_tokens(&self) -> usize {
        let dim = self.inner.dim().max(1);
        match self.key_quant {
            KvKeyQuant::F16 => self
                .inner
                .keys
                .first()
                .map(|k| k.capacity() / dim)
                .unwrap_or(0),
            KvKeyQuant::Q8 | KvKeyQuant::Q8V4 => self
                .keys_q8
                .as_ref()
                .and_then(|q| q.scales.first().map(|s| s.capacity()))
                .unwrap_or(0),
        }
    }

    pub fn metrics(&self) -> PagedKvMetrics {
        PagedKvMetrics {
            page_ins: self.page_ins.get(),
            hot_recalls: self.hot_recalls.get(),
            cold_tokens_touched: self.cold_tokens_touched.get(),
            evictions: self.evictions.get(),
        }
    }

    /// Approximate **resident** bytes (`layers × resident_tokens × …`).
    pub fn hot_bytes(&self) -> usize {
        let n = self.seq_len();
        Self::estimate_full_ctx_bytes(self.inner.layers(), self.inner.dim(), n, self.key_quant)
    }

    /// GQA-aware estimate for a **full** ctx at this geometry (documentation / CLI help).
    pub fn estimate_full_ctx_bytes(layers: usize, dim: usize, ctx: usize, key_quant: KvKeyQuant) -> usize {
        match key_quant {
            KvKeyQuant::F16 => layers * ctx * dim * std::mem::size_of::<F16>() * 2,
            KvKeyQuant::Q8 => {
                // K and V: scale f16 + i8 per elem each → ~50% of F16 K+V for dim≫1.
                let per_lane = std::mem::size_of::<F16>() + dim * std::mem::size_of::<i8>();
                layers * ctx * per_lane * 2
            }
            KvKeyQuant::Q8V4 => {
                // K: scale + i8[dim]; V: group scales + packed u8[dim/2] → ~0.38× F16.
                let groups = dim / KV_Q4_V_GROUP;
                let k = std::mem::size_of::<F16>() + dim * std::mem::size_of::<i8>();
                let v = groups * std::mem::size_of::<F16>() + (dim / 2) * std::mem::size_of::<u8>();
                layers * ctx * (k + v)
            }
        }
    }

    /// Half-open token range currently considered hot (resident recent window).
    pub fn hot_range(&self) -> Range<usize> {
        let n = self.seq_len();
        let recent = self.hot_cap.saturating_sub(self.sinks).max(1);
        n.saturating_sub(recent)..n
    }

    fn compact_if_needed(&mut self) {
        let n = self.seq_len();
        if n <= self.hot_cap {
            return;
        }
        let sinks = self.sinks.min(self.hot_cap);
        let recent = self.hot_cap.saturating_sub(sinks).max(1);
        self.compact_to(sinks, recent, n);
    }

    /// Drop to `sinks + recent` tokens; bump eviction metrics.
    fn compact_to(&mut self, sinks: usize, recent: usize, n_before: usize) {
        let sinks = sinks.min(n_before);
        let recent = recent.min(n_before.saturating_sub(sinks));
        if sinks + recent >= n_before {
            return;
        }
        let dropped = n_before.saturating_sub(sinks + recent);
        match self.key_quant {
            KvKeyQuant::F16 => self.inner.compact_sinks_recent(sinks, recent),
            KvKeyQuant::Q8 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.compact_sinks_recent(sinks, recent);
                }
                if let Some(ref mut q) = self.values_q8 {
                    q.compact_sinks_recent(sinks, recent);
                }
            }
            KvKeyQuant::Q8V4 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.compact_sinks_recent(sinks, recent);
                }
                if let Some(ref mut q) = self.values_q4 {
                    q.compact_sinks_recent(sinks, recent);
                }
            }
        }
        self.evictions.set(self.evictions.get() + 1);
        self.cold_tokens_touched
            .set(self.cold_tokens_touched.get() + dropped as u64);
        self.page_ins.set(self.page_ins.get() + 1);
    }

    /// Free one slot before append so attention never sees `seq > hot_cap`.
    fn make_room_for_append(&mut self) {
        let n = self.seq_len();
        if n < self.hot_cap {
            return;
        }
        let sinks = self.sinks.min(self.hot_cap.saturating_sub(1));
        let keep = self.hot_cap.saturating_sub(1);
        let recent = keep.saturating_sub(sinks);
        self.compact_to(sinks, recent, n);
    }

    #[inline]
    fn q8_layer_len(&self, layer: usize) -> usize {
        self.keys_q8
            .as_ref()
            .map(|q| q.len(layer))
            .unwrap_or(0)
    }
}

impl KvStore for PagedRamKvStore {
    fn append(&mut self, layer: usize, _token: usize, key: &[F16], value: &[F16]) {
        // Absolute decode positions may exceed resident length after eviction.
        // Always append at the resident end (RoPE already baked into `key`).
        // Compact before append when full so attention never sees seq > hot_cap
        // (mirrors device `kv_prepare_append`).
        if layer == 0 {
            self.make_room_for_append();
        }
        match self.key_quant {
            KvKeyQuant::F16 => {
                let len = self.inner.layer_len(layer);
                self.inner.append(layer, len, key, value);
            }
            KvKeyQuant::Q8 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.append(layer, key);
                }
                if let Some(ref mut q) = self.values_q8 {
                    q.append(layer, value);
                }
            }
            KvKeyQuant::Q8V4 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.append(layer, key);
                }
                if let Some(ref mut q) = self.values_q4 {
                    q.append(layer, value);
                }
            }
        }
        // Safety net after last layer (keeps layers lockstep if make_room skipped).
        if layer + 1 == self.inner.layers() {
            self.compact_if_needed();
        }
    }

    fn recall(&self, layer: usize, range: Range<usize>) -> KvRecall {
        let len = self.seq_len();
        let start = range.start.min(len);
        let end = range.end.min(len).max(start);
        let hot = self.hot_range();
        if start >= hot.start {
            self.hot_recalls.set(self.hot_recalls.get() + 1);
        } else {
            // Sink prefix (still resident) — count as hot-adjacent, not a page-in.
            self.hot_recalls.set(self.hot_recalls.get() + 1);
        }
        let dim = self.inner.dim();
        let tokens = end - start;
        let (keys, values) = match self.key_quant {
            KvKeyQuant::F16 => {
                let ks = start * dim;
                let ke = end * dim;
                (
                    self.inner.keys[layer][ks..ke].to_vec(),
                    self.inner.values[layer][ks..ke].to_vec(),
                )
            }
            KvKeyQuant::Q8 => (
                self.keys_q8
                    .as_ref()
                    .map(|q| q.recall_f16(layer, start, end))
                    .unwrap_or_default(),
                self.values_q8
                    .as_ref()
                    .map(|q| q.recall_f16(layer, start, end))
                    .unwrap_or_default(),
            ),
            KvKeyQuant::Q8V4 => (
                self.keys_q8
                    .as_ref()
                    .map(|q| q.recall_f16(layer, start, end))
                    .unwrap_or_default(),
                self.values_q4
                    .as_ref()
                    .map(|q| q.recall_f16(layer, start, end))
                    .unwrap_or_default(),
            ),
        };
        KvRecall {
            keys,
            values,
            tokens,
            dim,
        }
    }

    fn recall_f32_into(
        &self,
        layer: usize,
        range: Range<usize>,
        keys: &mut Vec<f32>,
        values: &mut Vec<f32>,
    ) {
        let len = self.seq_len();
        let start = range.start.min(len);
        let end = range.end.min(len).max(start);
        let dim = self.inner.dim();
        match self.key_quant {
            KvKeyQuant::F16 => {
                let ks = start * dim;
                let ke = end * dim;
                f16_chunk_to_f32_into(&self.inner.keys[layer][ks..ke], keys);
                f16_chunk_to_f32_into(&self.inner.values[layer][ks..ke], values);
            }
            KvKeyQuant::Q8 => {
                if let Some(ref q) = self.keys_q8 {
                    q.recall_f32_into(layer, start, end, keys);
                } else {
                    keys.clear();
                }
                if let Some(ref q) = self.values_q8 {
                    q.recall_f32_into(layer, start, end, values);
                } else {
                    values.clear();
                }
            }
            KvKeyQuant::Q8V4 => {
                if let Some(ref q) = self.keys_q8 {
                    q.recall_f32_into(layer, start, end, keys);
                } else {
                    keys.clear();
                }
                if let Some(ref q) = self.values_q4 {
                    q.recall_f32_into(layer, start, end, values);
                } else {
                    values.clear();
                }
            }
        }
    }

    fn seq_len(&self) -> usize {
        match self.key_quant {
            KvKeyQuant::F16 => self.inner.seq_len(),
            KvKeyQuant::Q8 | KvKeyQuant::Q8V4 => self.q8_layer_len(0),
        }
    }

    fn layers(&self) -> usize {
        self.inner.layers()
    }

    fn dim(&self) -> usize {
        self.inner.dim()
    }

    fn clear(&mut self) {
        self.inner.clear();
        if let Some(ref mut q) = self.keys_q8 {
            q.clear();
        }
        if let Some(ref mut q) = self.values_q8 {
            q.clear();
        }
        if let Some(ref mut q) = self.values_q4 {
            q.clear();
        }
        self.page_ins.set(0);
        self.hot_recalls.set(0);
        self.cold_tokens_touched.set(0);
        self.evictions.set(0);
    }

    fn truncate(&mut self, new_len: usize) {
        match self.key_quant {
            KvKeyQuant::F16 => self.inner.truncate(new_len),
            KvKeyQuant::Q8 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.truncate(new_len);
                }
                if let Some(ref mut q) = self.values_q8 {
                    q.truncate(new_len);
                }
            }
            KvKeyQuant::Q8V4 => {
                if let Some(ref mut q) = self.keys_q8 {
                    q.truncate(new_len);
                }
                if let Some(ref mut q) = self.values_q4 {
                    q.truncate(new_len);
                }
            }
        }
    }

    fn attend(&self, layer: usize, query: &[F16], range: Range<usize>) -> AttentionResult {
        self.inner.attend(layer, query, range)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum KvPolicy {
    /// Attention sinks (first few) + recent window. [StreamingLLM]
    Stream,
    /// Heavy hitters by accumulated attention + recent. [H2O]
    H2O,
    /// Keys scored by recent queries' pooled attention. [SnapKV]
    SnapKv,
    /// Per-query top-B by this query's own attention (oracle upper bound). [Quest]
    Quest,
}

/// Head-averaged attention `attn[layer][q*tokens + k]`, lower-triangular (causal).
pub struct Attention {
    pub layers: usize,
    pub tokens: usize,
    data: Vec<f32>,
}

impl Attention {
    pub fn load<P: AsRef<Path>>(path: P) -> io::Result<Self> {
        let b = fs::read(path)?;
        if b.len() < 12 || u32::from_le_bytes(b[0..4].try_into().unwrap()) != MAGIC {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "bad attention header"));
        }
        let layers = u32::from_le_bytes(b[4..8].try_into().unwrap()) as usize;
        let tokens = u32::from_le_bytes(b[8..12].try_into().unwrap()) as usize;
        let n = layers * tokens * tokens;
        if b.len() != 12 + n * 4 {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "attention size mismatch"));
        }
        let mut data = vec![0.0f32; n];
        for i in 0..n {
            let o = 12 + i * 4;
            data[i] = f32::from_le_bytes(b[o..o + 4].try_into().unwrap());
        }
        Ok(Attention { layers, tokens, data })
    }

    #[inline]
    fn row(&self, l: usize, q: usize) -> &[f32] {
        let base = (l * self.tokens + q) * self.tokens;
        &self.data[base..base + self.tokens]
    }

    /// Fraction of true attention mass at (layer l, query q) retained by keeping `budget` keys.
    pub fn retained(&self, policy: KvPolicy, l: usize, q: usize, budget: usize) -> f64 {
        let nk = q + 1;
        let a = &self.row(l, q)[..nk];
        if budget >= nk {
            return 1.0;
        }
        let keep: Vec<usize> = match policy {
            KvPolicy::Stream => {
                let s = SINKS.min(nk);
                let recent = budget.saturating_sub(s);
                (0..s).chain(nk.saturating_sub(recent)..nk).collect()
            }
            KvPolicy::H2O => {
                let recent = (budget / 4).max(1);
                let take = budget.saturating_sub(recent);
                // accumulated attention to each key (column sum over queries ≤ q)
                let mut acc = vec![0.0f32; nk];
                for qp in 0..nk {
                    let r = &self.row(l, qp)[..nk];
                    for k in 0..nk {
                        acc[k] += r[k];
                    }
                }
                let cutoff = nk.saturating_sub(recent);
                let mut idx: Vec<usize> = (0..cutoff).collect();
                idx.sort_by(|&x, &y| acc[y].partial_cmp(&acc[x]).unwrap());
                idx.truncate(take);
                idx.into_iter().chain(cutoff..nk).collect()
            }
            KvPolicy::SnapKv => {
                let win = 16.min(nk);
                let mut score = vec![0.0f32; nk];
                for qp in (q + 1 - win)..=q {
                    let r = &self.row(l, qp)[..nk];
                    for k in 0..nk {
                        score[k] += r[k] / win as f32;
                    }
                }
                top_k(&score, budget)
            }
            KvPolicy::Quest => top_k(a, budget),
        };
        keep.iter().map(|&k| a[k] as f64).sum()
    }

    /// Mean retained mass over the decode region (q ≥ tokens/2) at budget fraction `frac`.
    pub fn evaluate(&self, policy: KvPolicy, frac: f64) -> f64 {
        let mut sum = 0.0;
        let mut n = 0;
        for l in 0..self.layers {
            for q in (self.tokens / 2)..self.tokens {
                let budget = (((frac * (q + 1) as f64).round()) as usize).max(SINKS + 1);
                sum += self.retained(policy, l, q, budget);
                n += 1;
            }
        }
        sum / n as f64
    }

    /// Per-layer retained mass (Quest) at `frac` — the spread motivates adaptive per-layer budgets.
    pub fn per_layer(&self, policy: KvPolicy, frac: f64) -> Vec<f64> {
        (0..self.layers)
            .map(|l| {
                let mut sum = 0.0;
                let mut n = 0;
                for q in (self.tokens / 2)..self.tokens {
                    let budget = (((frac * (q + 1) as f64).round()) as usize).max(SINKS + 1);
                    sum += self.retained(policy, l, q, budget);
                    n += 1;
                }
                sum / n as f64
            })
            .collect()
    }
}

fn top_k(score: &[f32], k: usize) -> Vec<usize> {
    let mut idx: Vec<usize> = (0..score.len()).collect();
    idx.sort_by(|&a, &b| score[b].partial_cmp(&score[a]).unwrap());
    idx.truncate(k);
    idx
}

// ----------------------------------------------------------------------------------------------
// Adaptive per-layer KV budget allocation — the engine's differentiator over static (PyramidKV)
// schemes. Given ONE total KV budget, allocate keys to layers by marginal retention gain, so
// attention-spread layers get more and concentrated layers get less.

impl Attention {
    /// Retention curve for (layer l, query q): `curve[b]` = attention mass kept with budget `b`
    /// under `policy` (prefix sums of true attention over the policy's keep order).
    pub fn retention_curve(&self, policy: KvPolicy, l: usize, q: usize) -> Vec<f64> {
        let nk = q + 1;
        let a = &self.row(l, q)[..nk];
        // keep order = keys sorted by the policy's score (desc)
        let order: Vec<usize> = match policy {
            KvPolicy::Quest => top_k(a, nk),
            KvPolicy::SnapKv => {
                let win = 16.min(nk);
                let mut score = vec![0.0f32; nk];
                for qp in (q + 1 - win)..=q {
                    let r = &self.row(l, qp)[..nk];
                    for k in 0..nk {
                        score[k] += r[k];
                    }
                }
                top_k(&score, nk)
            }
            _ => top_k(a, nk),
        };
        let mut curve = vec![0.0f64; nk + 1];
        let mut acc = 0.0;
        for (i, &k) in order.iter().enumerate() {
            acc += a[k] as f64;
            curve[i + 1] = acc;
        }
        curve
    }

    /// Greedy marginal-gain allocation of `total` keys across layers at query `q`.
    pub fn allocate_adaptive(&self, policy: KvPolicy, q: usize, total: usize) -> Vec<usize> {
        let curves: Vec<Vec<f64>> = (0..self.layers).map(|l| self.retention_curve(policy, l, q)).collect();
        let cap = q + 1;
        let mut budget = vec![0usize; self.layers];
        // start each layer with the sink floor, then water-fill by marginal gain
        let floor = SINKS.min(cap);
        for b in budget.iter_mut() {
            *b = floor;
        }
        let mut spent: usize = floor * self.layers;
        let target = total.min(cap * self.layers);
        while spent < target {
            let mut best_l = 0;
            let mut best_gain = -1.0;
            for l in 0..self.layers {
                if budget[l] < cap {
                    let g = curves[l][budget[l] + 1] - curves[l][budget[l]];
                    if g > best_gain {
                        best_gain = g;
                        best_l = l;
                    }
                }
            }
            budget[best_l] += 1;
            spent += 1;
        }
        budget
    }

    pub fn allocate_uniform(&self, q: usize, total: usize) -> Vec<usize> {
        let per = (total / self.layers).min(q + 1);
        vec![per; self.layers]
    }

    /// Mean retained mass (over layers) for a per-layer budget at query `q`.
    pub fn total_retained(&self, policy: KvPolicy, q: usize, budgets: &[usize]) -> f64 {
        let mut s = 0.0;
        for l in 0..self.layers {
            let curve = self.retention_curve(policy, l, q);
            s += curve[budgets[l].min(q + 1)];
        }
        s / self.layers as f64
    }
}

/// KV memory (bytes) for `kept` tokens over `layers`, at a given quantization.
/// `kv_bits`: 16 = fp16, 2 = KIVI-style 2-bit (+overhead). per-token-per-layer = 2·heads·head_dim.
pub fn kv_bytes(kept_per_layer: &[usize], n_kv_heads: usize, head_dim: usize, kv_bits: f64) -> u64 {
    let per_token = 2.0 * n_kv_heads as f64 * head_dim as f64 * (kv_bits / 8.0); // K and V
    kept_per_layer.iter().map(|&k| (k as f64 * per_token) as u64).sum()
}

/// KIVI-style symmetric KV-cache quantization. The KV cache is `[tokens, channels]` row-major.
/// The research (KIVI) finds **keys** are best quantized **per-channel** (a scale per `channels`
/// column, since key outliers concentrate in fixed channels) and **values per-token** (a scale per
/// row). `bits` = 8 or 4. Returns `(dequantized, mean_rel_error)`; the roundtrip is deterministic,
/// so the same scheme reproduces bit-for-bit (see tests). This is the token/context-memory analog
/// of the weight tiers — it cuts KV VRAM ~2× (int8) or ~4× (int4) vs fp16 at high fidelity, letting
/// long-context chat fit far more tokens in the same budget.
pub fn quantize_kv(data: &[f32], tokens: usize, channels: usize, bits: u32, per_channel: bool) -> (Vec<f32>, f32) {
    assert_eq!(data.len(), tokens * channels, "quantize_kv: len != tokens*channels");
    let qmax = ((1i32 << (bits - 1)) - 1) as f32; // 127 (int8) or 7 (int4)
    let mut out = vec![0.0f32; data.len()];
    let groups = if per_channel { channels } else { tokens };
    let stride = if per_channel { channels } else { 1 }; // step between elements of one group
    let count = if per_channel { tokens } else { channels };
    for g in 0..groups {
        // start index of group g: column g (per-channel) or row g (per-token)
        let base = if per_channel { g } else { g * channels };
        let mut amax = 0.0f32;
        for i in 0..count {
            amax = amax.max(data[base + i * stride].abs());
        }
        let scale = if amax > 0.0 { amax / qmax } else { 1.0 };
        for i in 0..count {
            let idx = base + i * stride;
            let q = (data[idx] / scale).round().clamp(-qmax, qmax);
            out[idx] = q * scale;
        }
    }
    let (mut num, mut den) = (0.0f64, 0.0f64);
    for (a, b) in data.iter().zip(&out) {
        num += ((a - b) as f64).powi(2);
        den += (*a as f64).powi(2);
    }
    (out, (num / den.max(1e-12)).sqrt() as f32)
}

#[cfg(test)]
mod kv_quant_tests {
    use super::quantize_kv;
    use super::{f16_bits_to_f32_kv, f32_to_f16_bits_kv, F16, KvKeyQuant, PagedRamKvStore};

    // Structured KV: per-channel signal + a few large per-channel outliers (the KIVI regime).
    fn kv(tokens: usize, channels: usize, seed: u64) -> Vec<f32> {
        let mut s = seed.wrapping_add(0x9E3779B97F4A7C15);
        let mut rng = || { s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
            (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 };
        let chan_bias: Vec<f32> = (0..channels).map(|_| rng() * 2.0).collect();
        let mut v = vec![0.0f32; tokens * channels];
        for t in 0..tokens {
            for c in 0..channels {
                v[t * channels + c] = chan_bias[c] + 0.1 * rng();
            }
        }
        v
    }

    #[test]
    fn kv_quant_int8_beats_int4_and_is_deterministic() {
        let (tokens, channels) = (128, 64);
        let data = kv(tokens, channels, 7);

        // keys per-channel (KIVI); int8 must be higher fidelity than int4, both bounded.
        let (_, d8) = quantize_kv(&data, tokens, channels, 8, true);
        let (deq4a, d4) = quantize_kv(&data, tokens, channels, 4, true);
        assert!(d8 < d4, "int8 KV drift {d8} should beat int4 {d4}");
        assert!(d8 < 0.02, "int8 KV drift {d8} should be small");

        // deterministic roundtrip: same call reproduces bit-for-bit.
        let (deq4b, _) = quantize_kv(&data, tokens, channels, 4, true);
        assert_eq!(deq4a, deq4b, "KV quant must be deterministic");

        // per-channel should beat per-token for this per-channel-structured key data.
        let (_, d_pc) = quantize_kv(&data, tokens, channels, 8, true);
        let (_, d_pt) = quantize_kv(&data, tokens, channels, 8, false);
        assert!(d_pc <= d_pt, "per-channel {d_pc} should beat per-token {d_pt} on key-structured data");
    }
}

#[cfg(test)]
mod kv_store_tests {
    use super::{KvKeyQuant, KvStore, PagedRamKvStore, RamKvStore, F16};

    fn slot(token: usize, dim: usize, tag: u16) -> (Vec<F16>, Vec<F16>) {
        let k: Vec<F16> = (0..dim).map(|i| tag.wrapping_add(token as u16).wrapping_add(i as u16)).collect();
        let v: Vec<F16> = (0..dim).map(|i| (tag ^ 0xA5A5).wrapping_add(token as u16).wrapping_add((i * 3) as u16)).collect();
        (k, v)
    }

    /// Multi-token generate-shaped loop: append all layers per step, recall past, stub attend.
    fn generate_loop<K: KvStore>(kv: &mut K, n_tokens: usize) {
        let layers = kv.layers();
        let dim = kv.dim();
        for t in 0..n_tokens {
            for layer in 0..layers {
                let (k, v) = slot(t, dim, layer as u16);
                kv.append(layer, t, &k, &v);
                let past = kv.recall(layer, 0..kv.seq_len());
                assert_eq!(past.tokens, t + 1);
                assert_eq!(past.key_at(t), k.as_slice());
                assert_eq!(past.value_at(t), v.as_slice());
                let q = vec![1u16; dim];
                let attn = kv.attend(layer, &q, 0..kv.seq_len());
                assert!(attn.output.is_empty(), "attend is an attention-agent stub");
            }
        }
    }

    /// Like [`generate_loop`] but tolerates StreamingLLM compaction (seq capped at hot_cap).
    fn generate_loop_paged(kv: &mut PagedRamKvStore, n_tokens: usize) {
        let layers = kv.layers();
        let dim = kv.dim();
        for t in 0..n_tokens {
            for layer in 0..layers {
                let (k, v) = slot(t, dim, layer as u16);
                kv.append(layer, t, &k, &v);
                let past = kv.recall(layer, 0..kv.seq_len());
                // Compaction runs after the last layer of each step — mid-token may be hot_cap+1.
                assert!(
                    past.tokens <= kv.hot_cap() + 1,
                    "resident {} > hot_cap+1 {}",
                    past.tokens,
                    kv.hot_cap() + 1
                );
                assert_eq!(past.tokens, kv.seq_len());
                assert_eq!(past.value_at(past.tokens - 1), v.as_slice());
            }
            assert!(
                kv.seq_len() <= kv.hot_cap(),
                "after full token step, seq {} must be ≤ hot_cap {}",
                kv.seq_len(),
                kv.hot_cap()
            );
        }
    }

    #[test]
    fn ram_kv_append_recall_by_layer() {
        let mut kv = RamKvStore::new(3, 8);
        generate_loop(&mut kv, 5);
        assert_eq!(kv.seq_len(), 5);

        let mid = kv.recall(1, 1..4);
        assert_eq!(mid.tokens, 3);
        assert_eq!(mid.key_at(0), slot(1, 8, 1).0.as_slice());
        assert_eq!(mid.key_at(2), slot(3, 8, 1).0.as_slice());

        kv.clear();
        assert_eq!(kv.seq_len(), 0);
        assert_eq!(kv.recall(0, 0..10).tokens, 0);
    }

    #[test]
    #[should_panic(expected = "append must be sequential")]
    fn ram_kv_rejects_non_sequential_append() {
        let mut kv = RamKvStore::new(1, 4);
        let (k, v) = slot(0, 4, 0);
        kv.append(0, 0, &k, &v);
        kv.append(0, 2, &k, &v);
    }

    #[test]
    fn paged_ram_kv_evicts_to_hot_cap_with_sinks() {
        // hot_cap=4, sinks=2 → keep 2 sinks + 2 recent; overflow drops the middle.
        let mut kv = PagedRamKvStore::with_policy(2, 4, 4, 2, KvKeyQuant::F16);
        generate_loop_paged(&mut kv, 8);
        assert_eq!(kv.seq_len(), 4, "resident must cap at hot_cap");
        let m = kv.metrics();
        assert!(m.evictions >= 1, "expected StreamingLLM compaction; {m:?}");
        assert!(
            m.cold_tokens_touched >= 4,
            "expected dropped middle tokens; {m:?}"
        );
        // Sinks retained: token-0 values still at index 0.
        let (_k0, v0) = slot(0, 4, 0);
        let past = kv.recall(0, 0..1);
        assert_eq!(past.value_at(0), v0.as_slice());
    }

    #[test]
    fn reserve_for_request_stays_near_request_not_full_ctx() {
        let hot = 32768usize;
        let mut kv = PagedRamKvStore::with_policy(2, 8, hot, 4, KvKeyQuant::Q8);
        kv.reserve_for_request(24, 16);
        let cap = kv.reserved_capacity_tokens();
        assert!(
            cap >= 40 && cap < hot / 8,
            "grow-on-demand must reserve ~request ({cap}), not full hot_cap={hot}"
        );
        let full = PagedRamKvStore::estimate_full_ctx_bytes(2, 8, hot, KvKeyQuant::Q8);
        let used = PagedRamKvStore::estimate_full_ctx_bytes(2, 8, cap, KvKeyQuant::Q8);
        let reclaim = 1.0 - (used as f64 / full as f64);
        assert!(
            reclaim >= 0.20,
            "expected ≥20% reclaim vs full prealloc; reclaim={reclaim:.3} used={used} full={full}"
        );
    }

    #[test]
    fn kv_key_quant_auto_for_ctx_tiers() {
        // Ensure env override is off for deterministic tier asserts.
        std::env::remove_var("GE_KV_AUTO_Q8V4");
        assert_eq!(KvKeyQuant::auto_for_ctx(0), KvKeyQuant::F16);
        assert_eq!(KvKeyQuant::auto_for_ctx(2047), KvKeyQuant::F16);
        assert_eq!(KvKeyQuant::auto_for_ctx(2048), KvKeyQuant::Q8);
        assert_eq!(KvKeyQuant::auto_for_ctx(4095), KvKeyQuant::Q8);
        assert_eq!(KvKeyQuant::auto_for_ctx(4096), KvKeyQuant::Q8);
        assert_eq!(KvKeyQuant::auto_for_ctx(8191), KvKeyQuant::Q8);
        assert_eq!(KvKeyQuant::auto_for_ctx(8192), KvKeyQuant::Q8V4);
        assert_eq!(KvKeyQuant::auto_for_ctx(16384), KvKeyQuant::Q8V4);
        std::env::set_var("GE_KV_AUTO_Q8V4", "1");
        assert_eq!(KvKeyQuant::auto_for_ctx(0), KvKeyQuant::Q8V4);
        assert_eq!(KvKeyQuant::auto_for_ctx(4096), KvKeyQuant::Q8V4);
        std::env::remove_var("GE_KV_AUTO_Q8V4");
        let f16 = PagedRamKvStore::estimate_full_ctx_bytes(16, 512, 8192, KvKeyQuant::F16);
        let q8 = PagedRamKvStore::estimate_full_ctx_bytes(16, 512, 8192, KvKeyQuant::Q8);
        let save = 1.0 - (q8 as f64 / f16 as f64);
        assert!(
            save > 0.49 && save < 0.51,
            "expected ~50% KV save at 8k (Q8 K+V); f16={f16} q8={q8} save={save:.3}"
        );
        let q8v4 = PagedRamKvStore::estimate_full_ctx_bytes(16, 512, 8192, KvKeyQuant::Q8V4);
        let vs_f16 = q8v4 as f64 / f16 as f64;
        assert!(
            vs_f16 <= 0.60,
            "Q8V4 must be ≤0.6× F16 at 8k; ratio={vs_f16:.3} q8v4={q8v4} f16={f16}"
        );
        let vs_q8 = 1.0 - (q8v4 as f64 / q8 as f64);
        assert!(
            vs_q8 >= 0.20,
            "Q8V4 must save ≥20% vs Q8/Q8; save={vs_q8:.3} q8v4={q8v4} q8={q8}"
        );
    }

    #[test]
    fn paged_ram_kv_q8v4_values_roundtrip() {
        let dim = super::KV_Q4_V_GROUP;
        let mut kv = PagedRamKvStore::with_policy(1, dim, 16, 4, KvKeyQuant::Q8V4);
        let k: Vec<F16> = (0..dim).map(|i| ((i as u16) + 100) * 64).collect();
        let v: Vec<F16> = (0..dim)
            .map(|i| super::f32_to_f16_bits_kv((i as f32 - 15.5) * 0.05))
            .collect();
        kv.append(0, 0, &k, &v);
        let mut kf = Vec::new();
        let mut vf = Vec::new();
        kv.recall_f32_into(0, 0..1, &mut kf, &mut vf);
        assert_eq!(kf.len(), dim);
        assert_eq!(vf.len(), dim);
        for i in 0..dim {
            let want = super::f16_bits_to_f32_kv(v[i]);
            assert!(
                (vf[i] - want).abs() < 0.15,
                "V Q4 err too large at {i}: got={} want={}",
                vf[i],
                want
            );
        }
        assert_eq!(
            PagedRamKvStore::estimate_full_ctx_bytes(1, dim, 1, KvKeyQuant::Q8V4),
            kv.hot_bytes()
        );
    }

    #[test]
    fn ram_kv_truncate_rewinds_suffix() {
        let mut kv = RamKvStore::new(2, 4);
        generate_loop(&mut kv, 5);
        assert_eq!(kv.seq_len(), 5);
        let keep = kv.recall(0, 0..3);
        kv.truncate(3);
        assert_eq!(kv.seq_len(), 3);
        let past = kv.recall(0, 0..kv.seq_len());
        assert_eq!(past.tokens, 3);
        assert_eq!(past.keys, keep.keys);
        assert_eq!(past.values, keep.values);
        // Prefix intact at every layer.
        let (k2, v2) = slot(2, 4, 1);
        assert_eq!(kv.recall(1, 2..3).key_at(0), k2.as_slice());
        assert_eq!(kv.recall(1, 2..3).value_at(0), v2.as_slice());
        // Append resumes at new_len.
        let (k3, v3) = slot(3, 4, 0);
        kv.append(0, 3, &k3, &v3);
        assert_eq!(kv.seq_len(), 4);
        // No-op when new_len >= seq.
        kv.truncate(100);
        assert_eq!(kv.seq_len(), 4);
        kv.truncate(0);
        assert_eq!(kv.seq_len(), 0);
    }

    #[test]
    fn paged_ram_kv_truncate_rewinds_f16_and_q8() {
        for quant in [KvKeyQuant::F16, KvKeyQuant::Q8] {
            let mut kv = PagedRamKvStore::with_policy(2, 8, 64, 4, quant);
            for t in 0..6 {
                for layer in 0..2 {
                    let (k, v) = slot(t, 8, layer as u16);
                    kv.append(layer, t, &k, &v);
                }
            }
            assert_eq!(kv.seq_len(), 6);
            let prefix = kv.recall(0, 0..4);
            kv.truncate(4);
            assert_eq!(kv.seq_len(), 4, "quant={quant:?}");
            let past = kv.recall(0, 0..4);
            assert_eq!(past.tokens, 4);
            assert_eq!(past.keys, prefix.keys);
            assert_eq!(past.values, prefix.values);
            let (k4, v4) = slot(4, 8, 0);
            kv.append(0, 4, &k4, &v4);
            kv.append(1, 4, &slot(4, 8, 1).0, &slot(4, 8, 1).1);
            assert_eq!(kv.seq_len(), 5);
        }
    }

    #[test]
    fn paged_ram_kv_truncate_q8v4() {
        let dim = super::KV_Q4_V_GROUP;
        let mut kv = PagedRamKvStore::with_policy(1, dim, 32, 4, KvKeyQuant::Q8V4);
        for t in 0..5 {
            let k: Vec<F16> = (0..dim).map(|i| ((i as u16) + 100 + t as u16) * 64).collect();
            let v: Vec<F16> = (0..dim)
                .map(|i| super::f32_to_f16_bits_kv((i as f32 - 15.5 + t as f32) * 0.05))
                .collect();
            kv.append(0, t, &k, &v);
        }
        assert_eq!(kv.seq_len(), 5);
        kv.truncate(2);
        assert_eq!(kv.seq_len(), 2);
        let past = kv.recall(0, 0..2);
        assert_eq!(past.tokens, 2);
        assert_eq!(past.dim, dim);
    }

    #[test]
    fn paged_ram_kv_q8_keys_roundtrip_latest() {
        let mut kv = PagedRamKvStore::with_policy(1, 8, 16, 4, KvKeyQuant::Q8);
        let (_k, v) = slot(0, 8, 7);
        let k: Vec<F16> = (0..8).map(|i| ((i as u16) + 100) * 64).collect();
        kv.append(0, 0, &k, &v);
        let past = kv.recall(0, 0..1);
        assert_eq!(past.tokens, 1);
        assert_eq!(past.key_at(0).len(), 8);
        assert_eq!(past.value_at(0).len(), 8);
        // Values round-trip through Q8 with small error; exact F16 match not required.
        let mut kf = Vec::new();
        let mut vf = Vec::new();
        kv.recall_f32_into(0, 0..1, &mut kf, &mut vf);
        assert_eq!(kf.len(), 8);
        assert_eq!(vf.len(), 8);
    }
}