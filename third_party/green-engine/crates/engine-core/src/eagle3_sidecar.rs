//! R21-eagle steps 1–3 — EAGLE-3 sidecar loader, hidden export, draft-path stub.
//!
//! Step 1: parse llama-compatible `*-eagle3.gguf` metadata (`eagle3.target_layers`, `d2t`).
//! Step 2: [`Eagle3DecodeHiddenHook`] captures low/mid/high layer hiddens during target decode.
//! Step 3: [`Eagle3Drafter::draft`] validates fusion hiddens + seeds draft forward (FC/decoder
//! weights deferred — returns [`Eagle3DraftError::NotImplemented`]). Verify wiring is step 4.
//!
//! **KEEP** if `eagle3_sidecar` unit tests pass; else **REVERT**.

use std::fmt;
use std::io;
use std::path::Path;

use crate::gguf_io::{read_gguf, GgufFile};

/// GGUF architecture string for EAGLE-3 sidecars (`LLM_ARCH_EAGLE3`).
pub const EAGLE3_ARCH: &str = "eagle3";

/// KV key for fusion layer indices (`eagle3.target_layers`).
pub const TARGET_LAYERS_KEY: &str = "eagle3.target_layers";

/// Draft→target vocab scatter tensor name.
pub const D2T_TENSOR: &str = "d2t";

/// Expected fusion layer count (low / mid / high hidden export).
pub const TARGET_LAYER_COUNT: usize = 3;

/// Default draft length γ (R21-eagle; full forward uses 4–8).
pub const DEFAULT_GAMMA: usize = 4;

/// Upper bound for γ until adaptive-γ lands (R20-C).
pub const MAX_GAMMA: usize = 8;

/// GGML quantization type for `d2t` (I64 absolute target token ids).
pub const GGML_TYPE_I64: u32 = 11;

/// Parsed sidecar metadata sufficient for hidden-export hook sizing (step 2).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Eagle3SidecarMeta {
    /// Target layer indices for low / mid / high hidden states.
    pub target_layers: [u32; TARGET_LAYER_COUNT],
    /// Draft vocabulary size (`d2t` length).
    pub draft_vocab_size: u64,
}

/// Loaded EAGLE-3 sidecar (metadata only; weights deferred to step 3).
#[derive(Clone, Debug)]
pub struct Eagle3Sidecar {
    meta: Eagle3SidecarMeta,
}

/// Sidecar parse / contract validation errors (fail-closed).
#[derive(Debug)]
pub enum Eagle3LoadError {
    Io(io::Error),
    WrongArchitecture { got: String },
    MissingKey(&'static str),
    InvalidTargetLayers { got: usize },
    MissingD2t,
    InvalidD2tType { ggml_type: u32 },
    InvalidD2tShape { shape: Vec<u64> },
}

impl fmt::Display for Eagle3LoadError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Io(e) => write!(f, "eagle3 sidecar io: {e}"),
            Self::WrongArchitecture { got } => {
                write!(f, "eagle3 sidecar: expected architecture {EAGLE3_ARCH}, got {got:?}")
            }
            Self::MissingKey(k) => write!(f, "eagle3 sidecar: missing required key {k}"),
            Self::InvalidTargetLayers { got } => write!(
                f,
                "eagle3 sidecar: {TARGET_LAYERS_KEY} must have {TARGET_LAYER_COUNT} entries, got {got}"
            ),
            Self::MissingD2t => write!(f, "eagle3 sidecar: missing tensor {D2T_TENSOR}"),
            Self::InvalidD2tType { ggml_type } => write!(
                f,
                "eagle3 sidecar: {D2T_TENSOR} must be GGML I64 (type {GGML_TYPE_I64}), got {ggml_type}"
            ),
            Self::InvalidD2tShape { shape } => {
                write!(f, "eagle3 sidecar: {D2T_TENSOR} must be 1-D non-empty, shape={shape:?}")
            }
        }
    }
}

impl std::error::Error for Eagle3LoadError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Io(e) => Some(e),
            _ => None,
        }
    }
}

impl From<io::Error> for Eagle3LoadError {
    fn from(e: io::Error) -> Self {
        Self::Io(e)
    }
}

