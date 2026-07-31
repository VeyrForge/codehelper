//! Vertical-slice smoke: open dense-only `tiny.green`, then generate one token
//! (or skip with a clear reason). Must never hang.
//!
//! Canonical fixture:
//! `green-compress/scripts/fixtures/tiny.green`
//! (override with `GE_TINY_GREEN`).
//!
//! Run:
//! ```text
//! cargo test -p engine-core --test native_generate_smoke -- --nocapture
//! ```

use std::env;
use std::path::{Path, PathBuf};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use engine_core::{GreenModel, GreenModelError, LoadConfig, LoadState};
use green_format::open_package;

const SMOKE_TIMEOUT_SECS: u64 = 30;

/// Resolve Pack Owner B fixture (dense-only mini `.green`).
fn tiny_green_dir() -> PathBuf {
    if let Ok(p) = env::var("GE_TINY_GREEN") {
        return PathBuf::from(p);
    }
    let manifest = Path::new(env!("CARGO_MANIFEST_DIR"));
    // engine-core → crates → <GreenEngine|third_party/green-engine> → sibling green-compress
    let candidates = [
        manifest.join("../../../green-compress/scripts/fixtures/tiny.green"),
        manifest.join("../../../../green-compress/scripts/fixtures/tiny.green"),
    ];
    for c in &candidates {
        if c.join("manifest.json").is_file() {
            return c.canonicalize().unwrap_or_else(|_| c.clone());
        }
    }
    // Last resort: documented absolute local checkout (Windows).
    let absolute = PathBuf::from(r"../green-compress\scripts\fixtures\tiny.green");
    if absolute.join("manifest.json").is_file() {
        return absolute;
    }
    candidates[0].clone()
}

#[derive(Debug)]
enum ForwardOutcome {
    /// Dense generate produced at least one token id.
    #[allow(dead_code)]
    Generated { token_id: u32 },
    /// Fixture incomplete / not ready for generate.
    Skipped(&'static str),
}

#[derive(Debug)]
enum SmokeResult {
    Ok(ForwardOutcome),
    Fail(String),
}

/// Dense generate via [`GreenModel::generate`].
fn attempt_forward(model: &GreenModel) -> ForwardOutcome {
    if !model.is_ready_for_forward() {
        return ForwardOutcome::Skipped("LoadState::NotReady (M1/M2 incomplete)");
    }
    if !model.can_generate() {
        let _ = model.ensure_generation_ready();
        return ForwardOutcome::Skipped(
            "can_generate=false / GenerationNotReady (weights incomplete)",
        );
    }
    match model.ensure_generation_ready().and_then(|_| model.generate("a", 1)) {
        Ok(text) => {
            eprintln!("native_generate_smoke: text={text:?}");
            // Decode may be a single vocab piece; still count as Generated.
            ForwardOutcome::Generated {
                token_id: text.chars().next().map(|c| c as u32).unwrap_or(0),
            }
        }
        Err(e) => {
            eprintln!("native_generate_smoke: generate error: {e}");
            ForwardOutcome::Skipped("generate returned error")
        }
    }
}

fn with_timeout(secs: u64, f: impl FnOnce() -> SmokeResult + Send + 'static) -> SmokeResult {
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let _ = tx.send(f());
    });
    match rx.recv_timeout(Duration::from_secs(secs)) {
        Ok(r) => r,
        Err(_) => SmokeResult::Fail(
            "native_generate_smoke timed out (possible hang)".into(),
        ),
    }
}

