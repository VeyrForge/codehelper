use engine_core::gguf_load::{load_package_weights_lean, LeanTensor};
use engine_core::quant_mat::QuantMat;
use std::path::Path;

fn main() {
    let dense = Path::new(r"/tmp/.green/models/Llama-3.2-1B.green/dense.gguf");
    let meta = Path::new(r"/tmp/.green/models/Llama-3.2-1B.green/metadata.gguf");
    let w = load_package_weights_lean(dense, Some(meta)).unwrap();
    for name in ["blk.0.ffn_gate.weight", "blk.0.ffn_down.weight", "blk.0.attn_q.weight"] {
        let t = w.require(name).unwrap();
        let m = t.to_quant_mat().unwrap();
        let full = t.to_f32_owned().unwrap();
        assert_eq!(full.len(), m.in_dim * m.out_dim);
        // x = ones
        let x = vec![1.0f32; m.in_dim];
        let mut y_gemv = vec![0.0f32; m.out_dim];
        m.gemv(&x, &mut y_gemv);
        // reference: row-major (out,in) y[o]=sum_i W[o,i]*x[i]
        let mut y_ref = vec![0.0f32; m.out_dim];
        for o in 0..m.out_dim {
            let mut s = 0.0f32;
            let row = &full[o * m.in_dim..(o + 1) * m.in_dim];
            for i in 0..m.in_dim { s += row[i] * x[i]; }
            y_ref[o] = s;
        }
        let mut max = 0.0f32;
        let mut mean = 0.0f32;
        for o in 0..m.out_dim {
            let d = (y_gemv[o] - y_ref[o]).abs();
            max = max.max(d);
            mean += d;
        }
        mean /= m.out_dim as f32;
        println!("{name}: in={} out={} type={} max_err={max:.6e} mean_err={mean:.6e} y0_gemv={:.4} y0_ref={:.4}",
            m.in_dim, m.out_dim, m.ggml_type, y_gemv[0], y_ref[0]);
    }
}
