//! Per-tensor records in a Green model manifest.
//!
//! # Wire fields (`tensors[]` from `pack-model`)
//!
//! | JSON field | Type | Notes |
//! |------------|------|-------|
//! | `name` | string | Required GGUF tensor name |
//! | `role` | string | Pack emits fine roles (`ffn_down`, `attn_q`, …); mapped to [`TensorRole`] |
//! | `layer` | u32 \| null | Block index when applicable |
//! | `expert` | u16 \| null | Expert index when MoE |
//! | `shape` | u32[] | Logical shape (usually 2D) |
//! | `file` | string | Relative shard path (`dense.gguf`, …) |
//! | `offset` | u64 | Byte offset within `file` (pack currently writes `0`) |
//! | `compressed_size` | u64 | Payload size; also accepted as `length` |
//! | `checksum` | string | `sha256:<hex>` of the shard file (often shared for all dense rows) |
//! | `source_gguf_type` | string | Original GGML type name |
//! | `green_compression_type` | string | e.g. `green_baseline_q4_0` |
//! | `method` / `ggml_type` | string | Optional loader extensions |
//!
//! # Wire fields (`expert_tensors_pending[]`)
//!
//! Same identity fields as above, plus `status` (`"stub"` while greenpack compression is TBD).

use serde::{Deserialize, Deserializer, Serialize};

/// Logical role of a tensor within the model graph.
///
/// Pack emits fine-grained role strings (`ffn_down`, `attn_q`, …). Deserialization maps those
/// onto this coarse enum for scheduling; unknown strings become [`TensorRole::Other`].
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TensorRole {
    Dense,
    Expert,
    Embedding,
    Norm,
    Other,
}

fn deserialize_tensor_role<'de, D>(deserializer: D) -> Result<Option<TensorRole>, D::Error>
where
    D: Deserializer<'de>,
{
    let raw: Option<serde_json::Value> = Option::deserialize(deserializer)?;
    Ok(raw.map(|v| match v {
        serde_json::Value::String(s) => classify_tensor_role(&s),
        _ => TensorRole::Other,
    }))
}

/// Map pack-model / GGUF-style role strings onto [`TensorRole`].
pub fn classify_tensor_role(name: &str) -> TensorRole {
    match name {
        "dense" => TensorRole::Dense,
        "expert" => TensorRole::Expert,
        "embedding" => TensorRole::Embedding,
        "norm" => TensorRole::Norm,
        "other" | "output" => TensorRole::Other,
        s if s.starts_with("attn_") || s.starts_with("ffn_") => TensorRole::Dense,
        s if s.contains("ffn") || s.contains("attn") => TensorRole::Dense,
        _ => TensorRole::Other,
    }
}

/// One tensor entry pointing at a shard inside the package.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct TensorRecord {
    pub name: String,
    #[serde(default, deserialize_with = "deserialize_tensor_role")]
    pub role: Option<TensorRole>,
    #[serde(default)]
    pub layer: Option<u32>,
    #[serde(default)]
    pub expert: Option<u16>,
    #[serde(default)]
    pub shape: Vec<u32>,
    pub file: String,
    #[serde(default)]
    pub offset: u64,
    /// Byte length within `file` (loader extension; pack usually omits this).
    #[serde(default)]
    pub length: Option<u64>,
    #[serde(default)]
    pub checksum: Option<String>,
    #[serde(default)]
    pub method: Option<String>,
    #[serde(default)]
    pub ggml_type: Option<String>,
    #[serde(default)]
    pub source_gguf_type: Option<String>,
    #[serde(default)]
    pub green_compression_type: Option<String>,
    /// Pack writes this instead of `length`; [`Self::byte_length`] prefers `length` then this.
    #[serde(default)]
    pub compressed_size: Option<u64>,
}

impl TensorRecord {
    pub fn is_expert(&self) -> bool {
        matches!(self.role, Some(TensorRole::Expert)) || self.expert.is_some()
    }

    /// Prefer explicit `length`, else `compressed_size`.
    pub fn byte_length(&self) -> Option<u64> {
        self.length.or(self.compressed_size)
    }

    pub fn validate(&self) -> Result<(), String> {
        if self.name.is_empty() {
            return Err("tensor name is empty".into());
        }
        if self.file.is_empty() {
            return Err(format!("tensor {:?} has empty file path", self.name));
        }
        if self.file.contains('\0') || PathLooksUnsafe::check(&self.file) {
            return Err(format!("tensor {:?} has unsafe file path {:?}", self.name, self.file));
        }
        Ok(())
    }
}

/// Expert tensor not yet fully written into a greenpack (Phase 2 stub).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct PendingExpertRecord {
    pub name: String,
    #[serde(default, deserialize_with = "deserialize_tensor_role")]
    pub role: Option<TensorRole>,
    #[serde(default)]
    pub layer: Option<u32>,
    #[serde(default)]
    pub expert: Option<u16>,
    #[serde(default)]
    pub shape: Vec<u32>,
    #[serde(default)]
    pub source_gguf_type: Option<String>,
    #[serde(default)]
    pub file: String,
    /// Pack writes `"stub"` until expert compression lands.
    #[serde(default)]
    pub status: String,
}

impl PendingExpertRecord {
    pub fn is_stub(&self) -> bool {
        self.status.is_empty() || self.status == "stub"
    }

    pub fn validate(&self) -> Result<(), String> {
        if self.name.is_empty() {
            return Err("pending expert name is empty".into());
        }
        if !self.file.is_empty() && PathLooksUnsafe::check(&self.file) {
            return Err(format!(
                "pending expert {:?} has unsafe file path {:?}",
                self.name, self.file
            ));
        }
        Ok(())
    }
}

/// Reject path traversal / absolute paths in package-relative refs.
struct PathLooksUnsafe;

impl PathLooksUnsafe {
    fn check(rel: &str) -> bool {
        let p = std::path::Path::new(rel);
        p.is_absolute()
            || p.components().any(|c| matches!(c, std::path::Component::ParentDir))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn maps_pack_fine_roles() {
        assert_eq!(classify_tensor_role("ffn_down"), TensorRole::Dense);
        assert_eq!(classify_tensor_role("attn_q"), TensorRole::Dense);
        assert_eq!(classify_tensor_role("embedding"), TensorRole::Embedding);
        assert_eq!(classify_tensor_role("norm"), TensorRole::Norm);
        assert_eq!(classify_tensor_role("expert"), TensorRole::Expert);
        assert_eq!(classify_tensor_role("output"), TensorRole::Other);
    }

    #[test]
    fn accepts_compressed_size_as_length() {
        let raw = r#"{
            "name": "blk.0.ffn_down.weight",
            "role": "ffn_down",
            "shape": [256, 64],
            "file": "dense.gguf",
            "offset": 0,
            "compressed_size": 9216,
            "checksum": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        }"#;
        let t: TensorRecord = serde_json::from_str(raw).unwrap();
        assert_eq!(t.role, Some(TensorRole::Dense));
        assert_eq!(t.compressed_size, Some(9216));
        assert_eq!(t.byte_length(), Some(9216));
        t.validate().unwrap();
    }

    #[test]
    fn rejects_path_traversal() {
        let t = TensorRecord {
            name: "x".into(),
            role: None,
            layer: None,
            expert: None,
            shape: vec![],
            file: "../escape.bin".into(),
            offset: 0,
            length: None,
            checksum: None,
            method: None,
            ggml_type: None,
            source_gguf_type: None,
            green_compression_type: None,
            compressed_size: None,
        };
        assert!(t.validate().is_err());
    }
}