/// Draft-path validation / forward errors (fail-closed).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Eagle3DraftError {
    NotImplemented,
    EmptyHiddens,
    HiddenDimMismatch { expected: usize, got: usize },
    InvalidGamma { got: usize, max: usize },
}

impl fmt::Display for Eagle3DraftError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotImplemented => {
                write!(f, "eagle3 draft forward not implemented (R21 step 3 stub)")
            }
            Self::EmptyHiddens => write!(f, "eagle3 draft: fusion hiddens must be non-empty"),
            Self::HiddenDimMismatch { expected, got } => write!(
                f,
                "eagle3 draft: fusion hidden dim mismatch (expected {expected}, got {got})"
            ),
            Self::InvalidGamma { got, max } => write!(
                f,
                "eagle3 draft: gamma must be 1..={max}, got {got}"
            ),
        }
    }
}

impl std::error::Error for Eagle3DraftError {}

/// Fusion hidden states captured at one decode step (low / mid / high).
#[derive(Clone, Debug, PartialEq)]
pub struct Eagle3LayerHiddens {
    pub low: Vec<f32>,
    pub mid: Vec<f32>,
    pub high: Vec<f32>,
}

impl Eagle3LayerHiddens {
    /// Hidden width when low/mid/high agree and are non-empty.
    pub fn hidden_dim(&self) -> Option<usize> {
        let d = self.low.len();
        if d == 0 || self.mid.len() != d || self.high.len() != d {
            None
        } else {
            Some(d)
        }
    }

    /// FC fusion input length: `concat(h_low, h_mid, h_high)` → `3 * h`.
    pub fn fusion_input_len(&self) -> Result<usize, Eagle3DraftError> {
        let h = self.validate_fusion()?;
        Ok(h * TARGET_LAYER_COUNT)
    }

    /// Fail-closed: all three slices must share the same non-zero width.
    pub fn validate_fusion(&self) -> Result<usize, Eagle3DraftError> {
        let d = self.low.len();
        if d == 0 {
            return Err(Eagle3DraftError::EmptyHiddens);
        }
        if self.mid.len() != d {
            return Err(Eagle3DraftError::HiddenDimMismatch {
                expected: d,
                got: self.mid.len(),
            });
        }
        if self.high.len() != d {
            return Err(Eagle3DraftError::HiddenDimMismatch {
                expected: d,
                got: self.high.len(),
            });
        }
        Ok(d)
    }
}

/// Hidden-export hook errors (fail-closed).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Eagle3HiddenError {
    WrongHiddenDim { expected: usize, got: usize },
    DuplicateLayer { layer_idx: u32 },
    Incomplete,
}

impl fmt::Display for Eagle3HiddenError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::WrongHiddenDim { expected, got } => write!(
                f,
                "eagle3 hidden capture: hidden dim mismatch (expected {expected}, got {got})"
            ),
            Self::DuplicateLayer { layer_idx } => write!(
                f,
                "eagle3 hidden capture: duplicate capture at layer {layer_idx}"
            ),
            Self::Incomplete => write!(
                f,
                "eagle3 hidden capture: incomplete (expected {TARGET_LAYER_COUNT} layer hiddens)"
            ),
        }
    }
}

impl std::error::Error for Eagle3HiddenError {}

/// Hook invoked after each transformer layer during target decode.
pub trait Eagle3DecodeHiddenHook {
    fn on_layer_hidden(
        &mut self,
        layer_idx: u32,
        hidden: &[f32],
    ) -> Result<(), Eagle3HiddenError>;

    fn finish(self) -> Result<Eagle3LayerHiddens, Eagle3HiddenError>;
}

/// Captures three target-layer hiddens per [`Eagle3SidecarMeta::target_layers`].
#[derive(Clone, Debug)]
pub struct Eagle3HiddenCapture {
    targets: [u32; TARGET_LAYER_COUNT],
    hidden_dim: Option<usize>,
    slots: [Option<Vec<f32>>; TARGET_LAYER_COUNT],
}

impl Eagle3HiddenCapture {
    pub fn new(meta: &Eagle3SidecarMeta) -> Self {
        Self::from_targets(meta.target_layers)
    }

