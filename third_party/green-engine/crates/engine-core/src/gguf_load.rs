//! Load dense / metadata GGUF shards into f32 (M2 weight materialize).
//!
//! Supports F32, F16, Q4_0, Q8_0, Q4_K, Q6_K, and IQ2_XXS (CPU dequant via
//! [`crate::iq2_xxs`]; also `GE_IQ2=1` as an explicit enable alias — always on the
//! clear `dequant_slice` arm). Uses `memmap2` so packed 2D weights stay file-backed
//! (no `to_vec` double-buffer). Norms dequant to owned f32; embeddings stay packed
//! (dequant per row). No IQ2 encode / CUDA.

use std::fs::File;
use std::io;
use std::path::Path;
use std::sync::Arc;

use memmap2::Mmap;

use crate::gguf_io::{read_gguf, GgufFile};
use crate::quant_mat::PackedBytes;

const GGML_F32: u32 = 0;
const GGML_F16: u32 = 1;
const GGML_Q4_0: u32 = 2;
const GGML_Q8_0: u32 = 8;
const GGML_Q4_K: u32 = 12;
const GGML_Q6_K: u32 = 14;
// IQ2_XXS: dequant_slice + QuantMat::gemv. Other IQ ids: sizing only (docs R6.IQ).
const GGML_IQ2_XXS: u32 = 16;
const GGML_IQ2_XS: u32 = 17;
const GGML_IQ3_XXS: u32 = 18;
const GGML_IQ3_S: u32 = 21;
const GGML_IQ2_S: u32 = 22;

/// Whether IQ2_XXS may run through [`dequant_slice`].
///
/// Clear path: **on** when unset. `GE_IQ2=1|true|yes|on` keeps it on; `GE_IQ2=0|false|no|off`
/// disables (A/B / rollback).
#[inline]
pub fn iq2_enabled() -> bool {
    match std::env::var("GE_IQ2") {
        Ok(v) => !matches!(
            v.trim().to_ascii_lowercase().as_str(),
            "0" | "false" | "no" | "off"
        ),
        Err(_) => true,
    }
}

/// Human label for UnsupportedType / diagnostics (supported + IQ inventory).
pub fn ggml_type_label(ggml_type: u32) -> &'static str {
    match ggml_type {
        GGML_F32 => "F32",
        GGML_F16 => "F16",
        GGML_Q4_0 => "Q4_0",
        GGML_Q8_0 => "Q8_0",
        GGML_Q4_K => "Q4_K",
        GGML_Q6_K => "Q6_K",
        GGML_IQ2_XXS => "IQ2_XXS",
        GGML_IQ2_XS => "IQ2_XS",
        GGML_IQ3_XXS => "IQ3_XXS",
        GGML_IQ3_S => "IQ3_S",
        GGML_IQ2_S => "IQ2_S",
        19 => "IQ1_S",
        20 => "IQ4_NL",
        23 => "IQ4_XS",
        29 => "IQ1_M",
        34 => "TQ1_0",
        35 => "TQ2_0",
        39 => "MXFP4",
        40 => "NVFP4",
        _ => "unknown",
    }
}

const Q4_0_BLOCK: usize = 32;
const Q4_0_BYTES: usize = 18;

const Q8_0_BLOCK: usize = 32;
const Q8_0_BYTES: usize = 34;

const QK_K: usize = 256;
const Q4_K_BYTES: usize = 144; // 2+2+12+128
const Q6_K_BYTES: usize = 210; // 128+64+16+2

/// One dense tensor materialized as f32 (row-major, GGUF dim order preserved).
#[derive(Clone, Debug)]
pub struct LoadedTensor {
    pub name: String,
    /// GGUF shape as stored (ne[0] fastest-changing).
    pub shape: Vec<usize>,
    pub ggml_type: u32,
    pub data: Vec<f32>,
}

impl LoadedTensor {
    pub fn nelements(&self) -> usize {
        self.shape.iter().product()
    }

    /// Interpret 2D weight as matvec dims for pack-model shards.
    ///
    /// Pack-model / GGUFWriter stores shape as `[ne0=in_dim, ne1=out_dim]` (dims
    /// reversed vs a logical `(out, in)` numpy view) while the payload stays
    /// row-major `(out, in)`. So `in = shape[0]`, `out = shape[1]`.
    pub fn as_matvec_dims(&self) -> Option<(usize, usize)> {
        if self.shape.len() != 2 {
            return None;
        }
        Some((self.shape[0], self.shape[1]))
    }
}

/// All tensors loaded from one or more GGUF files (name → tensor).
#[derive(Clone, Debug, Default)]
pub struct DenseGgufWeights {
    pub tensors: Vec<LoadedTensor>,
}

impl DenseGgufWeights {
    pub fn get(&self, name: &str) -> Option<&LoadedTensor> {
        self.tensors.iter().find(|t| t.name == name)
    }

    pub fn require(&self, name: &str) -> Result<&LoadedTensor, GgufLoadError> {
        self.get(name)
            .ok_or_else(|| GgufLoadError::MissingTensor(name.into()))
    }
}

