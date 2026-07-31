//! Native `.green` package loader and generate seam.
//!
//! Validates packages via `green-format`, indexes dense.gguf, and builds tensor handles.
//! Dense generate goes through [`GreenModel::generate`] / [`crate::generate`] when
//! [`GreenModel::can_generate`] is true.
//!
//! The dense file is mapped lazily on [`DenseWeightStore::as_bytes`] / [`GreenModel::tensor_bytes`].
//! Decode packed weights use a separate mmap inside [`crate::quant_mat::QuantMat`] (no `to_vec`
//! copy). Avoiding an eager full-file map at [`GreenModel::open`] keeps peak RSS lower.

use std::fs::File;
use std::path::{Path, PathBuf};
use std::sync::{Arc, OnceLock};

use green_format::{
    open_package, GreenManifest, ModelMetadata, PackageError, TensorRecord, TensorRole,
};
use memmap2::Mmap;

/// Configuration for opening a `.green` package.
#[derive(Clone, Debug)]
pub struct LoadConfig {
    /// Verify SHA-256 checksums on tensor shards when present.
    pub verify_checksums: bool,
}

impl Default for LoadConfig {
    fn default() -> Self {
        LoadConfig {
            verify_checksums: false,
        }
    }
}

/// Structural readiness after a successful [`GreenModel::open`].
///
/// [`ReadyForForward`] means the dense map and tensor index are usable. Token generation
/// additionally requires [`GreenModel::can_generate`] (embd/output + real dense.gguf).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LoadState {
    /// Dense weights mapped and tensor index built; forward / generate may bind layers.
    ReadyForForward,
    /// Package validated but missing pieces needed for a forward slice.
    NotReady { reason: String },
}

/// Logical kind used by the forward agent (finer than [`TensorRole`]).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TensorKind {
    Embedding,
    Norm,
    Attn,
    Ffn,
    Expert,
    Output,
    Other,
}

/// Indexed view into a mapped dense (or expert-shard) tensor.
#[derive(Clone, Debug)]
pub struct TensorHandle {
    pub name: String,
    pub kind: TensorKind,
    pub role: Option<TensorRole>,
    pub layer: Option<u32>,
    pub expert: Option<u16>,
    pub shape: Vec<u32>,
    pub file: PathBuf,
    pub offset: u64,
    pub length: u64,
    pub ggml_type: Option<String>,
    pub green_compression_type: Option<String>,
    /// True when bytes live in [`GreenModel::dense_weights`] (not an expert shard file).
    pub in_dense: bool,
}

/// Errors from loading or dense generate.
#[derive(Debug)]
pub enum GreenModelError {
    Package(PackageError),
    Io(String),
    /// Dense map / tensor slice out of range.
    TensorBounds {
        name: String,
        offset: u64,
        length: u64,
        file_len: u64,
    },
    /// Package opened but is not ready for dense native generate.
    GenerationNotReady,
    /// Alias of [`GenerationNotReady`] (legacy name kept for callers/tests).
    RuntimeNotReady,
    /// MoE expert id outside the package index.
    ExpertUnavailable { layer: usize, expert: u16 },
}

impl std::fmt::Display for GreenModelError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GreenModelError::Package(e) => write!(f, "{e}"),
            GreenModelError::Io(e) => write!(f, "I/O: {e}"),
            GreenModelError::TensorBounds {
                name,
                offset,
                length,
                file_len,
            } => write!(
                f,
                "tensor {name:?} slice [{offset}..{offset}+{length}) exceeds file length {file_len}"
            ),
            GreenModelError::GenerationNotReady | GreenModelError::RuntimeNotReady => write!(
                f,
                "package is not ready for dense generate \
                 (need ReadyForForward + embd/output + dense.gguf > 128 bytes). \
                 For ordinary GGUF use `ge chat serve --model <file.gguf>` (llama.cpp path)"
            ),
            GreenModelError::ExpertUnavailable { layer, expert } => {
                write!(f, "expert L{layer} E{expert} unavailable in package")
            }
        }
    }
}

impl std::error::Error for GreenModelError {}

