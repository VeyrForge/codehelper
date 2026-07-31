//! Quantized matrix GEMV for dense forward without materializing full f32 weights.
//!
//! Pack-model 1B packages store 2D weights as Q4_0 (~0.85 GB). Dequantizing all layers to
//! f32 needs ~4–5 GB and OOMs on typical developer machines — keep packed and stream
//! dequant during GEMV instead.
//!
//! Hot path: **row-wise fused** Q4_0 block dots (AVX2/FMA f32×Q4 by default; no full-matrix
//! scratch). Rayon over output rows on a **physical-core** pool.
//! - `GE_GEMV_LEGACY=1` — old flat dequant path (A/B)
//! - `GE_GEMV_Q8=1` — opt-in Q4×Q8 act-quant int path (faster, quality risk)
//! - `GE_GEMV_FORCE_F32=1` — materialize f32 matrix per GEMV (debug only)

use std::ops::Deref;
use std::sync::{Arc, OnceLock};

use memmap2::Mmap;
use rayon::prelude::*;

use crate::gguf_load::{f16_to_f32, GgufLoadError};
use crate::quant_kernels::{
    self, ActQ8, GemvIsa, Q4_0_BLOCK, Q4_0_BYTES, Q4_K_BYTES, QK_K,
};
use crate::quant_repack::{self, GGML_Q4_0_4X8, GGML_Q4_0_8X8};
use crate::sys;

/// Packed weight bytes: file-backed mmap slice (preferred) or owned (repack / tests).
///
/// Lean GGUF load used to `to_vec()` every tensor off an ephemeral mmap, doubling RSS
/// versus llama.cpp's mmap-only residency. Keep the map alive and view ranges instead.
#[derive(Clone)]
pub enum PackedBytes {
    Mapped {
        map: Arc<Mmap>,
        start: usize,
        end: usize,
    },
    Owned(Vec<u8>),
}

impl PackedBytes {
    #[inline]
    pub fn owned(v: Vec<u8>) -> Self {
        PackedBytes::Owned(v)
    }

    #[inline]
    pub fn mapped(map: Arc<Mmap>, start: usize, end: usize) -> Self {
        debug_assert!(start <= end && end <= map.len());
        PackedBytes::Mapped { map, start, end }
    }

    #[inline]
    pub fn as_slice(&self) -> &[u8] {
        match self {
            PackedBytes::Mapped { map, start, end } => &map[*start..*end],
            PackedBytes::Owned(v) => v.as_slice(),
        }
    }

    #[inline]
    pub fn len(&self) -> usize {
        self.as_slice().len()
    }

    #[inline]
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    #[inline]
    pub fn as_ptr(&self) -> *const u8 {
        self.as_slice().as_ptr()
    }
}

impl Deref for PackedBytes {
    type Target = [u8];
    #[inline]
    fn deref(&self) -> &[u8] {
        self.as_slice()
    }
}

impl From<Vec<u8>> for PackedBytes {
    fn from(v: Vec<u8>) -> Self {
        PackedBytes::Owned(v)
    }
}

impl std::fmt::Debug for PackedBytes {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PackedBytes::Mapped { start, end, .. } => f
                .debug_struct("Mapped")
                .field("len", &(end - start))
                .field("start", start)
                .finish(),
            PackedBytes::Owned(v) => f.debug_tuple("Owned").field(&v.len()).finish(),
        }
    }
}

const Q8_0_BLOCK: usize = 32;
const Q8_0_BYTES: usize = 34;
const Q6_K_BYTES: usize = 210;

/// IQ2/IQ3 block nbytes (llama.cpp `sizeof(block_*)`, QK_K=256). Sizing only — no dequant/GEMV.
const IQ2_XXS_BYTES: usize = 66; // 2 + (QK_K/8)*2
const IQ2_XS_BYTES: usize = 74; // 2 + (QK_K/8)*2 + QK_K/32
const IQ3_XXS_BYTES: usize = 98; // 2 + 3*(QK_K/8)
const IQ3_S_BYTES: usize = 110; // 2 + 13*(QK_K/32) + QK_K/64
const IQ2_S_BYTES: usize = 82; // 2 + QK_K/4 + QK_K/16

pub const GGML_F32: u32 = 0;
pub const GGML_F16: u32 = 1;
pub const GGML_Q4_0: u32 = 2;
pub const GGML_Q8_0: u32 = 8;
pub const GGML_Q4_K: u32 = 12;
pub const GGML_Q6_K: u32 = 14;
/// ggml IQ inventory (docs R6.IQ) — accepted by [`QuantMat::nbytes_packed`] only.
pub const GGML_IQ2_XXS: u32 = 16;
pub const GGML_IQ2_XS: u32 = 17;
pub const GGML_IQ3_XXS: u32 = 18;
pub const GGML_IQ3_S: u32 = 21;
pub const GGML_IQ2_S: u32 = 22;
/// ggml MXFP4 / NVFP4 (W33 scaffold — sizing only until GEMV/TC).
pub const GGML_MXFP4: u32 = crate::weight_fmt::GGML_MXFP4;
pub const GGML_NVFP4: u32 = crate::weight_fmt::GGML_NVFP4;

pub fn maybe_repack_q4_0(m: QuantMat, tensor_name: &str) -> QuantMat {
    if !quant_repack::repack_enabled() || !quant_repack::should_repack_tensor(tensor_name) {
        return m;
    }
    if m.ggml_type != GGML_Q4_0 || m.in_dim % Q4_0_BLOCK != 0 {
        return m;
    }
    let nrows = quant_repack::repack_nrows();
    if nrows == 4 {
        if m.out_dim % 4 != 0 {
            return m;
        }
        return match quant_repack::repack_q4_0_4x8_mat(m.packed.as_slice(), m.in_dim, m.out_dim) {
            Ok(packed) => QuantMat {
                ggml_type: GGML_Q4_0_4X8,
                packed: PackedBytes::owned(packed),
                ..m
            },
            Err(e) => {
                eprintln!("GE repack4 skip {tensor_name}: {e}");
                m
            }
        };
    }
    if m.out_dim % 8 != 0 {
        return m;
    }
    match quant_repack::repack_q4_0_mat(m.packed.as_slice(), m.in_dim, m.out_dim) {
        Ok(packed) => QuantMat {
            ggml_type: GGML_Q4_0_8X8,
            packed: PackedBytes::owned(packed),
            ..m
        },
        Err(e) => {
            eprintln!("GE repack skip {tensor_name}: {e}");
            m
        }
    }
}

pub fn gemv_use_repack_q8() -> bool {
    !gemv_force_f32_env() && quant_repack::repack_enabled()
}

pub struct GemvActScratch { pub act: ActQ8 }
impl GemvActScratch {
    pub fn new(in_dim: usize) -> Self {
        let n = in_dim / Q4_0_BLOCK;
        GemvActScratch { act: ActQ8 { scales: Vec::with_capacity(n), qs: Vec::with_capacity(n * Q4_0_BLOCK) } }
    }
    pub fn quantize_into(&mut self, x: &[f32]) { self.act.quantize_into(x); }
}

