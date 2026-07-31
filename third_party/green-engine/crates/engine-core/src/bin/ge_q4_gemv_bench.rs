//! Isolated `ge_decode_q4_gemv` microbench for NCU MBU on WDDM.
//!
//! Loads real 8B weights (lm_head + FFN gate), uploads once, warms up, then runs
//! one tagged GEMV each. NVTX ranges wrap kernel launch only (via ge_decode_set_nvtx_tag).
//!
//!   cargo build -p engine-core --release --features gpu --bin ge_q4_gemv_bench
//!   set GREEN_ENGINE_KERNELS_DIR=crates\kernels
//!   target\release\ge_q4_gemv_bench.exe [path.green]
//!
//! NCU (one range per run for WDDM stability):
//!   ncu --nvtx --nvtx-include "gemv_lm_head/" --launch-count 1 --target-processes all ...
//!   ncu --nvtx --nvtx-include "gemv_ffn_gate/" --launch-count 1 --target-processes all ...

#[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
fn main() {
    eprintln!("ge_q4_gemv_bench: rebuild with --features gpu and built kernels DLL");
    eprintln!("  python scripts/build_kernels.py && set GREEN_ENGINE_KERNELS_DIR=crates\\kernels");
    std::process::exit(1);
}

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
mod imp {
    use std::env;
    use std::path::{Path, PathBuf};
    use std::time::Instant;

    use engine_core::gguf_load::load_package_weights_lean;
    use engine_core::gpu_gemv::{free_slot, GpuDecodeCtx, GpuQuantSlot};
    use engine_core::quant_mat::{QuantMat, GGML_Q4_0};

    fn q4_weight_bytes(in_dim: usize, out_dim: usize) -> usize {
        out_dim * (in_dim / 32) * 18
    }

    fn model_root() -> PathBuf {
        let home = env::var("USERPROFILE").unwrap_or_default();
        PathBuf::from(format!(
            r"{home}\.green\models\Meta-Llama-3.1-8B-Instruct.green"
        ))
    }

    fn load_mat(root: &Path, name: &str) -> QuantMat {
        let dense = root.join("dense.gguf");
        let meta = root.join("metadata.gguf");
        let w = load_package_weights_lean(&dense, Some(&meta))
            .unwrap_or_else(|e| panic!("load weights from {}: {e}", root.display()));
        w.require(name)
            .unwrap_or_else(|e| panic!("tensor {name}: {e}"))
            .to_quant_mat()
            .unwrap_or_else(|e| panic!("quant_mat {name}: {e}"))
    }

    fn upload(ctx: &GpuDecodeCtx, mat: &QuantMat) -> GpuQuantSlot {
        ctx.upload_mat(mat)
            .unwrap_or_else(|| panic!("CUDA upload failed (type={})", mat.ggml_type))
    }

    fn gemv_tagged(
        ctx: &GpuDecodeCtx,
        slot: &GpuQuantSlot,
        x: &[f32],
        y: &mut [f32],
        tag: &str,
    ) -> bool {
        ctx.with_nvtx_tag(tag, || slot.gemv(ctx, x, y))
    }

    fn bench_once(
        ctx: &GpuDecodeCtx,
        slot: &GpuQuantSlot,
        x: &[f32],
        y: &mut [f32],
        tag: &str,
    ) -> f64 {
        assert!(gemv_tagged(ctx, slot, x, y, tag), "gemv failed for {tag}");
        let t0 = Instant::now();
        assert!(gemv_tagged(ctx, slot, x, y, tag), "gemv failed for {tag}");
        t0.elapsed().as_secs_f64()
    }

    pub fn run() {
        let root = env::args()
            .nth(1)
            .map(PathBuf::from)
            .unwrap_or_else(model_root);

        let lm = load_mat(&root, "output.weight");
        let gate = load_mat(&root, "blk.0.ffn_gate.weight");
        if lm.ggml_type != GGML_Q4_0 || gate.ggml_type != GGML_Q4_0 {
            panic!(
                "expected Q4_0 weights; lm type={} gate type={}",
                lm.ggml_type, gate.ggml_type
            );
        }

        let ctx = GpuDecodeCtx::try_new(0).expect("CUDA device");
        let lm_slot = upload(&ctx, &lm);
        let gate_slot = upload(&ctx, &gate);

        let mut x_lm = vec![0.0f32; lm.in_dim];
        let mut y_lm = vec![0.0f32; lm.out_dim];
        let mut x_gate = vec![0.0f32; gate.in_dim];
        let mut y_gate = vec![0.0f32; gate.out_dim];
        for (i, v) in x_lm.iter_mut().enumerate() {
            *v = ((i as f32) * 0.013 - 0.5).sin() * 0.25;
        }
        for (i, v) in x_gate.iter_mut().enumerate() {
            *v = ((i as f32) * 0.011 + 0.3).cos() * 0.2;
        }

        println!(
            "ge_q4_gemv_bench: model={} device={}",
            root.display(),
            ctx.device_id
        );
        println!(
            "lm_head: in={} out={} w_bytes={}",
            lm.in_dim,
            lm.out_dim,
            q4_weight_bytes(lm.in_dim, lm.out_dim)
        );
        println!(
            "ffn_gate: in={} out={} w_bytes={}",
            gate.in_dim,
            gate.out_dim,
            q4_weight_bytes(gate.in_dim, gate.out_dim)
        );

        const WARM: usize = 4;
        for _ in 0..WARM {
            assert!(gemv_tagged(&ctx, &lm_slot, &x_lm, &mut y_lm, "gemv_warmup"));
            assert!(gemv_tagged(
                &ctx,
                &gate_slot,
                &x_gate,
                &mut y_gate,
                "gemv_warmup"
            ));
        }
        println!("warmup: {WARM} lm_head + {WARM} ffn_gate");

        let t_lm = bench_once(&ctx, &lm_slot, &x_lm, &mut y_lm, "gemv_lm_head");
        let t_gate = bench_once(&ctx, &gate_slot, &x_gate, &mut y_gate, "gemv_ffn_gate");

        let w_lm = q4_weight_bytes(lm.in_dim, lm.out_dim) as f64;
        let w_gate = q4_weight_bytes(gate.in_dim, gate.out_dim) as f64;
        let gbs_lm = w_lm / t_lm / 1e9;
        let gbs_gate = w_gate / t_gate / 1e9;

        println!(
            "profile: gemv_lm_head t_us={:.1} eff_gbs={:.1} y0={:.4}",
            t_lm * 1e6,
            gbs_lm,
            y_lm[0]
        );
        println!(
            "profile: gemv_ffn_gate t_us={:.1} eff_gbs={:.1} y0={:.4}",
            t_gate * 1e6,
            gbs_gate,
            y_gate[0]
        );
        println!("ncu_ready: use --nvtx-include gemv_lm_head/ or gemv_ffn_gate/");

        free_slot(&ctx, &lm_slot);
        free_slot(&ctx, &gate_slot);
    }
}

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
fn main() {
    imp::run();
}
