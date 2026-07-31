use engine_core::generate::load_forward_cached;
use engine_core::green_model::{GreenModel, LoadConfig};
use engine_core::gguf_load::load_package_weights_lean;
use std::path::PathBuf;
fn main() {
    let path = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).unwrap();
    let w = load_package_weights_lean(&model.dense_weights.path, model.metadata.metadata_gguf.as_deref()).unwrap();
    for name in ["blk.0.attn_norm.weight","blk.0.ffn_norm.weight","output_norm.weight"] {
        let t = w.require(name).unwrap();
        let d = t.to_f32_owned().unwrap();
        let mean = d.iter().sum::<f32>()/d.len() as f32;
        let mn = d.iter().cloned().fold(f32::INFINITY, f32::min);
        let mx = d.iter().cloned().fold(f32::NEG_INFINITY, f32::max);
        println!("{name}: len={} mean={mean:.4} min={mn:.4} max={mx:.4} first3={:?}", d.len(), &d[..3]);
    }
}