/// 2D weight matrix from pack-model / GGUFWriter dense shards.
///
/// Pack-model GGUF shape is `[ne0=in_dim, ne1=out_dim]` (writer reverses a logical
/// `(out, in)` view). Payload bytes are **row-major `(out, in)`** (C-order):
/// `W[o, i] = flat[o * in_dim + i]`.
#[derive(Clone, Debug)]
pub struct QuantMat {
    pub ggml_type: u32,
    pub in_dim: usize,
    pub out_dim: usize,
    /// Packed bytes (or raw f32 LE for GGML_F32). Prefer mmap-backed [`PackedBytes::Mapped`].
    pub packed: PackedBytes,
}

impl QuantMat {
    pub fn from_f32(in_dim: usize, out_dim: usize, data: &[f32]) -> Self {
        let mut packed = Vec::with_capacity(data.len() * 4);
        for &v in data {
            packed.extend_from_slice(&v.to_le_bytes());
        }
        QuantMat {
            ggml_type: GGML_F32,
            in_dim,
            out_dim,
            packed: PackedBytes::owned(packed),
        }
    }

    pub fn nbytes_packed(ggml_type: u32, nelems: usize) -> Result<usize, GgufLoadError> {
        match ggml_type {
            GGML_F32 => Ok(nelems * 4),
            GGML_F16 => Ok(nelems * 2),
            GGML_Q4_0 => Ok(((nelems + Q4_0_BLOCK - 1) / Q4_0_BLOCK) * Q4_0_BYTES),
            GGML_Q8_0 => Ok(((nelems + Q8_0_BLOCK - 1) / Q8_0_BLOCK) * Q8_0_BYTES),
            GGML_Q4_K => Ok(((nelems + QK_K - 1) / QK_K) * Q4_K_BYTES),
            GGML_Q6_K => Ok(((nelems + QK_K - 1) / QK_K) * Q6_K_BYTES),
            GGML_IQ2_XXS => Ok(((nelems + QK_K - 1) / QK_K) * IQ2_XXS_BYTES),
            GGML_IQ2_XS => Ok(((nelems + QK_K - 1) / QK_K) * IQ2_XS_BYTES),
            GGML_IQ3_XXS => Ok(((nelems + QK_K - 1) / QK_K) * IQ3_XXS_BYTES),
            GGML_IQ3_S => Ok(((nelems + QK_K - 1) / QK_K) * IQ3_S_BYTES),
            GGML_IQ2_S => Ok(((nelems + QK_K - 1) / QK_K) * IQ2_S_BYTES),
            GGML_NVFP4 | GGML_MXFP4 => crate::weight_fmt::nbytes_fp4(ggml_type, nelems).ok_or(
                GgufLoadError::UnsupportedType {
                    name: "QuantMat".into(),
                    ggml_type,
                },
            ),
            other => Err(GgufLoadError::UnsupportedType {
                name: "QuantMat".into(),
                ggml_type: other,
            }),
        }
    }

    /// `y[o] = Σ_i x[i] · W[o, i]` with W row-major `(out, in)`.
    pub fn gemv(&self, x: &[f32], y: &mut [f32]) {
        assert_eq!(x.len(), self.in_dim);
        assert_eq!(y.len(), self.out_dim);
        // Escape hatch: materialize f32 and dense GEMV (A/B correctness vs fused Q4_K).
        if gemv_force_f32_env() {
            if let Ok(w) = self.to_f32() {
                assert_eq!(w.len(), self.in_dim * self.out_dim);
                for o in 0..self.out_dim {
                    let mut s = 0.0f32;
                    let row = &w[o * self.in_dim..(o + 1) * self.in_dim];
                    for i in 0..self.in_dim {
                        s += x[i] * row[i];
                    }
                    y[o] = s;
                }
                return;
            }
        }
        if gemv_legacy_env() {
            match self.ggml_type {
                GGML_F32 => gemv_f32(&self.packed, self.in_dim, self.out_dim, x, y),
                GGML_Q4_0 => gemv_q4_0_legacy(&self.packed, self.in_dim, self.out_dim, x, y),
                GGML_Q8_0 => gemv_q8_0_legacy(&self.packed, self.in_dim, self.out_dim, x, y),
                GGML_Q4_K => gemv_via_blocks_legacy(
                    &self.packed,
                    self.in_dim,
                    self.out_dim,
                    x,
                    y,
                    QK_K,
                    Q4_K_BYTES,
                    dequant_q4_k_block,
                ),
                GGML_Q6_K => gemv_via_blocks_legacy(
                    &self.packed,
                    self.in_dim,
                    self.out_dim,
                    x,
                    y,
                    QK_K,
                    Q6_K_BYTES,
                    dequant_q6_k_block,
                ),
                // Spike step 4: IQ2_XXS only (legacy block path). No IQ2_XS/S / CUDA.
                GGML_IQ2_XXS => gemv_via_blocks_legacy(
                    &self.packed,
                    self.in_dim,
                    self.out_dim,
                    x,
                    y,
                    QK_K,
                    IQ2_XXS_BYTES,
                    crate::iq2_xxs::dequant_iq2_xxs_block,
                ),
                GGML_F16 => gemv_f16(&self.packed, self.in_dim, self.out_dim, x, y),
                _ => y.fill(0.0),
            }
            return;
        }
        match self.ggml_type {
            GGML_F32 => gemv_f32(&self.packed, self.in_dim, self.out_dim, x, y),
            GGML_Q4_0 => gemv_q4_0(&self.packed, self.in_dim, self.out_dim, x, y),
            GGML_Q4_0_4X8 => gemv_q4_0_4x8(&self.packed, self.in_dim, self.out_dim, x, y),
            GGML_Q4_0_8X8 => gemv_q4_0_8x8(&self.packed, self.in_dim, self.out_dim, x, y),
            GGML_Q8_0 => gemv_q8_0(&self.packed, self.in_dim, self.out_dim, x, y),
            GGML_Q4_K => gemv_q4_k(&self.packed, self.in_dim, self.out_dim, x, y),
            GGML_Q6_K => gemv_q6_k(&self.packed, self.in_dim, self.out_dim, x, y),
            // Spike step 4: IQ2_XXS CPU GEMV via block dequant (mirror Q4_K legacy).
            GGML_IQ2_XXS => gemv_via_blocks_legacy(
                &self.packed,
                self.in_dim,
                self.out_dim,
                x,
                y,
                QK_K,
                IQ2_XXS_BYTES,
                crate::iq2_xxs::dequant_iq2_xxs_block,
            ),
            GGML_F16 => gemv_f16(&self.packed, self.in_dim, self.out_dim, x, y),
            _ => y.fill(0.0),
        }
    }

