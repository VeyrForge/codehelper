//! Gravity-prefix prefill top token on Q4_K_M GGUF (Q6_K embedding sanity).
use engine_core::forward::DenseForward;
use std::env;
use std::path::PathBuf;

const IDS: [u32; 8] = [128000, 849, 21435, 24128, 304, 832, 11914, 13];

fn main() {
    let gguf = env::args()
        .nth(1)
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            PathBuf::from(env::var("USERPROFILE").unwrap_or_default())
                .join(".green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf")
        });
    let fwd = DenseForward::load(&gguf, Some(gguf.as_path())).expect("load");
    let logits = fwd.prefill_logits_f32_kv(&IDS).expect("prefill");
    let mut best = 0usize;
    let mut bv = f32::NEG_INFINITY;
    for (i, &v) in logits.iter().enumerate() {
        if v > bv {
            bv = v;
            best = i;
        }
    }
    println!("top {}", best);
    let log_path = std::env::temp_dir().join("ge_q4km_logits.f32");
    let nbytes = logits.len() * std::mem::size_of::<f32>();
    let bytes = unsafe { std::slice::from_raw_parts(logits.as_ptr() as *const u8, nbytes) };
    std::fs::write(&log_path, bytes).expect("write logits");
    println!("logits {}", log_path.display());
}
