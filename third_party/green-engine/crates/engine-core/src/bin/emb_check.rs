use engine_core::gguf_load::load_package_weights_lean;
use std::path::Path;

fn main() {
    let pkg = Path::new(r"/tmp/.green/models/Llama-3.2-1B.green/dense.gguf");
    let src = Path::new(r"/tmp/.green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf");
    let meta = Path::new(r"/tmp/.green/models/Llama-3.2-1B.green/metadata.gguf");
    let w = load_package_weights_lean(pkg, Some(meta)).unwrap();
    let emb = w.require("token_embd.weight").unwrap();
    let d = emb.to_f32_owned().unwrap();
    println!("pkg emb shape={:?} first8={:?}", emb.shape(), &d[..8]);

    let ws = load_package_weights_lean(src, None).unwrap();
    let es = ws.require("token_embd.weight").unwrap();
    let ds = es.to_f32_owned().unwrap();
    println!("src emb shape={:?} first8={:?}", es.shape(), &ds[..8]);
    let n = d.len().min(ds.len()).min(2048);
    let mut max = 0.0f32;
    for i in 0..n {
        max = max.max((d[i] - ds[i]).abs());
    }
    println!("token0 max_diff={max:.6e} (over {n} dims)");
    if d.len() >= 101 * 2048 && ds.len() >= 101 * 2048 {
        let mut max = 0.0f32;
        for i in 0..2048 {
            max = max.max((d[100 * 2048 + i] - ds[100 * 2048 + i]).abs());
        }
        println!("token100 max_diff={max:.6e}");
    }
}
