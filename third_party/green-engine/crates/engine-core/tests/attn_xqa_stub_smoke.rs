//! R19 step 2 — TRT-XQA JIT GQA decode attn stub: unit tests + Hello parity gate.
//!
//! Three gates (must all pass):
//!
//! 1. **Config validation** — page_size ∈ {16,32}; bad configs rejected.
//! 2. **GQA 32q/8kv online-softmax output** — tiny synthetic KV, expected
//!    output computed by hand.  Checks the paged-walk + online-softmax
//!    produces numerically correct results.
//! 3. **Parity vs `attention::softmax_inplace` path** — for a short sequence
//!    (seq ≤ page_size) both paths must agree within 1e-5 absolute.  This is
//!    the "Hello parity gate" required by R19.
//!
//! Run:
//! ```text
//! cargo test -p engine-core --test attn_xqa_stub_smoke -- --nocapture
//! ```

use engine_core::attn_xqa_stub::{gqa_decode_xqa_stub, validate_config, GqaConfig, PagedKvTable};
use engine_core::attention::{attn_scale, softmax_inplace};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn max_abs_diff(a: &[f32], b: &[f32]) -> f32 {
    a.iter()
        .zip(b)
        .map(|(x, y)| (x - y).abs())
        .fold(0.0f32, f32::max)
}

/// Build a paged KV table from flat `k_flat` / `v_flat` (seq × kv_dim each).
fn build_paged(cfg: GqaConfig, k_flat: &[f32], v_flat: &[f32], seq: usize) -> PagedKvTable {
    let kv_dim = cfg.kv_dim();
    let n_pages = (seq + cfg.page_size - 1) / cfg.page_size;
    let mut kv = PagedKvTable::new(cfg, n_pages + 1);
    for t in 0..seq {
        kv.append(&k_flat[t * kv_dim..(t + 1) * kv_dim], &v_flat[t * kv_dim..(t + 1) * kv_dim]);
    }
    kv
}

/// Reference GQA decode: contiguous KV (not paged), standard two-pass softmax.
///
/// Mirrors `attention::attend_one_head` logic, used as parity oracle.
fn gqa_decode_ref(
    q: &[f32],
    k_flat: &[f32],
    v_flat: &[f32],
    seq: usize,
    n_heads: usize,
    n_kv_heads: usize,
    head_dim: usize,
    out: &mut [f32],
) {
    let kv_dim = n_kv_heads * head_dim;
    let scale = attn_scale(head_dim);
    let reps = n_heads / n_kv_heads;
    let mut scores = vec![0.0f32; seq];
    for h in 0..n_heads {
        let kv_h = h / reps;
        let q_head = &q[h * head_dim..(h + 1) * head_dim];
        for t in 0..seq {
            let k_head = &k_flat[t * kv_dim + kv_h * head_dim..t * kv_dim + (kv_h + 1) * head_dim];
            let dot: f32 = q_head.iter().zip(k_head).map(|(a, b)| a * b).sum();
            scores[t] = dot * scale;
        }
        softmax_inplace(&mut scores[..seq]);
        let out_head = &mut out[h * head_dim..(h + 1) * head_dim];
        for d in 0..head_dim {
            let mut acc = 0.0f32;
            for t in 0..seq {
                let v_head = &v_flat[t * kv_dim + kv_h * head_dim..];
                acc += scores[t] * v_head[d];
            }
            out_head[d] = acc;
        }
    }
}

// ---------------------------------------------------------------------------
// Gate 1 — config validation
// ---------------------------------------------------------------------------

#[test]
fn xqa_stub_config_valid_page_sizes() {
    for ps in [16usize, 32usize] {
        let cfg = GqaConfig { page_size: ps, ..GqaConfig::LLAMA_8B };
        validate_config(&cfg).expect("page_size {ps} must be valid");
    }
    eprintln!("xqa_stub_config_valid_page_sizes KEEP");
}

#[test]
fn xqa_stub_config_rejects_bad_page_size() {
    for bad in [8usize, 64, 128] {
        let cfg = GqaConfig { page_size: bad, ..GqaConfig::LLAMA_8B };
        let err = validate_config(&cfg).expect_err("page_size {bad} must be rejected");
        assert!(
            err.contains("page_size must be 16 or 32"),
            "unexpected error: {err}"
        );
    }
    eprintln!("xqa_stub_config_rejects_bad_page_size KEEP");
}

#[test]
fn xqa_stub_config_rejects_bad_gqa_ratio() {
    let cfg = GqaConfig { n_heads: 7, n_kv_heads: 3, ..GqaConfig::LLAMA_8B };
    let err = validate_config(&cfg).expect_err("7/3 GQA must be rejected");
    assert!(err.contains("divisible"), "unexpected error: {err}");
    eprintln!("xqa_stub_config_rejects_bad_gqa_ratio KEEP");
}

