//! Green Compress — post-training weight compression and CPU layer inference for low-RAM machines.
//!
//! # Pipeline
//! - **I/O:** [`io`] — `.mx`, Q4/Q8, repair, fused-weight (`.fcw`) formats
//! - **Quantize:** [`q4`], [`q8`], [`cmd_quant`]
//! - **Repair:** [`repair`] — low-rank + sparse + outlier correction
//! - **Infer:** [`infer`] — Q8 matmul with optional prepacked fused weights ([`matmul`])
//! - **MoE:** [`moe`], [`expert_cache`] — LRU-budgeted expert layers
//!
//! # Model compression
//! - GGUF export (Phase 1): `greencompress export-gguf` → `scripts/export_gguf.py`
//! - Green pack (Phase 2): `greencompress pack-model` → `scripts/pack_model.py`
//! - GGUF extract: `scripts/extract_gguf.py` (used by `scripts/compress_model.py`)
//! - Prepack for lower RAM at inference: `greencompress prepack --layer-dir DIR`

pub mod backend;
pub mod benchmark;
pub mod benchmark_compare;
pub mod cmd_gguf;
pub mod cmd_io;
pub mod cmd_quant;
pub mod error;
#[cfg(feature = "gpu")]
pub mod gpu;
pub mod infer;
pub mod io;
pub mod matmul;
pub mod npy;
pub mod expert_cache;
pub mod mmap_file;
pub mod moe;
pub mod q4;
pub mod q4k;
pub mod q8;
pub mod qn;
pub mod repair;
pub mod simd;
pub mod subspace;
pub mod sweep;
pub mod types;
pub mod util;

pub use backend::{ComputeBackend, GpuSession};
pub use error::{GreenError, Result};
#[cfg(feature = "gpu")]
pub use gpu::gpu_available;