impl From<PackageError> for GreenModelError {
    fn from(value: PackageError) -> Self {
        GreenModelError::Package(value)
    }
}

/// Dense weights sidecar (`dense.gguf`): path + size; mmap created on first byte access.
pub struct DenseWeightStore {
    pub path: PathBuf,
    len: usize,
    map: OnceLock<Arc<Mmap>>,
}

impl DenseWeightStore {
    fn open(path: PathBuf) -> Result<Self, GreenModelError> {
        let meta = std::fs::metadata(&path).map_err(|e| {
            GreenModelError::Io(format!("stat dense {}: {e}", path.display()))
        })?;
        Ok(DenseWeightStore {
            path,
            len: meta.len() as usize,
            map: OnceLock::new(),
        })
    }

    fn ensure_map(&self) -> Result<&Mmap, GreenModelError> {
        if let Some(m) = self.map.get() {
            return Ok(m.as_ref());
        }
        #[cfg(windows)]
        let file = {
            use std::fs::OpenOptions;
            use std::os::windows::fs::OpenOptionsExt;
            OpenOptions::new()
                .read(true)
                .custom_flags(0x1000_0000) // FILE_FLAG_RANDOM_ACCESS
                .open(&self.path)
                .map_err(|e| {
                    GreenModelError::Io(format!("open dense {}: {e}", self.path.display()))
                })?
        };
        #[cfg(not(windows))]
        let file = File::open(&self.path).map_err(|e| {
            GreenModelError::Io(format!("open dense {}: {e}", self.path.display()))
        })?;
        let map = unsafe { Mmap::map(&file) }.map_err(|e| {
            GreenModelError::Io(format!("mmap dense {}: {e}", self.path.display()))
        })?;
        let _ = self.map.set(Arc::new(map));
        Ok(self.map.get().expect("dense map just set").as_ref())
    }

    pub fn len(&self) -> usize {
        self.len
    }

    pub fn is_empty(&self) -> bool {
        self.len == 0
    }

    pub fn as_bytes(&self) -> Result<&[u8], GreenModelError> {
        Ok(self.ensure_map()?)
    }

    pub fn slice(&self, offset: u64, length: u64) -> Result<&[u8], GreenModelError> {
        let start = offset as usize;
        let end = start.saturating_add(length as usize);
        let bytes = self.as_bytes()?;
        if end > bytes.len() || start > bytes.len() {
            return Err(GreenModelError::TensorBounds {
                name: self.path.display().to_string(),
                offset,
                length,
                file_len: bytes.len() as u64,
            });
        }
        Ok(&bytes[start..end])
    }
}

impl Clone for DenseWeightStore {
    fn clone(&self) -> Self {
        let map = OnceLock::new();
        if let Some(m) = self.map.get() {
            let _ = map.set(Arc::clone(m));
        }
        DenseWeightStore {
            path: self.path.clone(),
            len: self.len,
            map,
        }
    }
}

impl std::fmt::Debug for DenseWeightStore {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("DenseWeightStore")
            .field("path", &self.path)
            .field("len", &self.len())
            .field("mapped", &self.map.get().is_some())
            .finish()
    }
}

/// Index of expert tensor shards for package MoE (see [`crate::green_experts::PackageExpertStore`]).
#[derive(Clone, Debug, Default)]
pub struct GreenExpertStore {
    pub records: Vec<TensorRecord>,
    pub handles: Vec<TensorHandle>,
}

impl GreenExpertStore {
    pub fn has_experts(&self) -> bool {
        !self.records.is_empty()
    }
}

/// Tokenizer sidecar path (loaded by [`crate::tokenize`] / [`crate::generate`]).
#[derive(Clone, Debug, Default)]
pub struct Tokenizer {
    pub path: Option<PathBuf>,
}

/// Layer/expert id returned by [`ExpertProvider::acquire`].
#[derive(Clone, Debug)]
pub struct ExpertHandle {
    pub layer: usize,
    pub expert: u16,
}

