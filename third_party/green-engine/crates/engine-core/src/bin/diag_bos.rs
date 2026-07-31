use engine_core::generate::load_forward_cached;
use engine_core::green_model::{GreenModel, LoadConfig};
use engine_core::sample::SampleParams;
use std::path::PathBuf;

fn main() {
    let path = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).unwrap();
    let (fwd, _, _) = load_forward_cached(&model.dense_weights.path, model.metadata.metadata_gguf.as_deref(), 0).unwrap();
    let bos = vec![128000u32];
    let out = fwd.generate_paged(&bos, 1, 8, &[], &SampleParams::greedy()).unwrap();
    println!("native next after BOS={:?}", out.new_tokens);
}