    /// Materialize full f32 (tests / tiny models). Prefer [`Self::gemv`] on 1B+.
    pub fn to_f32(&self) -> Result<Vec<f32>, GgufLoadError> {
        let n = self.in_dim * self.out_dim;
        crate::gguf_load::dequant_slice(self.ggml_type, self.packed.as_slice(), n)
    }

    /// Dequant one row `row` (0..out_dim) into `out` (len = in_dim). No full-table alloc.
    pub fn dequant_row(&self, row: usize, out: &mut [f32]) -> Result<(), GgufLoadError> {
        if row >= self.out_dim {
            return Err(GgufLoadError::Shape(format!(
                "dequant_row {row} >= out_dim {}",
                self.out_dim
            )));
        }
        if out.len() != self.in_dim {
            return Err(GgufLoadError::Shape(format!(
                "dequant_row out len {} != in_dim {}",
                out.len(),
                self.in_dim
            )));
        }
        let packed = self.packed.as_slice();
        match self.ggml_type {
            GGML_F32 => {
                let base = row * self.in_dim * 4;
                for (i, chunk) in out.iter_mut().enumerate() {
                    let o = base + i * 4;
                    *chunk = f32::from_le_bytes([
                        packed[o],
                        packed[o + 1],
                        packed[o + 2],
                        packed[o + 3],
                    ]);
                }
            }
            GGML_F16 => {
                let base = row * self.in_dim * 2;
                for (i, chunk) in out.iter_mut().enumerate() {
                    let o = base + i * 2;
                    *chunk = f16_to_f32(u16::from_le_bytes([packed[o], packed[o + 1]]));
                }
            }
            GGML_Q4_0 => {
                let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
                let row_base = row * rb;
                let mut scratch = [0.0f32; Q4_0_BLOCK];
                let mut i = 0usize;
                while i < self.in_dim {
                    let block = i / Q4_0_BLOCK;
                    dequant_q4_0_block(
                        &packed[row_base + block * Q4_0_BYTES..row_base + (block + 1) * Q4_0_BYTES],
                        &mut scratch,
                    );
                    let take = (self.in_dim - i).min(Q4_0_BLOCK);
                    out[i..i + take].copy_from_slice(&scratch[..take]);
                    i += Q4_0_BLOCK;
                }
            }
            GGML_Q8_0 => {
                let rb = (self.in_dim / Q8_0_BLOCK) * Q8_0_BYTES;
                let row_base = row * rb;
                let mut i = 0usize;
                while i < self.in_dim {
                    let block = i / Q8_0_BLOCK;
                    let off = row_base + block * Q8_0_BYTES;
                    let scale = f16_to_f32(u16::from_le_bytes([packed[off], packed[off + 1]]));
                    for j in 0..Q8_0_BLOCK {
                        if i + j >= self.in_dim {
                            break;
                        }
                        out[i + j] = (packed[off + 2 + j] as i8) as f32 * scale;
                    }
                    i += Q8_0_BLOCK;
                }
            }
            GGML_Q4_K => {
                let rb = (self.in_dim / QK_K) * Q4_K_BYTES;
                let row_base = row * rb;
                let mut scratch = vec![0.0f32; QK_K];
                let mut i = 0usize;
                while i < self.in_dim {
                    let block = i / QK_K;
                    dequant_q4_k_block(
                        &packed[row_base + block * Q4_K_BYTES..row_base + (block + 1) * Q4_K_BYTES],
                        &mut scratch,
                    );
                    let take = (self.in_dim - i).min(QK_K);
                    out[i..i + take].copy_from_slice(&scratch[..take]);
                    i += QK_K;
                }
            }
            GGML_Q6_K => {
                let rb = (self.in_dim / QK_K) * Q6_K_BYTES;
                let row_base = row * rb;
                let mut scratch = vec![0.0f32; QK_K];
                let mut i = 0usize;
                while i < self.in_dim {
                    let block = i / QK_K;
                    dequant_q6_k_block(
                        &packed[row_base + block * Q6_K_BYTES..row_base + (block + 1) * Q6_K_BYTES],
                        &mut scratch,
                    );
                    let take = (self.in_dim - i).min(QK_K);
                    out[i..i + take].copy_from_slice(&scratch[..take]);
                    i += QK_K;
                }
            }
            other => {
                return Err(GgufLoadError::UnsupportedType {
                    name: format!("dequant_row row={row}"),
                    ggml_type: other,
                });
            }
        }
        Ok(())
    }

    pub fn gemv_with_act(&self, act: &GemvActScratch, y: &mut [f32]) {
        assert_eq!(y.len(), self.out_dim);
        if self.ggml_type == GGML_Q4_0_4X8 && gemv_use_repack_q8() {
            quant_repack::gemv_q4_0_4x8(&act.act, &self.packed, self.in_dim, self.out_dim, y);
            return;
        }
        if self.ggml_type == GGML_Q4_0_8X8 && gemv_use_repack_q8() {
            quant_repack::gemv_q4_0_8x8(&act.act, &self.packed, self.in_dim, self.out_dim, y);
            return;
        }
        if self.ggml_type == GGML_Q4_0 && gemv_use_q8_act() && self.in_dim % Q4_0_BLOCK == 0 {
            gemv_q4_0_shared(&self.packed, self.in_dim, self.out_dim, &act.act, y);
        } else {
            y.fill(0.0);
        }
    }


    /// Greedy argmax with a pre-quantized activation (lm_head shared Q8).
    pub fn argmax_gemv_act(&self, x: &[f32], act: &ActQ8) -> u32 {
        let _ = x;
        assert_eq!(self.in_dim % Q4_0_BLOCK, 0);
        let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let packed = self.packed.as_slice();
        let out_dim = self.out_dim;
        let in_dim = self.in_dim;
        Self::argmax_gemv_chunked(out_dim, |start, end| {
            let mut best_s = f32::NEG_INFINITY;
            let mut best_o = start as u32;
            let mut o = start;
            while o + 4 <= end {
                for r in 0..4 {
                    let s = quant_kernels::gemv_q4_0_row_q8(&packed[(o + r) * rb..(o + r + 1) * rb], act);
                    if s >= best_s {
                        best_s = s;
                        best_o = (o + r) as u32;
                    }
                }
                o += 4;
            }
            while o < end {
                let s = quant_kernels::gemv_q4_0_row_q8(&packed[o * rb..(o + 1) * rb], act);
                if s >= best_s {
                    best_s = s;
                    best_o = o as u32;
                }
                o += 1;
            }
            (best_s, best_o)
        })
    }