/// Errors from GGUF weight materialization.
#[derive(Debug)]
pub enum GgufLoadError {
    Io(io::Error),
    UnsupportedType { name: String, ggml_type: u32 },
    MissingTensor(String),
    Shape(String),
    Message(String),
}

impl std::fmt::Display for GgufLoadError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GgufLoadError::Io(e) => write!(f, "{e}"),
            GgufLoadError::UnsupportedType { name, ggml_type } => {
                write!(
                    f,
                    "tensor {name}: unsupported ggml type {ggml_type} ({}) (need F32/F16/Q4_0/Q8_0/Q4_K/Q6_K/IQ2_XXS; other IQ2/IQ3 inventory in docs R6.IQ)",
                    ggml_type_label(*ggml_type)
                )
            }
            GgufLoadError::MissingTensor(n) => write!(f, "missing tensor {n}"),
            GgufLoadError::Shape(m) => write!(f, "shape: {m}"),
            GgufLoadError::Message(m) => write!(f, "{m}"),
        }
    }
}

impl std::error::Error for GgufLoadError {}

impl From<io::Error> for GgufLoadError {
    fn from(value: io::Error) -> Self {
        GgufLoadError::Io(value)
    }
}

/// Load every supported tensor from a GGUF file into f32 (mmap; one-tensor dequant).
pub fn load_gguf_f32(path: &Path) -> Result<DenseGgufWeights, GgufLoadError> {
    let meta = read_gguf(path, false)?;
    let file = File::open(path)?;
    let map = unsafe { Mmap::map(&file) }.map_err(GgufLoadError::Io)?;
    materialize(&meta, &map)
}

/// Load `dense.gguf` + optional `metadata.gguf` (norms live in metadata for pack-model).
///
/// Untied models (Llama-3.1 8B) ship a real `output.weight` distinct from `token_embd.weight`
/// — keep it. Tied Llama-3.2 packs omit `output.weight`; DenseForward reuses the embedding table.
pub fn load_package_weights(
    dense_gguf: &Path,
    metadata_gguf: Option<&Path>,
) -> Result<DenseGgufWeights, GgufLoadError> {
    let mut out = load_gguf_f32(dense_gguf)?;
    if let Some(meta_path) = metadata_gguf {
        if meta_path.is_file() {
            let meta = load_gguf_f32(meta_path)?;
            for t in meta.tensors {
                if out.get(&t.name).is_none() {
                    out.tensors.push(t);
                }
            }
        }
    }
    Ok(out)
}

fn materialize(meta: &GgufFile, file_bytes: &[u8]) -> Result<DenseGgufWeights, GgufLoadError> {
    let data_base = infer_data_base(meta, file_bytes)?;
    let mut tensors = Vec::with_capacity(meta.tensors.len());
    for t in &meta.tensors {
        let shape: Vec<usize> = t.shape.iter().map(|&d| d as usize).collect();
        let n: usize = shape.iter().copied().product();
        if n == 0 {
            continue;
        }
        let abs = data_base.saturating_add(t.offset as usize);
        let data = match t.ggml_type {
            GGML_F32 => {
                let nbytes = n.saturating_mul(4);
                let slice = file_bytes
                    .get(abs..abs + nbytes)
                    .ok_or_else(|| GgufLoadError::Message(format!("F32 OOB {}", t.name)))?;
                let mut v = Vec::with_capacity(n);
                for c in slice.chunks_exact(4) {
                    v.push(f32::from_le_bytes([c[0], c[1], c[2], c[3]]));
                }
                v
            }
            GGML_F16 => {
                let nbytes = n.saturating_mul(2);
                let slice = file_bytes
                    .get(abs..abs + nbytes)
                    .ok_or_else(|| GgufLoadError::Message(format!("F16 OOB {}", t.name)))?;
                let mut v = Vec::with_capacity(n);
                for c in slice.chunks_exact(2) {
                    v.push(f16_to_f32(u16::from_le_bytes([c[0], c[1]])));
                }
                v
            }
            GGML_Q4_0 => {
                let n_blocks = (n + Q4_0_BLOCK - 1) / Q4_0_BLOCK;
                let nbytes = n_blocks * Q4_0_BYTES;
                let slice = file_bytes
                    .get(abs..abs + nbytes)
                    .ok_or_else(|| GgufLoadError::Message(format!("Q4_0 OOB {}", t.name)))?;
                dequant_q4_0(slice, n)?
            }
            GGML_Q8_0 => {
                let n_blocks = (n + Q8_0_BLOCK - 1) / Q8_0_BLOCK;
                let nbytes = n_blocks * Q8_0_BYTES;
                let slice = file_bytes
                    .get(abs..abs + nbytes)
                    .ok_or_else(|| GgufLoadError::Message(format!("Q8_0 OOB {}", t.name)))?;
                dequant_q8_0(slice, n)?
            }
            GGML_Q4_K => {
                let n_blocks = (n + QK_K - 1) / QK_K;
                let nbytes = n_blocks * Q4_K_BYTES;
                let slice = file_bytes
                    .get(abs..abs + nbytes)
                    .ok_or_else(|| GgufLoadError::Message(format!("Q4_K OOB {}", t.name)))?;
                dequant_q4_k(slice, n)?
            }
            GGML_Q6_K => {
                let n_blocks = (n + QK_K - 1) / QK_K;
                let nbytes = n_blocks * Q6_K_BYTES;
                let slice = file_bytes
                    .get(abs..abs + nbytes)
                    .ok_or_else(|| GgufLoadError::Message(format!("Q6_K OOB {}", t.name)))?;
                dequant_q6_k(slice, n)?
            }
            other => {
                return Err(GgufLoadError::UnsupportedType {
                    name: t.name.clone(),
                    ggml_type: other,
                });
            }
        };
        if data.len() != n {
            return Err(GgufLoadError::Shape(format!(
                "{}: got {} f32 values, expected {n}",
                t.name,
                data.len()
            )));
        }
        tensors.push(LoadedTensor {
            name: t.name.clone(),
            shape,
            ggml_type: t.ggml_type,
            data,
        });
    }
    Ok(DenseGgufWeights { tensors })
}