    pub fn from_targets(target_layers: [u32; TARGET_LAYER_COUNT]) -> Self {
        Self {
            targets: target_layers,
            hidden_dim: None,
            slots: [None, None, None],
        }
    }

    fn slot_for_layer(&self, layer_idx: u32) -> Option<usize> {
        self.targets
            .iter()
            .position(|&t| t == layer_idx)
    }
}

impl Eagle3DecodeHiddenHook for Eagle3HiddenCapture {
    fn on_layer_hidden(
        &mut self,
        layer_idx: u32,
        hidden: &[f32],
    ) -> Result<(), Eagle3HiddenError> {
        let Some(slot) = self.slot_for_layer(layer_idx) else {
            return Ok(());
        };
        if self.slots[slot].is_some() {
            return Err(Eagle3HiddenError::DuplicateLayer { layer_idx });
        }
        if let Some(dim) = self.hidden_dim {
            if hidden.len() != dim {
                return Err(Eagle3HiddenError::WrongHiddenDim {
                    expected: dim,
                    got: hidden.len(),
                });
            }
        } else {
            self.hidden_dim = Some(hidden.len());
        }
        self.slots[slot] = Some(hidden.to_vec());
        Ok(())
    }

    fn finish(self) -> Result<Eagle3LayerHiddens, Eagle3HiddenError> {
        let [Some(low), Some(mid), Some(high)] = self.slots else {
            return Err(Eagle3HiddenError::Incomplete);
        };
        Ok(Eagle3LayerHiddens { low, mid, high })
    }
}

/// Parse and validate EAGLE-3 sidecar metadata from an on-disk GGUF file.
pub fn parse_eagle3_sidecar(path: &Path) -> Result<Eagle3SidecarMeta, Eagle3LoadError> {
    let gguf = read_gguf(path, false)?;
    parse_eagle3_from_gguf(&gguf)
}

/// Parse and validate EAGLE-3 metadata from an already-read GGUF header.
pub fn parse_eagle3_from_gguf(gguf: &GgufFile) -> Result<Eagle3SidecarMeta, Eagle3LoadError> {
    let arch = gguf
        .get("general.architecture")
        .and_then(|v| v.as_str())
        .ok_or(Eagle3LoadError::MissingKey("general.architecture"))?;
    if arch != EAGLE3_ARCH {
        return Err(Eagle3LoadError::WrongArchitecture {
            got: arch.to_string(),
        });
    }

    let layers = gguf
        .get(TARGET_LAYERS_KEY)
        .ok_or(Eagle3LoadError::MissingKey(TARGET_LAYERS_KEY))?;
    let layer_vec = layers
        .as_i32_array()
        .or_else(|| {
            layers
                .as_u32_array()
                .map(|v| v.into_iter().map(|x| x as i32).collect())
        })
        .ok_or(Eagle3LoadError::MissingKey(TARGET_LAYERS_KEY))?;
    if layer_vec.len() != TARGET_LAYER_COUNT {
        return Err(Eagle3LoadError::InvalidTargetLayers {
            got: layer_vec.len(),
        });
    }
    let target_layers = [layer_vec[0] as u32, layer_vec[1] as u32, layer_vec[2] as u32];

    let d2t = gguf.tensor(D2T_TENSOR).ok_or(Eagle3LoadError::MissingD2t)?;
    if d2t.ggml_type != GGML_TYPE_I64 {
        return Err(Eagle3LoadError::InvalidD2tType {
            ggml_type: d2t.ggml_type,
        });
    }
    if d2t.shape.len() != 1 || d2t.shape[0] == 0 {
        return Err(Eagle3LoadError::InvalidD2tShape {
            shape: d2t.shape.clone(),
        });
    }

    Ok(Eagle3SidecarMeta {
        target_layers,
        draft_vocab_size: d2t.shape[0],
    })
}

impl Eagle3Sidecar {
    /// Load sidecar from path (metadata validation only).
    pub fn load(path: &Path) -> Result<Self, Eagle3LoadError> {
        let meta = parse_eagle3_sidecar(path)?;
        Ok(Self { meta })
    }

