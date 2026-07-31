//! Load a `.green` package directory from disk.
//!
//! # Loader contract (M0)
//!
//! ```ignore
//! use green_format::open_package;
//! let pkg = open_package(path_to_model_dot_green, /* verify_checksums */ true)?;
//! // pkg.manifest  — normalized green-model v1
//! // pkg.metadata  — resolved sidecar paths
//! // pkg.checksums — optional map from checksums.json
//! ```
//!
//! Acceptance for pack-model output:
//! 1. Parse `manifest.json` wire fields (`source_model`, `tensor_files`, `expert_tensors_pending`, …).
//! 2. Require every sidecar listed in [`crate::manifest::GreenManifest::required_sidecars`].
//! 3. When `verify_checksums` is true: verify `checksums.json` (if present) and any per-tensor
//!    `checksum` fields against on-disk shards.
//! 4. Missing **catalogued** expert shards (`tensors[]` with expert role) →
//!    [`PackageError::ExpertShardsIncomplete`]. Pending stubs alone do not fail open.

use std::fs;
use std::path::{Path, PathBuf};

use crate::checksum::{self, PackageChecksums};
use crate::manifest::{GreenManifest, ModelMetadata};
use crate::schema::MANIFEST_FILE;
use crate::tensor::TensorRecord;

/// Errors while opening a package directory.
#[derive(Debug, PartialEq)]
pub enum PackageError {
    Io(String),
    MissingManifest,
    Json(String),
    InvalidManifest(String),
    MissingFile(PathBuf),
    ChecksumMismatch { path: PathBuf, expected: String },
    ExpertShardsIncomplete { missing: Vec<PathBuf> },
}

impl std::fmt::Display for PackageError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PackageError::Io(e) => write!(f, "{e}"),
            PackageError::MissingManifest => {
                write!(f, "missing {MANIFEST_FILE} in package directory")
            }
            PackageError::Json(e) => write!(f, "manifest JSON: {e}"),
            PackageError::InvalidManifest(e) => write!(f, "invalid manifest: {e}"),
            PackageError::MissingFile(p) => write!(f, "missing package file {}", p.display()),
            PackageError::ChecksumMismatch { path, expected } => {
                write!(
                    f,
                    "checksum mismatch for {} (expected {expected})",
                    path.display()
                )
            }
            PackageError::ExpertShardsIncomplete { missing } => {
                write!(
                    f,
                    "Phase 2 not complete: {} expert shard(s) missing",
                    missing.len()
                )
            }
        }
    }
}

impl std::error::Error for PackageError {}

/// Opened `.green` package with validated manifest and resolved paths.
#[derive(Clone, Debug)]
pub struct GreenPackage {
    pub root: PathBuf,
    pub manifest: GreenManifest,
    pub metadata: ModelMetadata,
    /// Present when `checksums.json` exists (always loaded when on disk).
    pub checksums: Option<PackageChecksums>,
}

impl GreenPackage {
    pub fn tensor_path(&self, record: &TensorRecord) -> PathBuf {
        self.root.join(&record.file)
    }

    pub fn expert_records(&self) -> impl Iterator<Item = &TensorRecord> {
        self.manifest.expert_tensors()
    }
}

