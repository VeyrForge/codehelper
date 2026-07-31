//! Warm decode bench for Llama-3.2-1B.green (load once, time decode-only).
//!
//!   cargo run -p engine-core --release --bin decode_1b_bench -- [path.green] [n_new]
//!
//! Set GE_GEMV_LEGACY=1 to time the old flat Q4 GEMV.
//! Set GE_GEMV_F32=1 to force fused f32A-Q4 (skip act quant).
//! Set GE_THREADS=N / GE_CPU_THREADS=N to override physical-core decode pool.
//! Set GE_GAME_MODE=1 for gaming-safe defaults (low threads, GPU soft-cap, BelowNormal).
//! Set GE_CTX=N to request context length (clamped to MAX_CTX_LEN_CPU).
//! Set GE_KV_QUANT=F16|Q8|Q8V4 to force KV lane quant (default: auto_for_ctx).
//! Q8V4 = MC.3 paired K=Q8 + V=Q4 (packed).
//! Set GE_BENCH_PROMPT or GE_BENCH_PROMPT_FILE for a custom prefill (long-ctx gates).

use std::env;
use std::path::PathBuf;
use std::time::Instant;

use engine_core::kv::KvKeyQuant;
use engine_core::quant_kernels;
use engine_core::sys;
use engine_core::{generate, GenerateRequest, GreenModel, LoadConfig};

fn main() {
    engine_core::game_mode::apply_process_defaults();
    let mut args = env::args().skip(1);
    let home = env::var("USERPROFILE").unwrap_or_default();
    let path = args
        .next()
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(format!(r"{home}\.green\models\Llama-3.2-1B.green")));
    let n_new: usize = args
        .next()
        .and_then(|s| s.parse().ok())
        .unwrap_or(16);
    let legacy = env::var("GE_GEMV_LEGACY").ok().as_deref() == Some("1");
    let cpu = sys::detect_cpu();
    let isa = quant_kernels::detect_gemv_isa();

    let ctx_len = env::var("GE_CTX")
        .ok()
        .and_then(|s| s.parse::<usize>().ok());
    let kv_quant = match env::var("GE_KV_QUANT").ok().as_deref() {
        Some("F16") | Some("f16") => Some(KvKeyQuant::F16),
        Some("Q8") | Some("q8") => Some(KvKeyQuant::Q8),
        Some("Q8V4") | Some("q8v4") | Some("Q8_V4") | Some("K8V4") | Some("k8v4") => {
            Some(KvKeyQuant::Q8V4)
        }
        _ => None,
    };

    println!(
        "decode_1b_bench: {} n_new={n_new} legacy={legacy} ge_ctx={:?} ge_kv_quant={:?}",
        path.display(),
        ctx_len,
        kv_quant.map(|q| q.label())
    );
    println!(
        "cpu: logical={} physical={} decode_threads={} simd={} gemv_isa={} features=[{}]",
        cpu.cores,
        cpu.physical_cores,
        sys::decode_threads(),
        cpu.simd,
        isa.as_str(),
        cpu.features.join(",")
    );

    let t_open = Instant::now();
    let model = GreenModel::open(
        &path,
        &LoadConfig {
            verify_checksums: false,
        },
    )
    .expect("open .green");
    let open_secs = t_open.elapsed().as_secs_f64();

    let prompt = bench_prompt();
    let mut req = GenerateRequest::new(&prompt, n_new);
    if let Some(c) = ctx_len {
        req = req.with_ctx_len(c);
    }
    if let Some(q) = kv_quant {
        req = req.with_kv_key_quant(q);
    }

    let r1 = generate(&model, &req).expect("cold generate");
    println!(
        "cold: load={:.2}s prefill={:.2}s decode={:.2}s | decode={:.2} tok/s wall={:.2} tok/s (open={:.2}s) n={} ctx={} kv={} hot_bytes={}",
        r1.load_secs,
        r1.prefill_secs,
        r1.decode_secs,
        r1.decode_tok_s(),
        r1.wall_tok_s(),
        open_secs,
        r1.new_tokens.len(),
        r1.ctx_len,
        r1.kv_key_quant.label(),
        r1.kv_hot_bytes
    );

    let r2 = generate(&model, &req).expect("warm generate");
    println!(
        "warm: load={:.2}s prefill={:.2}s decode={:.2}s | decode={:.2} tok/s wall={:.2} tok/s cache_hit={} n={} ctx={} kv={} hot_bytes={} seq={} hot_cap={} reserved={}",
        r2.load_secs,
        r2.prefill_secs,
        r2.decode_secs,
        r2.decode_tok_s(),
        r2.wall_tok_s(),
        r2.forward_cache_hit,
        r2.new_tokens.len(),
        r2.ctx_len,
        r2.kv_key_quant.label(),
        r2.kv_hot_bytes,
        r2.kv_seq_len,
        r2.kv_hot_cap,
        r2.kv_reserved_tokens
    );
    println!("text_warm={:?}", r2.text);
    println!("ids_warm={:?}", r2.new_tokens);
}

fn bench_prompt() -> String {
    if let Ok(p) = env::var("GE_BENCH_PROMPT") {
        if !p.is_empty() {
            return p;
        }
    }
    if let Ok(path) = env::var("GE_BENCH_PROMPT_FILE") {
        if let Ok(s) = std::fs::read_to_string(&path) {
            let t = s.trim().to_string();
            if !t.is_empty() {
                return t;
            }
        }
    }
    "Say hello in one short sentence.".to_string()
}
