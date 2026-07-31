//! Token embedding lookup (CPU f32) for a native forward slice.
//!
//! Accepts dense f32 or mmap'd quant `token_embd.weight`. Quant tables dequant one row per lookup
//! so unused vocab pages stay unfaulted (critical for 8B RSS).

use std::path::Path;

use crate::gguf_io::read_gguf;
use crate::gguf_load::LeanTensor;
use crate::quant_mat::QuantMat;

/// Errors from embedding construction / lookup.
#[derive(Debug)]
pub enum EmbedError {
    Io(std::io::Error),
    Shape { expected: usize, got: usize },
    MissingTensor(String),
    NotF32(String),
    BadToken { id: u32, vocab: usize },
    Message(String),
}

impl std::fmt::Display for EmbedError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            EmbedError::Io(e) => write!(f, "{e}"),
            EmbedError::Shape { expected, got } => {
                write!(f, "embedding weight length {got}, expected {expected}")
            }
            EmbedError::MissingTensor(n) => write!(f, "missing tensor {n}"),
            EmbedError::NotF32(n) => write!(f, "tensor {n} is not F32 (dequant first)"),
            EmbedError::BadToken { id, vocab } => {
                write!(f, "token id {id} out of range (vocab={vocab})")
            }
            EmbedError::Message(m) => write!(f, "{m}"),
        }
    }
}

impl std::error::Error for EmbedError {}

impl From<std::io::Error> for EmbedError {
    fn from(value: std::io::Error) -> Self {
        EmbedError::Io(value)
    }
}

/// Backing store: owned f32 (tiny / tests) or mmap'd quant rows (production).
#[derive(Clone, Debug)]
enum EmbStore {
    Dense(Vec<f32>),
    Quant(QuantMat),
}

/// Embedding table: row `i` is token `i`, length [`EmbeddingTable::n_embd`].
#[derive(Clone, Debug)]
pub struct EmbeddingTable {
    pub n_vocab: usize,
    pub n_embd: usize,
    store: EmbStore,
}

impl EmbeddingTable {
    pub fn from_rows(n_vocab: usize, n_embd: usize, weight: Vec<f32>) -> Result<Self, EmbedError> {
        let expected = n_vocab.saturating_mul(n_embd);
        if weight.len() != expected {
            return Err(EmbedError::Shape {
                expected,
                got: weight.len(),
            });
        }
        Ok(EmbeddingTable {
            n_vocab,
            n_embd,
            store: EmbStore::Dense(weight),
        })
    }

    /// Prefer mmap'd quant rows — no full-table f32 materialize (saves ~2 GiB on 8B).
    pub fn from_lean(t: &LeanTensor, n_embd: usize) -> Result<Self, EmbedError> {
        let shape = t.shape();
        if shape.len() != 2 {
            return Err(EmbedError::Message(format!(
                "embd shape must be 2D, got {shape:?}"
            )));
        }
        let d0 = shape[0];
        let d1 = shape[1];
        match t {
            LeanTensor::F32 { data, .. } => {
                // GGUF/ggml: ne0 innermost. token_embd is typically [n_embd, n_vocab].
                if d0 == n_embd {
                    Self::from_rows(d1, n_embd, data.clone())
                } else if d1 == n_embd {
                    Self::from_rows(d0, n_embd, data.clone())
                } else {
                    Err(EmbedError::Message(format!(
                        "embd {shape:?} does not match n_embd {n_embd}"
                    )))
                }
            }
            LeanTensor::Packed { .. } => {
                let mut m = t
                    .to_quant_mat()
                    .map_err(|e| EmbedError::Message(e.to_string()))?;
                // Payload is row-major (out=vocab, in=embd). Fix swapped labels only.
                if m.in_dim != n_embd && m.out_dim == n_embd {
                    std::mem::swap(&mut m.in_dim, &mut m.out_dim);
                }
                if m.in_dim != n_embd {
                    return Err(EmbedError::Message(format!(
                        "embd quant in_dim={} != n_embd {n_embd} (shape {shape:?})",
                        m.in_dim
                    )));
                }
                Ok(EmbeddingTable {
                    n_vocab: m.out_dim,
                    n_embd,
                    store: EmbStore::Quant(m),
                })
            }
        }
    }

    /// True when rows are mmap'd quant (unused vocab pages stay unfaulted).
    #[inline]
    pub fn is_mmap_quant(&self) -> bool {
        matches!(self.store, EmbStore::Quant(_))
    }