fn infer_data_base(meta: &GgufFile, file_bytes: &[u8]) -> Result<usize, GgufLoadError> {
    if meta.tensors.is_empty() {
        return Ok(0);
    }
    let mut o = 4usize; // after magic
    if file_bytes.len() < 24 {
        return Err(GgufLoadError::Message("GGUF too short".into()));
    }
    o += 4; // version
    let tensor_count = u64::from_le_bytes(file_bytes[o..o + 8].try_into().unwrap()) as usize;
    o += 8;
    let kv_count = u64::from_le_bytes(file_bytes[o..o + 8].try_into().unwrap()) as usize;
    o += 8;
    for _ in 0..kv_count {
        o = skip_string(file_bytes, o)?;
        o = skip_value(file_bytes, o)?;
    }
    for _ in 0..tensor_count {
        o = skip_string(file_bytes, o)?;
        if o + 4 > file_bytes.len() {
            return Err(GgufLoadError::Message("tensor dims OOB".into()));
        }
        let n_dims = u32::from_le_bytes(file_bytes[o..o + 4].try_into().unwrap()) as usize;
        o += 4 + n_dims * 8 + 4 + 8; // dims + type + offset
    }
    Ok(align_up(o, 32))
}

fn align_up(v: usize, align: usize) -> usize {
    (v + align - 1) & !(align - 1)
}

fn skip_string(buf: &[u8], o: usize) -> Result<usize, GgufLoadError> {
    if o + 8 > buf.len() {
        return Err(GgufLoadError::Message("string len OOB".into()));
    }
    let n = u64::from_le_bytes(buf[o..o + 8].try_into().unwrap()) as usize;
    let end = o + 8 + n;
    if end > buf.len() {
        return Err(GgufLoadError::Message("string data OOB".into()));
    }
    Ok(end)
}

fn skip_value(buf: &[u8], mut o: usize) -> Result<usize, GgufLoadError> {
    if o + 4 > buf.len() {
        return Err(GgufLoadError::Message("value type OOB".into()));
    }
    let ty = u32::from_le_bytes(buf[o..o + 4].try_into().unwrap());
    o += 4;
    Ok(match ty {
        0 | 1 | 7 => o + 1,
        2 | 3 => o + 2,
        4 | 5 | 6 => o + 4,
        10 | 11 | 12 => o + 8,
        8 => skip_string(buf, o)?,
        9 => {
            if o + 12 > buf.len() {
                return Err(GgufLoadError::Message("array header OOB".into()));
            }
            let et = u32::from_le_bytes(buf[o..o + 4].try_into().unwrap());
            let n = u64::from_le_bytes(buf[o + 4..o + 12].try_into().unwrap()) as usize;
            o += 12;
            for _ in 0..n {
                o = skip_array_elem(buf, o, et)?;
            }
            o
        }
        _ => {
            return Err(GgufLoadError::Message(format!("unknown GGUF value type {ty}")));
        }
    })
}

fn skip_array_elem(buf: &[u8], o: usize, et: u32) -> Result<usize, GgufLoadError> {
    Ok(match et {
        0 | 1 | 7 => o + 1,
        2 | 3 => o + 2,
        4 | 5 | 6 => o + 4,
        10 | 11 | 12 => o + 8,
        8 => skip_string(buf, o)?,
        _ => {
            return Err(GgufLoadError::Message(format!("unsupported array elem type {et}")));
        }
    })
}

fn dequant_q4_0(packed: &[u8], n: usize) -> Result<Vec<f32>, GgufLoadError> {
    let mut out = vec![0.0f32; n];
    let mut i = 0usize;
    let mut off = 0usize;
    while i < n {
        if off + Q4_0_BYTES > packed.len() {
            return Err(GgufLoadError::Message("Q4_0 truncated".into()));
        }
        let scale = f16_to_f32(u16::from_le_bytes([packed[off], packed[off + 1]]));
        let qs = &packed[off + 2..off + 18];
        for j in 0..16 {
            let byte = qs[j];
            let x0 = (byte & 0x0f) as i8 - 8;
            let x1 = (byte >> 4) as i8 - 8;
            if i + j < n {
                out[i + j] = x0 as f32 * scale;
            }
            if i + j + 16 < n {
                out[i + j + 16] = x1 as f32 * scale;
            }
        }
        i += Q4_0_BLOCK;
        off += Q4_0_BYTES;
    }
    Ok(out)
}

