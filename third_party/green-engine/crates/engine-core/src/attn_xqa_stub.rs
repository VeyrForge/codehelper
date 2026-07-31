//! R19 step 2 — TRT-XQA / FA2 JIT GQA decode attn CPU stub.
//!
//! **Scope (spike only):** pure-Rust, no CUDA, no large GEMM.  Models the
//! paged-KV table walk + per-head online (flash-style) softmax that the GPU
//! path will eventually adopt, using the TRT-LLM / FlashInfer data-structure
//! contract (NHD layout, page_size ∈ {16, 32}).
//!
//! **Not** a drop-in replacement for `attention::multi_head_attend_decode_*`.
//! Use as a doc-level contract proof and unit-test gate before touching the `.cu`.
//!
//! ## Layout contract (mirrors TRT-LLM XQA / FI FA2 paged KV)
//!
//! ```text
//! PagedKvTable::k_pages / v_pages:
//!   [page_idx][page_token][kv_head][dim_elem]
//!   = flat[page_idx * page_size * kv_dim + tok * kv_dim + kv_head * head_dim + d]
//! ```
//!
//! `page_table[seq_pos / page_size]` → physical page index.
//! Sequence position within page: `seq_pos % page_size`.
//!
//! ## Computation (per Q-head, online softmax)
//!
//! For decode (q_len = 1) one Q-head attends over seq KV tokens:
//!
//! ```text
//! m = -∞,  l = 0,  acc[d] = 0
//! for each KV token t:
//!   score = dot(q_h, k_h[t]) * scale
//!   m_new = max(m, score)
//!   alpha  = exp(m - m_new)
//!   l      = alpha * l + exp(score - m_new)
//!   acc    = alpha * acc + exp(score - m_new) * v_h[t]
//!   m      = m_new
//! out[d] = acc[d] / l
//! ```
//!
//! No O(seq) score array ← this is the key structural difference vs Green's
//! current `gqa_attn_decode_kernel` (scores in shared mem, hard cap ~4k).
//!
//! ## R10 gates this stub satisfies
//!
//! | Gate | Status |
//! |------|--------|
//! | Paged KV (16 / 32) + NHD layout | ✓ stub impl |
//! | Online softmax (no O(seq) smem) | ✓ stub impl |
//! | GQA 32q / 8kv, head_dim=128 | ✓ unit test |
//! | Bit-near vs `softmax_inplace` path | ✓ parity test ≤ 1e-5 |
//! | No CUDA / no large GEMM | ✓ CPU-only |
//! | Long-ctx capable (page-walk, no score cap) | ✓ no fixed array |
//!
//! ## GPU port (R19 spike — ISO, util-light)
//!
//! `gqa_attn_decode_kernel` in `decode_q4_gemv.cu` now mirrors this online-softmax loop
//! (tiled TILE=64, smem = head_dim + TILE floats). Q8 paths still use O(seq) score smem.
//! **KEEP** if `attn_xqa_stub_smoke` 8/8 + Hello parity pass; else **REVERT** `.cu` launch+kernel.
//!
//! ## Out of scope (future)
//!
//! - TC GQA (tensor-core cooperative warp tiles) — Hard, R10 #3.
//! - FP8 / INT8 KV dequant inside the kernel — Med, R10 #4.
//! - Multi-block reduction for long-ctx — Med, extends this online softmax pattern.
//! - CC dispatch (SM89 → this path; SM90+ → FI XQA) — Easy, R10 #1; env var guard.

/// GQA configuration for the stub (Llama-3.1-8B decode defaults).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct GqaConfig {
    /// Total Q-heads (32 for Llama-3.1-8B).
    pub n_heads: usize,
    /// KV-heads (8 for Llama-3.1-8B GQA).
    pub n_kv_heads: usize,
    /// Elements per head (128 for Llama-3.1-8B).
    pub head_dim: usize,
    /// Tokens per KV page; must be 16 or 32 (TRT/FI supported sizes).
    pub page_size: usize,
}

impl GqaConfig {
    /// Llama-3.1-8B defaults used by the R19 unit test.
    pub const LLAMA_8B: GqaConfig = GqaConfig {
        n_heads: 32,
        n_kv_heads: 8,
        head_dim: 128,
        page_size: 16,
    };