/// Package-facing expert residency seam (`prefetch` / `acquire` / `evict`).
///
/// Distinct from [`crate::paged::ExpertProvider`] (MoE runtime `with_expert`). Re-exported
/// as [`crate::GreenExpertProvider`]. Dense no-op: [`StubExpertProvider`]. Live MoE:
/// [`crate::green_experts::PackageExpertStore`].
pub trait ExpertProvider {
    fn prefetch(&self, layer: usize, experts: &[u16]);
    fn acquire(&self, layer: usize, expert: u16) -> Result<ExpertHandle, GreenModelError>;
    fn evict(&self, layer: usize, expert: u16);
}

/// Opened `.green` model package (dense-valid packs return [`Ok`]).
pub struct GreenModel {
    pub metadata: ModelMetadata,
    pub dense_weights: DenseWeightStore,
    pub experts: GreenExpertStore,
    pub tokenizer: Tokenizer,
    pub load_state: LoadState,
    manifest: GreenManifest,
    root: PathBuf,
    tensors: Vec<TensorHandle>,
}

impl GreenModel {
    /// Open a `.green` directory produced by Green Compress `pack-model`.
    ///
    /// Validates via [`open_package`], requires `dense.gguf`, memory-maps dense weights, and
    /// indexes tensor handles. Returns [`Ok`] for complete dense packages. Use
    /// [`Self::can_generate`] / [`Self::generate`] for the native dense token path.
    pub fn open(path: &Path, cfg: &LoadConfig) -> Result<Self, GreenModelError> {
        let pkg = open_package(path, cfg.verify_checksums)?;
        let dense_path = pkg.metadata.dense_gguf.clone().ok_or_else(|| {
            GreenModelError::Package(PackageError::MissingFile(path.join("dense.gguf")))
        })?;
        let dense_weights = DenseWeightStore::open(dense_path)?;
        let dense_len = dense_weights.len() as u64;

        let mut tensors = Vec::with_capacity(pkg.manifest.tensors.len());
        let mut expert_handles = Vec::new();
        for rec in &pkg.manifest.tensors {
            let handle = tensor_handle_from_record(rec, &pkg.root, dense_len, &dense_weights.path)?;
            if handle.kind == TensorKind::Expert {
                expert_handles.push(handle.clone());
            }
            tensors.push(handle);
        }

        let experts_records: Vec<TensorRecord> = pkg.expert_records().cloned().collect();
        let load_state = classify_load_state(&tensors, dense_len);

        Ok(GreenModel {
            metadata: pkg.metadata.clone(),
            dense_weights,
            experts: GreenExpertStore {
                records: experts_records,
                handles: expert_handles,
            },
            tokenizer: Tokenizer {
                path: pkg.metadata.tokenizer.clone(),
            },
            load_state,
            manifest: pkg.manifest.clone(),
            root: pkg.root,
            tensors,
        })
    }

    pub fn package_root(&self) -> &Path {
        &self.root
    }

    pub fn manifest(&self) -> &GreenManifest {
        &self.manifest
    }

    pub fn load_state(&self) -> &LoadState {
        &self.load_state
    }

    pub fn is_ready_for_forward(&self) -> bool {
        matches!(self.load_state, LoadState::ReadyForForward)
    }

    /// True when this package can run the dense native generate path (M3).
    pub fn can_generate(&self) -> bool {
        crate::generate::model_can_generate(self)
    }

    pub fn ensure_generation_ready(&self) -> Result<(), GreenModelError> {
        crate::generate::ensure_ready(self).map_err(GreenModelError::from)
    }

    /// Dense generate (tokenize → forward → decode). Used by `ge run`.
    pub fn generate(&self, prompt: &str, max_new_tokens: usize) -> Result<String, GreenModelError> {
        let out = self.generate_detailed(prompt, max_new_tokens)?;
        Ok(out.text)
    }

    /// Same as [`Self::generate`] but returns timing / KV metrics (warm-path benches / chat logs).
    pub fn generate_detailed(
        &self,
        prompt: &str,
        max_new_tokens: usize,
    ) -> Result<crate::generate::GenerateResult, GreenModelError> {
        let req = crate::generate::GenerateRequest::new(prompt, max_new_tokens);
        crate::generate::generate(self, &req).map_err(GreenModelError::from)
    }

