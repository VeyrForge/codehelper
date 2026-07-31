//! M4 MoE FFN smoke: open Pack Owner B's `tiny-moe.green` (real experts-000.greenpack),
//! build [`PackageExpertStore`], run [`ffn_step_auto`], match dense_provider reference.
//!
//! Must not surface RuntimeNotReady / GenerationNotReady for the MoE FFN path.
//!
//! Canonical fixture:
//! `green-compress/scripts/fixtures/tiny-moe.green`
//! (override with `GE_TINY_MOE_GREEN`).
//!
//! Run:
//! ```text
//! cargo test -p engine-core --test native_moe_ffn_smoke -- --nocapture
//! ```

use std::env;
use std::path::{Path, PathBuf};

use engine_core::backend::{CpuBackend, Scratch};
use engine_core::paged::{dense_provider, ExpertProvider};
use engine_core::{
    ffn_step_auto, route_topk, GreenModel, GreenModelError, LoadConfig, LoadState,
};
use green_format::open_package;

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

#[test]
fn tiny_moe_fixture_present() {
    let root = tiny_moe_green_dir();
    for name in [
        "manifest.json",
        "metadata.gguf",
        "dense.gguf",
        "experts-000.greenpack",
        "checksums.json",
    ] {
        let p = root.join(name);
        assert!(
            p.is_file(),
            "expected {} under {} — rebuild with green-compress make_test_moe_gguf + pack-model",
            name,
            root.display()
        );
    }
    let mut hdr = [0u8; 4];
    let mut f = std::fs::File::open(root.join("experts-000.greenpack")).unwrap();
    use std::io::Read;
    f.read_exact(&mut hdr).unwrap();
    assert_eq!(&hdr, b"GRNP", "greenpack magic");
}

#[test]
fn native_moe_ffn_smoke() {
    let root = tiny_moe_green_dir();
    assert!(
        root.join("manifest.json").is_file(),
        "missing tiny-moe.green at {} — set GE_TINY_MOE_GREEN or build sibling fixture",
        root.display()
    );

    let pkg = open_package(&root, true).unwrap_or_else(|e| {
        panic!("open_package failed for {}: {e}", root.display());
    });
    let n_experts = pkg.manifest.expert_tensors().count();
    assert!(
        n_experts >= 6,
        "expected catalogued expert tensors in tensors[], got {n_experts}"
    );
    assert!(
        pkg.manifest.expert_tensors_pending.is_empty(),
        "M4 fixture must pack experts (pending empty), got {}",
        pkg.manifest.expert_tensors_pending.len()
    );
    assert!(
        pkg.metadata.experts_greenpack.as_ref().is_some_and(|p| p.is_file()),
        "experts greenpack path missing"
    );

    let model = match GreenModel::open(
        &root,
        &LoadConfig {
            verify_checksums: true,
        },
    ) {
        Ok(m) => m,
        Err(GreenModelError::RuntimeNotReady) | Err(GreenModelError::GenerationNotReady) => {
            panic!("M4 MoE open must not return *NotReady (FFN path is independent of generate)");
        }
        Err(e) => panic!("GreenModel::open failed: {e}"),
    };
    assert!(
        matches!(model.load_state(), LoadState::ReadyForForward),
        "load_state={:?}",
        model.load_state()
    );
    assert!(
        model.experts.has_experts(),
        "GreenModel must index expert records from tensors[]"
    );

    let store = model
        .package_expert_store(4)
        .expect("PackageExpertStore::from_records")
        .expect("MoE package must yield Some(PackageExpertStore)");
    assert_eq!(store.layers(), 1);
    assert!(store.experts() >= 2);
    let h = store.hidden();
    let inter = store.inter();
    assert_eq!(h, 64, "fixture hidden");
    assert_eq!(inter, 128, "fixture inter");

    let backend = CpuBackend;
    let mut seed = 42u64;
    let mut lcg = || {
        seed ^= seed >> 12;
        seed ^= seed << 25;
        seed ^= seed >> 27;
        (seed.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5
    };
    let x: Vec<f32> = (0..h).map(|_| lcg()).collect();
    let e = store.experts();
    let logits: Vec<f32> = (0..e).map(|_| lcg()).collect();
    let top_k = 2.min(e);
    let route = route_topk(&logits, top_k);

    let via_dense = dense_provider(&store, &backend, 0, &x, &route.experts, &route.gates);
    let mut scratch = Scratch::new(h, inter);
    let mut out = vec![0.0f32; h];
    let sel = ffn_step_auto(
        &backend,
        None,
        Some(&store),
        0,
        &x,
        Some(&logits),
        top_k,
        &mut scratch,
        &mut out,
    )
    .unwrap_or_else(|err| panic!("ffn_step_auto MoE failed: {err}"));
    assert_eq!(sel.experts, route.experts);
    for i in 0..h {
        assert!(
            (out[i] - via_dense[i]).abs() < 1e-5,
            "ffn_step_auto vs dense_provider at {i}: {} vs {}",
            out[i],
            via_dense[i]
        );
    }
    assert!(
        store.metrics().misses >= 1,
        "expected at least one expert shard materialize"
    );
    eprintln!(
        "native_moe_ffn_smoke: ok experts={} top_k={} misses={}",
        e,
        top_k,
        store.metrics().misses
    );
}
