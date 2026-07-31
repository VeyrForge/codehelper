//! On-disk format identifier for `.green` packages.
//!
//! # Wire contract (green-model v1)
//!
//! Produced by Green Compress `pack-model` (`scripts/pack_model.py`):
//!
//! | File | Role |
//! |------|------|
//! | `manifest.json` | Package header + tensor index ([`crate::manifest::GreenManifest`]) |
//! | `checksums.json` | Map of relative path → lowercase SHA-256 hex ([`crate::checksum::PackageChecksums`]) |
//! | `metadata.gguf` | KV + non-2D tensors (norms, biases, tokenizer fields) |
//! | `dense.gguf` | Dense / embedding 2D weights |
//! | `experts-000.greenpack` | Expert shard (raw f32 after GRNP header; M4) |
//!
//! Pack writes wire field names (`source_model`, `architecture`, `method`, `tensor_files`,
//! `expert_tensors_pending`). This crate normalizes them into [`crate::manifest::GreenManifest`]
//! for the native loader. See module docs on `manifest` for the full field table.

/// Manifest `format` field value.
pub const FORMAT_NAME: &str = "green-model";

/// Manifest `version` field value (integer).
pub const FORMAT_VERSION: u32 = 1;

/// Package directory entry for the manifest.
pub const MANIFEST_FILE: &str = "manifest.json";

/// Package directory entry for the SHA-256 map written by `pack-model`.
pub const CHECKSUMS_FILE: &str = "checksums.json";

/// Default relative path for metadata / tokenizer sidecar.
pub const DEFAULT_METADATA_GGUF: &str = "metadata.gguf";

/// Default relative path for dense weights sidecar.
pub const DEFAULT_DENSE_GGUF: &str = "dense.gguf";

/// Default relative path for the first expert greenpack (raw-f32 or stub).
pub const DEFAULT_EXPERTS_GREENPACK: &str = "experts-000.greenpack";
