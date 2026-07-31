use engine_core::gguf_io::read_gguf;
use engine_core::gguf_load::load_package_weights_lean;
use std::path::Path;
fn main() {
    let root = Path::new(r"/tmp/.green/models/Llama-3.2-1B.green");
    let dense = root.join("dense.gguf");
    let meta = root.join("metadata.gguf");
    let g = read_gguf(&dense, false).unwrap();
    for name in ["token_embd.weight","blk.0.attn_v.weight","blk.0.ffn_gate.weight","blk.0.ffn_down.weight"] {
        if let Some(t) = g.tensors.iter().find(|t| t.name == name) {
            println!("file {name}: dims={:?} type={} offset={}", t.shape, t.ggml_type, t.offset);
        } else { println!("file {name}: MISSING"); }
    }
    let w = load_package_weights_lean(&dense, Some(&meta)).unwrap();
    let emb = w.require("token_embd.weight").unwrap();
    let d = emb.to_f32_owned().unwrap();
    println!("emb loaded len={} finite={} sample={:?}", d.len(), d.iter().all(|x| x.is_finite()), &d[..8]);
    // Compare first block dequant from SOURCE gguf token_embd if same layout
    let src = Path::new(r"/tmp/.green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf");
    if src.is_file() {
        let gs = read_gguf(src, false).unwrap();
        if let Some(t) = gs.tensors.iter().find(|t| t.name == "token_embd.weight") {
            println!("src emb: dims={:?} type={}", t.shape, t.ggml_type);
        }
    }
}