    /// Full request (chat template + sampling knobs).
    pub fn generate_with(
        &self,
        req: &crate::generate::GenerateRequest,
    ) -> Result<crate::generate::GenerateResult, GreenModelError> {
        crate::generate::generate(self, req).map_err(GreenModelError::from)
    }

    /// Build a RAM-LRU [`crate::green_experts::PackageExpertStore`] from expert tensor records.
    /// Returns `Ok(None)` for dense packages (no expert shards) — use [`crate::green_experts::FfnMode::Dense`].
    pub fn package_expert_store(
        &self,
        ram_experts_per_layer: usize,
    ) -> Result<Option<crate::green_experts::PackageExpertStore>, crate::green_experts::MoeError>
    {
        self.package_expert_store_budget(crate::paged::SlotBudget::per_layer(
            ram_experts_per_layer,
        ))
    }

    /// Like [`package_expert_store`](Self::package_expert_store) with an explicit [`crate::paged::SlotBudget`].
    pub fn package_expert_store_budget(
        &self,
        budget: crate::paged::SlotBudget,
    ) -> Result<Option<crate::green_experts::PackageExpertStore>, crate::green_experts::MoeError>
    {
        crate::green_experts::PackageExpertStore::from_records_budget(
            &self.root,
            &self.experts.records,
            budget,
        )
    }

    pub fn tensors(&self) -> &[TensorHandle] {
        &self.tensors
    }

    pub fn tensor_by_name(&self, name: &str) -> Option<&TensorHandle> {
        self.tensors.iter().find(|t| t.name == name)
    }

    pub fn embedding(&self) -> Option<&TensorHandle> {
        self.tensors
            .iter()
            .find(|t| t.kind == TensorKind::Embedding)
    }

    pub fn norms(&self) -> impl Iterator<Item = &TensorHandle> {
        self.tensors.iter().filter(|t| t.kind == TensorKind::Norm)
    }

    pub fn attn(&self) -> impl Iterator<Item = &TensorHandle> {
        self.tensors.iter().filter(|t| t.kind == TensorKind::Attn)
    }

    pub fn ffn(&self) -> impl Iterator<Item = &TensorHandle> {
        self.tensors.iter().filter(|t| t.kind == TensorKind::Ffn)
    }

    /// lm_head tensor, or the embedding table when weights are tied (no `output.weight`).
    pub fn output(&self) -> Option<&TensorHandle> {
        self.tensors
            .iter()
            .find(|t| t.kind == TensorKind::Output)
            .or_else(|| self.embedding())
    }

    /// Layer count from manifest, else inferred from tensor `layer` fields (0 if unknown).
    pub fn num_layers(&self) -> usize {
        if let Some(n) = self.manifest.layers {
            return n as usize;
        }
        self.tensors
            .iter()
            .filter(|t| t.kind != TensorKind::Expert)
            .filter_map(|t| t.layer)
            .map(|l| l as usize + 1)
            .max()
            .unwrap_or(0)
    }

    /// Bytes for a dense-backed handle (expert shard files are not mapped here).
    pub fn tensor_bytes(&self, handle: &TensorHandle) -> Result<&[u8], GreenModelError> {
        if !handle.in_dense {
            return Err(GreenModelError::Io(format!(
                "tensor {:?} is not in dense.gguf (expert/shard path {}); use ExpertProvider",
                handle.name,
                handle.file.display()
            )));
        }
        self.dense_weights
            .slice(handle.offset, handle.length)
            .map_err(|e| match e {
                GreenModelError::TensorBounds {
                    offset,
                    length,
                    file_len,
                    ..
                } => GreenModelError::TensorBounds {
                    name: handle.name.clone(),
                    offset,
                    length,
                    file_len,
                },
                other => other,
            })
    }
}

