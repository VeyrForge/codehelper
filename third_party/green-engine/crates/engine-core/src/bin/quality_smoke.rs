use engine_core::chat::ChatApplyMode;
use engine_core::{generate, GenerateRequest, GreenModel, LoadConfig};
use std::path::PathBuf;
use std::time::Instant;

fn main() {
    let path = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).unwrap();
    for (label, chat) in [("auto", ChatApplyMode::Auto), ("off", ChatApplyMode::Off), ("force", ChatApplyMode::Force)] {
        let mut req = GenerateRequest::new("Say hello in one short sentence.", 16);
        req.chat = chat;
        let t0 = Instant::now();
        let r = generate(&model, &req).expect("gen");
        println!("{label}: decode={:.2} tok/s text={:?} (prompt_tokens={})",
            r.decode_tok_s(), r.text, r.prompt_tokens.len());
        let _ = t0;
    }
    // Completions-style prompts that worked in quality doc
    for prompt in ["The sky is", "2+2=", "OK"] {
        let mut req = GenerateRequest::new(prompt, 8);
        req.chat = ChatApplyMode::Off;
        let r = generate(&model, &req).unwrap();
        println!("off {prompt:?} -> {:?}", r.text);
    }
}