    pub fn meta(&self) -> &Eagle3SidecarMeta {
        &self.meta
    }

    /// Build a drafter with default γ ([`DEFAULT_GAMMA`]).
    pub fn drafter(&self) -> Eagle3Drafter {
        Eagle3Drafter::new(self.clone(), DEFAULT_GAMMA)
    }

    /// Build a drafter with explicit γ (clamped to `1..=[`MAX_GAMMA`]).
    pub fn drafter_with_gamma(&self, gamma: usize) -> Eagle3Drafter {
        Eagle3Drafter::new(self.clone(), gamma)
    }
}

/// Step-3 draft path: validates captured hiddens, then defers FC + 1-layer decode.
#[derive(Clone, Debug)]
pub struct Eagle3Drafter {
    sidecar: Eagle3Sidecar,
    gamma: usize,
}

impl Eagle3Drafter {
    pub fn new(sidecar: Eagle3Sidecar, gamma: usize) -> Self {
        Self {
            sidecar,
            gamma: gamma.clamp(1, MAX_GAMMA),
        }
    }

    pub fn gamma(&self) -> usize {
        self.gamma
    }

    pub fn sidecar(&self) -> &Eagle3Sidecar {
        &self.sidecar
    }

    fn validate_gamma(gamma: usize) -> Result<(), Eagle3DraftError> {
        if (1..=MAX_GAMMA).contains(&gamma) {
            Ok(())
        } else {
            Err(Eagle3DraftError::InvalidGamma {
                got: gamma,
                max: MAX_GAMMA,
            })
        }
    }

    /// Draft up to γ target tokens from fusion hiddens + last committed target token.
    ///
    /// Validates inputs for the R21 graph (`fc(concat)` → 1-layer AR → `d2t` scatter).
    /// Weights and matmul are step 4+; this stub fails closed with [`Eagle3DraftError::NotImplemented`].
    pub fn draft(
        &self,
        hiddens: &Eagle3LayerHiddens,
        last_target_token: u32,
    ) -> Result<Vec<u32>, Eagle3DraftError> {
        Self::validate_gamma(self.gamma)?;
        let _hidden_dim = hiddens.validate_fusion()?;
        let _fusion_len = hiddens.fusion_input_len()?;
        let _draft_vocab = self.sidecar.meta.draft_vocab_size;
        let _ = last_target_token;
        // Step 4: g = fc(concat); x = concat(g, token_embd[draft_tok]); blk.0 AR × γ; d2t map.
        Err(Eagle3DraftError::NotImplemented)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gguf_io::{write_test_gguf, TestGgufTensor, TestKv};
    use tempfile::TempDir;

    fn write_minimal_eagle3(path: &Path, layer_kv: TestKv, draft_vocab: u64) {
        write_test_gguf(
            path,
            &[
                ("general.architecture", TestKv::String(EAGLE3_ARCH)),
                (TARGET_LAYERS_KEY, layer_kv),
            ],
            &[TestGgufTensor {
                name: D2T_TENSOR,
                shape: &[draft_vocab],
                ggml_type: GGML_TYPE_I64,
                f32_data: None,
            }],
        )
        .unwrap();
    }

    #[test]
    fn parses_minimal_eagle3_metadata() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("draft-eagle3.gguf");
        write_minimal_eagle3(&path, TestKv::I32Array(&[2, 16, 29]), 4);

        let meta = parse_eagle3_sidecar(&path).unwrap();
        assert_eq!(meta.target_layers, [2, 16, 29]);
        assert_eq!(meta.draft_vocab_size, 4);

        let sidecar = Eagle3Sidecar::load(&path).unwrap();
        assert_eq!(sidecar.meta(), &meta);
        let drafter = sidecar.drafter();
        assert_eq!(drafter.gamma(), DEFAULT_GAMMA);
    }

    #[test]
    fn rejects_wrong_architecture() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("bad.gguf");
        write_test_gguf(
            &path,
            &[
                ("general.architecture", TestKv::String("llama")),
                (TARGET_LAYERS_KEY, TestKv::I32Array(&[1, 2, 3])),
            ],
            &[TestGgufTensor {
                name: D2T_TENSOR,
                shape: &[4u64],
                ggml_type: GGML_TYPE_I64,
                f32_data: None,
            }],
        )
        .unwrap();

