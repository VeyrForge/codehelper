use std::collections::HashMap;
use std::path::{Path, PathBuf};

use crate::error::{fail, Result};
use crate::infer::load_layer_runtime;
use crate::types::{LayerRuntime, Matrix};

#[derive(Clone, Debug)]
pub struct ExpertEntry {
    pub id: String,
    pub path: PathBuf,
    pub bytes: u64,
}

/// LRU cache of decompressed layer runtimes keyed by expert id.
pub struct ExpertLruCache {
    pub budget_bytes: u64,
    pub entries: HashMap<String, CachedExpert>,
    pub order: Vec<String>,
    pub hits: u64,
    pub misses: u64,
}

struct CachedExpert {
    runtime: LayerRuntime,
    bytes: u64,
}

impl ExpertLruCache {
    pub fn new(budget_bytes: u64) -> Self {
        Self {
            budget_bytes,
            entries: HashMap::new(),
            order: Vec::new(),
            hits: 0,
            misses: 0,
        }
    }

    fn current_bytes(&self) -> u64 {
        self.entries.values().map(|e| e.bytes).sum()
    }

    fn touch(&mut self, id: &str) {
        self.order.retain(|k| k != id);
        self.order.push(id.to_string());
    }

    fn evict(&mut self, protect: Option<&str>) {
        while self.current_bytes() > self.budget_bytes {
            let Some(victim) = self
                .order
                .iter()
                .find(|id| protect.map(|p| *id != p).unwrap_or(true))
                .cloned()
            else {
                break;
            };
            if let Some(removed) = self.entries.remove(&victim) {
                self.order.retain(|k| k != &victim);
                let _ = removed;
            } else {
                break;
            }
        }
    }

    pub fn get_or_load(&mut self, entry: &ExpertEntry) -> Result<&LayerRuntime> {
        if self.entries.contains_key(&entry.id) {
            self.hits += 1;
            let id = entry.id.clone();
            self.touch(&id);
            return Ok(&self.entries.get(&id).expect("expert").runtime);
        }
        self.misses += 1;
        let runtime = load_layer_runtime(&entry.path)?;
        let bytes = entry.bytes.max(expert_dir_bytes(&entry.path)).max(1024);
        let id = entry.id.clone();
        self.entries.insert(
            id.clone(),
            CachedExpert {
                runtime,
                bytes,
            },
        );
        self.touch(&id);
        self.evict(Some(&id));
        self.entries
            .get(&id)
            .map(|e| &e.runtime)
            .ok_or_else(|| fail(format!("expert evicted under cache budget: {id}")))
    }

    pub fn infer_expert(
        &mut self,
        entry: &ExpertEntry,
        activations: &Matrix,
    ) -> Result<Matrix> {
        let rt = self.get_or_load(entry)?;
        crate::infer::infer_layer_runtime(rt, activations)
    }
}

pub fn expert_dir_bytes(dir: &Path) -> u64 {
    let mut total = 0u64;
    if let Ok(rd) = std::fs::read_dir(dir) {
        for e in rd.flatten() {
            if let Ok(meta) = e.metadata() {
                if meta.is_file() {
                    total += meta.len();
                }
            }
        }
    }
    total
}

pub fn load_expert_manifest(path: &Path) -> Result<Vec<ExpertEntry>> {
    let raw = std::fs::read_to_string(path).map_err(|e| fail(e.to_string()))?;
    let doc: serde_json::Value =
        serde_json::from_str(&raw).map_err(|e| fail(format!("expert manifest json: {e}")))?;
    let mut out = Vec::new();
    let items = doc
        .get("experts")
        .and_then(|v| v.as_array())
        .ok_or_else(|| fail("experts[] missing"))?;
    for item in items {
        let id = item
            .get("id")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let path = item
            .get("path")
            .and_then(|v| v.as_str())
            .ok_or_else(|| fail("expert path missing"))?;
        let p = PathBuf::from(path);
        let bytes = item
            .get("bytes")
            .and_then(|v| v.as_u64())
            .unwrap_or_else(|| expert_dir_bytes(&p));
        out.push(ExpertEntry { id, path: p, bytes });
    }
    Ok(out)
}
