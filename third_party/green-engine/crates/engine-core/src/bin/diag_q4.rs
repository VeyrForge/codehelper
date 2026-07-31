use engine_core::gguf_load::load_package_weights_lean;
use engine_core::chat::ChatApplyMode;
use engine_core::generate::{generate, load_forward_cached, GenerateRequest};
use engine_core::green_model::{GreenModel, LoadConfig};
use engine_core::sample::SampleParams;
use std::path::PathBuf;
fn main() {
    let path = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).unwrap();
    let w = load_package_weights_lean(&model.dense_weights.path, model.metadata.metadata_gguf.as_deref()).unwrap();
    for name in ["blk.0.ffn_gate.weight","blk.0.ffn_down.weight","token_embd.weight"] {
        let t = w.require(name).unwrap();
        println!("{name}: shape={:?} matvec={:?} ty={}", t.shape(), t.as_matvec_dims(), match t {
            engine_core::gguf_load::LeanTensor::F32{..} => 0,
            engine_core::gguf_load::LeanTensor::Packed{ggml_type,..} => *ggml_type,
        });
    }
    let (fwd,_,_) = load_forward_cached(&model.dense_weights.path, model.metadata.metadata_gguf.as_deref(), 0).unwrap();
    println!("heads={} kv={} rope_theta={}", fwd.n_heads, fwd.n_kv_heads, fwd.rope.theta);

    for (label, chat) in [("off", ChatApplyMode::Off), ("force", ChatApplyMode::Force)] {
        let mut req = GenerateRequest::new("Explain gravity in one sentence.", 32);
        req.chat = chat;
        req.sample = SampleParams::greedy();
        let r = generate(&model, &req).unwrap();
        let uniq = r.new_tokens.iter().collect::<std::collections::HashSet<_>>().len();
        println!("{label}: uniq={uniq} n={} text={:?}", r.new_tokens.len(), r.text);
    }
    // llama token ids forced
    let ids = vec![128000u32, 849, 21435, 24128, 304, 832, 11914, 13];
    let out = fwd.generate_paged(&ids, 8, 64, &[], &SampleParams::greedy()).unwrap();
    println!("forced-llama-ids next={:?}", out.new_tokens);
}