    /// GQA repetition factor (n_heads / n_kv_heads).
    #[inline]
    pub fn gqa_reps(&self) -> usize {
        self.n_heads / self.n_kv_heads
    }

    /// KV dimension per token (n_kv_heads * head_dim).
    #[inline]
    pub fn kv_dim(&self) -> usize {
        self.n_kv_heads * self.head_dim
    }

    /// Q dimension per token (n_heads * head_dim).
    #[inline]
    pub fn q_dim(&self) -> usize {
        self.n_heads * self.head_dim
    }

    /// Bytes per page (f32, single K or V tensor).
    #[inline]
    pub fn page_floats(&self) -> usize {
        self.page_size * self.kv_dim()
    }

    /// Attention scale: 1/√head_dim.
    #[inline]
    pub fn scale(&self) -> f32 {
        1.0 / (self.head_dim as f32).sqrt()
    }
}

/// Paged KV table (f32, NHD layout).
///
/// Physical layout for `k_pages` and `v_pages`:
/// ```text
/// flat[page * page_floats + tok_in_page * kv_dim + kv_head * head_dim + d]
/// ```
///
/// `page_table[logical_seq_pos / page_size]` = physical page index.
pub struct PagedKvTable {
    /// Physical page storage: `[n_physical_pages][page_size][kv_dim]` flattened.
    pub k_pages: Vec<f32>,
    pub v_pages: Vec<f32>,
    /// Maps logical sequence position `pos / page_size` → physical page index.
    pub page_table: Vec<usize>,
    /// Number of valid tokens already stored (= current sequence length before append).
    pub seq_len: usize,
    pub cfg: GqaConfig,
}

impl PagedKvTable {
    /// Allocate `n_physical_pages` empty pages (zeroed).
    pub fn new(cfg: GqaConfig, n_physical_pages: usize) -> Self {
        let pf = cfg.page_floats();
        Self {
            k_pages: vec![0.0f32; n_physical_pages * pf],
            v_pages: vec![0.0f32; n_physical_pages * pf],
            page_table: Vec::new(),
            seq_len: 0,
            cfg,
        }
    }

    /// Number of physical pages allocated.
    pub fn n_physical_pages(&self) -> usize {
        let pf = self.cfg.page_floats();
        if pf == 0 { 0 } else { self.k_pages.len() / pf }
    }

    /// Append one KV token at `seq_len` (auto-allocates next page when needed).
    ///
    /// `k` and `v` must be slices of length `kv_dim`.
    pub fn append(&mut self, k: &[f32], v: &[f32]) {
        let kv_dim = self.cfg.kv_dim();
        assert_eq!(k.len(), kv_dim, "k length mismatch");
        assert_eq!(v.len(), kv_dim, "v length mismatch");

        let pos = self.seq_len;
        let page_idx_logical = pos / self.cfg.page_size;
        let tok_in_page = pos % self.cfg.page_size;

        // Allocate a new physical page mapping when we start a new logical page.
        if tok_in_page == 0 {
            let phys = page_idx_logical; // simple 1-to-1 for the stub (no fragmentation)
            assert!(
                phys < self.n_physical_pages(),
                "PagedKvTable: out of physical pages (phys={phys}, allocated={})",
                self.n_physical_pages()
            );
            self.page_table.push(phys);
        }

        let phys_page = self.page_table[page_idx_logical];
        let pf = self.cfg.page_floats();
        let base_k = phys_page * pf + tok_in_page * kv_dim;
        let base_v = phys_page * pf + tok_in_page * kv_dim;
        self.k_pages[base_k..base_k + kv_dim].copy_from_slice(k);
        self.v_pages[base_v..base_v + kv_dim].copy_from_slice(v);

        self.seq_len += 1;
    }

    /// Read K for token at logical position `pos`, KV-head `kv_h`.
    /// Returns a slice of length `head_dim`.
    pub fn k_head(&self, pos: usize, kv_h: usize) -> &[f32] {
        let cfg = &self.cfg;
        let phys = self.page_table[pos / cfg.page_size];
        let tok_in_page = pos % cfg.page_size;
        let pf = cfg.page_floats();
        let base = phys * pf + tok_in_page * cfg.kv_dim() + kv_h * cfg.head_dim;
        &self.k_pages[base..base + cfg.head_dim]
    }

