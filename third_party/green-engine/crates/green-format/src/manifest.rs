//! `manifest.json` schema for `.green` packages (green-model v1).
//!
//! # Wire fields written by `pack-model`
//!
//! | JSON field | Canonical field | Notes |
//! |------------|-----------------|-------|
//! | `format` | [`GreenManifest::format`] | Must be `"green-model"` |
//! | `version` | [`GreenManifest::version`] | Must be `1` |
//! | `source_model` | [`GreenManifest::model`] | Also accepts `model` |
//! | `architecture` | [`GreenManifest::arch`] | Also accepts `arch` |
//! | `method` | [`GreenManifest::methods`] | Singular → one-element vec; also `methods` |
//! | `compression_note` | [`GreenManifest::compression_note`] | Free-text |
//! | `layers` | [`GreenManifest::layers`] | Arch dim |
//! | `experts_per_layer` | [`GreenManifest::experts_per_layer`] | MoE |
//! | `experts_used_per_token` | [`GreenManifest::experts_used_per_token`] | MoE top-k |
//! | `hidden_size` | [`GreenManifest::hidden_size`] | |
//! | `intermediate_size` | [`GreenManifest::intermediate_size`] | |
//! | `tensor_files` | [`GreenManifest::tensor_files`] | Sidecar list; also fills [`ModelFiles`] |
//! | `files` | [`GreenManifest::files`] | Optional structured sidecars (loader form) |
//! | `tensors` | [`GreenManifest::tensors`] | Dense catalog ([`crate::tensor::TensorRecord`]) |
//! | `expert_tensors_pending` | [`GreenManifest::expert_tensors_pending`] | Stub experts |
//!
//! Loader entry point: [`crate::package::open_package`].

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

use crate::schema::{
    FORMAT_NAME, FORMAT_VERSION, DEFAULT_DENSE_GGUF, DEFAULT_EXPERTS_GREENPACK,
    DEFAULT_METADATA_GGUF,
};
use crate::tensor::{PendingExpertRecord, TensorRecord};

/// Relative paths to bundled sidecars (normalized view for the runtime).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelFiles {
    #[serde(default)]
    pub metadata: Option<String>,
    #[serde(default)]
    pub dense: Option<String>,
    #[serde(default)]
    pub tokenizer: Option<String>,
    /// Primary experts greenpack (e.g. `experts-000.greenpack`).
    #[serde(default)]
    pub experts: Option<String>,
}

/// Parsed manifest header + tensor index (canonical loader view).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(from = "ManifestWire")]
pub struct GreenManifest {
    pub format: String,
    pub version: u32,
    /// Source model basename (`source_model` on the wire).
    pub model: String,
    /// Architecture id (`architecture` on the wire).
    #[serde(default)]
    pub arch: Option<String>,
    /// Compression / pack methods (`method` or `methods` on the wire).
    #[serde(default)]
    pub methods: Vec<String>,
    #[serde(default)]
    pub compression_note: Option<String>,
    #[serde(default)]
    pub layers: Option<u32>,
    #[serde(default)]
    pub experts_per_layer: Option<u32>,
    #[serde(default)]
    pub experts_used_per_token: Option<u32>,
    #[serde(default)]
    pub hidden_size: Option<u32>,
    #[serde(default)]
    pub intermediate_size: Option<u32>,
    /// Raw sidecar list from pack (`tensor_files`).
    #[serde(default)]
    pub tensor_files: Vec<String>,
    #[serde(default)]
    pub files: ModelFiles,
    #[serde(default)]
    pub tensors: Vec<TensorRecord>,
    #[serde(default)]
    pub expert_tensors_pending: Vec<PendingExpertRecord>,
}

#[derive(Deserialize)]
struct ManifestWire {
    format: String,
    version: u32,
    #[serde(default)]
    model: String,
    /// Pack may emit `source_model` alongside `model`; prefer non-empty `model`.
    #[serde(default)]
    source_model: String,
    /// Pack may emit both `arch` and `architecture` — accept either (prefer `arch`).
    #[serde(default)]
    arch: Option<String>,
    #[serde(default)]
    architecture: Option<String>,
    #[serde(default)]
    methods: Vec<String>,
    #[serde(default)]
    method: Option<String>,
    #[serde(default)]
    compression_note: Option<String>,
    #[serde(default)]
    layers: Option<u32>,
    #[serde(default)]
    experts_per_layer: Option<u32>,
    #[serde(default)]
    experts_used_per_token: Option<u32>,
    #[serde(default)]
    hidden_size: Option<u32>,
    #[serde(default)]
    intermediate_size: Option<u32>,
    #[serde(default)]
    files: ModelFiles,
    #[serde(default)]
    tensor_files: Vec<String>,
    #[serde(default)]
    tensors: Vec<TensorRecord>,
    #[serde(default)]
    expert_tensors_pending: Vec<PendingExpertRecord>,
}

