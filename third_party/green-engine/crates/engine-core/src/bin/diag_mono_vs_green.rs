fn main() {
    use engine_core::forward::DenseForward;
    use std::path::Path;
    let ids = vec![128000u32, 849, 21435, 24128, 304, 832, 11914, 13];
    let mono = Path::new(r"/tmp/.green/models/_tmp_llama32_1b_q4_0.gguf");
    let green_d = Path::new(r"/tmp/.green/models/Llama-3.2-1B.green/dense.gguf");
    let green_m = Path::new(r"/tmp/.green/models/Llama-3.2-1B.green/metadata.gguf");
    let fm = DenseForward::load(mono, Some(mono)).unwrap();
    let fg = DenseForward::load(green_d, Some(green_m)).unwrap();
    println!("mono heads={} kv={} theta={}", fm.n_heads, fm.n_kv_heads, fm.rope.theta);
    println!("green heads={} kv={} theta={}", fg.n_heads, fg.n_kv_heads, fg.rope.theta);
    for n in [1usize, 2, 3, 4, 8] {
        let lm = fm.prefill_logits_f32_kv(&ids[..n]).unwrap();
        let lg = fg.prefill_logits_f32_kv(&ids[..n]).unwrap();
        let mut bm=0; let mut bmv=f32::NEG_INFINITY;
        let mut bg=0; let mut bgv=f32::NEG_INFINITY;
        let mut dot=0.0f64; let mut nm=0.0f64; let mut ng=0.0f64;
        for i in 0..lm.len() {
            let a = lm[i] as f64; let b = lg[i] as f64;
            dot += a*b; nm += a*a; ng += b*b;
            if lm[i] > bmv { bmv = lm[i]; bm = i; }
            if lg[i] > bgv { bgv = lg[i]; bg = i; }
        }
        let cos = dot / (nm.sqrt()*ng.sqrt()+1e-12);
        println!("n={n} mono_top={bm}({bmv:.3}) green_top={bg}({bgv:.3}) cos={cos:.6}");
    }
}