    /// Read V for token at logical position `pos`, KV-head `kv_h`.
    pub fn v_head(&self, pos: usize, kv_h: usize) -> &[f32] {
        let cfg = &self.cfg;
        let phys = self.page_table[pos / cfg.page_size];
        let tok_in_page = pos % cfg.page_size;
        let pf = cfg.page_floats();
        let base = phys * pf + tok_in_page * cfg.kv_dim() + kv_h * cfg.head_dim;
        &self.v_pages[base..base + cfg.head_dim]
    }
}

/// Inline dot product (f32, unrolled ×4).
#[inline]
fn dot_f32(a: &[f32], b: &[f32]) -> f32 {
    let n = a.len();
    let mut s = 0.0f32;
    let mut i = 0;
    while i + 4 <= n {
        s += a[i] * b[i] + a[i + 1] * b[i + 1] + a[i + 2] * b[i + 2] + a[i + 3] * b[i + 3];
        i += 4;
    }
    while i < n {
        s += a[i] * b[i];
        i += 1;
    }
    s
}

/// GQA decode attention for one query token using paged KV + online softmax.
///
/// - `q`: `[n_heads * head_dim]` — current token Q projections.
/// - `kv`: paged KV table with `seq_len` tokens already stored (including the
///   current token's K/V, which must have been appended before calling this).
/// - `out`: `[n_heads * head_dim]` — output attention vector.
///
/// Online softmax (Dagrau-Ohlsson) processes one KV token per iteration; no
/// intermediate scores array — O(1) extra memory per head.
pub fn gqa_decode_xqa_stub(q: &[f32], kv: &PagedKvTable, out: &mut [f32]) {
    let cfg = &kv.cfg;
    let seq = kv.seq_len;
    assert_eq!(q.len(), cfg.q_dim(), "q length mismatch");
    assert_eq!(out.len(), cfg.q_dim(), "out length mismatch");
    assert!(seq > 0, "seq must be > 0 (current token must be appended first)");

    let scale = cfg.scale();
    let reps = cfg.gqa_reps();
    let hd = cfg.head_dim;

    let mut acc = vec![0.0f32; hd];

    for h in 0..cfg.n_heads {
        let kv_h = h / reps;
        let q_head = &q[h * hd..(h + 1) * hd];
        let out_head = &mut out[h * hd..(h + 1) * hd];

        // Online softmax state.
        let mut m = f32::NEG_INFINITY;
        let mut l = 0.0f32;
        for a in acc.iter_mut() {
            *a = 0.0;
        }

        for t in 0..seq {
            let k_head = kv.k_head(t, kv_h);
            let v_head = kv.v_head(t, kv_h);

            let score = dot_f32(q_head, k_head) * scale;
            let m_new = m.max(score);
            let alpha = (m - m_new).exp(); // = exp(m_old - m_new); 1.0 when m == -inf initially
            let p = (score - m_new).exp();

            l = alpha * l + p;
            for d in 0..hd {
                acc[d] = alpha * acc[d] + p * v_head[d];
            }
            m = m_new;
        }

        let inv_l = if l > 0.0 { 1.0 / l } else { 0.0 };
        for d in 0..hd {
            out_head[d] = acc[d] * inv_l;
        }
    }
}

/// Validate config: page_size ∈ {16, 32} (TRT/FI supported sizes).
///
/// Returns `Err` with a human-readable message if the config is invalid.
pub fn validate_config(cfg: &GqaConfig) -> Result<(), String> {
    if cfg.page_size != 16 && cfg.page_size != 32 {
        return Err(format!(
            "attn_xqa_stub: page_size must be 16 or 32 (TRT/FI contract); got {}",
            cfg.page_size
        ));
    }
    if cfg.n_heads == 0 || cfg.n_kv_heads == 0 || cfg.head_dim == 0 {
        return Err("attn_xqa_stub: n_heads/n_kv_heads/head_dim must be > 0".into());
    }
    if cfg.n_heads % cfg.n_kv_heads != 0 {
        return Err(format!(
            "attn_xqa_stub: n_heads ({}) must be divisible by n_kv_heads ({})",
            cfg.n_heads, cfg.n_kv_heads
        ));
    }
    Ok(())
}