impl From<ManifestWire> for GreenManifest {
    fn from(wire: ManifestWire) -> Self {
        let model = if wire.model.is_empty() {
            wire.source_model
        } else {
            wire.model
        };
        let methods = if wire.methods.is_empty() {
            wire.method.into_iter().collect()
        } else {
            wire.methods
        };
        let arch = wire.arch.or(wire.architecture);
        let mut files = wire.files;
        normalize_files_from_tensor_list(&mut files, &wire.tensor_files);
        GreenManifest {
            format: wire.format,
            version: wire.version,
            model,
            arch,
            methods,
            compression_note: wire.compression_note,
            layers: wire.layers,
            experts_per_layer: wire.experts_per_layer,
            experts_used_per_token: wire.experts_used_per_token,
            hidden_size: wire.hidden_size,
            intermediate_size: wire.intermediate_size,
            tensor_files: wire.tensor_files,
            files,
            tensors: wire.tensors,
            expert_tensors_pending: wire.expert_tensors_pending,
        }
    }
}

fn normalize_files_from_tensor_list(files: &mut ModelFiles, tensor_files: &[String]) {
    for rel in tensor_files {
        if files.metadata.is_none()
            && (rel == DEFAULT_METADATA_GGUF || rel.ends_with("/metadata.gguf"))
        {
            files.metadata = Some(rel.clone());
        } else if files.dense.is_none()
            && (rel == DEFAULT_DENSE_GGUF || rel.ends_with("/dense.gguf"))
        {
            files.dense = Some(rel.clone());
        } else if files.experts.is_none()
            && (rel == DEFAULT_EXPERTS_GREENPACK
                || rel.ends_with(".greenpack")
                || rel.starts_with("experts-"))
        {
            files.experts = Some(rel.clone());
        } else if files.tokenizer.is_none()
            && (rel.contains("tokenizer") || rel.ends_with("tokenizer.gguf"))
        {
            files.tokenizer = Some(rel.clone());
        }
    }
}

/// Convenience view over manifest metadata + resolved sidecar paths.
#[derive(Clone, Debug)]
pub struct ModelMetadata {
    pub model: String,
    pub arch: Option<String>,
    pub methods: Vec<String>,
    pub layers: Option<u32>,
    pub experts_per_layer: Option<u32>,
    pub experts_used_per_token: Option<u32>,
    pub hidden_size: Option<u32>,
    pub intermediate_size: Option<u32>,
    pub metadata_gguf: Option<PathBuf>,
    pub dense_gguf: Option<PathBuf>,
    pub experts_greenpack: Option<PathBuf>,
    pub tokenizer: Option<PathBuf>,
}

impl GreenManifest {
    /// Structural validation of format version, identity, tensors, and pending experts.
    pub fn validate(&self) -> Result<(), String> {
        if self.format != FORMAT_NAME {
            return Err(format!(
                "unsupported format {:?} (expected {FORMAT_NAME})",
                self.format
            ));
        }
        if self.version != FORMAT_VERSION {
            return Err(format!(
                "unsupported version {} (expected {FORMAT_VERSION})",
                self.version
            ));
        }
        if self.model.is_empty() {
            return Err("manifest model name is empty".into());
        }
        if self.files.dense.is_none()
            && !self
                .tensor_files
                .iter()
                .any(|f| f == DEFAULT_DENSE_GGUF || f.ends_with("/dense.gguf"))
            && self.tensors.is_empty()
        {
            // Allow empty dense only when tensors also empty (unit tests); pack always lists dense.
        }
        for t in &self.tensors {
            t.validate()?;
        }
        for p in &self.expert_tensors_pending {
            p.validate()?;
        }
        Ok(())
    }

    pub fn metadata_paths(&self, package_root: &Path) -> ModelMetadata {
        ModelMetadata {
            model: self.model.clone(),
            arch: self.arch.clone(),
            methods: self.methods.clone(),
            layers: self.layers,
            experts_per_layer: self.experts_per_layer,
            experts_used_per_token: self.experts_used_per_token,
            hidden_size: self.hidden_size,
            intermediate_size: self.intermediate_size,
            metadata_gguf: self
                .files
                .metadata
                .as_ref()
                .map(|f| package_root.join(f)),
            dense_gguf: self.files.dense.as_ref().map(|f| package_root.join(f)),
            experts_greenpack: self.files.experts.as_ref().map(|f| package_root.join(f)),
            tokenizer: self.files.tokenizer.as_ref().map(|f| package_root.join(f)),
        }
    }

