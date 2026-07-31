//! RSS / speed probe: cold then warm generate (fair tok/s after weight pages faulted).
use engine_core::chat::ChatApplyMode;
use engine_core::{generate, GenerateRequest, GreenModel, LoadConfig};
use std::env;
use std::path::PathBuf;

fn main() {
    let path = env::args()
        .nth(1)
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green")
        });
    let n_new: usize = env::args()
        .nth(2)
        .and_then(|s| s.parse().ok())
        .unwrap_or(8);
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).expect("open");
    let mut req = GenerateRequest::new("Say hello in one short sentence.", n_new);
    // Match decode_1b_bench default chat template path.
    req.chat = ChatApplyMode::Auto;
    if let Ok(c) = env::var("GE_CTX") {
        if let Ok(n) = c.parse::<usize>() {
            req = req.with_ctx_len(n);
        }
    }
    let r1 = generate(&model, &req).expect("cold");
    println!(
        "cold: decode={:.2} tok/s n={} ctx={} kv={} hot_bytes={}",
        r1.decode_tok_s(),
        r1.new_tokens.len(),
        r1.ctx_len,
        r1.kv_key_quant.label(),
        r1.kv_hot_bytes
    );
    let r2 = generate(&model, &req).expect("warm");
    println!(
        "warm: decode={:.2} tok/s n={} ctx={} kv={} hot_bytes={} seq={} hot_cap={} text={:?} ids={:?}",
        r2.decode_tok_s(),
        r2.new_tokens.len(),
        r2.ctx_len,
        r2.kv_key_quant.label(),
        r2.kv_hot_bytes,
        r2.kv_seq_len,
        r2.kv_hot_cap,
        r2.text,
        r2.new_tokens
    );
}