/// Public helper for [`crate::quant_mat::QuantMat::to_f32`].
///
/// IQ2_XXS uses the clear [`crate::iq2_xxs::dequantize_row_iq2_xxs`] path (CPU scalar;
/// no encode). Set `GE_IQ2=0` to force-reject IQ2_XXS here (A/B / rollback).
pub fn dequant_slice(ggml_type: u32, packed: &[u8], n: usize) -> Result<Vec<f32>, GgufLoadError> {
    match ggml_type {
        GGML_F32 => {
            let mut v = Vec::with_capacity(n);
            for c in packed.chunks_exact(4).take(n) {
                v.push(f32::from_le_bytes([c[0], c[1], c[2], c[3]]));
            }
            if v.len() != n {
                return Err(GgufLoadError::Shape(format!("F32 got {} want {n}", v.len())));
            }
            Ok(v)
        }
        GGML_F16 => {
            let mut v = Vec::with_capacity(n);
            for c in packed.chunks_exact(2).take(n) {
                v.push(f16_to_f32(u16::from_le_bytes([c[0], c[1]])));
            }
            Ok(v)
        }
        GGML_Q4_0 => dequant_q4_0(packed, n),
        GGML_Q8_0 => dequant_q8_0(packed, n),
        GGML_Q4_K => dequant_q4_k(packed, n),
        GGML_Q6_K => dequant_q6_k(packed, n),
        GGML_IQ2_XXS => {
            if iq2_enabled() {
                crate::iq2_xxs::dequantize_row_iq2_xxs(packed, n).map_err(GgufLoadError::Shape)
            } else {
                Err(GgufLoadError::UnsupportedType {
                    name: "dequant_slice".into(),
                    ggml_type: GGML_IQ2_XXS,
                })
            }
        }
        other => Err(GgufLoadError::UnsupportedType {
            name: "dequant_slice".into(),
            ggml_type: other,
        }),
    }
}

/// Packed or f32 tensor for lean 1B loads (2D weights stay quantized).
#[derive(Clone, Debug)]
pub enum LeanTensor {
    F32 {
        name: String,
        shape: Vec<usize>,
        data: Vec<f32>,
    },
    Packed {
        name: String,
        shape: Vec<usize>,
        ggml_type: u32,
        packed: PackedBytes,
    },
}

impl LeanTensor {
    pub fn name(&self) -> &str {
        match self {
            LeanTensor::F32 { name, .. } | LeanTensor::Packed { name, .. } => name,
        }
    }

    pub fn shape(&self) -> &[usize] {
        match self {
            LeanTensor::F32 { shape, .. } | LeanTensor::Packed { shape, .. } => shape,
        }
    }

    pub fn as_matvec_dims(&self) -> Option<(usize, usize)> {
        let shape = self.shape();
        if shape.len() != 2 {
            return None;
        }
        // Pack-model: file shape `[in, out]`; payload row-major `(out, in)`.
        Some((shape[0], shape[1]))
    }

    pub fn as_f32_slice(&self) -> Option<&[f32]> {
        match self {
            LeanTensor::F32 { data, .. } => Some(data),
            LeanTensor::Packed { .. } => None,
        }
    }

    pub fn to_quant_mat(&self) -> Result<crate::quant_mat::QuantMat, GgufLoadError> {
        let (in_dim, out_dim) = self.as_matvec_dims().ok_or_else(|| {
            GgufLoadError::Shape(format!("{} not 2D", self.name()))
        })?;
        match self {
            LeanTensor::F32 { data, .. } => Ok(crate::quant_mat::QuantMat::from_f32(in_dim, out_dim, data)),
            LeanTensor::Packed {
                ggml_type, packed, ..
            } => Ok(crate::quant_mat::QuantMat {
                ggml_type: *ggml_type,
                in_dim,
                out_dim,
                // Arc clone for Mapped — no weight byte copy.
                packed: packed.clone(),
            }),
        }
    }

    pub fn to_f32_owned(&self) -> Result<Vec<f32>, GgufLoadError> {
        match self {
            LeanTensor::F32 { data, .. } => Ok(data.clone()),
            LeanTensor::Packed {
                ggml_type,
                packed,
                shape,
                ..
            } => {
                let n: usize = shape.iter().product();
                dequant_slice(*ggml_type, packed.as_slice(), n)
            }
        }
    }
}

#[derive(Clone, Debug, Default)]
pub struct LeanGgufWeights {
    pub tensors: Vec<LeanTensor>,
}

impl LeanGgufWeights {
    pub fn get(&self, name: &str) -> Option<&LeanTensor> {
        self.tensors.iter().find(|t| t.name() == name)
    }

    pub fn require(&self, name: &str) -> Result<&LeanTensor, GgufLoadError> {
        self.get(name)
            .ok_or_else(|| GgufLoadError::MissingTensor(name.into()))
    }
}