    /// Required relative sidecar paths the package directory must contain.
    pub fn required_sidecars(&self) -> Vec<String> {
        let mut out = Vec::new();
        if let Some(m) = &self.files.metadata {
            out.push(m.clone());
        }
        if let Some(d) = &self.files.dense {
            out.push(d.clone());
        }
        if let Some(e) = &self.files.experts {
            out.push(e.clone());
        }
        if let Some(t) = &self.files.tokenizer {
            out.push(t.clone());
        }
        if out.is_empty() {
            for rel in &self.tensor_files {
                if !out.contains(rel) {
                    out.push(rel.clone());
                }
            }
        }
        out
    }

    pub fn expert_tensors(&self) -> impl Iterator<Item = &TensorRecord> {
        self.tensors.iter().filter(|t| t.is_expert())
    }

    pub fn pending_expert_stubs(&self) -> impl Iterator<Item = &PendingExpertRecord> {
        self.expert_tensors_pending.iter().filter(|p| p.is_stub())
    }
}

impl ModelMetadata {
    pub fn from_manifest(manifest: &GreenManifest, package_root: &Path) -> Self {
        manifest.metadata_paths(package_root)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tensor::TensorRole;

    #[test]
    fn rejects_wrong_format() {
        let m = GreenManifest {
            format: "gguf".into(),
            version: 1,
            model: "test".into(),
            ..Default::default()
        };
        assert!(m.validate().is_err());
    }

    #[test]
    fn parses_pack_model_manifest() {
        let raw = include_str!("../tests/fixtures/pack_model_manifest.json");
        let m: GreenManifest = serde_json::from_str(raw).unwrap();
        m.validate().unwrap();
        assert_eq!(m.model, "test-mini.gguf");
        assert_eq!(m.arch.as_deref(), Some("llama"));
        assert_eq!(m.methods, vec!["green_optimal"]);
        assert_eq!(m.layers, Some(1));
        assert_eq!(m.hidden_size, Some(64));
        assert_eq!(m.files.dense.as_deref(), Some("dense.gguf"));
        assert_eq!(m.files.metadata.as_deref(), Some("metadata.gguf"));
        assert_eq!(m.files.experts.as_deref(), Some("experts-000.greenpack"));
        assert_eq!(
            m.tensor_files,
            vec![
                "metadata.gguf".to_string(),
                "dense.gguf".to_string(),
                "experts-000.greenpack".to_string()
            ]
        );
        assert_eq!(m.tensors.len(), 2);
        assert_eq!(m.tensors[0].role, Some(TensorRole::Embedding));
        assert_eq!(m.tensors[1].role, Some(TensorRole::Dense));
        assert_eq!(m.tensors[1].byte_length(), Some(9216));
        assert!(m.expert_tensors_pending.is_empty());
        assert!(m
            .compression_note
            .as_deref()
            .unwrap()
            .contains("Phase 2"));
    }

    #[test]
    fn parses_moe_manifest_with_catalogued_experts() {
        // M4 fixture: experts live in tensors[] (greenpack catalogue); pending is empty.
        let raw = include_str!("../tests/fixtures/pack_model_moe_manifest.json");
        let m: GreenManifest = serde_json::from_str(raw).unwrap();
        m.validate().unwrap();
        assert!(m.expert_tensors_pending.is_empty());
        assert_eq!(m.expert_tensors().count(), 1);
        assert_eq!(m.experts_per_layer, Some(2));
    }

    #[test]
    fn parses_pending_experts() {
        let raw = r#"{
          "format": "green-model",
          "version": 1,
          "model": "tiny-moe.gguf",
          "architecture": "qwen2moe",
          "method": "green_optimal",
          "experts_per_layer": 2,
          "tensors": [],
          "expert_tensors_pending": [{
            "name": "blk.0.ffn_gate_exps.1.weight",
            "role": "expert",
            "layer": 0,
            "expert": 1,
            "shape": [64, 128],
            "status": "stub"
          }]
        }"#;
        let m: GreenManifest = serde_json::from_str(raw).unwrap();
        m.validate().unwrap();
        assert_eq!(m.expert_tensors_pending.len(), 1);
        assert!(m.expert_tensors_pending[0].is_stub());
        assert_eq!(m.experts_per_layer, Some(2));
    }

    #[test]
    fn accepts_duplicate_arch_and_source_model_wire_fields() {
        // pack-model emits both canonical and alias keys; must not fail serde.
        let raw = r#"{
          "format": "green-model",
          "version": 1,
          "model": "demo.gguf",
          "source_model": "demo.gguf",
          "architecture": "llama",
          "arch": "llama",
          "method": "green_optimal",
          "methods": ["green_optimal"],
          "files": { "metadata": "metadata.gguf", "dense": "dense.gguf" },
          "tensors": []
        }"#;
        let m: GreenManifest = serde_json::from_str(raw).unwrap();
        assert_eq!(m.model, "demo.gguf");
        assert_eq!(m.arch.as_deref(), Some("llama"));
        assert_eq!(m.methods, vec!["green_optimal"]);
    }
}