    /// Dense f32 slice when available (tied lm_head fallback / tests). `None` for quant store.
    pub fn as_dense_weight(&self) -> Option<&[f32]> {
        match &self.store {
            EmbStore::Dense(w) => Some(w.as_slice()),
            EmbStore::Quant(_) => None,
        }
    }

    /// Load F32 `token_embd.weight` (or `name`) from a GGUF — typically `dense.gguf`.
    ///
    /// GGUF stores dims as `[n_embd, n_vocab]` or `[n_vocab, n_embd]` depending on writer;
    /// we accept either when `shape.len() == 2` and `product == data.len()`.
    pub fn load_f32_gguf(path: &Path, name: &str) -> Result<Self, EmbedError> {
        let g = read_gguf(path, true)?;
        let t = g
            .tensor(name)
            .ok_or_else(|| EmbedError::MissingTensor(name.into()))?;
        let data = t
            .f32_data
            .as_ref()
            .ok_or_else(|| EmbedError::NotF32(name.into()))?;
        if t.shape.len() != 2 {
            return Err(EmbedError::Message(format!(
                "embedding tensor {name} must be 2D, got shape {:?}",
                t.shape
            )));
        }
        let d0 = t.shape[0] as usize;
        let d1 = t.shape[1] as usize;
        // llama.cpp often publishes [n_embd, n_vocab]; our tables are row-per-token [vocab, embd].
        // Heuristic: when dim0 < dim1, treat as [embd, vocab] and transpose.
        let (n_vocab, n_embd, weight) = if d0 < d1 {
            let n_embd = d0;
            let n_vocab = d1;
            let mut weight = vec![0.0f32; n_vocab * n_embd];
            for e in 0..n_embd {
                for tok in 0..n_vocab {
                    weight[tok * n_embd + e] = data[e * n_vocab + tok];
                }
            }
            (n_vocab, n_embd, weight)
        } else {
            (d0, d1, data.clone())
        };
        Self::from_rows(n_vocab, n_embd, weight)
    }

    /// Build from a dense F32 byte slice + manifest shape `[d0, d1]` (same heuristics as GGUF load).
    ///
    /// Hook for [`crate::GreenModel::tensor_bytes`] when `ggml_type` / source is F32.
    pub fn from_f32_bytes(shape: &[u32], bytes: &[u8]) -> Result<Self, EmbedError> {
        if shape.len() != 2 {
            return Err(EmbedError::Message(format!(
                "embedding shape must be 2D, got {shape:?}"
            )));
        }
        if bytes.len() % 4 != 0 {
            return Err(EmbedError::Message(format!(
                "F32 embedding bytes length {} not divisible by 4",
                bytes.len()
            )));
        }
        let data: Vec<f32> = bytes
            .chunks_exact(4)
            .map(|c| f32::from_le_bytes([c[0], c[1], c[2], c[3]]))
            .collect();
        let d0 = shape[0] as usize;
        let d1 = shape[1] as usize;
        if d0.saturating_mul(d1) != data.len() {
            return Err(EmbedError::Shape {
                expected: d0.saturating_mul(d1),
                got: data.len(),
            });
        }
        if d0 < d1 {
            let n_embd = d0;
            let n_vocab = d1;
            let mut weight = vec![0.0f32; n_vocab * n_embd];
            for e in 0..n_embd {
                for tok in 0..n_vocab {
                    weight[tok * n_embd + e] = data[e * n_vocab + tok];
                }
            }
            Self::from_rows(n_vocab, n_embd, weight)
        } else {
            Self::from_rows(d0, d1, data)
        }
    }

    /// Write token `id` row into `out` (len = n_embd). Quant path dequants one row only.
    pub fn write_row(&self, id: u32, out: &mut [f32]) -> Result<(), EmbedError> {
        let i = id as usize;
        if i >= self.n_vocab {
            return Err(EmbedError::BadToken {
                id,
                vocab: self.n_vocab,
            });
        }
        if out.len() != self.n_embd {
            return Err(EmbedError::Shape {
                expected: self.n_embd,
                got: out.len(),
            });
        }
        match &self.store {
            EmbStore::Dense(w) => {
                let o = i * self.n_embd;
                out.copy_from_slice(&w[o..o + self.n_embd]);
                Ok(())
            }
            EmbStore::Quant(m) => m
                .dequant_row(i, out)
                .map_err(|e| EmbedError::Message(e.to_string())),
        }
    }