fn packed_nbytes(ggml_type: u32, n: usize) -> Result<usize, GgufLoadError> {
    crate::quant_mat::QuantMat::nbytes_packed(ggml_type, n)
}

/// True for IQ2/IQ3 types accepted by [`crate::quant_mat::QuantMat::nbytes_packed`] sizing.
#[inline]
pub fn is_iq_sizing_type(ggml_type: u32) -> bool {
    matches!(
        ggml_type,
        GGML_IQ2_XXS | GGML_IQ2_XS | GGML_IQ3_XXS | GGML_IQ3_S | GGML_IQ2_S
    )
}

/// IQ-only mmap size-check result (no dequant / GEMV).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct IqMmapSizeReport {
    pub iq_tensors: usize,
    pub checked_bytes: usize,
}

/// Assert `buf_len == nbytes_packed(ggml_type, nelems)` for an IQ sizing type.
///
/// Used by the IQ2 spike step-2 size gate (synthetic buffers or real GGUF extents).
pub fn assert_iq_nbytes_matches(
    ggml_type: u32,
    nelems: usize,
    buf_len: usize,
) -> Result<(), GgufLoadError> {
    if !is_iq_sizing_type(ggml_type) {
        return Err(GgufLoadError::UnsupportedType {
            name: "assert_iq_nbytes_matches".into(),
            ggml_type,
        });
    }
    let want = packed_nbytes(ggml_type, nelems)?;
    if buf_len != want {
        return Err(GgufLoadError::Message(format!(
            "IQ packed len mismatch type={} ({}) nelems={nelems}: got {buf_len} want {want}",
            ggml_type,
            ggml_type_label(ggml_type)
        )));
    }
    Ok(())
}

/// Lean mmap size-check for IQ2/IQ3 tensors only — no f32 materialize, no GEMV.
///
/// Walks GGUF tensor metadata, mmaps the file, and asserts each IQ tensor's file
/// extent equals [`crate::quant_mat::QuantMat::nbytes_packed`]. Non-IQ tensors are
/// skipped (so mixed emb/out higher-quant packs still size-check).
pub fn check_iq_gguf_mmap_sizes(path: &Path) -> Result<IqMmapSizeReport, GgufLoadError> {
    let meta = read_gguf(path, false)?;
    let file = open_mmap_file(path)?;
    let map = unsafe { Mmap::map(&file) }.map_err(GgufLoadError::Io)?;
    let data_base = infer_data_base(&meta, map.as_ref())?;
    let mut iq_tensors = 0usize;
    let mut checked_bytes = 0usize;
    for t in &meta.tensors {
        if !is_iq_sizing_type(t.ggml_type) {
            continue;
        }
        let shape: Vec<usize> = t.shape.iter().map(|&d| d as usize).collect();
        let n: usize = shape.iter().copied().product();
        if n == 0 {
            continue;
        }
        let nbytes = packed_nbytes(t.ggml_type, n)?;
        let abs = data_base.saturating_add(t.offset as usize);
        let end = abs.saturating_add(nbytes);
        if end > map.len() || abs > map.len() {
            return Err(GgufLoadError::Message(format!(
                "IQ OOB {}: abs={abs} nbytes={nbytes} file_len={}",
                t.name,
                map.len()
            )));
        }
        assert_iq_nbytes_matches(t.ggml_type, n, end - abs)?;
        iq_tensors += 1;
        checked_bytes = checked_bytes.saturating_add(nbytes);
    }
    if iq_tensors == 0 {
        return Err(GgufLoadError::Message(
            "no IQ2/IQ3 tensors in GGUF (need IQ2_XXS/XS/S or IQ3_XXS/S)".into(),
        ));
    }
    Ok(IqMmapSizeReport {
        iq_tensors,
        checked_bytes,
    })
}

/// Lean load: norms → f32; embeddings + other 2D weights stay mmap'd packed (RAM-safe for 1B).
pub fn load_package_weights_lean(
    dense_gguf: &Path,
    metadata_gguf: Option<&Path>,
) -> Result<LeanGgufWeights, GgufLoadError> {
    let mut out = load_gguf_lean(dense_gguf)?;
    if let Some(meta_path) = metadata_gguf {
        if meta_path.is_file() {
            let meta = load_gguf_lean(meta_path)?;
            for t in meta.tensors {
                if out.get(t.name()).is_none() {
                    out.tensors.push(t);
                }
            }
        }
    }
    Ok(out)
}

fn load_gguf_lean(path: &Path) -> Result<LeanGgufWeights, GgufLoadError> {
    let meta = read_gguf(path, false)?;
    // Windows sequential prefetch of a 670MiB mapping inflates Working Set before GEMV
    // needs those pages; random-access open discourages Prefetch/Superfetch pull-in.
    let file = open_mmap_file(path)?;
    let map = Arc::new(unsafe { Mmap::map(&file) }.map_err(GgufLoadError::Io)?);
    materialize_lean(&meta, map)
}