fn classify_load_state(tensors: &[TensorHandle], dense_len: u64) -> LoadState {
    if dense_len == 0 {
        return LoadState::NotReady {
            reason: "dense.gguf is empty".into(),
        };
    }
    if tensors.is_empty() {
        // Dense sidecar present — forward can still open GGUF-style dense; ReadyForForward.
        return LoadState::ReadyForForward;
    }
    let has_embed = tensors.iter().any(|t| t.kind == TensorKind::Embedding);
    let has_layer = tensors
        .iter()
        .any(|t| matches!(t.kind, TensorKind::Attn | TensorKind::Ffn | TensorKind::Norm));
    if has_embed || has_layer {
        LoadState::ReadyForForward
    } else {
        LoadState::NotReady {
            reason: "no embedding / attn / ffn / norm tensors indexed in manifest".into(),
        }
    }
}

fn classify_kind(rec: &TensorRecord) -> TensorKind {
    if rec.is_expert() {
        return TensorKind::Expert;
    }
    if let Some(role) = rec.role {
        match role {
            TensorRole::Embedding => return TensorKind::Embedding,
            TensorRole::Norm => return TensorKind::Norm,
            TensorRole::Expert => return TensorKind::Expert,
            TensorRole::Dense | TensorRole::Other => {}
        }
    }
    let n = rec.name.to_ascii_lowercase();
    if n.contains("token_embd") || n.contains("tok_embeddings") || n.contains("embed") {
        return TensorKind::Embedding;
    }
    if n.contains("output_norm") || n.contains("rmsnorm") || n.contains("norm") {
        return TensorKind::Norm;
    }
    if n.contains("attn")
        || n.contains("q_proj")
        || n.contains("k_proj")
        || n.contains("v_proj")
        || n.contains("o_proj")
        || n.contains("wq")
        || n.contains("wk")
        || n.contains("wv")
        || n.contains("wo")
    {
        return TensorKind::Attn;
    }
    if n.contains("ffn") || n.contains("feed_forward") || n.contains("mlp") {
        return TensorKind::Ffn;
    }
    if n.contains("output") || n.contains("lm_head") || n == "output.weight" {
        return TensorKind::Output;
    }
    match rec.role {
        Some(TensorRole::Dense) => TensorKind::Ffn,
        _ => TensorKind::Other,
    }
}

fn tensor_byte_len(rec: &TensorRecord, file_len: u64) -> u64 {
    rec.byte_length()
        .unwrap_or_else(|| file_len.saturating_sub(rec.offset))
}

fn tensor_handle_from_record(
    rec: &TensorRecord,
    root: &Path,
    dense_len: u64,
    dense_path: &Path,
) -> Result<TensorHandle, GreenModelError> {
    let kind = classify_kind(rec);
    let file = root.join(&rec.file);
    let in_dense = file == dense_path
        || rec.file == "dense.gguf"
        || dense_path.file_name().and_then(|s| s.to_str()) == Some(rec.file.as_str());
    let file_len = if in_dense {
        dense_len
    } else {
        std::fs::metadata(&file)
            .map(|m| m.len())
            .unwrap_or(0)
    };
    let length = tensor_byte_len(rec, file_len);
    if in_dense && rec.offset.saturating_add(length) > dense_len {
        return Err(GreenModelError::TensorBounds {
            name: rec.name.clone(),
            offset: rec.offset,
            length,
            file_len: dense_len,
        });
    }
    Ok(TensorHandle {
        name: rec.name.clone(),
        kind,
        role: rec.role,
        layer: rec.layer,
        expert: rec.expert,
        shape: rec.shape.clone(),
        file,
        offset: rec.offset,
        length,
        ggml_type: rec.ggml_type.clone(),
        green_compression_type: rec.green_compression_type.clone(),
        in_dense,
    })
}

/// Dense-package no-op for [`ExpertProvider`]: prefetch/evict are empty; acquire returns
/// an [`ExpertHandle`] without loading weights. MoE weight I/O uses
/// [`crate::green_experts::PackageExpertStore`] ([`crate::paged::ExpertProvider`]).
#[derive(Clone, Copy, Debug, Default)]
pub struct StubExpertProvider;

impl ExpertProvider for StubExpertProvider {
    fn prefetch(&self, _layer: usize, _experts: &[u16]) {}