/// Parse `manifest.json` and verify required sidecar files exist.
///
/// Set `verify_checksums` to `true` for pack-model acceptance (uses `checksums.json` +
/// per-tensor `checksum` fields).
pub fn open_package(path: &Path, verify_checksums: bool) -> Result<GreenPackage, PackageError> {
    if !path.is_dir() {
        return Err(PackageError::Io(format!(
            "{} is not a directory",
            path.display()
        )));
    }
    let manifest_path = path.join(MANIFEST_FILE);
    if !manifest_path.is_file() {
        return Err(PackageError::MissingManifest);
    }
    let raw = fs::read_to_string(&manifest_path).map_err(|e| PackageError::Io(e.to_string()))?;
    let manifest: GreenManifest =
        serde_json::from_str(&raw).map_err(|e| PackageError::Json(e.to_string()))?;
    manifest
        .validate()
        .map_err(PackageError::InvalidManifest)?;

    let metadata = manifest.metadata_paths(path);
    for rel in manifest.required_sidecars() {
        let p = path.join(&rel);
        if !p.is_file() {
            return Err(PackageError::MissingFile(p));
        }
    }

    let checksums = checksum::load_checksums(path).map_err(|e| PackageError::Io(e.to_string()))?;

    let mut missing_experts = Vec::new();
    for rec in manifest.expert_tensors() {
        let shard = path.join(&rec.file);
        if !shard.is_file() {
            missing_experts.push(shard);
        }
    }
    if !missing_experts.is_empty() {
        return Err(PackageError::ExpertShardsIncomplete {
            missing: missing_experts,
        });
    }

    if verify_checksums {
        if let Some(map) = &checksums {
            checksum::verify_package_checksums(path, map).map_err(|e| match e {
                checksum::ChecksumError::Mismatch { path, expected } => {
                    PackageError::ChecksumMismatch { path, expected }
                }
                checksum::ChecksumError::Missing { path, .. } => PackageError::MissingFile(path),
                checksum::ChecksumError::Invalid(msg) => PackageError::InvalidManifest(msg),
                checksum::ChecksumError::Io(msg) => PackageError::Io(msg),
            })?;
        }
        // Per-tensor checksums (dedupe by file path).
        let mut verified_files = std::collections::BTreeSet::new();
        for rec in &manifest.tensors {
            if let Some(expected) = &rec.checksum {
                if expected.is_empty() {
                    continue;
                }
                let shard = path.join(&rec.file);
                if !verified_files.insert(rec.file.clone()) {
                    continue;
                }
                if !shard.is_file() {
                    return Err(PackageError::MissingFile(shard));
                }
                match checksum::verify_file(&shard, expected) {
                    Ok(true) => {}
                    Ok(false) => {
                        return Err(PackageError::ChecksumMismatch {
                            path: shard,
                            expected: expected.clone(),
                        });
                    }
                    Err(e) => return Err(PackageError::Io(e.to_string())),
                }
            }
        }
    }

    Ok(GreenPackage {
        root: path.to_path_buf(),
        metadata,
        manifest,
        checksums,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::checksum::sha256_file;
    use crate::manifest::ModelFiles;
    use crate::schema::{FORMAT_NAME, FORMAT_VERSION, CHECKSUMS_FILE};
    use crate::tensor::{TensorRecord, TensorRole};
    use tempfile::TempDir;

    fn write_manifest(dir: &Path, manifest: &GreenManifest) {
        let p = dir.join(MANIFEST_FILE);
        fs::write(p, serde_json::to_string_pretty(manifest).unwrap()).unwrap();
    }

    #[test]
    fn opens_minimal_dense_package() {
        let dir = TempDir::new().unwrap();
        fs::write(dir.path().join("metadata.gguf"), b"meta").unwrap();
        fs::write(dir.path().join("dense.gguf"), b"dense").unwrap();
        let manifest = GreenManifest {
            format: FORMAT_NAME.into(),
            version: FORMAT_VERSION,
            model: "demo".into(),
            arch: Some("llama".into()),
            methods: vec!["green_spqr".into()],
            files: ModelFiles {
                metadata: Some("metadata.gguf".into()),
                dense: Some("dense.gguf".into()),
                ..Default::default()
            },
            ..Default::default()
        };
        write_manifest(dir.path(), &manifest);
        let pkg = open_package(dir.path(), false).unwrap();
        assert_eq!(pkg.metadata.model, "demo");
    }

    #[test]
    fn reports_missing_expert_shards() {
        let dir = TempDir::new().unwrap();
        fs::write(dir.path().join("metadata.gguf"), b"meta").unwrap();
        fs::write(dir.path().join("dense.gguf"), b"dense").unwrap();
        let manifest = GreenManifest {
            format: FORMAT_NAME.into(),
            version: FORMAT_VERSION,
            model: "moe".into(),
            files: ModelFiles {
                metadata: Some("metadata.gguf".into()),
                dense: Some("dense.gguf".into()),
                ..Default::default()
            },
            tensors: vec![TensorRecord {
                name: "blk.0.ffn.expert.0.up".into(),
                role: Some(TensorRole::Expert),
                layer: Some(0),
                expert: Some(0),
                shape: vec![2048, 1024],
                file: "experts/l0_e0.bin".into(),
                offset: 0,
                length: Some(4096),
                checksum: None,
                method: None,
                ggml_type: None,
                source_gguf_type: None,
                green_compression_type: None,
                compressed_size: None,
            }],
            ..Default::default()
        };
        write_manifest(dir.path(), &manifest);
        let err = open_package(dir.path(), false).unwrap_err();
        assert!(matches!(err, PackageError::ExpertShardsIncomplete { .. }));
        assert!(err.to_string().contains("Phase 2 not complete"));
    }

    #[test]
    fn opens_pack_model_fixture_with_checksums() {
        let dir = TempDir::new().unwrap();
        let meta = b"metadata-sidecar";
        let dense = b"dense-sidecar-bytes";
        let experts = b"GRNP\x00\x01\x00\x00\x00\x00";
        fs::write(dir.path().join("metadata.gguf"), meta).unwrap();
        fs::write(dir.path().join("dense.gguf"), dense).unwrap();
        fs::write(dir.path().join("experts-000.greenpack"), experts).unwrap();

        let dense_digest = sha256_file(&dir.path().join("dense.gguf")).unwrap();
        let meta_digest = sha256_file(&dir.path().join("metadata.gguf")).unwrap();
        let experts_digest = sha256_file(&dir.path().join("experts-000.greenpack")).unwrap();

        let mut manifest_json: serde_json::Value =
            serde_json::from_str(include_str!("../tests/fixtures/pack_model_manifest.json"))
                .unwrap();
        for t in manifest_json["tensors"].as_array_mut().unwrap() {
            t["checksum"] = serde_json::Value::String(format!("sha256:{dense_digest}"));
        }
        fs::write(
            dir.path().join(MANIFEST_FILE),
            serde_json::to_string_pretty(&manifest_json).unwrap(),
        )
        .unwrap();

        let manifest_digest = sha256_file(&dir.path().join(MANIFEST_FILE)).unwrap();
        let checksums = serde_json::json!({
            "metadata.gguf": meta_digest,
            "dense.gguf": dense_digest,
            "experts-000.greenpack": experts_digest,
            "manifest.json": manifest_digest,
        });
        fs::write(
            dir.path().join(CHECKSUMS_FILE),
            serde_json::to_string_pretty(&checksums).unwrap(),
        )
        .unwrap();

        let pkg = open_package(dir.path(), true).unwrap();
        assert_eq!(pkg.manifest.model, "test-mini.gguf");
        assert_eq!(pkg.metadata.dense_gguf.as_ref().unwrap().file_name().unwrap(), "dense.gguf");
        assert!(pkg.checksums.is_some());
        assert_eq!(pkg.manifest.tensors.len(), 2);
    }

    #[test]
    fn opens_moe_pack_with_catalogued_experts() {
        // M4 fixture: expert catalogue in tensors[]; pending empty; greenpack present.
        let dir = TempDir::new().unwrap();
        fs::write(dir.path().join("metadata.gguf"), b"m").unwrap();
        fs::write(dir.path().join("dense.gguf"), b"d").unwrap();
        fs::write(dir.path().join("experts-000.greenpack"), b"GRNP\x00\x01").unwrap();
        fs::write(
            dir.path().join(MANIFEST_FILE),
            include_str!("../tests/fixtures/pack_model_moe_manifest.json"),
        )
        .unwrap();
        let pkg = open_package(dir.path(), false).unwrap();
        assert!(pkg.manifest.expert_tensors_pending.is_empty());
        assert_eq!(pkg.expert_records().count(), 1);
        assert!(pkg.metadata.experts_greenpack.is_some());
    }
}