fn open_mmap_file(path: &Path) -> Result<File, GgufLoadError> {
    #[cfg(windows)]
    {
        use std::fs::OpenOptions;
        use std::os::windows::fs::OpenOptionsExt;
        // FILE_FLAG_RANDOM_ACCESS = 0x10000000 — hint against sequential prefetch.
        OpenOptions::new()
            .read(true)
            .custom_flags(0x1000_0000)
            .open(path)
            .map_err(GgufLoadError::Io)
    }
    #[cfg(not(windows))]
    {
        File::open(path).map_err(GgufLoadError::Io)
    }
}

fn should_materialize_f32(_name: &str, shape: &[usize]) -> bool {
    if shape.len() != 2 {
        return true; // norms / 1D
    }
    // Embeddings stay packed — DenseForward dequants one row per token; emb_lm shares mmap.
    false
}

fn materialize_lean(meta: &GgufFile, map: Arc<Mmap>) -> Result<LeanGgufWeights, GgufLoadError> {
    let file_bytes: &[u8] = map.as_ref();
    let data_base = infer_data_base(meta, file_bytes)?;
    let mut tensors = Vec::with_capacity(meta.tensors.len());
    for t in &meta.tensors {
        // Keep `output.weight` when present (untied Llama-3.1). Tied Llama-3.2 packs omit it;
        // DenseForward then reuses `token_embd.weight` as lm_head. Do NOT skip by shape alone —
        // untied lm_head has the same nelems as embd but different packed bytes.
        let shape: Vec<usize> = t.shape.iter().map(|&d| d as usize).collect();
        let n: usize = shape.iter().copied().product();
        if n == 0 {
            continue;
        }
        let abs = data_base.saturating_add(t.offset as usize);
        let nbytes = packed_nbytes(t.ggml_type, n)?;
        let end = abs.saturating_add(nbytes);
        if end > file_bytes.len() || abs > file_bytes.len() {
            return Err(GgufLoadError::Message(format!("OOB {}", t.name)));
        }
        let slice = &file_bytes[abs..end];

        if should_materialize_f32(&t.name, &shape) || t.ggml_type == GGML_F32 {
            let data = dequant_slice(t.ggml_type, slice, n)?;
            tensors.push(LeanTensor::F32 {
                name: t.name.clone(),
                shape: shape.clone(),
                data,
            });
        } else {
            tensors.push(LeanTensor::Packed {
                name: t.name.clone(),
                shape,
                ggml_type: t.ggml_type,
                packed: PackedBytes::mapped(Arc::clone(&map), abs, end),
            });
        }
    }
    Ok(LeanGgufWeights { tensors })
}

fn dequant_q8_0(packed: &[u8], n: usize) -> Result<Vec<f32>, GgufLoadError> {
    let mut out = vec![0.0f32; n];
    let mut i = 0usize;
    let mut off = 0usize;
    while i < n {
        if off + Q8_0_BYTES > packed.len() {
            return Err(GgufLoadError::Message("Q8_0 truncated".into()));
        }
        let scale = f16_to_f32(u16::from_le_bytes([packed[off], packed[off + 1]]));
        for j in 0..Q8_0_BLOCK {
            if i + j >= n {
                break;
            }
            let q = packed[off + 2 + j] as i8;
            out[i + j] = q as f32 * scale;
        }
        i += Q8_0_BLOCK;
        off += Q8_0_BYTES;
    }
    Ok(out)
}

fn unpack_q4k_scales(scales: &[u8]) -> ([u8; 8], [u8; 8]) {
    // scales[0..4]=d, [4..8]=m, [8..12]=m_d  (see ggml / gguf.quants.Q4_K)
    let mut sc = [0u8; 8];
    let mut m = [0u8; 8];
    for i in 0..4 {
        sc[i] = scales[i] & 0x3F;
        m[i] = scales[i + 4] & 0x3F;
        sc[i + 4] = (scales[8 + i] & 0x0F) | ((scales[i] >> 2) & 0x30);
        m[i + 4] = (scales[8 + i] >> 4) | ((scales[i + 4] >> 2) & 0x30);
    }
    (sc, m)
}

fn dequant_q4_k(packed: &[u8], n: usize) -> Result<Vec<f32>, GgufLoadError> {
    let mut out = vec![0.0f32; n];
    let n_blocks = (n + QK_K - 1) / QK_K;
    for ib in 0..n_blocks {
        let off = ib * Q4_K_BYTES;
        if off + Q4_K_BYTES > packed.len() {
            return Err(GgufLoadError::Message("Q4_K truncated".into()));
        }
        let block = &packed[off..off + Q4_K_BYTES];
        let d = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
        let dmin = f16_to_f32(u16::from_le_bytes([block[2], block[3]]));
        let (sc, m) = unpack_q4k_scales(&block[4..16]);
        let qs = &block[16..144];
        // qs: 128 bytes → 8 sub-blocks × 32 nibbles (low then high nibble planes of 32).
        let base = ib * QK_K;
        for sub in 0..8 {
            let scale = d * sc[sub] as f32;
            let minv = dmin * m[sub] as f32;
            let qs_off = (sub / 2) * 32;
            let shift = if sub % 2 == 0 { 0 } else { 4 };
            for j in 0..32 {
                let idx = base + sub * 32 + j;
                if idx >= n {
                    break;
                }
                let q = ((qs[qs_off + j] >> shift) & 0x0F) as f32;
                out[idx] = scale * q - minv;
            }
        }
    }
    Ok(out)
}