    /// Chunked parallel argmax — ~4× threads tasks, not 128k per-row tasks.
    fn argmax_gemv_chunked(
        out_dim: usize,
        chunk_fn: impl Fn(usize, usize) -> (f32, u32) + Sync,
    ) -> u32 {
        use rayon::prelude::*;
        let reduce = || (f32::NEG_INFINITY, 0u32);
        let cmp = |a: (f32, u32), b: (f32, u32)| {
            if a.0 > b.0 || (a.0 == b.0 && a.1 > b.1) {
                a
            } else {
                b
            }
        };
        let par = || {
            let chunk = gemv_chunk_rows(out_dim);
            let nchunks = (out_dim + chunk - 1) / chunk;
            (0..nchunks)
                .into_par_iter()
                .map(|ci| {
                    let start = ci * chunk;
                    let end = (start + chunk).min(out_dim);
                    chunk_fn(start, end)
                })
                .reduce(reduce, cmp)
                .1
        };
        if rayon::current_thread_index().is_some() {
            par()
        } else {
            sys::decode_pool().install(par)
        }
    }

    /// Greedy argmax over output rows without materializing `out_dim` logits.
    pub fn argmax_gemv(&self, x: &[f32]) -> u32 {
        assert_eq!(x.len(), self.in_dim);
        if self.ggml_type == GGML_Q4_0_4X8 && gemv_use_repack_q8() {
            let act = ActQ8::quantize(x);
            return quant_repack::argmax_q4_0_4x8(&act, &self.packed, self.in_dim, self.out_dim);
        }
        if self.ggml_type == GGML_Q4_0_8X8 && gemv_use_repack_q8() {
            let act = ActQ8::quantize(x);
            return quant_repack::argmax_q4_0_8x8(&act, &self.packed, self.in_dim, self.out_dim);
        }
        debug_assert_eq!(self.ggml_type, GGML_Q4_0);
        debug_assert_eq!(self.in_dim % Q4_0_BLOCK, 0);
        let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let packed = self.packed.as_slice();
        let in_dim = self.in_dim;
        let out_dim = self.out_dim;
        if gemv_use_q8_act() {
            let act = ActQ8::quantize(x);
            return self.argmax_gemv_act(x, &act);
        }
        Self::argmax_gemv_chunked(out_dim, |start, end| {
            let mut best_s = f32::NEG_INFINITY;
            let mut best_o = start as u32;
            let mut o = start;
            while o + 4 <= end {
                let scores = quant_kernels::gemv_q4_0_rows4_f32(packed, rb, o, x, in_dim);
                for (r, &s) in scores.iter().enumerate() {
                    if s >= best_s {
                        best_s = s;
                        best_o = (o + r) as u32;
                    }
                }
                o += 4;
            }
            while o < end {
                let s = quant_kernels::gemv_q4_0_row_f32(&packed[o * rb..(o + 1) * rb], x, in_dim);
                if s >= best_s {
                    best_s = s;
                    best_o = o as u32;
                }
                o += 1;
            }
            (best_s, best_o)
        })
    }

}

fn gemv_legacy_env() -> bool {
    match std::env::var("GE_GEMV_LEGACY") {
        Ok(v) => matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES"),
        Err(_) => false,
    }
}

fn gemv_force_f32_env() -> bool {
    match std::env::var("GE_GEMV_FORCE_F32") {
        Ok(v) => matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES"),
        Err(_) => false,
    }
}

pub fn gemv_use_q8_act() -> bool {
    if gemv_force_f32_env() { return false; }
    match std::env::var("GE_GEMV_Q8") {
        Ok(v) if matches!(v.as_str(), "0" | "false" | "FALSE" | "no" | "NO") => false,
        Ok(v) if matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES") => {
            quant_kernels::has_avx_vnni()
        }
        Err(_) => false,
        _ => false,
    }
}

/// lm_head-only Q4×Q8 path (`GE_LM_HEAD_Q8=1`); FFN/attn stay on f32×Q4 unless `GE_GEMV_Q8=1`.
pub fn lm_head_use_q8_act() -> bool {
    if gemv_force_f32_env() { return false; }
    match std::env::var("GE_LM_HEAD_Q8") {
        Ok(v) if matches!(v.as_str(), "0" | "false" | "FALSE" | "no" | "NO") => false,
        Ok(v) if matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES") => {
            quant_kernels::has_avx_vnni()
        }
        Err(_) => gemv_use_q8_act(),
        _ => false,
    }
}


fn gemv_isa() -> GemvIsa {
    static ISA: OnceLock<GemvIsa> = OnceLock::new();
    *ISA.get_or_init(quant_kernels::detect_gemv_isa)
}

fn gemv_f32(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    for o in 0..out_dim {
        let mut s = 0.0f32;
        let base = o * in_dim * 4;
        for i in 0..in_dim {
            let off = base + i * 4;
            let w = f32::from_le_bytes([
                packed[off],
                packed[off + 1],
                packed[off + 2],
                packed[off + 3],
            ]);
            s += x[i] * w;
        }
        y[o] = s;
    }
}

fn gemv_f16(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    for o in 0..out_dim {
        let mut s = 0.0f32;
        let base = o * in_dim * 2;
        for i in 0..in_dim {
            let off = base + i * 2;
            let w = f16_to_f32(u16::from_le_bytes([packed[off], packed[off + 1]]));
            s += x[i] * w;
        }
        y[o] = s;
    }
}

#[inline(always)]
fn q8_0_block_dot(block: &[u8], x: &[f32]) -> f32 {
    let scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let mut sum = 0.0f32;
    for j in 0..Q8_0_BLOCK {
        sum += (block[2 + j] as i8) as f32 * x[j];
    }
    sum * scale
}

