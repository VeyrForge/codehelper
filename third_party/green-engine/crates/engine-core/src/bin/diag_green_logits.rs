fn main() {
    use engine_core::generate::load_forward_cached;
    use engine_core::green_model::{GreenModel, LoadConfig};
    use engine_core::tokenize::load_for_package;
    use std::path::PathBuf;

    let path = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&path, &LoadConfig { verify_checksums: false }).unwrap();
    let dense = &model.dense_weights.path;
    let meta = model.metadata.metadata_gguf.as_deref();
    let (fwd, _, _) = load_forward_cached(dense, meta, 0).unwrap();
    println!(
        "n_embd={} layers={} heads={} kv={} head_dim={} rope_theta={} freq_scale={} ctx={:?}",
        fwd.n_embd,
        fwd.n_layers,
        fwd.n_heads,
        fwd.n_kv_heads,
        fwd.head_dim,
        fwd.rope.theta,
        fwd.rope.freq_scale,
        fwd.rope.context_length
    );
    let tok = load_for_package(meta, model.tokenizer.path.as_deref()).unwrap();
    let prompt = "Explain gravity in one sentence.";
    let ids = tok.encode(prompt).unwrap();
    println!("ids={:?} len={}", ids, ids.len());
    for n in 1..=ids.len() {
        let prefix = &ids[..n];
        let logits = fwd.prefill_logits_f32_kv(prefix).unwrap();
        let mut best = 0usize;
        let mut bv = f32::NEG_INFINITY;
        for (i, &v) in logits.iter().enumerate() {
            if v > bv {
                bv = v;
                best = i;
            }
        }
        let out_path = format!(r"/tmp/.green/models/_green_logits_n{n}.f32");
        let bytes: Vec<u8> = logits.iter().flat_map(|v| v.to_le_bytes()).collect();
        std::fs::write(&out_path, bytes).unwrap();
        println!("n={n} top={best} max={bv:.4}");
    }
    let out = fwd.generate_greedy(&ids, 16).unwrap();
    println!("gen ids={out:?}");
}