    fn acquire(&self, layer: usize, expert: u16) -> Result<ExpertHandle, GreenModelError> {
        Ok(ExpertHandle { layer, expert })
    }

    fn evict(&self, _layer: usize, _expert: u16) {}
}

#[cfg(test)]
mod tests {
    use super::*;
    use green_format::manifest::ModelFiles;
    use green_format::schema::{FORMAT_NAME, FORMAT_VERSION};
    use green_format::tensor::TensorRole;
    use std::fs;
    use tempfile::TempDir;

    fn write_minimal_dense(dir: &Path, with_tensors: bool) {
        fs::write(dir.join("metadata.gguf"), b"meta").unwrap();
        let dense = [0u8; 64];
        fs::write(dir.join("dense.gguf"), dense).unwrap();
        let tensors = if with_tensors {
            vec![
                TensorRecord {
                    name: "token_embd.weight".into(),
                    role: Some(TensorRole::Embedding),
                    layer: None,
                    expert: None,
                    shape: vec![8, 4],
                    file: "dense.gguf".into(),
                    offset: 0,
                    length: Some(32),
                    checksum: None,
                    method: None,
                    ggml_type: None,
                    source_gguf_type: Some("F32".into()),
                    green_compression_type: None,
                    compressed_size: None,
                },
                TensorRecord {
                    name: "blk.0.attn_q.weight".into(),
                    role: Some(TensorRole::Dense),
                    layer: Some(0),
                    expert: None,
                    shape: vec![4, 4],
                    file: "dense.gguf".into(),
                    offset: 32,
                    length: Some(16),
                    checksum: None,
                    method: None,
                    ggml_type: None,
                    source_gguf_type: Some("F32".into()),
                    green_compression_type: None,
                    compressed_size: None,
                },
                TensorRecord {
                    name: "blk.0.ffn_down.weight".into(),
                    role: Some(TensorRole::Dense),
                    layer: Some(0),
                    expert: None,
                    shape: vec![4, 4],
                    file: "dense.gguf".into(),
                    offset: 48,
                    length: Some(16),
                    checksum: None,
                    method: None,
                    ggml_type: None,
                    source_gguf_type: Some("F32".into()),
                    green_compression_type: None,
                    compressed_size: None,
                },
            ]
        } else {
            vec![]
        };
        let manifest = green_format::GreenManifest {
            format: FORMAT_NAME.into(),
            version: FORMAT_VERSION,
            model: "demo".into(),
            arch: Some("llama".into()),
            methods: vec![],
            files: ModelFiles {
                metadata: Some("metadata.gguf".into()),
                dense: Some("dense.gguf".into()),
                tokenizer: None,
                experts: None,
            },
            tensors,
            layers: if with_tensors { Some(1) } else { None },
            ..Default::default()
        };
        fs::write(
            dir.join("manifest.json"),
            serde_json::to_string(&manifest).unwrap(),
        )
        .unwrap();
    }

    #[test]
    fn open_returns_ok_for_valid_dense_package() {
        let dir = TempDir::new().unwrap();
        write_minimal_dense(dir.path(), false);
        let model = GreenModel::open(dir.path(), &LoadConfig::default()).unwrap();
        assert!(model.is_ready_for_forward());
        assert!(!model.can_generate());
        assert!(matches!(
            model.ensure_generation_ready(),
            Err(GreenModelError::GenerationNotReady) | Err(GreenModelError::Io(_))
        ));
        assert_eq!(model.dense_weights.len(), 64);
        assert_eq!(model.metadata.model, "demo");
    }

    #[test]
    fn open_indexes_embed_attn_ffn_handles() {
        let dir = TempDir::new().unwrap();
        write_minimal_dense(dir.path(), true);
        let model = GreenModel::open(dir.path(), &LoadConfig::default()).unwrap();
        assert!(matches!(model.load_state, LoadState::ReadyForForward));
        assert_eq!(model.embedding().unwrap().name, "token_embd.weight");
        assert_eq!(model.attn().count(), 1);
        assert_eq!(model.ffn().count(), 1);
        assert_eq!(model.num_layers(), 1);
        let emb = model.embedding().unwrap();
        let bytes = model.tensor_bytes(emb).unwrap();
        assert_eq!(bytes.len(), 32);
    }