fn dequant_q6_k(packed: &[u8], n: usize) -> Result<Vec<f32>, GgufLoadError> {
    let mut out = vec![0.0f32; n];
    let n_blocks = (n + QK_K - 1) / QK_K;
    for ib in 0..n_blocks {
        let off = ib * Q6_K_BYTES;
        if off + Q6_K_BYTES > packed.len() {
            return Err(GgufLoadError::Message("Q6_K truncated".into()));
        }
        let block = &packed[off..off + Q6_K_BYTES];
        let mut ql_i = 0usize;
        let mut qh_i = 128usize;
        let mut sc_i = 192usize;
        let d = f16_to_f32(u16::from_le_bytes([block[208], block[209]]));
        let mut out_i = ib * QK_K;
        for _ in 0..QK_K / 128 {
            for l in 0..32 {
                let is = l / 16;
                let q1 = ((block[ql_i + l] & 0x0F) | ((block[qh_i + l] & 3) << 4)) as i8 - 32;
                let q2 = ((block[ql_i + 32 + l] & 0x0F) | (((block[qh_i + l] >> 2) & 3) << 4)) as i8 - 32;
                let q3 = (((block[ql_i + l] >> 4) & 0x0F) | (((block[qh_i + l] >> 4) & 3) << 4)) as i8 - 32;
                let q4 = (((block[ql_i + 32 + l] >> 4) & 0x0F)
                    | (((block[qh_i + l] >> 6) & 3) << 4)) as i8
                    - 32;
                if out_i + l < n {
                    out[out_i + l] = d * block[sc_i + is] as i8 as f32 * q1 as f32;
                }
                if out_i + 32 + l < n {
                    out[out_i + 32 + l] = d * block[sc_i + is + 2] as i8 as f32 * q2 as f32;
                }
                if out_i + 64 + l < n {
                    out[out_i + 64 + l] = d * block[sc_i + is + 4] as i8 as f32 * q3 as f32;
                }
                if out_i + 96 + l < n {
                    out[out_i + 96 + l] = d * block[sc_i + is + 6] as i8 as f32 * q4 as f32;
                }
            }
            out_i += 128;
            ql_i += 64;
            qh_i += 32;
            sc_i += 8;
        }
    }
    Ok(out)
}

