//! MoE generate smoke: open `tiny-moe.green`, run native generate for ≥1–8 tokens
//! via router + [`PackageExpertStore`] (not dense-only FFN).
//!
//! Canonical fixture:
//! `green-compress/scripts/fixtures/tiny-moe.green`
//! (override with `GE_TINY_MOE_GREEN`).
//!
//! Run:
//! ```text
//! cargo test -p engine-core --test native_moe_generate_smoke -- --nocapture
//! ```

use std::env;
use std::path::{Path, PathBuf};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use engine_core::{
    generate, GenerateRequest, GreenModel, GreenModelError, LoadConfig, LoadState,
};

const SMOKE_TIMEOUT_SECS: u64 = 60;

fn tiny_moe_green_dir() -> PathBuf {
    if let Ok(p) = env::var("GE_TINY_MOE_GREEN") {
        return PathBuf::from(p);
    }
    let manifest = Path::new(env!("CARGO_MANIFEST_DIR"));
    let candidates = [
        manifest.join("../../../green-compress/scripts/fixtures/tiny-moe.green"),
        manifest.join("../../../../green-compress/scripts/fixtures/tiny-moe.green"),
    ];
    for c in &candidates {
        if c.join("manifest.json").is_file() {
            return c.canonicalize().unwrap_or_else(|_| c.clone());
        }
    }
    let absolute =
        PathBuf::from(r"../green-compress\scripts\fixtures\tiny-moe.green");
    if absolute.join("manifest.json").is_file() {
        return absolute;
    }
    candidates[0].clone()
}

fn with_timeout<T: Send + 'static>(
    secs: u64,
    f: impl FnOnce() -> T + Send + 'static,
) -> Result<T, String> {
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let _ = tx.send(f());
    });
    rx.recv_timeout(Duration::from_secs(secs))
        .map_err(|_| format!("native_moe_generate_smoke timed out after {secs}s"))
}

#[test]
fn tiny_moe_fixture_present() {
    let root = tiny_moe_green_dir();
    for name in [
        "manifest.json",
        "metadata.gguf",
        "dense.gguf",
        "experts-000.greenpack",
        "tokenizer.json",
    ] {
        let p = root.join(name);
        assert!(
            p.is_file(),
            "expected {} under {}",
            name,
            root.display()
        );
    }
}

#[test]
fn native_moe_generate_smoke() {
    let root = tiny_moe_green_dir();
    assert!(
        root.join("manifest.json").is_file(),
        "missing tiny-moe.green at {} — set GE_TINY_MOE_GREEN",
        root.display()
    );

    let root_for_thread = root.clone();
    let result = with_timeout(SMOKE_TIMEOUT_SECS, move || {
        let model = match GreenModel::open(
            &root_for_thread,
            &LoadConfig {
                verify_checksums: true,
            },
        ) {
            Ok(m) => m,
            Err(GreenModelError::RuntimeNotReady) | Err(GreenModelError::GenerationNotReady) => {
                return Err(format!("MoE open must not return *NotReady"));
            }
            Err(e) => return Err(format!("GreenModel::open: {e}")),
        };
        assert!(
            matches!(model.load_state(), LoadState::ReadyForForward),
            "load_state={:?}",
            model.load_state()
        );
        assert!(
            model.experts.has_experts(),
            "expected expert tensor index on tiny-moe.green"
        );
        assert!(
            model.can_generate(),
            "can_generate=false for MoE package (dense+embd/output required)"
        );

        // Prefer a vocab piece that exists in the tiny fixture tokenizer.
        let req = GenerateRequest::new("t3", 8).with_ctx_len(64);
        let out = generate(&model, &req).map_err(|e| format!("generate: {e}"))?;
        if out.new_tokens.is_empty() {
            return Err("MoE generate returned 0 new tokens".into());
        }
        eprintln!(
            "native_moe_generate_smoke: n_new={} text={:?} load={:.3}s decode={:.3}s",
            out.new_tokens.len(),
            out.text,
            out.load_secs,
            out.decode_secs
        );
        Ok(out.new_tokens.len())
    })
    .expect("timeout channel");

    match result {
        Ok(n) => {
            assert!(n >= 1, "expected ≥1 token, got {n}");
            eprintln!("native_moe_generate_smoke: PASS ({n} tokens)");
        }
        Err(e) => panic!("{e}"),
    }
}

#[test]
fn native_moe_generate_via_model_api() {
    let root = tiny_moe_green_dir();
    if !root.join("manifest.json").is_file() {
        return;
    }
    let model = GreenModel::open(
        &root,
        &LoadConfig {
            verify_checksums: false,
        },
    )
    .expect("open tiny-moe");
    let text = model
        .generate("t3", 4)
        .unwrap_or_else(|e| panic!("GreenModel::generate MoE: {e}"));
    eprintln!("native_moe_generate_via_model_api: text={text:?}");
    // Tiny random weights may decode to empty-looking strings for some ids; token count is the contract.
    let detailed = model
        .generate_detailed("t3", 4)
        .unwrap_or_else(|e| panic!("generate_detailed: {e}"));
    assert!(
        !detailed.new_tokens.is_empty(),
        "expected new tokens from MoE generate_detailed"
    );
}