    #[test]
    fn open_fixture_dir_under_testdata() {
        let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/mini_green");
        if !root.join("manifest.json").is_file() {
            eprintln!("skip: testdata/mini_green missing");
            return;
        }
        let model = GreenModel::open(&root, &LoadConfig::default()).unwrap();
        assert!(model.is_ready_for_forward());
        assert!(model.embedding().is_some());
    }

    fn tiny_green_path() -> Option<PathBuf> {
        let candidates = [
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("testdata/tiny.green"),
            PathBuf::from(r"../green-compress\scripts\fixtures\tiny.green"),
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../../green-compress/scripts/fixtures/tiny.green"),
        ];
        candidates.into_iter().find(|p| p.join("manifest.json").is_file())
    }

    #[test]
    fn open_tiny_green_pack_model_fixture_ok() {
        let Some(root) = tiny_green_path() else {
            eprintln!("skip: tiny.green fixture not found");
            return;
        };
        // M0 contract: open_package(..., true) then map dense.
        let model = GreenModel::open(
            &root,
            &LoadConfig {
                verify_checksums: true,
            },
        )
        .unwrap_or_else(|e| panic!("tiny.green must open Ok, got: {e}"));
        assert!(
            model.is_ready_for_forward(),
            "load_state={:?}",
            model.load_state
        );
        // M3+: real tiny.green pack is generation-ready (tokenizer + full dense stack).
        assert!(model.can_generate());
        assert!(model.embedding().is_some());
        assert!(model.attn().count() > 0);
        assert!(model.ffn().count() > 0);
        assert_eq!(model.num_layers(), 1);
        assert!(!model.experts.has_experts());
        assert!(model.tokenizer.path.is_some());
        assert!(model.ensure_generation_ready().is_ok());
    }

    #[test]
    fn pending_expert_stubs_alone_do_not_fail_open() {
        let dir = TempDir::new().unwrap();
        write_minimal_dense(dir.path(), true);
        // Inject pending stubs into the on-disk manifest without expert shard files.
        let manifest_path = dir.path().join("manifest.json");
        let raw = fs::read_to_string(&manifest_path).unwrap();
        let mut v: serde_json::Value = serde_json::from_str(&raw).unwrap();
        v["expert_tensors_pending"] = serde_json::json!([{
            "name": "blk.0.ffn_up_exps.weight",
            "role": "expert",
            "layer": 0,
            "expert": 0,
            "shape": [4, 4],
            "file": "experts-000.greenpack",
            "status": "stub"
        }]);
        fs::write(&manifest_path, serde_json::to_string(&v).unwrap()).unwrap();
        let model = GreenModel::open(dir.path(), &LoadConfig::default())
            .unwrap_or_else(|e| panic!("pending stubs must not fail open: {e}"));
        assert!(model.is_ready_for_forward());
        assert_eq!(model.manifest().expert_tensors_pending.len(), 1);
    }

    #[test]
    fn rejects_missing_dense() {
        let dir = TempDir::new().unwrap();
        fs::write(dir.path().join("metadata.gguf"), b"m").unwrap();
        let manifest = green_format::GreenManifest {
            format: FORMAT_NAME.into(),
            version: FORMAT_VERSION,
            model: "demo".into(),
            arch: None,
            methods: vec![],
            files: ModelFiles {
                metadata: Some("metadata.gguf".into()),
                dense: None,
                tokenizer: None,
                experts: None,
            },
            tensors: vec![],
            ..Default::default()
        };
        fs::write(
            dir.path().join("manifest.json"),
            serde_json::to_string(&manifest).unwrap(),
        )
        .unwrap();
        let err = match GreenModel::open(dir.path(), &LoadConfig::default()) {
            Err(e) => e,
            Ok(_) => panic!("expected missing dense to fail"),
        };
        assert!(matches!(err, GreenModelError::Package(_)));
    }
}