// ---------------------------------------------------------------------------
// Gate 2 — tiny synthetic: single-token seq, all-ones K/V/Q
// ---------------------------------------------------------------------------

#[test]
fn xqa_stub_single_token_all_ones() {
    // seq=1: softmax of one score = 1.0; out = V (no mixing needed).
    let cfg = GqaConfig::LLAMA_8B; // 32q/8kv, head_dim=128, page_size=16
    validate_config(&cfg).unwrap();
    let hd = cfg.head_dim;
    let kv_dim = cfg.kv_dim();
    let q_dim = cfg.q_dim();

    // Q = 0.1 (all elements), K = 0.1, V = 0.5
    let q = vec![0.1f32; q_dim];
    let k = vec![0.1f32; kv_dim];
    let v = vec![0.5f32; kv_dim];

    let mut kv = PagedKvTable::new(cfg, 2);
    kv.append(&k, &v);

    let mut out = vec![0.0f32; q_dim];
    gqa_decode_xqa_stub(&q, &kv, &mut out);

    // Expected: for each head, out[d] = 0.5 (only one token, softmax weight = 1).
    for (i, &o) in out.iter().enumerate() {
        assert!(
            (o - 0.5f32).abs() < 1e-6,
            "single-token all-ones: out[{i}]={o} expected 0.5"
        );
    }
    eprintln!("xqa_stub_single_token_all_ones KEEP (32q/8kv head_dim={hd})");
}

// ---------------------------------------------------------------------------
// Gate 3 — Hello parity gate: online-softmax vs two-pass ref, seq ≤ page_size
// ---------------------------------------------------------------------------

const PARITY_TOL: f32 = 1e-5;

/// Generate a simple deterministic f32 sequence for synthetic KV/Q.
fn synthetic_vec(len: usize, seed: f32) -> Vec<f32> {
    (0..len)
        .map(|i| ((i as f32) * 0.03 + seed).sin() * 0.5)
        .collect()
}

fn run_parity(cfg: GqaConfig, seq: usize) {
    validate_config(&cfg).unwrap();
    let kv_dim = cfg.kv_dim();
    let q_dim = cfg.q_dim();

    let q = synthetic_vec(q_dim, 1.1);
    let k_flat: Vec<f32> = (0..seq)
        .flat_map(|t| synthetic_vec(kv_dim, 0.7 + t as f32 * 0.13))
        .collect();
    let v_flat: Vec<f32> = (0..seq)
        .flat_map(|t| synthetic_vec(kv_dim, 0.3 + t as f32 * 0.17))
        .collect();

    // Paged stub output.
    let kv = build_paged(cfg, &k_flat, &v_flat, seq);
    let mut out_xqa = vec![0.0f32; q_dim];
    gqa_decode_xqa_stub(&q, &kv, &mut out_xqa);

    // Reference two-pass softmax output.
    let mut out_ref = vec![0.0f32; q_dim];
    gqa_decode_ref(
        &q, &k_flat, &v_flat, seq,
        cfg.n_heads, cfg.n_kv_heads, cfg.head_dim,
        &mut out_ref,
    );

    let err = max_abs_diff(&out_xqa, &out_ref);
    assert!(
        err <= PARITY_TOL,
        "parity FAIL cfg={cfg:?} seq={seq}: max_abs_diff={err:.3e} > {PARITY_TOL:.3e}"
    );
    eprintln!(
        "xqa_stub_parity cfg=32q/8kv/hd={}/ps={} seq={seq}: max_abs={err:.3e} KEEP",
        cfg.head_dim, cfg.page_size
    );
}

/// Hello parity gate: seq fits within one page (page_size=16, seq=8).
#[test]
fn xqa_stub_parity_vs_two_pass_short_seq() {
    run_parity(GqaConfig::LLAMA_8B, 8);
}

/// Parity across page boundary (seq=20 > page_size=16 → two pages).
#[test]
fn xqa_stub_parity_vs_two_pass_cross_page_boundary() {
    run_parity(GqaConfig::LLAMA_8B, 20);
}

/// Parity with page_size=32.
#[test]
fn xqa_stub_parity_page_size_32() {
    let cfg = GqaConfig { page_size: 32, ..GqaConfig::LLAMA_8B };
    run_parity(cfg, 48); // crosses page boundary
}

/// Parity at seq=1 (degenerate: single token, no softmax competition).
#[test]
fn xqa_stub_parity_seq_1() {
    run_parity(GqaConfig::LLAMA_8B, 1);
}