        match parse_eagle3_sidecar(&path).unwrap_err() {
            Eagle3LoadError::WrongArchitecture { got } => assert_eq!(got, "llama"),
            e => panic!("expected WrongArchitecture, got {e:?}"),
        }
    }

    #[test]
    fn rejects_bad_target_layers() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("bad-layers.gguf");
        write_test_gguf(
            &path,
            &[
                ("general.architecture", TestKv::String(EAGLE3_ARCH)),
                (TARGET_LAYERS_KEY, TestKv::I32Array(&[2, 16])),
            ],
            &[TestGgufTensor {
                name: D2T_TENSOR,
                shape: &[4u64],
                ggml_type: GGML_TYPE_I64,
                f32_data: None,
            }],
        )
        .unwrap();

        match parse_eagle3_sidecar(&path).unwrap_err() {
            Eagle3LoadError::InvalidTargetLayers { got } => assert_eq!(got, 2),
            e => panic!("expected InvalidTargetLayers, got {e:?}"),
        }
    }

    #[test]
    fn rejects_missing_d2t() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("no-d2t.gguf");
        write_test_gguf(
            &path,
            &[
                ("general.architecture", TestKv::String(EAGLE3_ARCH)),
                (TARGET_LAYERS_KEY, TestKv::I32Array(&[2, 16, 29])),
            ],
            &[],
        )
        .unwrap();

        assert!(matches!(
            parse_eagle3_sidecar(&path).unwrap_err(),
            Eagle3LoadError::MissingD2t
        ));
    }

    #[test]
    fn rejects_d2t_wrong_type() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("d2t-f32.gguf");
        write_test_gguf(
            &path,
            &[
                ("general.architecture", TestKv::String(EAGLE3_ARCH)),
                (TARGET_LAYERS_KEY, TestKv::I32Array(&[2, 16, 29])),
            ],
            &[TestGgufTensor {
                name: D2T_TENSOR,
                shape: &[4u64],
                ggml_type: 0,
                f32_data: Some(&[0.0, 1.0, 2.0, 3.0]),
            }],
        )
        .unwrap();

        match parse_eagle3_sidecar(&path).unwrap_err() {
            Eagle3LoadError::InvalidD2tType { ggml_type } => assert_eq!(ggml_type, 0),
            e => panic!("expected InvalidD2tType, got {e:?}"),
        }
    }

    /// Mock decode: walk layers 0..n_layers and feed distinguishable hiddens into the hook.
    fn mock_decode_layers(
        hook: &mut dyn Eagle3DecodeHiddenHook,
        n_layers: usize,
        hidden_dim: usize,
    ) {
        for layer in 0..n_layers {
            let hidden: Vec<f32> = (0..hidden_dim)
                .map(|d| (layer * 1000 + d) as f32)
                .collect();
            hook.on_layer_hidden(layer as u32, &hidden).unwrap();
        }
    }

    #[test]
    fn captures_three_target_layer_hiddens_during_mock_decode() {
        let targets = [2u32, 16, 29];
        let mut capture = Eagle3HiddenCapture::from_targets(targets);
        mock_decode_layers(&mut capture, 32, 4);

        let got = capture.finish().unwrap();
        assert_eq!(got.low, vec![2000.0, 2001.0, 2002.0, 2003.0]);
        assert_eq!(got.mid, vec![16000.0, 16001.0, 16002.0, 16003.0]);
        assert_eq!(got.high, vec![29000.0, 29001.0, 29002.0, 29003.0]);
    }

    #[test]
    fn hidden_capture_uses_sidecar_meta_targets() {
        let meta = Eagle3SidecarMeta {
            target_layers: [1, 5, 9],
            draft_vocab_size: 8,
        };
        let mut capture = Eagle3HiddenCapture::new(&meta);
        mock_decode_layers(&mut capture, 12, 2);

        let got = capture.finish().unwrap();
        assert_eq!(got.low, vec![1000.0, 1001.0]);
        assert_eq!(got.mid, vec![5000.0, 5001.0]);
        assert_eq!(got.high, vec![9000.0, 9001.0]);
    }

    #[test]
    fn hidden_capture_fail_closed_incomplete() {
        let mut capture = Eagle3HiddenCapture::from_targets([2, 16, 29]);
        mock_decode_layers(&mut capture, 10, 4);
        assert_eq!(capture.finish().unwrap_err(), Eagle3HiddenError::Incomplete);
    }

    #[test]
    fn hidden_capture_fail_closed_wrong_dim() {
        let mut capture = Eagle3HiddenCapture::from_targets([2, 16, 29]);
        capture.on_layer_hidden(2, &[1.0, 2.0, 3.0]).unwrap();
        assert_eq!(
            capture.on_layer_hidden(16, &[4.0, 5.0]).unwrap_err(),
            Eagle3HiddenError::WrongHiddenDim {
                expected: 3,
                got: 2
            }
        );
    }

    #[test]
    fn hidden_capture_fail_closed_duplicate_layer() {
        let mut capture = Eagle3HiddenCapture::from_targets([2, 16, 29]);
        capture.on_layer_hidden(2, &[1.0, 2.0]).unwrap();
        assert_eq!(
            capture.on_layer_hidden(2, &[1.0, 2.0]).unwrap_err(),
            Eagle3HiddenError::DuplicateLayer { layer_idx: 2 }
        );
    }

    fn sample_hiddens(dim: usize) -> Eagle3LayerHiddens {
        Eagle3LayerHiddens {
            low: vec![1.0; dim],
            mid: vec![2.0; dim],
            high: vec![3.0; dim],
        }
    }

    #[test]
    fn layer_hiddens_fusion_input_len() {
        let h = sample_hiddens(4);
        assert_eq!(h.hidden_dim(), Some(4));
        assert_eq!(h.fusion_input_len().unwrap(), 12);
    }

    #[test]
    fn draft_stub_validates_then_not_implemented() {
        let meta = Eagle3SidecarMeta {
            target_layers: [2, 16, 29],
            draft_vocab_size: 32000,
        };
        let drafter = Eagle3Sidecar { meta }.drafter_with_gamma(4);
        assert_eq!(
            drafter.draft(&sample_hiddens(8), 42).unwrap_err(),
            Eagle3DraftError::NotImplemented
        );
    }

    #[test]
    fn draft_rejects_empty_hiddens() {
        let meta = Eagle3SidecarMeta {
            target_layers: [2, 16, 29],
            draft_vocab_size: 4,
        };
        let drafter = Eagle3Sidecar { meta }.drafter();
        let empty = Eagle3LayerHiddens {
            low: vec![],
            mid: vec![],
            high: vec![],
        };
        assert_eq!(
            drafter.draft(&empty, 0).unwrap_err(),
            Eagle3DraftError::EmptyHiddens
        );
    }

    #[test]
    fn draft_rejects_mismatched_hidden_dims() {
        let meta = Eagle3SidecarMeta {
            target_layers: [2, 16, 29],
            draft_vocab_size: 4,
        };
        let drafter = Eagle3Sidecar { meta }.drafter();
        let bad = Eagle3LayerHiddens {
            low: vec![1.0, 2.0],
            mid: vec![1.0],
            high: vec![1.0, 2.0],
        };
        assert_eq!(
            drafter.draft(&bad, 0).unwrap_err(),
            Eagle3DraftError::HiddenDimMismatch { expected: 2, got: 1 }
        );
    }

    #[test]
    fn capture_to_draft_path_end_to_end_stub() {
        let meta = Eagle3SidecarMeta {
            target_layers: [2, 16, 29],
            draft_vocab_size: 8,
        };
        let mut capture = Eagle3HiddenCapture::new(&meta);
        mock_decode_layers(&mut capture, 32, 4);
        let hiddens = capture.finish().unwrap();
        let drafter = Eagle3Sidecar { meta }.drafter_with_gamma(6);
        assert_eq!(drafter.gamma(), 6);
        assert_eq!(
            drafter.draft(&hiddens, 100).unwrap_err(),
            Eagle3DraftError::NotImplemented
        );
    }
}
