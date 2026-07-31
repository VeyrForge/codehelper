use engine_core::chat::ChatApplyMode;
use engine_core::generate::{load_forward_cached, generate, GenerateRequest};
use engine_core::green_model::{GreenModel, LoadConfig};
use engine_core::sample::SampleParams;
use std::path::PathBuf;

fn main() {
    let path = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).unwrap();
    let dense = &model.dense_weights.path;
    let meta = model.metadata.metadata_gguf.as_deref();
    let (fwd, _, _) = load_forward_cached(dense, meta, 0).unwrap();
    println!("n_embd={} n_layers={} n_heads={} n_kv={} head_dim={} vocab={}",
        fwd.n_embd, fwd.n_layers, fwd.n_heads, fwd.n_kv_heads, fwd.head_dim, fwd.n_vocab);
    println!("rope theta={} freq_scale={} ctx={:?} scaling={:?}",
        fwd.rope.theta, fwd.rope.freq_scale, fwd.rope.context_length, fwd.rope.scaling);

    let mut req = GenerateRequest::new("Explain gravity in one sentence.", 8);
    req.chat = ChatApplyMode::Off;
    req.sample = SampleParams::greedy();
    let r = generate(&model, &req).unwrap();
    println!("off text={:?} ids={:?}", r.text, r.new_tokens);

    let mut req = GenerateRequest::new("Explain gravity in one sentence.", 32);
    req.chat = ChatApplyMode::Force;
    let r = generate(&model, &req).unwrap();
    println!("force text={:?} ids={:?}", r.text, &r.new_tokens[..r.new_tokens.len().min(16)]);
}
