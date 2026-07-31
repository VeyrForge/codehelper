//! Shared types for Green `.green` model packages produced by Green Compress `pack-model`.
//!
//! This crate is independent and publishable; Green Engine and (future) Green Compress depend on it.
//!
//! # M0 loader usage
//!
//! ```ignore
//! use std::path::Path;
//! use green_format::open_package;
//!
//! // Pack output directory (model.green/) containing manifest.json + sidecars + checksums.json
//! let pkg = open_package(Path::new("model.green"), true)?;
//! assert_eq!(pkg.manifest.format, green_format::FORMAT_NAME);
//! let dense = pkg.metadata.dense_gguf.as_ref().expect("dense.gguf");
//! let _experts = &pkg.manifest.expert_tensors_pending; // stubs until Phase 2
//! ```
//!
//! Wire field documentation lives on [`manifest`] and [`tensor`]. Constants: [`schema`].

pub mod checksum;
pub mod manifest;
pub mod package;
pub mod schema;
pub mod tensor;

pub use checksum::{load_checksums, verify_file, PackageChecksums};
pub use manifest::{GreenManifest, ModelFiles, ModelMetadata};
pub use package::{open_package, PackageError, GreenPackage};
pub use schema::{
    FORMAT_NAME, FORMAT_VERSION, CHECKSUMS_FILE, MANIFEST_FILE, DEFAULT_DENSE_GGUF,
    DEFAULT_EXPERTS_GREENPACK, DEFAULT_METADATA_GGUF,
};
pub use tensor::{PendingExpertRecord, TensorRecord, TensorRole};