fn gemv_q4_0_shared(packed: &[u8], in_dim: usize, out_dim: usize, act: &ActQ8, y: &mut [f32]) {
    let rb = (in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
    gemv_rows_parallel(out_dim, y, |o, yo| {
        *yo = quant_kernels::gemv_q4_0_row_q8(&packed[o * rb..(o + 1) * rb], act);
    });
}
fn gemv_q4_0_4x8(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    if gemv_use_repack_q8() {
        let act = ActQ8::quantize(x);
        quant_repack::gemv_q4_0_4x8(&act, packed, in_dim, out_dim, y);
        return;
    }
    // Fallback should not run for R4 packs (Q8 act required); zero rather than garbage.
    y.fill(0.0);
}
fn gemv_q4_0_8x8(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    if gemv_use_repack_q8() {
        let act = ActQ8::quantize(x);
        quant_repack::gemv_q4_0_8x8(&act, packed, in_dim, out_dim, y);
        return;
    }
    y.fill(0.0);
}

/// Rows per rayon task (~4× threads) — cuts 128k-task overhead on lm_head argmax.
fn gemv_chunk_rows(out_dim: usize) -> usize {
    let threads = sys::decode_threads();
    let nchunks = threads.saturating_mul(4).max(1);
    ((out_dim + nchunks - 1) / nchunks).max(64)
}

fn gemv_q4_0_rows4_f32_chunk(
    base: usize,
    cy: &mut [f32],
    packed: &[u8],
    rb: usize,
    x: &[f32],
    in_dim: usize,
) {
    let mut o = 0usize;
    let len = cy.len();
    while o + 4 <= len {
        let batch = quant_kernels::gemv_q4_0_rows4_f32(packed, rb, base + o, x, in_dim);
        cy[o..o + 4].copy_from_slice(&batch);
        o += 4;
    }
    while o < len {
        let row = base + o;
        cy[o] = quant_kernels::gemv_q4_0_row_f32(&packed[row * rb..(row + 1) * rb], x, in_dim);
        o += 1;
    }
}

/// Row-parallel Q4_0×f32 GEMV with 4-row activation reuse (FFN down / WO / QKV / lm_head).
fn gemv_q4_0_rows4_parallel(
    out_dim: usize,
    y: &mut [f32],
    packed: &[u8],
    rb: usize,
    x: &[f32],
    in_dim: usize,
) {
    assert_eq!(y.len(), out_dim);
    if out_dim < 512 {
        gemv_q4_0_rows4_f32_chunk(0, y, packed, rb, x, in_dim);
        return;
    }
    let chunk = gemv_chunk_rows(out_dim);
    let mut par = || {
        y.par_chunks_mut(chunk).enumerate().for_each(|(ci, cy)| {
            let base = ci * chunk;
            gemv_q4_0_rows4_f32_chunk(base, cy, packed, rb, x, in_dim);
        });
    };
    if rayon::current_thread_index().is_some() {
        par();
    } else {
        sys::decode_pool().install(par);
    }
}

fn gemv_q4_0(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    if in_dim % Q4_0_BLOCK != 0 { gemv_q4_0_legacy(packed, in_dim, out_dim, x, y); return; }
    let rb = (in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
    if gemv_use_q8_act() {
        let a = ActQ8::quantize(x);
        gemv_q4_0_shared(packed, in_dim, out_dim, &a, y);
    } else if std::env::var("GE_GEMV_NO_ROWS4").ok().as_deref() == Some("1") {
        // A/B: single-row baseline (default is rows4 activation reuse).
        gemv_rows_parallel(out_dim, y, |o, yo| {
            *yo = quant_kernels::gemv_q4_0_row_f32(&packed[o * rb..(o + 1) * rb], x, in_dim);
        });
    } else {
        gemv_q4_0_rows4_parallel(out_dim, y, packed, rb, x, in_dim);
    }
}

fn gemv_q8_0(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    if in_dim % Q8_0_BLOCK == 0 {
        let row_bytes = (in_dim / Q8_0_BLOCK) * Q8_0_BYTES;
        gemv_rows_parallel(out_dim, y, |o, yo| {
            let row = &packed[o * row_bytes..(o + 1) * row_bytes];
            let n_blocks = in_dim / Q8_0_BLOCK;
            let mut sum = 0.0f32;
            let mut off = 0usize;
            for b in 0..n_blocks {
                sum += q8_0_block_dot(&row[off..off + Q8_0_BYTES], &x[b * Q8_0_BLOCK..]);
                off += Q8_0_BYTES;
            }
            *yo = sum;
        });
        return;
    }
    gemv_q8_0_legacy(packed, in_dim, out_dim, x, y);
}

fn gemv_q4_k(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    if in_dim % QK_K == 0 {
        let row_bytes = (in_dim / QK_K) * Q4_K_BYTES;
        gemv_rows_parallel(out_dim, y, |o, yo| {
            let row = &packed[o * row_bytes..(o + 1) * row_bytes];
            let n_blocks = in_dim / QK_K;
            let mut sum = 0.0f32;
            let mut off = 0usize;
            for b in 0..n_blocks {
                sum += quant_kernels::q4_k_block_dot_f32(
                    &row[off..off + Q4_K_BYTES],
                    &x[b * QK_K..],
                );
                off += Q4_K_BYTES;
            }
            *yo = sum;
        });
        return;
    }
    gemv_via_blocks_legacy(
        packed,
        in_dim,
        out_dim,
        x,
        y,
        QK_K,
        Q4_K_BYTES,
        dequant_q4_k_block,
    );
}

fn gemv_q6_k(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    if in_dim % QK_K == 0 {
        let row_bytes = (in_dim / QK_K) * Q6_K_BYTES;
        gemv_rows_parallel(out_dim, y, |o, yo| {
            let row = &packed[o * row_bytes..(o + 1) * row_bytes];
            let n_blocks = in_dim / QK_K;
            let mut sum = 0.0f32;
            let mut off = 0usize;
            let mut scratch = [0.0f32; QK_K];
            for b in 0..n_blocks {
                dequant_q6_k_block(&row[off..off + Q6_K_BYTES], &mut scratch);
                let xb = &x[b * QK_K..(b + 1) * QK_K];
                for j in 0..QK_K {
                    sum += scratch[j] * xb[j];
                }
                off += Q6_K_BYTES;
            }
            *yo = sum;
        });
        return;
    }
    gemv_via_blocks_legacy(
        packed,
        in_dim,
        out_dim,
        x,
        y,
        QK_K,
        Q6_K_BYTES,
        dequant_q6_k_block,
    );
}


/// Parallelize over output rows when the matrix is large enough to amortize rayon.
fn gemv_rows_parallel(out_dim: usize, y: &mut [f32], f: impl Fn(usize, &mut f32) + Sync) {
    if out_dim < 512 {
        for (o, yo) in y.iter_mut().enumerate() {
            f(o, yo);
        }
        return;
    }
    let chunk = gemv_chunk_rows(out_dim);
    let mut par = || {
        y.par_chunks_mut(chunk)
            .enumerate()
            .for_each(|(ci, chunk_y)| {
                let base = ci * chunk;
                for (i, yo) in chunk_y.iter_mut().enumerate() {
                    f(base + i, yo);
                }
            });
    };
    if rayon::current_thread_index().is_some() {
        par();
    } else {
        sys::decode_pool().install(par);
    }
}


/// Fused gate+up row parallel — one pool pass, no nested `join` oversubscribe.
fn gemv_rows_parallel2(
    out_dim: usize,
    g: &mut [f32],
    u: &mut [f32],
    f: impl Fn(usize, &mut f32, &mut f32) + Sync,
) {
    assert_eq!(g.len(), out_dim);
    assert_eq!(u.len(), out_dim);
    if out_dim < 512 {
        for o in 0..out_dim {
            f(o, &mut g[o], &mut u[o]);
        }
        return;
    }
    let chunk = gemv_chunk_rows(out_dim);
    let mut par = || {
        g.par_chunks_mut(chunk)
            .zip(u.par_chunks_mut(chunk))
            .enumerate()
            .for_each(|(ci, (cg, cu))| {
                let base = ci * chunk;
                for (i, (go, uo)) in cg.iter_mut().zip(cu.iter_mut()).enumerate() {
                    f(base + i, go, uo);
                }
            });
    };
    if rayon::current_thread_index().is_some() {
        par();
    } else {
        sys::decode_pool().install(par);
    }
}

fn gemv_gate_up_rows_f32_chunk(
    base: usize,
    cg: &mut [f32],
    cu: &mut [f32],
    gp: &[u8],
    upk: &[u8],
    rb: usize,
    x: &[f32],
    in_dim: usize,
) {
    let mut o = 0usize;
    let len = cg.len();
    while o + 4 <= len {
        let (g_batch, u_batch) =
            quant_kernels::gemv_q4_0_gate_up_rows4_f32(gp, upk, rb, base + o, x, in_dim);
        for r in 0..4 {
            cg[o + r] = g_batch[r];
            cu[o + r] = u_batch[r];
        }
        o += 4;
    }
    while o < len {
        let row = base + o;
        let (go, uo) = quant_kernels::gemv_q4_0_gate_up_row_f32(
            &gp[row * rb..(row + 1) * rb],
            &upk[row * rb..(row + 1) * rb],
            x,
            in_dim,
        );
        cg[o] = go;
        cu[o] = uo;
        o += 1;
    }
}

/// Fused gate+up row parallel — batches 4 rows per activation pass.
fn gemv_gate_up_rows_parallel(
    out_dim: usize,
    g: &mut [f32],
    u: &mut [f32],
    gp: &[u8],
    upk: &[u8],
    rb: usize,
    x: &[f32],
    in_dim: usize,
) {
    assert_eq!(g.len(), out_dim);
    assert_eq!(u.len(), out_dim);
    if out_dim < 512 {
        gemv_gate_up_rows_f32_chunk(0, g, u, gp, upk, rb, x, in_dim);
        return;
    }
    let chunk = gemv_chunk_rows(out_dim);
    let mut par = || {
        g.par_chunks_mut(chunk)
            .zip(u.par_chunks_mut(chunk))
            .enumerate()
            .for_each(|(ci, (cg, cu))| {
                let base = ci * chunk;
                gemv_gate_up_rows_f32_chunk(base, cg, cu, gp, upk, rb, x, in_dim);
            });
    };
    if rayon::current_thread_index().is_some() {
        par();
    } else {
        sys::decode_pool().install(par);
    }
}

fn swiglu_gate_up_f32(
    gate: &QuantMat,
    up: &QuantMat,
    x: &[f32],
    g: &mut [f32],
    u: &mut [f32],
) {
    if gate.ggml_type == GGML_Q4_0
        && up.ggml_type == GGML_Q4_0
        && gate.in_dim == up.in_dim
        && gate.out_dim == up.out_dim
        && gate.in_dim % Q4_0_BLOCK == 0
    {
        let in_dim = gate.in_dim;
        let out_dim = gate.out_dim;
        let rb = (in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let gp = gate.packed.as_slice();
        let upk = up.packed.as_slice();
        gemv_gate_up_rows_parallel(out_dim, g, u, gp, upk, rb, x, in_dim);
    } else {
        gate.gemv(x, g);
        up.gemv(x, u);
    }
}

fn swiglu_gate_up_q8_shared(
    gate: &QuantMat,
    up: &QuantMat,
    act: &GemvActScratch,
    g: &mut [f32],
    u: &mut [f32],
) {
    if gate.ggml_type == GGML_Q4_0
        && up.ggml_type == GGML_Q4_0
        && gate.in_dim == up.in_dim
        && gate.out_dim == up.out_dim
        && gate.in_dim % Q4_0_BLOCK == 0
    {
        let out_dim = gate.out_dim;
        let rb = (gate.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let gp = gate.packed.as_slice();
        let upk = up.packed.as_slice();
        gemv_rows_parallel2(out_dim, g, u, |o, go, uo| {
            *go = quant_kernels::gemv_q4_0_row_q8(&gp[o * rb..(o + 1) * rb], &act.act);
            *uo = quant_kernels::gemv_q4_0_row_q8(&upk[o * rb..(o + 1) * rb], &act.act);
        });
    } else {
        gate.gemv_with_act(act, g);
        up.gemv_with_act(act, u);
    }
}

// --- Legacy flat paths (div/mod per element; used by GE_GEMV_LEGACY=1 and odd dims) ---

fn gemv_q4_0_legacy(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    y.fill(0.0);
    let n = in_dim * out_dim;
    let mut flat = 0usize;
    let mut off = 0usize;
    let mut scratch = [0.0f32; Q4_0_BLOCK];
    let mut o = 0usize;
    let mut i = 0usize;
    while flat < n {
        if off + Q4_0_BYTES > packed.len() {
            break;
        }
        dequant_q4_0_block(&packed[off..off + Q4_0_BYTES], &mut scratch);
        for b in 0..Q4_0_BLOCK {
            if flat + b >= n {
                break;
            }
            y[o] += x[i] * scratch[b];
            i += 1;
            if i == in_dim {
                i = 0;
                o += 1;
            }
        }
        flat += Q4_0_BLOCK;
        off += Q4_0_BYTES;
    }
}

fn gemv_q8_0_legacy(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    y.fill(0.0);
    let n = in_dim * out_dim;
    let mut flat = 0usize;
    let mut off = 0usize;
    let mut o = 0usize;
    let mut i = 0usize;
    while flat < n {
        if off + Q8_0_BYTES > packed.len() {
            break;
        }
        let scale = f16_to_f32(u16::from_le_bytes([packed[off], packed[off + 1]]));
        for j in 0..Q8_0_BLOCK {
            if flat + j >= n {
                break;
            }
            let w = (packed[off + 2 + j] as i8) as f32 * scale;
            y[o] += x[i] * w;
            i += 1;
            if i == in_dim {
                i = 0;
                o += 1;
            }
        }
        flat += Q8_0_BLOCK;
        off += Q8_0_BYTES;
    }
}

/// Flat block-dequant GEMV (Q4_K / Q6_K / IQ2_XXS spike). `pub(crate)` for
/// [`crate::iq2_xxs::gemv_iq2_xxs_via_blocks_legacy`].
pub(crate) fn gemv_via_blocks_legacy<F>(
    packed: &[u8],
    in_dim: usize,
    out_dim: usize,
    x: &[f32],
    y: &mut [f32],
    block_n: usize,
    block_bytes: usize,
    dequant: F,
) where
    F: Fn(&[u8], &mut [f32]),
{
    y.fill(0.0);
    let n = in_dim * out_dim;
    let mut flat = 0usize;
    let mut off = 0usize;
    let mut scratch = vec![0.0f32; block_n];
    let mut o = 0usize;
    let mut i = 0usize;
    while flat < n {
        if off + block_bytes > packed.len() {
            break;
        }
        dequant(&packed[off..off + block_bytes], &mut scratch);
        for b in 0..block_n {
            if flat + b >= n {
                break;
            }
            y[o] += x[i] * scratch[b];
            i += 1;
            if i == in_dim {
                i = 0;
                o += 1;
            }
        }
        flat += block_n;
        off += block_bytes;
    }
}

fn dequant_q4_0_block(block: &[u8], out: &mut [f32; Q4_0_BLOCK]) {
    let scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let qs = &block[2..18];
    for j in 0..16 {
        let byte = qs[j];
        out[j] = ((byte & 0x0f) as i8 - 8) as f32 * scale;
        out[j + 16] = ((byte >> 4) as i8 - 8) as f32 * scale;
    }
}

fn dequant_q4_k_block(block: &[u8], out: &mut [f32]) {
    let d = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let dmin = f16_to_f32(u16::from_le_bytes([block[2], block[3]]));
    let (sc, m) = quant_kernels::unpack_q4k_scales(&block[4..16]);
    let qs = &block[16..144];
    for sub in 0..8 {
        let scale = d * sc[sub] as f32;
        let minv = dmin * m[sub] as f32;
        let g = sub / 2;
        let plane = sub % 2;
        let qs_off = g * 32;
        let shift = plane * 4;
        for j in 0..32 {
            let q = ((qs[qs_off + j] >> shift) & 0x0F) as f32;
            out[sub * 32 + j] = scale * q - minv;
        }
    }
}

fn dequant_q6_k_block(block: &[u8], out: &mut [f32]) {
    let d = f16_to_f32(u16::from_le_bytes([block[208], block[209]]));
    let mut ql_i = 0usize;
    let mut qh_i = 128usize;
    let mut sc_i = 192usize;
    let mut out_i = 0usize;
    for _ in 0..QK_K / 128 {
        for l in 0..32 {
            let is = l / 16;
            let q1 = ((block[ql_i + l] & 0x0F) | ((block[qh_i + l] & 3) << 4)) as i8 - 32;
            let q2 = ((block[ql_i + 32 + l] & 0x0F) | (((block[qh_i + l] >> 2) & 3) << 4)) as i8 - 32;
            let q3 = (((block[ql_i + l] >> 4) & 0x0F) | (((block[qh_i + l] >> 4) & 3) << 4)) as i8 - 32;
            let q4 = (((block[ql_i + 32 + l] >> 4) & 0x0F) | (((block[qh_i + l] >> 6) & 3) << 4)) as i8
                - 32;
            out[out_i + l] = d * block[sc_i + is] as i8 as f32 * q1 as f32;
            out[out_i + 32 + l] = d * block[sc_i + is + 2] as i8 as f32 * q2 as f32;
            out[out_i + 64 + l] = d * block[sc_i + is + 4] as i8 as f32 * q3 as f32;
            out[out_i + 96 + l] = d * block[sc_i + is + 6] as i8 as f32 * q4 as f32;
        }
        out_i += 128;
        ql_i += 64;
        qh_i += 32;
        sc_i += 8;
    }
}

/// Parallel Q/K/V GEMV for decode.
pub fn project_qkv_quant(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, x: &[f32],
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    if rayon::current_thread_index().is_some() {
        wq.gemv(x, q);
        wk.gemv(x, k);
        wv.gemv(x, v);
    } else {
        sys::decode_pool().install(|| {
            rayon::join(|| wq.gemv(x, q), || rayon::join(|| wk.gemv(x, k), || wv.gemv(x, v)));
        });
    }
}

pub fn project_qkv_quant_shared(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, act: &GemvActScratch,
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    if rayon::current_thread_index().is_some() {
        wq.gemv_with_act(act, q);
        wk.gemv_with_act(act, k);
        wv.gemv_with_act(act, v);
    } else {
        sys::decode_pool().install(|| {
            rayon::join(
                || wq.gemv_with_act(act, q),
                || rayon::join(|| wk.gemv_with_act(act, k), || wv.gemv_with_act(act, v)),
            );
        });
    }
}

pub fn swiglu_quant_repack(
    act: &GemvActScratch, gate: &QuantMat, up: &QuantMat, down: &QuantMat,
    g: &mut [f32], u: &mut [f32], out: &mut [f32],
) {
    gate.gemv_with_act(act, g);
    up.gemv_with_act(act, u);
    for j in 0..g.len() { g[j] = silu(g[j]) * u[j]; }
    down.gemv(g, out);
}

pub fn swiglu_quant_shared(
    act: &GemvActScratch, gate: &QuantMat, up: &QuantMat, down: &QuantMat,
    g: &mut [f32], u: &mut [f32], out: &mut [f32],
) {
    swiglu_gate_up_q8_shared(gate, up, act, g, u);
    for j in 0..g.len() { g[j] = silu(g[j]) * u[j]; }
    down.gemv(g, out);
}

/// SiLU for SwiGLU.
#[inline]
pub fn silu(v: f32) -> f32 {
    v / (1.0 + (-v).exp())
}

/// Dense SwiGLU using three quantized matrices (no full f32 materialize).
pub fn swiglu_quant(
    x: &[f32],
    gate: &QuantMat,
    up: &QuantMat,
    down: &QuantMat,
    g: &mut [f32],
    u: &mut [f32],
    out: &mut [f32],
) {
    swiglu_gate_up_f32(gate, up, x, g, u);
    for j in 0..g.len() {
        g[j] = silu(g[j]) * u[j];
    }
    down.gemv(g, out);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn iq_nbytes_packed_ceil_n_over_256() {
        let cases: &[(u32, usize)] = &[
            (GGML_IQ2_XXS, IQ2_XXS_BYTES),
            (GGML_IQ2_XS, IQ2_XS_BYTES),
            (GGML_IQ3_XXS, IQ3_XXS_BYTES),
            (GGML_IQ3_S, IQ3_S_BYTES),
            (GGML_IQ2_S, IQ2_S_BYTES),
        ];
        for &(ty, block_bytes) in cases {
            for n in [0usize, 1, 255, 256, 257, 512, 1000] {
                let got = QuantMat::nbytes_packed(ty, n).expect("IQ sizing");
                let want = ((n + QK_K - 1) / QK_K) * block_bytes;
                assert_eq!(got, want, "type={ty} n={n}");
            }
            // Other IQ types still rejected in dequant_slice; IQ2_XXS is on the clear path.
            if ty == GGML_IQ2_XXS {
                let got = crate::gguf_load::dequant_slice(ty, &[], 0).expect("IQ2_XXS n=0");
                assert!(got.is_empty());
                continue;
            }
            let err = crate::gguf_load::dequant_slice(ty, &[], 0).unwrap_err();
            let msg = err.to_string();
            assert!(
                msg.contains("unsupported ggml type"),
                "expected UnsupportedType, got {msg}"
            );
            assert!(
                msg.contains(crate::gguf_load::ggml_type_label(ty)),
                "expected type label in {msg}"
            );
        }
    }

    /// Spike step 2 fallback: synthetic packed buffer length must match `nbytes_packed`
    /// (no real IQ GGUF / no encode / no dequant).
    #[test]
    fn iq_synthetic_buffer_len_matches_nbytes_packed() {
        let cases: &[(u32, usize)] = &[
            (GGML_IQ2_XXS, IQ2_XXS_BYTES),
            (GGML_IQ2_XS, IQ2_XS_BYTES),
            (GGML_IQ3_XXS, IQ3_XXS_BYTES),
            (GGML_IQ3_S, IQ3_S_BYTES),
            (GGML_IQ2_S, IQ2_S_BYTES),
        ];
        for &(ty, block_bytes) in cases {
            for &(in_dim, out_dim) in &[(256usize, 1), (256, 2), (512, 3), (64, 4)] {
                let n = in_dim * out_dim;
                let nbytes = QuantMat::nbytes_packed(ty, n).expect("IQ sizing");
                let want_blocks = (n + QK_K - 1) / QK_K;
                assert_eq!(nbytes, want_blocks * block_bytes, "type={ty} n={n}");
                let buf = vec![0u8; nbytes];
                crate::gguf_load::assert_iq_nbytes_matches(ty, n, buf.len())
                    .unwrap_or_else(|e| panic!("exact len type={ty} n={n}: {e}"));
                let m = QuantMat {
                    ggml_type: ty,
                    in_dim,
                    out_dim,
                    packed: PackedBytes::owned(buf),
                };
                assert_eq!(
                    m.packed.len(),
                    QuantMat::nbytes_packed(ty, m.in_dim * m.out_dim).unwrap()
                );
                if nbytes > 0 {
                    let bad = crate::gguf_load::assert_iq_nbytes_matches(ty, n, nbytes - 1);
                    assert!(bad.is_err(), "truncated buf must fail type={ty} n={n}");
                }
            }
        }
    }

    #[test]
    fn f32_gemv_row_major_out_in() {
        let w = [1.0f32, 2.0, 3.0, 4.0, 5.0, 6.0];
        let m = QuantMat::from_f32(3, 2, &w);
        let x = [1.0f32, 10.0, 100.0];
        let mut y = [0.0f32; 2];
        m.gemv(&x, &mut y);
        assert!((y[0] - 321.0).abs() < 1e-5, "y0={}", y[0]);
        assert!((y[1] - 654.0).abs() < 1e-5, "y1={}", y[1]);
    }


    #[test]
    fn q4_0_argmax_gemv_matches_gemv_max() {
        std::env::set_var("GE_GEMV_Q8", "0");
        let in_dim = 64usize;
        let out_dim = 16usize;
        let n = in_dim * out_dim;
        let n_blocks = n / Q4_0_BLOCK;
        let mut packed = vec![0u8; n_blocks * Q4_0_BYTES];
        for b in 0..n_blocks {
            let off = b * Q4_0_BYTES;
            packed[off] = 0x00;
            packed[off + 1] = 0x34;
            for j in 0..16 {
                packed[off + 2 + j] = ((j as u8) & 0x0f) | ((((j + 3) as u8) & 0x0f) << 4);
            }
        }
        let m = QuantMat {
            ggml_type: GGML_Q4_0,
            in_dim,
            out_dim,
            packed: PackedBytes::owned(packed),
        };
        let x: Vec<f32> = (0..in_dim).map(|i| (i as f32) * 0.01 - 0.3).collect();
        let mut y = vec![0.0f32; out_dim];
        m.gemv(&x, &mut y);
        let (exp_id, _) = y
            .iter()
            .enumerate()
            .max_by(|(_, a), (_, b)| a.partial_cmp(b).unwrap_or(std::cmp::Ordering::Equal))
            .map(|(i, s)| (i as u32, *s))
            .unwrap_or((0, f32::NEG_INFINITY));
        assert_eq!(m.argmax_gemv(&x), exp_id);
    }

    #[test]
    fn q4_0_fast_matches_legacy() {
        std::env::set_var("GE_GEMV_Q8", "0");
        let in_dim = 64usize;
        let out_dim = 8usize;
        let n = in_dim * out_dim;
        let n_blocks = n / Q4_0_BLOCK;
        let mut packed = vec![0u8; n_blocks * Q4_0_BYTES];
        for b in 0..n_blocks {
            let off = b * Q4_0_BYTES;
            packed[off] = 0x00;
            packed[off + 1] = 0x34;
            for j in 0..16 {
                packed[off + 2 + j] = ((j as u8) & 0x0f) | ((((j + 3) as u8) & 0x0f) << 4);
            }
        }
        let x: Vec<f32> = (0..in_dim).map(|i| (i as f32) * 0.01 - 0.3).collect();
        // Default path is fused f32×Q4_0 (matches legacy closely).
        let mut y_fast = vec![0.0f32; out_dim];
        let mut y_leg = vec![0.0f32; out_dim];
        gemv_q4_0(&packed, in_dim, out_dim, &x, &mut y_fast);
        gemv_q4_0_legacy(&packed, in_dim, out_dim, &x, &mut y_leg);
        let max = y_fast
            .iter()
            .zip(&y_leg)
            .map(|(a, b)| (a - b).abs())
            .fold(0.0f32, f32::max);
        assert!(max < 1e-4, "fast vs legacy max diff {max}");
    }

    /// Follow-up: Q4_K_M passthrough packs currently collapse decode to one-token stutter
    /// (same tokens as llama.cpp → wrong next id). Prefer `--requant q4_0` until fixed.
    /// See docs/improvement-backlog-from-web.md P0.1 / agent notes 2026-07-27.
    #[test]
    #[ignore = "Q4_K passthrough quality FAIL — use requant q4_0; fix GEMV/lm_head follow-up"]
    fn q4_k_passthrough_gravity_not_stutter() {
        use crate::chat::ChatApplyMode;
        use crate::generate::{generate, GenerateRequest};
        use crate::green_model::{GreenModel, LoadConfig};
        use std::path::PathBuf;

        let path = PathBuf::from(
            std::env::var("USERPROFILE").unwrap_or_default() + r"\.green\models\Llama-3.2-1B.green",
        );
        if !path.is_dir() {
            eprintln!("skip: {path:?} missing");
            return;
        }
        let cfg = serde_json::from_str::<serde_json::Value>(
            &std::fs::read_to_string(path.join("config.json")).unwrap_or_default(),
        )
        .unwrap_or_default();
        let requant = cfg.get("requant").and_then(|v| v.as_str()).unwrap_or("");
        if requant == "q4_0" {
            eprintln!("skip: package is q4_0 requant, not passthrough");
            return;
        }
        let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false })
            .expect("open");
        let mut req = GenerateRequest::new("Explain gravity in one sentence.", 32);
        req.chat = ChatApplyMode::Off;
        let r = generate(&model, &req).expect("generate");
        let uniq: std::collections::HashSet<&u32> = r.new_tokens.iter().collect();
        assert!(
            uniq.len() > 3,
            "Q4_K passthrough stutter: uniq={} text={:?}",
            uniq.len(),
            r.text
        );
        let lower = r.text.to_ascii_lowercase();
        assert!(
            lower.contains("gravity")
                || lower.contains("force")
                || lower.contains("attract")
                || lower.contains("mass"),
            "expected gravity-ish English, got {:?}",
            r.text
        );
    }
}