    /// Gather rows for `ids` into `out` (`ids.len() * n_embd`), or allocate.
    pub fn lookup(&self, ids: &[u32], out: &mut [f32]) -> Result<(), EmbedError> {
        let need = ids.len().saturating_mul(self.n_embd);
        if out.len() != need {
            return Err(EmbedError::Shape {
                expected: need,
                got: out.len(),
            });
        }
        for (t, &id) in ids.iter().enumerate() {
            let dst = &mut out[t * self.n_embd..(t + 1) * self.n_embd];
            self.write_row(id, dst)?;
        }
        Ok(())
    }

    pub fn lookup_vec(&self, ids: &[u32]) -> Result<Vec<f32>, EmbedError> {
        let mut out = vec![0.0; ids.len() * self.n_embd];
        self.lookup(ids, &mut out)?;
        Ok(out)
    }

    /// Dense-only row borrow (tests / tiny tables). Prefer [`Self::write_row`] for quant.
    pub fn row(&self, id: u32) -> Result<&[f32], EmbedError> {
        let i = id as usize;
        if i >= self.n_vocab {
            return Err(EmbedError::BadToken {
                id,
                vocab: self.n_vocab,
            });
        }
        match &self.store {
            EmbStore::Dense(w) => {
                let o = i * self.n_embd;
                Ok(&w[o..o + self.n_embd])
            }
            EmbStore::Quant(_) => Err(EmbedError::Message(
                "row() unavailable for mmap quant emb; use write_row".into(),
            )),
        }
    }
}

/// Loader hook: build table from an already-dequantized f32 buffer.
pub fn table_from_dequant(
    n_vocab: usize,
    n_embd: usize,
    weight: Vec<f32>,
) -> Result<EmbeddingTable, EmbedError> {
    EmbeddingTable::from_rows(n_vocab, n_embd, weight)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gguf_io::{write_test_gguf, TestGgufTensor, TestKv};
    use tempfile::TempDir;

    #[test]
    fn lookup_copies_rows() {
        // vocab=3, embd=2
        let emb = EmbeddingTable::from_rows(
            3,
            2,
            vec![
                1.0, 2.0, // 0
                3.0, 4.0, // 1
                5.0, 6.0, // 2
            ],
        )
        .unwrap();
        let out = emb.lookup_vec(&[2, 0, 1]).unwrap();
        assert_eq!(out, vec![5.0, 6.0, 1.0, 2.0, 3.0, 4.0]);
    }

    #[test]
    fn load_f32_gguf_vocab_major() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("dense.gguf");
        // shape [vocab=4, embd=2]
        let data = [
            0.0, 1.0, // tok0
            2.0, 3.0, // tok1
            4.0, 5.0, // tok2
            6.0, 7.0, // tok3
        ];
        write_test_gguf(
            &path,
            &[("general.architecture", TestKv::String("llama"))],
            &[TestGgufTensor {
                name: "token_embd.weight",
                shape: &[4u64, 2],
                ggml_type: 0,
                f32_data: Some(&data),
            }],
        )
        .unwrap();
        let emb = EmbeddingTable::load_f32_gguf(&path, "token_embd.weight").unwrap();
        assert_eq!(emb.n_vocab, 4);
        assert_eq!(emb.n_embd, 2);
        assert_eq!(emb.row(1).unwrap(), &[2.0, 3.0]);
    }

    #[test]
    fn load_f32_gguf_embd_major_transposes() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("dense.gguf");
        // shape [embd=2, vocab=3]: rows are emb channels
        // data layout: e0 across toks, then e1 across toks
        let data = [
            10.0, 20.0, 30.0, // emb0 for tok0..2
            11.0, 21.0, 31.0, // emb1
        ];
        write_test_gguf(
            &path,
            &[],
            &[TestGgufTensor {
                name: "token_embd.weight",
                shape: &[2u64, 3],
                ggml_type: 0,
                f32_data: Some(&data),
            }],
        )
        .unwrap();
        let emb = EmbeddingTable::load_f32_gguf(&path, "token_embd.weight").unwrap();
        assert_eq!(emb.n_vocab, 3);
        assert_eq!(emb.n_embd, 2);
        assert_eq!(emb.row(0).unwrap(), &[10.0, 11.0]);
        assert_eq!(emb.row(2).unwrap(), &[30.0, 31.0]);
    }
}
