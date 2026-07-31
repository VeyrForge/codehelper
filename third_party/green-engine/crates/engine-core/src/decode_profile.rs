//! Decode timing breakdown when `GE_DECODE_PROFILE=1`.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::OnceLock;

#[derive(Default)]
pub struct ProfileAccum {
    pub qkv_ns: AtomicU64,
    pub rope_ns: AtomicU64,
    pub kv_append_ns: AtomicU64,
    pub kv_recall_ns: AtomicU64,
    pub attn_ns: AtomicU64,
    pub wo_ns: AtomicU64,
    pub ffn_ns: AtomicU64,
    pub lm_head_ns: AtomicU64,
    pub tokens: AtomicU64,
}

static PROFILE: OnceLock<ProfileAccum> = OnceLock::new();

pub fn enabled() -> bool {
    static ON: OnceLock<bool> = OnceLock::new();
    *ON.get_or_init(|| std::env::var("GE_DECODE_PROFILE").ok().as_deref() == Some("1"))
}

pub fn accum() -> &'static ProfileAccum {
    PROFILE.get_or_init(ProfileAccum::default)
}

pub fn reset() {
    if !enabled() {
        return;
    }
    let a = accum();
    a.qkv_ns.store(0, Ordering::Relaxed);
    a.rope_ns.store(0, Ordering::Relaxed);
    a.kv_append_ns.store(0, Ordering::Relaxed);
    a.kv_recall_ns.store(0, Ordering::Relaxed);
    a.attn_ns.store(0, Ordering::Relaxed);
    a.wo_ns.store(0, Ordering::Relaxed);
    a.ffn_ns.store(0, Ordering::Relaxed);
    a.lm_head_ns.store(0, Ordering::Relaxed);
    a.tokens.store(0, Ordering::Relaxed);
}

pub fn add_ns(slot: &AtomicU64, ns: u128) {
    if enabled() {
        slot.fetch_add(ns as u64, Ordering::Relaxed);
    }
}

pub fn report() {
    if !enabled() {
        return;
    }
    let a = accum();
    let tok = a.tokens.load(Ordering::Relaxed).max(1);
    let qkv = a.qkv_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let rope = a.rope_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let kv_a = a.kv_append_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let kv_r = a.kv_recall_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let attn = a.attn_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let wo = a.wo_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let ffn = a.ffn_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let lm = a.lm_head_ns.load(Ordering::Relaxed) as f64 / tok as f64;
    let total = qkv + rope + kv_a + kv_r + attn + wo + ffn + lm;
    if total <= 0.0 {
        return;
    }
    let pct = |x: f64| x * 100.0 / total;
    println!(
        "decode_profile: tokens={tok} avg_us/tok total={:.0} qkv={:.0}({:.1}%) rope={:.0}({:.1}%) kv_append={:.0}({:.1}%) kv_recall={:.0}({:.1}%) attn={:.0}({:.1}%) wo={:.0}({:.1}%) ffn={:.0}({:.1}%) lm_head={:.0}({:.1}%)",
        total / 1000.0,
        qkv / 1000.0, pct(qkv),
        rope / 1000.0, pct(rope),
        kv_a / 1000.0, pct(kv_a),
        kv_r / 1000.0, pct(kv_r),
        attn / 1000.0, pct(attn),
        wo / 1000.0, pct(wo),
        ffn / 1000.0, pct(ffn),
        lm / 1000.0, pct(lm),
    );
}