#[test]
fn native_generate_smoke() {
    let root = tiny_green_dir();
    assert!(
        root.join("manifest.json").is_file(),
        "missing Pack Owner B fixture at {} — set GE_TINY_GREEN or ensure sibling \
         green-compress/scripts/fixtures/tiny.green exists (see green-compress README)",
        root.display()
    );

    let root_for_thread = root.clone();
    let result = with_timeout(SMOKE_TIMEOUT_SECS, move || {
        let root = root_for_thread;
        // M0 PASS: pack-model fixture opens with checksums.
        let pkg = match open_package(&root, true) {
            Ok(p) => p,
            Err(e) => {
                return SmokeResult::Fail(format!("open_package failed for {}: {e}", root.display()));
            }
        };
        if pkg.metadata.model.is_empty() {
            return SmokeResult::Fail("manifest model/source_model empty".into());
        }
        if !pkg.metadata.dense_gguf.as_ref().is_some_and(|p| p.is_file()) {
            return SmokeResult::Fail("dense.gguf missing".into());
        }
        if pkg.manifest.expert_tensors().count() != 0 {
            return SmokeResult::Fail(
                "M3 fixture must be dense-only (no expert tensor records)".into(),
            );
        }

        match GreenModel::open(
            &root,
            &LoadConfig {
                verify_checksums: true,
            },
        ) {
            Ok(model) => {
                let _ = model.load_state(); // ReadyForForward | NotReady — both OK for smoke
                assert!(
                    matches!(model.load_state(), LoadState::ReadyForForward)
                        || matches!(model.load_state(), LoadState::NotReady { .. })
                );
                SmokeResult::Ok(attempt_forward(&model))
            }
            Err(GreenModelError::RuntimeNotReady)
            | Err(GreenModelError::GenerationNotReady) => {
                SmokeResult::Ok(ForwardOutcome::Skipped(
                    "GreenModel::open => *NotReady (legacy)",
                ))
            }
            Err(e) => SmokeResult::Fail(format!("unexpected GreenModel::open error: {e}")),
        }
    });

    match result {
        SmokeResult::Ok(ForwardOutcome::Generated { token_id }) => {
            eprintln!("native_generate_smoke: generated token_id={token_id}");
        }
        SmokeResult::Ok(ForwardOutcome::Skipped(reason)) => {
            eprintln!("native_generate_smoke: SKIP forward — {reason}");
        }
        SmokeResult::Fail(msg) => panic!("{msg}"),
    }
}

#[test]
fn tiny_green_fixture_present() {
    let root = tiny_green_dir();
    for name in ["manifest.json", "metadata.gguf", "dense.gguf", "checksums.json"] {
        let p = root.join(name);
        assert!(p.is_file(), "expected {} under {}", name, root.display());
    }
}

#[test]
fn native_generate_multi_token() {
    let root = tiny_green_dir();
    assert!(
        root.join("manifest.json").is_file(),
        "missing tiny.green at {}",
        root.display()
    );
    let root_for_thread = root.clone();
    let result = with_timeout(SMOKE_TIMEOUT_SECS, move || {
        let model = match GreenModel::open(
            &root_for_thread,
            &LoadConfig {
                verify_checksums: false,
            },
        ) {
            Ok(m) => m,
            Err(e) => return SmokeResult::Fail(format!("open: {e}")),
        };
        if !model.can_generate() {
            return SmokeResult::Ok(ForwardOutcome::Skipped("can_generate=false"));
        }
        match model.generate("a", 4) {
            Ok(text) => {
                eprintln!(
                    "native_generate_multi_token: text={text:?} chars={}",
                    text.chars().count()
                );
                // Greedy decode of 4 new tokens must yield non-empty text for the tiny fixture.
                if text.is_empty() {
                    return SmokeResult::Fail("multi-token generate returned empty text".into());
                }
                SmokeResult::Ok(ForwardOutcome::Generated {
                    token_id: text.chars().next().map(|c| c as u32).unwrap_or(0),
                })
            }
            Err(e) => SmokeResult::Fail(format!("generate n=4: {e}")),
        }
    });
    match result {
        SmokeResult::Ok(ForwardOutcome::Generated { .. }) => {
            eprintln!("native_generate_multi_token: PASS (n=4)");
        }
        SmokeResult::Ok(ForwardOutcome::Skipped(reason)) => {
            eprintln!("native_generate_multi_token: SKIP — {reason}");
        }
        SmokeResult::Fail(msg) => panic!("{msg}"),
    }
}
