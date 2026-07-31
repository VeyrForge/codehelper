use engine_core::chat::ChatApplyMode;
use engine_core::{generate, GenerateRequest, GreenModel, LoadConfig};
use std::path::PathBuf;

fn main() {
    let path = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).expect("open");
    let ctx: usize = std::env::var("GE_CTX")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(32768);
    let mut req = GenerateRequest::new("Say hello in one short sentence.", 8);
    req.chat = ChatApplyMode::Auto;
    req = req.with_ctx_len(ctx);
    let r = generate(&model, &req).expect("generate");
    println!(
        "m54: ctx={} reserved={} hot_cap={} hot_bytes={} seq={} kv={}",
        r.ctx_len,
        r.kv_reserved_tokens,
        r.kv_hot_cap,
        r.kv_hot_bytes,
        r.kv_seq_len,
        r.kv_key_quant.label()
    );
}