/// IEEE-754 binary16 → f32 (dep-free).
pub fn f16_to_f32(bits: u16) -> f32 {
    let sign = ((bits >> 15) & 1) as u32;
    let exp = ((bits >> 10) & 0x1f) as u32;
    let frac = (bits & 0x3ff) as u32;
    let f32_bits = if exp == 0 {
        if frac == 0 {
            sign << 31
        } else {
            let mut f = frac;
            let mut e = 127 - 15 + 1;
            while f & 0x400 == 0 {
                f <<= 1;
                e -= 1;
            }
            f &= 0x3ff;
            (sign << 31) | ((e as u32) << 23) | (f << 13)
        }
    } else if exp == 31 {
        (sign << 31) | (0xff << 23) | (frac << 13)
    } else {
        (sign << 31) | ((exp + 127 - 15) << 23) | (frac << 13)
    };
    f32::from_bits(f32_bits)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn q4_0_matches_fixture_embd_prefix() {
        let path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../../green-compress/scripts/fixtures/tiny.green/dense.gguf");
        let path = path.canonicalize().unwrap_or(path);
        let alt = Path::new(r"../green-compress\scripts\fixtures\tiny.green\dense.gguf");
        let p = if path.is_file() {
            path
        } else if alt.is_file() {
            alt.to_path_buf()
        } else {
            eprintln!("skip: tiny.green dense.gguf missing");
            return;
        };
        let w = load_gguf_f32(&p).unwrap();
        let emb = w.require("token_embd.weight").unwrap();
        assert_eq!(emb.data.len(), 32 * 64);
        assert!(emb.data.iter().all(|x| x.is_finite()));
    }

    #[test]
    fn q4_k_block_matches_golden() {
        // Golden from Llama-3.2-1B Q4_K_M blk.0.attn_k first block (see agent notes).
        let hex = "750e831baab2ffe79bacbf6359019bd1";
        // Need full 144-byte block — fetch from file when present.
        let gguf = Path::new(&std::env::var("USERPROFILE").unwrap_or_default())
            .join(".green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf");
        if !gguf.is_file() {
            eprintln!("skip q4_k golden: source gguf missing");
            return;
        }
        let _ = hex;
        let meta = read_gguf(&gguf, false).unwrap();
        let file = File::open(&gguf).unwrap();
        let map = unsafe { Mmap::map(&file) }.unwrap();
        let data_base = infer_data_base(&meta, &map).unwrap();
        let t = meta.tensors.iter().find(|t| t.name == "blk.0.attn_k.weight").unwrap();
        let abs = data_base + t.offset as usize;
        let block = &map[abs..abs + Q4_K_BYTES];
        let deq = dequant_q4_k(block, QK_K).unwrap();
        let expected = [
            0.04994058609008789f32,
            0.1161503791809082,
            0.06649303436279297,
            -0.0328216552734375,
            -0.04937410354614258,
            -0.09903144836425781,
            -0.06592655181884766,
            -0.06592655181884766,
        ];
        for i in 0..8 {
            assert!(
                (deq[i] - expected[i]).abs() < 1e-5,
                "q4_k[{i}] got {} want {}",
                deq[i],
                expected[i]
            );
        }
    }

    #[test]
    fn q6_k_block_matches_golden() {
        let gguf = Path::new(&std::env::var("USERPROFILE").unwrap_or_default())
            .join(".green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf");
        if !gguf.is_file() {
            eprintln!("skip q6_k golden: source gguf missing");
            return;
        }
        let meta = read_gguf(&gguf, false).unwrap();
        let file = File::open(&gguf).unwrap();
        let map = unsafe { Mmap::map(&file) }.unwrap();
        let data_base = infer_data_base(&meta, &map).unwrap();
        let t = meta
            .tensors
            .iter()
            .find(|t| t.name == "blk.0.ffn_down.weight")
            .unwrap();
        let abs = data_base + t.offset as usize;
        let block = &map[abs..abs + Q6_K_BYTES];
        let deq = dequant_q6_k(block, QK_K).unwrap();
        let expected = [
            -0.02655029296875f32,
            0.0177001953125,
            0.0,
            -0.02301025390625,
            0.021240234375,
            0.01593017578125,
            0.0035400390625,
            -0.02301025390625,
        ];
        for i in 0..8 {
            assert!(
                (deq[i] - expected[i]).abs() < 1e-4,
                "q6_k[{i}] got {} want {}",
                deq[i],
                expected[i]
            );
        }
    }

    /// Minimal GGUF v3 with one 2D IQ tensor and zero-filled packed payload (not an encode).
    fn write_synthetic_iq_gguf(path: &Path, ggml_type: u32, in_dim: u64, out_dim: u64) {
        use std::io::Write;
        let n = (in_dim * out_dim) as usize;
        let nbytes = packed_nbytes(ggml_type, n).expect("IQ nbytes");
        let name = b"blk.0.attn_q.weight";
        let mut info = Vec::new();
        info.extend_from_slice(b"GGUF");
        info.extend_from_slice(&3u32.to_le_bytes());
        info.extend_from_slice(&1u64.to_le_bytes()); // tensor_count
        info.extend_from_slice(&0u64.to_le_bytes()); // kv_count
        info.extend_from_slice(&(name.len() as u64).to_le_bytes());
        info.extend_from_slice(name);
        info.extend_from_slice(&2u32.to_le_bytes()); // n_dims
        info.extend_from_slice(&in_dim.to_le_bytes());
        info.extend_from_slice(&out_dim.to_le_bytes());
        info.extend_from_slice(&ggml_type.to_le_bytes());
        info.extend_from_slice(&0u64.to_le_bytes()); // relative offset
        let data_base = align_up(info.len(), 32);
        let mut file = vec![0u8; data_base + nbytes];
        file[..info.len()].copy_from_slice(&info);
        // payload stays zeros — size-check only
        let mut f = File::create(path).expect("create synthetic iq gguf");
        f.write_all(&file).expect("write synthetic iq gguf");
    }

    #[test]
    fn iq_synthetic_gguf_mmap_size_check() {
        let dir = tempfile::tempdir().expect("tempdir");
        let path = dir.path().join("synthetic_iq2_xxs.gguf");
        write_synthetic_iq_gguf(&path, GGML_IQ2_XXS, 256, 2);
        let report = check_iq_gguf_mmap_sizes(&path).expect("mmap size-check");
        assert_eq!(report.iq_tensors, 1);
        assert_eq!(report.checked_bytes, packed_nbytes(GGML_IQ2_XXS, 512).unwrap());
        // Truncated payload must fail OOB / size gate.
        let bad = dir.path().join("truncated_iq.gguf");
        write_synthetic_iq_gguf(&bad, GGML_IQ2_XXS, 256, 2);
        {
            use std::fs::OpenOptions;
            use std::io::Write;
            let meta = std::fs::metadata(&bad).unwrap();
            let keep = meta.len().saturating_sub(1);
            let mut bytes = std::fs::read(&bad).unwrap();
            bytes.truncate(keep as usize);
            let mut f = OpenOptions::new()
                .write(true)
                .truncate(true)
                .open(&bad)
                .unwrap();
            f.write_all(&bytes).unwrap();
        }
        assert!(check_iq_gguf_mmap_sizes(&bad).is_err());
    }

    #[test]
    fn iq_assert_nbytes_rejects_non_iq() {
        let err = assert_iq_nbytes_matches(GGML_Q4_0, 32, 18).unwrap_err();
        assert!(err.to_string().contains("unsupported"), "{err}");
    }
}
