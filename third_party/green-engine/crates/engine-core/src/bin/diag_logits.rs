use engine_core::forward::DenseForward;
use std::path::Path;
fn main() {
    let mono = Path::new(r"/tmp/.green/models/_tmp_llama32_1b_q4_0.gguf");
    let fwd = DenseForward::load(mono, Some(mono)).unwrap();
    let ids = vec![128000u32, 849, 21435, 24128, 304, 832, 11914, 13];
    let logits = fwd.prefill_logits_f32_kv(&ids).unwrap();
    let mut best=0usize; let mut bv=f32::NEG_INFINITY;
    for (i,&v) in logits.iter().enumerate() { if v>bv { bv=v; best=i; } }
    println!("mono top={best} best={bv:.4} G={:.4} I={:.4}", logits[48590], logits[358]);
}
