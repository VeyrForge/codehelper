#!/usr/bin/env python3
"""Master recovery: KV merge + parallel decode + resident warm serve.

Single command::

    python scripts/recover_engine_core.py --full

CI regression (patch + build + tests + bench gate)::

    powershell -NoProfile -ExecutionPolicy Bypass -File scripts/ci_regression.ps1

Runs `recover_engine_core.py --full`, release `engine-core` tests, optional diag
binaries, `decode_1b_bench` n=32 with `GE_BENCH_IGNORE_EOS=1` (fail if warm decode
< 15 tok/s), and optional `scripts/run_fair_compare.py` when llama-cpp-python is installed.

Ordered steps (Python UTF-8 writes for Rust; never StrReplace forward.rs in-editor):
  1. git checkout 3bd7f37 -- crates/engine-core/src/
  2. prune orphan src/ files
  3. patch_atomic_merge (quant_kernels, generate, lib)
  4. patch_all.py (KvStore::recall_f32_into + Q8 keys + attention helpers)
  5. patch_parallel_support + patch_parallel_forward (inlined decode_pool, parallel attn)
  6. scripts/install_resident.py --after-merge + patch_forward (DecodeSession)
  7. score_cap borrow fix if missing; GE_BENCH_IGNORE_EOS for bench
  8. recover-safe VNNI: patch_qk + patch_qm (no full _vnni_once)
  9. forward_token shared-act quant + P1 greedy lm_head argmax (no 128k logits vec)
  9b. Q6_K dequant scale indexing (gguf_load + quant_mat); GE_GEMV_Q8 gated to Q4_0 only
 10. P0.1–P0.4 ggml row GEMV + pool nesting + lm_head argmax_gemv_act
 11. Q4_0 8×8 repack at load + repacked AVX2/VNNI GEMV (patch_repack_gemv.py)
 11. cargo build --release -p ge (+ --features gpu when CUDA/kernels present)
 11. cargo test -p engine-core --lib; native_generate_smoke
 12. decode_1b_bench warm n=32 GE_BENCH_IGNORE_EOS=1
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import os
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
EC = REPO / "crates" / "engine-core"
SRC = EC / "src"
FWD = SRC / "forward.rs"
PY = sys.executable
BASE = "3bd7f37"

DECODE_PROFILE_RS = r'''//! Decode timing breakdown when `GE_DECODE_PROFILE=1`.

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
'''

GEMV_DOT_MICROBENCH_RS = r'''//! Isolated Q4_0 block-dot + lm_head row GEMV microbench.
use std::time::Instant;
use engine_core::quant_kernels::{
    self, gemv_q4_0_row_f32, gemv_q4_0_row_q8, q4_0_block_dot_f32, q4_0_block_dot_q8, ActQ8,
    Q4_0_BLOCK, Q4_0_BYTES,
};
fn pack_block(scale_bits: u16, nibbles: &[u8; 16]) -> [u8; Q4_0_BYTES] {
    let mut b = [0u8; Q4_0_BYTES];
    b[0] = (scale_bits & 0xff) as u8;
    b[1] = (scale_bits >> 8) as u8;
    b[2..18].copy_from_slice(nibbles);
    b
}
fn bench<F: Fn() -> f32>(iters: usize, f: F) -> f64 {
    for _ in 0..100 { let _ = f(); }
    let t0 = Instant::now();
    for _ in 0..iters { let _ = f(); }
    iters as f64 / t0.elapsed().as_secs_f64()
}
fn main() {
    let mut nib = [0u8; 16];
    for j in 0..16 { nib[j] = ((j as u8) & 0x0f) | ((((j + 5) as u8) & 0x0f) << 4); }
    let block = pack_block(0x3400, &nib);
    let x: Vec<f32> = (0..Q4_0_BLOCK).map(|i| (i as f32) * 0.07 - 1.1).collect();
    let act = ActQ8::quantize(&x);
    let qs = &act.qs[..Q4_0_BLOCK];
    let act_scale = act.scales[0];
    let in_dim = 2048usize;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut packed_row = vec![0u8; n_blocks * Q4_0_BYTES];
    for b in 0..n_blocks {
        packed_row[b * Q4_0_BYTES..(b + 1) * Q4_0_BYTES].copy_from_slice(&block);
    }
    let row_x: Vec<f32> = (0..in_dim).map(|i| (i as f32) * 0.01 - 10.0).collect();
    let row_act = ActQ8::quantize(&row_x);
    let block_iters = 500_000;
    let row_iters = 80_000;
    let sps_f32 = bench(block_iters, || q4_0_block_dot_f32(&block, &x));
    let sps_q8 = bench(block_iters, || q4_0_block_dot_q8(&block, qs, act_scale));
    let row_f32 = bench(row_iters, || gemv_q4_0_row_f32(&packed_row, &row_x, in_dim));
    let row_q8 = bench(row_iters, || gemv_q4_0_row_q8(&packed_row, &row_act));
    let block_ratio = sps_q8 / sps_f32.max(1.0);
    let row_ratio = row_q8 / row_f32.max(1.0);
    println!("gemv_isa={} vnni={}", quant_kernels::detect_gemv_isa().as_str(), quant_kernels::has_avx_vnni());
    println!("block_dots_per_s: f32_simd={:.0} q8_simd={:.0} ratio={:.2}x", sps_f32, sps_q8, block_ratio);
    println!("row_dots_per_s: f32_simd={:.0} q8_simd={:.0} ratio={:.2}x row_f32_us={:.2} row_q8_us={:.2}",
        row_f32, row_q8, row_ratio, 1e6 / row_f32, 1e6 / row_q8);
    println!("recommend GE_LM_HEAD_Q8=1 when row_q8 >= row_f32 (row_ratio={:.2}x)", row_ratio);
    if row_ratio < 1.0 {
        eprintln!("WARN: row Q8 GEMV slower than f32 (row_ratio={row_ratio:.2}x); default GE_LM_HEAD_Q8=0");
    }
}
'''


DIAG_Q4KM_PREFILL_RS = r'''//! Gravity-prefix prefill top token on Q4_K_M GGUF (Q6_K embedding sanity).
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
'''


def run(cmd: list[str], *, label: str | None = None, env: dict | None = None) -> None:
    tag = label or " ".join(cmd)
    print(f"\n=== RUN {tag} ===", flush=True)
    rc = subprocess.call(cmd, cwd=REPO, env=env)
    if rc != 0:
        raise SystemExit(rc)


def run_capture(cmd: list[str], *, label: str | None = None, env: dict | None = None) -> str:
    tag = label or " ".join(cmd)
    print(f"\n=== RUN {tag} ===", flush=True)
    out = subprocess.check_output(cmd, cwd=REPO, env=env, text=True, encoding="utf-8", errors="replace")
    print(out, end="", flush=True)
    return out


def prune_orphan_src() -> None:
    listed = set(
        subprocess.check_output(
            ["git", "ls-tree", "-r", "--name-only", BASE, "crates/engine-core/src/"],
            cwd=REPO,
            text=True,
            encoding="utf-8",
        ).splitlines()
    )
    for path in sorted(SRC.rglob("*")):
        if not path.is_file():
            continue
        rel = path.relative_to(REPO).as_posix()
        if rel not in listed:
            path.unlink()
            print(f"removed orphan {rel}", flush=True)
    lib = SRC / "lib.rs"
    if lib.is_file() and not (SRC / "gpu_layer.rs").is_file():
        text = lib.read_text(encoding="utf-8")
        cleaned = text.replace("pub mod gpu_layer;\n", "").replace(
            "#[cfg(feature = \"gpu\")]\npub mod gpu_gemv;\n", ""
        )
        if cleaned != text:
            lib.write_bytes(cleaned.encode("utf-8"))
            print("stripped orphan gpu_layer/gpu_gemv from lib.rs", flush=True)


def prune_orphan_cargo_bins() -> None:
    """Drop [[bin]] stanzas in Cargo.toml when src/bin/*.rs is missing (blocks cargo test)."""
    cargo_p = EC / "Cargo.toml"
    cargo = cargo_p.read_text(encoding="utf-8")
    lines = cargo.splitlines(keepends=True)
    out: list[str] = []
    i = 0
    removed = 0
    while i < len(lines):
        if lines[i].strip() == "[[bin]]":
            block_end = i + 1
            while block_end < len(lines) and not (
                lines[block_end].startswith("[[") and block_end > i
            ):
                block_end += 1
            block = "".join(lines[i:block_end])
            path_m = re.search(r'path = "([^"]+)"', block)
            if path_m:
                bin_src = EC / path_m.group(1)
                if not bin_src.is_file():
                    removed += 1
                    print(f"pruned orphan bin {path_m.group(1)} from Cargo.toml", flush=True)
                    i = block_end
                    continue
            out.extend(lines[i:block_end])
            i = block_end
        else:
            out.append(lines[i])
            i += 1
    if removed:
        cargo_p.write_bytes("".join(out).encode("utf-8"))


def load_install_resident():
    spec = importlib.util.spec_from_file_location(
        "install_resident", REPO / "scripts" / "install_resident.py"
    )
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


def patch_forward_kv_dyn(text: str) -> str:
    """Coerce session.kv to &mut dyn KvStore for forward_token; keep concrete kv for metrics."""
    replacements = [
        (
            "        let kv: &mut dyn KvStore = &mut session.kv;",
            "        let kv_dyn: &mut dyn KvStore = &mut session.kv;",
        ),
        (
            "        let kv = &mut session.kv;",
            "        let kv_dyn: &mut dyn KvStore = &mut session.kv;",
        ),
        ("self.forward_token(token_pos, kv, scratch)", "self.forward_token(token_pos, kv_dyn, scratch)"),
        ("kv_metrics: kv.metrics()", "kv_metrics: session.kv.metrics()"),
        ("kv_seq_len: kv.seq_len()", "kv_seq_len: session.kv.seq_len()"),
        ("kv_hot_cap: kv.hot_cap()", "kv_hot_cap: session.kv.hot_cap()"),
        ("kv_hot_bytes: kv.hot_bytes()", "kv_hot_bytes: session.kv.hot_bytes()"),
    ]
    for old, new in replacements:
        if old in text:
            text = text.replace(old, new)
    return text


def patch_forward_decode_session() -> None:
    ir = load_install_resident()
    fwd = FWD.read_text(encoding="utf-8")
    patched = patch_forward_kv_dyn(ir.patch_forward(fwd))
    if "pub struct DecodeSession" not in patched:
        raise SystemExit("patch_forward failed: DecodeSession struct missing")
    if "let kv_dyn: &mut dyn KvStore = &mut session.kv;" not in patched:
        raise SystemExit("patch_forward failed: session.kv dyn coercion missing")
    if "kv_metrics: session.kv.metrics()" not in patched:
        raise SystemExit("patch_forward failed: session.kv metrics wiring missing")
    if patched != fwd:
        FWD.write_bytes(patched.encode("utf-8"))
        print("forward.rs: DecodeSession wired", flush=True)
    else:
        print("forward.rs: DecodeSession already present", flush=True)


def apply_score_cap_borrow_fix_on(fwd: str) -> tuple[str, bool]:
    """Insert score_cap binding and replace inline scratch.attn.score_cap() in parallel attn."""
    changed = False
    inline_variants = [
        (
            "                &mut scratch.attn.head_scores,\n"
            "                scratch.attn.score_cap(),\n"
            "                &mut scratch.attn.attn,",
            "                &mut scratch.attn.head_scores,\n"
            "                score_cap,\n"
            "                &mut scratch.attn.attn,",
        ),
        (
            "                &mut scratch.attn.mha_per_kv, &mut scratch.attn.head_scores,\n"
            "                scratch.attn.score_cap(), &mut scratch.attn.attn,",
            "                &mut scratch.attn.mha_per_kv, &mut scratch.attn.head_scores,\n"
            "                score_cap, &mut scratch.attn.attn,",
        ),
    ]
    for old, new in inline_variants:
        if old in fwd:
            fwd = fwd.replace(old, new, 1)
            changed = True

    anchor = "            scratch.attn.ensure_parallel(resident, self.head_dim);\n"
    let_line = "            let score_cap = scratch.attn.score_cap();\n"
    if anchor in fwd and "multi_head_attend_decode_parallel" in fwd:
        parts = fwd.split(anchor, 1)
        before, after = parts[0], parts[1]
        pre_call = after.split("multi_head_attend_decode_parallel", 1)[0]
        if let_line.strip() not in pre_call:
            fwd = before + anchor + let_line + after
            changed = True

    return fwd, changed


def apply_score_cap_borrow_fix() -> None:
    fwd = FWD.read_text(encoding="utf-8")
    if "multi_head_attend_decode_parallel" not in fwd:
        print("forward.rs: parallel attn block not found (skip score_cap fix)", flush=True)
        return
    patched, changed = apply_score_cap_borrow_fix_on(fwd)
    if "scratch.attn.score_cap()," in patched:
        raise SystemExit(
            "forward.rs: score_cap borrow fix incomplete (inline scratch.attn.score_cap() remains)"
        )
    if not changed:
        print("forward.rs: score_cap borrow fix already applied", flush=True)
        return
    FWD.write_bytes(patched.encode("utf-8"))
    print("forward.rs: score_cap borrow fix applied", flush=True)


def normalize_forward_utf8() -> str:
    """Force forward.rs to UTF-8 (no BOM). Auto-repair UTF-16 from editors/Set-Content."""
    raw = FWD.read_bytes()
    kind = "utf-8"
    if raw.startswith(b"\xff\xfe"):
        text = raw[2:].decode("utf-16-le")
        kind = "utf-16-le-bom"
    elif raw.startswith(b"\xfe\xff"):
        text = raw[2:].decode("utf-16-be")
        kind = "utf-16-be-bom"
    elif len(raw) > 8 and raw[0] != 0 and raw[1] == 0 and raw[2] != 0 and raw[3] == 0:
        text = raw.decode("utf-16-le")
        kind = "utf-16-le-nobom"
    else:
        if raw.startswith(b"\xef\xbb\xbf"):
            raw = raw[3:]
            kind = "utf-8-bom-stripped"
        text = raw.decode("utf-8")
    text = text.replace("\u2192", "->").replace("\u2014", "-").replace("\u2013", "-")
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    if not text.endswith("\n"):
        text += "\n"
    FWD.write_bytes(text.encode("utf-8"))
    digest = hashlib.sha256(FWD.read_bytes()).hexdigest()
    print(f"forward.rs: UTF-8 OK ({kind}) sha256={digest}", flush=True)
    return digest




def patch_bench_ignore_eos() -> None:
    gen_p = SRC / "generate.rs"
    gen = gen_p.read_text(encoding="utf-8")
    if "GE_BENCH_IGNORE_EOS" in gen:
        print("generate.rs: GE_BENCH_IGNORE_EOS already present", flush=True)
        return
    old = "    let stops = stop_token_ids(&tok);"
    new = """    let stops = if std::env::var("GE_BENCH_IGNORE_EOS").ok().as_deref() == Some("1") {
        Vec::new()
    } else {
        stop_token_ids(&tok)
    };"""
    if old not in gen:
        print("generate.rs: stop_token_ids anchor missing (skip GE_BENCH_IGNORE_EOS)", flush=True)
        return
    gen_p.write_bytes(gen.replace(old, new, 1).encode("utf-8"))
    print("generate.rs: GE_BENCH_IGNORE_EOS hook added", flush=True)


def apply_decode_opt_quant_mat() -> None:
    """P2: GemvActScratch + gemv_with_act + gemv_use_q8_act + shared GEMV helpers."""
    qm_p = SRC / "quant_mat.rs"
    qm = qm_p.read_text(encoding="utf-8")
    changed = False

    if "pub struct GemvActScratch" not in qm:
        qm = qm.replace(
            "pub const GGML_Q6_K: u32 = 14;\n\n/// 2D weight matrix",
            "pub const GGML_Q6_K: u32 = 14;\n\n"
            "pub struct GemvActScratch { pub act: ActQ8 }\n"
            "impl GemvActScratch {\n"
            "    pub fn new(in_dim: usize) -> Self {\n"
            "        let n = in_dim / Q4_0_BLOCK;\n"
            "        GemvActScratch {\n"
            "            act: ActQ8 {\n"
            "                scales: Vec::with_capacity(n),\n"
            "                qs: Vec::with_capacity(n * Q4_0_BLOCK),\n"
            "            },\n"
            "        }\n"
            "    }\n"
            "    pub fn quantize_into(&mut self, x: &[f32]) { self.act.quantize_into(x); }\n"
            "}\n\n/// 2D weight matrix",
        )
        changed = True
        print("quant_mat.rs: GemvActScratch added", flush=True)

    if "pub fn gemv_with_act" not in qm:
        qm = qm.replace(
            "    /// Materialize full f32 (tests / tiny models). Prefer [`Self::gemv`] on 1B+.",
            "    pub fn gemv_with_act(&self, act: &GemvActScratch, y: &mut [f32]) {\n"
            "        assert_eq!(y.len(), self.out_dim);\n"
            "        if self.ggml_type == GGML_Q4_0 && gemv_use_q8_act() && self.in_dim % Q4_0_BLOCK == 0 {\n"
            "            let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;\n"
            "            gemv_rows_parallel(self.out_dim, y, |o, yo| {\n"
            "                *yo = quant_kernels::gemv_q4_0_row_q8(&self.packed[o * rb..(o + 1) * rb], &act.act);\n"
            "            });\n"
            "        } else {\n"
            "            y.fill(0.0);\n"
            "        }\n"
            "    }\n\n"
            "    /// Materialize full f32 (tests / tiny models). Prefer [`Self::gemv`] on 1B+.",
        )
        changed = True
        print("quant_mat.rs: gemv_with_act added", flush=True)

    if "pub fn gemv_use_q8_act" not in qm:
        qm = qm.replace(
            "fn gemv_isa() -> GemvIsa {",
            "pub fn gemv_use_q8_act() -> bool {\n"
            "    if gemv_force_f32_env() { return false; }\n"
            "    if !quant_kernels::has_avx_vnni() { return false; }\n"
            "    match std::env::var(\"GE_GEMV_Q8\") {\n"
            "        Ok(v) if matches!(v.as_str(), \"0\" | \"false\" | \"FALSE\" | \"no\" | \"NO\") => false,\n"
            "        _ => true,\n"
            "    }\n"
            "}\n\nfn gemv_isa() -> GemvIsa {",
        )
        changed = True
        print("quant_mat.rs: gemv_use_q8_act added", flush=True)

    qm2, n = re.subn(
        r"let use_q8 = gemv_q8_act_env\(\)[\s\S]*?&& in_dim >= Q4_0_BLOCK;",
        "let use_q8 = gemv_use_q8_act() && in_dim >= Q4_0_BLOCK;",
        qm,
        count=1,
    )
    if n:
        qm = qm2
        changed = True

    if "pub fn project_qkv_quant_shared" not in qm:
        extra = """
pub fn project_qkv_quant_shared(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, x: &[f32], act: &GemvActScratch,
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    if gemv_use_q8_act() && wq.in_dim % Q4_0_BLOCK == 0 {
        wq.gemv_with_act(act, q);
        sys::decode_pool().install(|| {
            rayon::join(|| wk.gemv_with_act(act, k), || wv.gemv_with_act(act, v));
        });
    } else {
        project_qkv_quant(wq, wk, wv, x, q, k, v);
    }
}

pub fn swiglu_quant_shared(
    act: &GemvActScratch, gate: &QuantMat, up: &QuantMat, down: &QuantMat,
    g: &mut [f32], u: &mut [f32], out: &mut [f32],
) {
    sys::decode_pool().install(|| {
        rayon::join(|| gate.gemv_with_act(act, g), || up.gemv_with_act(act, u));
    });
    for j in 0..g.len() {
        g[j] = silu(g[j]) * u[j];
    }
    down.gemv(g, out);
}

"""
        qm = qm.replace("/// SiLU for SwiGLU.", extra + "/// SiLU for SwiGLU.")
        changed = True
        print("quant_mat.rs: shared GEMV helpers added", flush=True)

    if changed:
        qm_p.write_bytes(qm.encode("utf-8"))
    else:
        print("quant_mat.rs: decode opt P2 already present", flush=True)


def apply_decode_opt() -> None:
    """P1-P3: greedy argmax + shared activation quant on recovered parallel forward."""
    sm_p = SRC / "sample.rs"
    sm = sm_p.read_text(encoding="utf-8")
    if "needs_full_logits" not in sm:
        old = (
            "    pub fn is_greedy(&self) -> bool {\n"
            "        self.temperature <= 0.0\n"
            "    }\n}"
        )
        new = (
            "    pub fn is_greedy(&self) -> bool {\n"
            "        self.temperature <= 0.0\n"
            "    }\n\n"
            "    pub fn needs_full_logits(&self) -> bool {\n"
            "        if self.is_greedy() {\n"
            "            (self.repetition_penalty - 1.0).abs() > 1e-6\n"
            "                || self.presence_penalty.abs() > 1e-6\n"
            "                || self.frequency_penalty.abs() > 1e-6\n"
            "        } else {\n"
            "            true\n"
            "        }\n"
            "    }\n}"
        )
        if old not in sm:
            raise SystemExit("sample.rs: is_greedy anchor missing (cannot add needs_full_logits)")
        sm_p.write_bytes(sm.replace(old, new, 1).encode("utf-8"))
        print("sample.rs: needs_full_logits added", flush=True)
    else:
        print("sample.rs: needs_full_logits already present", flush=True)

    apply_decode_opt_quant_mat()

    fwd = FWD.read_text(encoding="utf-8")
    changed = False

    for old_imp, new_imp in [
        (
            "use crate::quant_mat::{project_qkv_quant, swiglu_quant, QuantMat};\nuse crate::sys;",
            "use crate::quant_mat::{\n"
            "    gemv_use_q8_act, project_qkv_quant, project_qkv_quant_shared, swiglu_quant,\n"
            "    swiglu_quant_shared, GemvActScratch, QuantMat,\n"
            "};\nuse crate::sys;",
        ),
        (
            "use crate::quant_mat::{project_qkv_quant, swiglu_quant, QuantMat};",
            "use crate::quant_mat::{\n"
            "    gemv_use_q8_act, project_qkv_quant, project_qkv_quant_shared, swiglu_quant,\n"
            "    swiglu_quant_shared, GemvActScratch, QuantMat,\n"
            "};",
        ),
    ]:
        if old_imp in fwd:
            fwd = fwd.replace(old_imp, new_imp, 1)
            changed = True

    if "gemv_act: GemvActScratch" not in fwd:
        fwd = fwd.replace(
            "    norm_tmp: Vec<f32>,\n    past_k: Vec<f32>,",
            "    norm_tmp: Vec<f32>,\n    probs: Vec<f32>,\n    gemv_act: GemvActScratch,\n    past_k: Vec<f32>,",
        )
        fwd = fwd.replace(
            "            norm_tmp: vec![0.0; hidden],\n            past_k: Vec::new(),",
            "            norm_tmp: vec![0.0; hidden],\n"
            "            probs: vec![0.0; n_vocab],\n"
            "            gemv_act: GemvActScratch::new(hidden),\n"
            "            past_k: Vec::new(),",
        )
        changed = True
        print("forward.rs: gemv_act scratch added", flush=True)

    qkv_old = (
        "            project_qkv_quant(\n"
        "                &layer.wq, &layer.wk, &layer.wv, &scratch.attn.xn,\n"
        "                &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,\n"
        "            );"
    )
    qkv_new = (
        "            if gemv_use_q8_act() {\n"
        "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
        "                project_qkv_quant_shared(\n"
        "                    &layer.wq, &layer.wk, &layer.wv, &scratch.attn.xn, &scratch.gemv_act,\n"
        "                    &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,\n"
        "                );\n"
        "            } else {\n"
        "                project_qkv_quant(\n"
        "                    &layer.wq, &layer.wk, &layer.wv, &scratch.attn.xn,\n"
        "                    &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,\n"
        "                );\n"
        "            }"
    )
    if qkv_old in fwd:
        fwd = fwd.replace(qkv_old, qkv_new, 1)
        changed = True
        print("forward.rs: shared QKV projection patched", flush=True)

    ffn_old = (
        "            swiglu_quant(\n"
        "                &scratch.attn.xn,\n"
        "                &layer.gate,\n"
        "                &layer.up,\n"
        "                &layer.down,\n"
        "                &mut scratch.ffn_g,\n"
        "                &mut scratch.ffn_u,\n"
        "                &mut scratch.ffn_out,\n"
        "            );"
    )
    ffn_new = (
        "            if gemv_use_q8_act() {\n"
        "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
        "                swiglu_quant_shared(\n"
        "                    &scratch.gemv_act, &layer.gate, &layer.up, &layer.down,\n"
        "                    &mut scratch.ffn_g, &mut scratch.ffn_u, &mut scratch.ffn_out,\n"
        "                );\n"
        "            } else {\n"
        "                swiglu_quant(\n"
        "                    &scratch.attn.xn,\n"
        "                    &layer.gate,\n"
        "                    &layer.up,\n"
        "                    &layer.down,\n"
        "                    &mut scratch.ffn_g,\n"
        "                    &mut scratch.ffn_u,\n"
        "                    &mut scratch.ffn_out,\n"
        "                );\n"
        "            }"
    )
    if ffn_old in fwd:
        fwd = fwd.replace(ffn_old, ffn_new, 1)
        changed = True
        print("forward.rs: shared SwiGLU patched", flush=True)

    if "logits_argmax_greedy" not in fwd:
        anchor = "    fn compute_logits("
        insert = """    fn logits_argmax_greedy(
        &self,
        hidden: &[f32],
        norm_tmp: &mut [f32],
    ) -> Result<u32, ForwardError> {
        let h = if let Some(ref wn) = self.output_norm {
            rms_norm(hidden, wn, self.rms_eps, norm_tmp);
            norm_tmp.as_ref()
        } else {
            hidden
        };
        let table = if self.tied_output {
            self.emb.weight.as_slice()
        } else {
            self.output.as_slice()
        };
        let n_embd = self.n_embd;
        let n_vocab = self.n_vocab;
        let (_best_score, best_id) = sys::decode_pool().install(|| {
            use rayon::prelude::*;
            (0..n_vocab)
                .into_par_iter()
                .map(|v| {
                    let row = &table[v * n_embd..(v + 1) * n_embd];
                    let mut s = 0.0f32;
                    let mut i = 0usize;
                    while i + 4 <= n_embd {
                        s += h[i] * row[i]
                            + h[i + 1] * row[i + 1]
                            + h[i + 2] * row[i + 2]
                            + h[i + 3] * row[i + 3];
                        i += 4;
                    }
                    while i < n_embd {
                        s += h[i] * row[i];
                        i += 1;
                    }
                    (s, v as u32)
                })
                .reduce(
                    || (f32::NEG_INFINITY, 0u32),
                    |a, b| if a.0 > b.0 { a } else { b },
                )
        });
        Ok(best_id)
    }

"""
        if anchor not in fwd:
            raise SystemExit("forward.rs: compute_logits anchor missing (cannot add logits_argmax_greedy)")
        fwd = fwd.replace(anchor, insert + anchor, 1)
        changed = True
        print("forward.rs: logits_argmax_greedy added", flush=True)

    decode_old = (
        "        let mut probs = vec![0.0f32; self.n_vocab];\n\n"
        "        let mut generated = Vec::with_capacity(max_new);\n"
        "        let mut first_token_secs = 0.0f64;\n"
        "        let t_decode = Instant::now();\n"
        "        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));\n"
        "        for _ in 0..max_new {\n"
        "            self.compute_logits(&scratch.hidden, &mut scratch.logits, &mut scratch.norm_tmp)?;\n"
        "            let next = crate::sample::sample_token(\n"
        "                &mut scratch.logits,\n"
        "                &history,\n"
        "                sample,\n"
        "                &mut rng,\n"
        "                &mut probs,\n"
        "            );"
    )
    decode_new = (
        "        let mut generated = Vec::with_capacity(max_new);\n"
        "        let mut first_token_secs = 0.0f64;\n"
        "        let t_decode = Instant::now();\n"
        "        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));\n"
        "        for _ in 0..max_new {\n"
        "            let next = if sample.needs_full_logits() {\n"
        "                self.compute_logits(&scratch.hidden, &mut scratch.logits, &mut scratch.norm_tmp)?;\n"
        "                crate::sample::sample_token(\n"
        "                    &mut scratch.logits,\n"
        "                    &history,\n"
        "                    sample,\n"
        "                    &mut rng,\n"
        "                    &mut scratch.probs,\n"
        "                )\n"
        "            } else {\n"
        "                self.logits_argmax_greedy(&scratch.hidden, &mut scratch.norm_tmp)?\n"
        "            };"
    )
    if decode_old in fwd:
        fwd = fwd.replace(decode_old, decode_new, 1)
        changed = True
        print("forward.rs: greedy argmax decode loop patched", flush=True)
    elif "needs_full_logits()" in fwd:
        print("forward.rs: greedy argmax decode loop already patched", flush=True)
    else:
        print("forward.rs: decode loop anchor missing (skip greedy argmax loop)", flush=True)

    if changed:
        FWD.write_bytes(fwd.encode("utf-8"))


apply_decode_opt_p1 = apply_decode_opt


def patch_qk(qk: str) -> str:
    """VNNI dpbusd kernels + dispatch (recover-safe; from _apply_vnni_pass)."""
    if "pub fn has_avx_vnni" not in qk:
        qk = qk.replace(
            "/// One-time ISA probe for fused Q4 GEMV.\npub fn detect_gemv_isa()",
            '''pub fn has_avx_vnni() -> bool {
    #[cfg(target_arch = "x86_64")] {
        if std::is_x86_feature_detected!("avxvnni") { return true; }
        if std::is_x86_feature_detected!("avx512vnni") && std::is_x86_feature_detected!("avx512vl") { return true; }
    }
    false
}

/// One-time ISA probe for fused Q4 GEMV.
pub fn detect_gemv_isa()''',
        )
    qk = re.sub(
        r"#\[inline\(always\)\]\npub fn q4_0_block_dot_q8\(.*?\n\}\n\n/// Q4_K",
        '''#[inline(always)]
pub fn q4_0_block_dot_q8(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    #[cfg(target_arch = "x86_64")] {
        if is_x86_feature_detected!("avxvnni") {
            unsafe { return q4_0_block_dot_q8_avxvnni(block, qs, act_scale); }
        }
        if is_x86_feature_detected!("avx512vnni") && is_x86_feature_detected!("avx512vl") {
            unsafe { return q4_0_block_dot_q8_avx512vnni(block, qs, act_scale); }
        }
        if is_x86_feature_detected!("avx2") {
            unsafe { return q4_0_block_dot_q8_avx2(block, qs, act_scale); }
        }
    }
    q4_0_block_dot_q8_scalar(block, qs, act_scale)
}

/// Q4_K''',
        qk,
        count=1,
        flags=re.S,
    )
    anchor = (
        '#[cfg(target_arch = "x86_64")]\n'
        '#[target_feature(enable = "avx2")]\n'
        'unsafe fn q4_0_block_dot_q8_avx2(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {'
    )
    if "unsafe fn q4_0_block_dot_q8_avxvnni" not in qk and anchor in qk:
        ins = '''#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn bytes_from_nibbles_32(qs_ptr: *const u8) -> std::arch::x86_64::__m256i {
    use std::arch::x86_64::*;
    let tmp = _mm_loadu_si128(qs_ptr as *const __m128i);
    _mm256_and_si256(_mm256_set1_epi8(0x0f), _mm256_set_m128i(_mm_srli_epi16(tmp, 4), tmp))
}
#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn hsum_epi32_256(v: std::arch::x86_64::__m256i) -> i32 {
    use std::arch::x86_64::*;
    let s = _mm_add_epi32(_mm256_castsi256_si128(v), _mm256_extracti128_si256(v, 1));
    let s = _mm_add_epi32(s, _mm_shuffle_epi32(s, 0xEE));
    _mm_cvtsi128_si32(_mm_add_epi32(s, _mm_shuffle_epi32(s, 0x01)))
}
#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "avxvnni")]
unsafe fn q4_0_block_dot_q8_avxvnni(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let w = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let ax = _mm256_sign_epi8(qx, qx);
    let sy = _mm256_sign_epi8(qy, qx);
    hsum_epi32_256(_mm256_dpbusd_avx_epi32(_mm256_setzero_si256(), ax, sy)) as f32 * w
}
#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "avx512vnni", enable = "avx512vl")]
unsafe fn q4_0_block_dot_q8_avx512vnni(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let w = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let ax = _mm256_sign_epi8(qx, qx);
    let sy = _mm256_sign_epi8(qy, qx);
    hsum_epi32_256(_mm256_dpbusd_epi32(_mm256_setzero_si256(), ax, sy)) as f32 * w
}
''' + anchor
        qk = qk.replace(anchor, ins)
    return qk


def patch_qm(qm: str) -> str:
    """VNNI-gated Q8-act GEMV + shared helpers (recover-safe; from _apply_vnni_pass)."""
    qm = qm.replace(
        "//! - `GE_GEMV_Q8=1` â€” opt-in Q4Ã—Q8 act-quant int path (faster, quality risk)",
        "//! - `GE_GEMV_Q8=0` â€” force fused f32Ã—Q4 even when AVX-VNNI is present",
    )
    if "GemvActScratch" not in qm:
        qm = qm.replace(
            "pub const GGML_Q6_K: u32 = 14;\n\n/// 2D",
            """pub const GGML_Q6_K: u32 = 14;

pub struct GemvActScratch { pub act: ActQ8 }
impl GemvActScratch {
    pub fn new(in_dim: usize) -> Self {
        let n = in_dim / Q4_0_BLOCK;
        GemvActScratch { act: ActQ8 { scales: Vec::with_capacity(n), qs: Vec::with_capacity(n * Q4_0_BLOCK) } }
    }
    pub fn quantize_into(&mut self, x: &[f32]) { self.act.quantize_into(x); }
}

/// 2D""",
        )
    gemv_use_q8_body = """pub fn gemv_use_q8_act() -> bool {
    if gemv_force_f32_env() { return false; }
    match std::env::var("GE_GEMV_Q8") {
        Ok(v) if matches!(v.as_str(), "0" | "false" | "FALSE" | "no" | "NO") => false,
        Ok(v) if matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES") => {
            quant_kernels::has_avx_vnni()
        }
        Err(_) => false,
        _ => false,
    }
}

"""
    if "pub fn gemv_use_q8_act" not in qm:
        qm2, n = re.subn(
            r"/// Opt-in Q4.{1,4}Q8 activation-quantized GEMV[^\n]*\nfn gemv_q8_act_env\(\) -> bool \{.*?\n\}\n",
            gemv_use_q8_body,
            qm,
            count=1,
            flags=re.S,
        )
        if n:
            qm = qm2
        else:
            old_fn = (
                "fn gemv_q8_act_env() -> bool {\n"
                "    match std::env::var(\"GE_GEMV_Q8\") {\n"
                "        Ok(v) => matches!(v.as_str(), \"1\" | \"true\" | \"TRUE\" | \"yes\" | \"YES\"),\n"
                "        Err(_) => false,\n"
                "    }\n"
                "}\n"
            )
            if old_fn not in qm:
                raise SystemExit("quant_mat.rs: gemv_q8_act_env anchor missing")
            qm = qm.replace(old_fn, gemv_use_q8_body, 1)
        print("quant_mat.rs: gemv_use_q8_act added", flush=True)
    if "pub fn gemv_with_act" not in qm:
        qm = qm.replace(
            "        crate::gguf_load::dequant_slice(self.ggml_type, &self.packed, n)\n    }\n}",
            """        crate::gguf_load::dequant_slice(self.ggml_type, &self.packed, n)
    }

    pub fn gemv_with_act(&self, act: &GemvActScratch, y: &mut [f32]) {
        assert_eq!(y.len(), self.out_dim);
        if self.ggml_type == GGML_Q4_0 && gemv_use_q8_act() && self.in_dim % Q4_0_BLOCK == 0 {
            gemv_q4_0_shared(&self.packed, self.in_dim, self.out_dim, &act.act, y);
        } else {
            y.fill(0.0);
        }
    }
}""",
        )
    if "fn gemv_q4_0_shared" not in qm:
        qm = qm.replace(
            "fn gemv_q4_0(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {",
            """fn gemv_q4_0_shared(packed: &[u8], in_dim: usize, out_dim: usize, act: &ActQ8, y: &mut [f32]) {
    let rb = (in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
    gemv_rows_parallel(out_dim, y, |o, yo| {
        *yo = quant_kernels::gemv_q4_0_row_q8(&packed[o * rb..(o + 1) * rb], act);
    });
}
fn gemv_q4_0(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {""",
        )
        qm = re.sub(
            r"fn gemv_q4_0\(packed: &\[u8\], in_dim: usize, out_dim: usize, x: &\[f32\], y: &mut \[f32\]\) \{.*?\n\}\n\nfn gemv_q8_0",
            """fn gemv_q4_0(packed: &[u8], in_dim: usize, out_dim: usize, x: &[f32], y: &mut [f32]) {
    if in_dim % Q4_0_BLOCK != 0 { gemv_q4_0_legacy(packed, in_dim, out_dim, x, y); return; }
    let rb = (in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
    if gemv_use_q8_act() {
        let a = ActQ8::quantize(x);
        gemv_q4_0_shared(packed, in_dim, out_dim, &a, y);
    } else {
        gemv_rows_parallel(out_dim, y, |o, yo| {
            *yo = quant_kernels::gemv_q4_0_row_f32(&packed[o * rb..(o + 1) * rb], x, in_dim);
        });
    }
}

fn gemv_q8_0""",
            qm,
            count=1,
            flags=re.S,
        )
    if "project_qkv_quant_shared" not in qm:
        qm = qm.replace(
            "/// SiLU for SwiGLU.",
            """pub fn project_qkv_quant_shared(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, act: &GemvActScratch,
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    wq.gemv_with_act(act, q);
    sys::decode_pool().install(|| rayon::join(|| wk.gemv_with_act(act, k), || wv.gemv_with_act(act, v)));
}

pub fn swiglu_quant_shared(
    act: &GemvActScratch, gate: &QuantMat, up: &QuantMat, down: &QuantMat,
    g: &mut [f32], u: &mut [f32], out: &mut [f32],
) {
    sys::decode_pool().install(|| rayon::join(|| gate.gemv_with_act(act, g), || up.gemv_with_act(act, u)));
    for j in 0..g.len() { g[j] = silu(g[j]) * u[j]; }
    down.gemv(g, out);
}

/// SiLU for SwiGLU.""",
        )
    return qm


def apply_vnni_step8() -> None:
    """Step 8: patch_qk + patch_qm (no full _vnni_once â€” keeps resident safe)."""
    qk_p = SRC / "quant_kernels.rs"
    qm_p = SRC / "quant_mat.rs"
    qk = patch_qk(qk_p.read_text(encoding="utf-8"))
    qm = patch_qm(qm_p.read_text(encoding="utf-8"))
    qk_p.write_bytes(qk.encode("utf-8"))
    qm_p.write_bytes(qm.encode("utf-8"))
    for sym, blob in (
        ("has_avx_vnni", qk),
        ("q4_0_block_dot_q8_avxvnni", qk),
        ("pub fn gemv_use_q8_act", qm),
    ):
        if sym not in blob:
            raise SystemExit(f"step 8 failed: {sym} missing after VNNI patch")
    print("step 8: VNNI patch_qk + patch_qm OK", flush=True)


def apply_forward_token_shared_act() -> None:
    """Wire shared activation quant in forward_token only (not generate/decode loop)."""
    fwd = FWD.read_text(encoding="utf-8")
    changed = False
    for old_imp, new_imp in [
        (
            "use crate::quant_mat::{project_qkv_quant, swiglu_quant, QuantMat};\nuse crate::sys;",
            "use crate::quant_mat::{gemv_use_q8_act, project_qkv_quant, project_qkv_quant_shared, swiglu_quant, swiglu_quant_shared, GemvActScratch, QuantMat};\nuse crate::sys;",
        ),
        (
            "use crate::quant_mat::{project_qkv_quant, swiglu_quant, QuantMat};",
            "use crate::quant_mat::{gemv_use_q8_act, project_qkv_quant, project_qkv_quant_shared, swiglu_quant, swiglu_quant_shared, GemvActScratch, QuantMat};",
        ),
    ]:
        if old_imp in fwd and "GemvActScratch" not in fwd.split(old_imp)[0][-200:]:
            fwd = fwd.replace(old_imp, new_imp, 1)
            changed = True
    if "gemv_act: GemvActScratch" not in fwd:
        fwd = fwd.replace(
            "    norm_tmp: Vec<f32>,\n    past_k: Vec<f32>,",
            "    norm_tmp: Vec<f32>,\n    gemv_act: GemvActScratch,\n    past_k: Vec<f32>,",
        )
        fwd = fwd.replace(
            "            norm_tmp: vec![0.0; hidden],\n            past_k: Vec::new(),",
            "            norm_tmp: vec![0.0; hidden],\n            gemv_act: GemvActScratch::new(hidden),\n            past_k: Vec::new(),",
        )
        changed = True
    qkv_old = (
        "            project_qkv_quant(\n"
        "                &layer.wq, &layer.wk, &layer.wv, &scratch.attn.xn,\n"
        "                &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,\n"
        "            );"
    )
    qkv_new = (
        "            if gemv_use_q8_act() {\n"
        "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
        "                project_qkv_quant_shared(\n"
        "                    &layer.wq, &layer.wk, &layer.wv, &scratch.gemv_act,\n"
        "                    &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,\n"
        "                );\n"
        "            } else {\n"
        "                project_qkv_quant(\n"
        "                    &layer.wq, &layer.wk, &layer.wv, &scratch.attn.xn,\n"
        "                    &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,\n"
        "                );\n"
        "            }"
    )
    if qkv_old in fwd:
        fwd = fwd.replace(qkv_old, qkv_new, 1)
        changed = True
    ffn_old = (
        "            swiglu_quant(\n"
        "                &scratch.attn.xn,\n"
        "                &layer.gate,\n"
        "                &layer.up,\n"
        "                &layer.down,\n"
        "                &mut scratch.ffn_g,\n"
        "                &mut scratch.ffn_u,\n"
        "                &mut scratch.ffn_out,\n"
        "            );"
    )
    ffn_new = (
        "            if gemv_use_q8_act() {\n"
        "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
        "                swiglu_quant_shared(\n"
        "                    &scratch.gemv_act, &layer.gate, &layer.up, &layer.down,\n"
        "                    &mut scratch.ffn_g, &mut scratch.ffn_u, &mut scratch.ffn_out,\n"
        "                );\n"
        "            } else {\n"
        "                swiglu_quant(\n"
        "                    &scratch.attn.xn,\n"
        "                    &layer.gate,\n"
        "                    &layer.up,\n"
        "                    &layer.down,\n"
        "                    &mut scratch.ffn_g,\n"
        "                    &mut scratch.ffn_u,\n"
        "                    &mut scratch.ffn_out,\n"
        "                );\n"
        "            }"
    )
    if ffn_old in fwd:
        fwd = fwd.replace(ffn_old, ffn_new, 1)
        changed = True
    if changed:
        FWD.write_bytes(fwd.encode("utf-8"))
        print("step 8: forward_token shared-act quant wired", flush=True)
    else:
        print("step 8: forward_token shared-act already present", flush=True)


def apply_greedy_argmax_p1() -> None:
    """Step 9: P1 greedy lm_head argmax without materializing 128k logits vec."""
    sm_p = SRC / "sample.rs"
    sm = sm_p.read_text(encoding="utf-8")
    if "needs_full_logits" not in sm:
        old = (
            "    pub fn is_greedy(&self) -> bool {\n"
            "        self.temperature <= 0.0\n"
            "    }\n}"
        )
        new = (
            "    pub fn is_greedy(&self) -> bool {\n"
            "        self.temperature <= 0.0\n"
            "    }\n\n"
            "    pub fn needs_full_logits(&self) -> bool {\n"
            "        if self.is_greedy() {\n"
            "            (self.repetition_penalty - 1.0).abs() > 1e-6\n"
            "                || self.presence_penalty.abs() > 1e-6\n"
            "                || self.frequency_penalty.abs() > 1e-6\n"
            "        } else {\n"
            "            true\n"
            "        }\n"
            "    }\n}"
        )
        if old not in sm:
            raise SystemExit("sample.rs: is_greedy anchor missing (cannot add needs_full_logits)")
        sm_p.write_bytes(sm.replace(old, new, 1).encode("utf-8"))
        print("step 9: sample.rs needs_full_logits added", flush=True)
    else:
        print("step 9: sample.rs needs_full_logits already present", flush=True)

    fwd = FWD.read_text(encoding="utf-8")
    changed = False
    if "probs: Vec<f32>" not in fwd:
        fwd = fwd.replace(
            "    norm_tmp: Vec<f32>,\n    gemv_act: GemvActScratch,\n    past_k: Vec<f32>,",
            "    norm_tmp: Vec<f32>,\n    probs: Vec<f32>,\n    gemv_act: GemvActScratch,\n    past_k: Vec<f32>,",
        )
        fwd = fwd.replace(
            "            norm_tmp: vec![0.0; hidden],\n            gemv_act: GemvActScratch::new(hidden),\n            past_k: Vec::new(),",
            "            norm_tmp: vec![0.0; hidden],\n            probs: vec![0.0; n_vocab],\n            gemv_act: GemvActScratch::new(hidden),\n            past_k: Vec::new(),",
        )
        changed = True
    if "logits_argmax_greedy" not in fwd:
        anchor = "    fn compute_logits("
        insert = """    fn logits_argmax_greedy(
        &self,
        hidden: &[f32],
        norm_tmp: &mut [f32],
    ) -> Result<u32, ForwardError> {
        let h = if let Some(ref wn) = self.output_norm {
            rms_norm(hidden, wn, self.rms_eps, norm_tmp);
            norm_tmp.as_ref()
        } else {
            hidden
        };
        let table = if self.tied_output {
            self.emb.weight.as_slice()
        } else {
            self.output.as_slice()
        };
        let n_embd = self.n_embd;
        let n_vocab = self.n_vocab;
        let (_best_score, best_id) = sys::decode_pool().install(|| {
            use rayon::prelude::*;
            (0..n_vocab)
                .into_par_iter()
                .map(|v| {
                    let row = &table[v * n_embd..(v + 1) * n_embd];
                    let mut s = 0.0f32;
                    let mut i = 0usize;
                    while i + 4 <= n_embd {
                        s += h[i] * row[i]
                            + h[i + 1] * row[i + 1]
                            + h[i + 2] * row[i + 2]
                            + h[i + 3] * row[i + 3];
                        i += 4;
                    }
                    while i < n_embd {
                        s += h[i] * row[i];
                        i += 1;
                    }
                    (s, v as u32)
                })
                .reduce(
                    || (f32::NEG_INFINITY, 0u32),
                    |a, b| if a.0 > b.0 { a } else { b },
                )
        });
        Ok(best_id)
    }

"""
        if anchor not in fwd:
            raise SystemExit("forward.rs: compute_logits anchor missing (cannot add logits_argmax_greedy)")
        fwd = fwd.replace(anchor, insert + anchor, 1)
        changed = True
    decode_old = (
        "        let mut probs = vec![0.0f32; self.n_vocab];\n\n"
        "        let mut generated = Vec::with_capacity(max_new);\n"
        "        let mut first_token_secs = 0.0f64;\n"
        "        let t_decode = Instant::now();\n"
        "        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));\n"
        "        for _ in 0..max_new {\n"
        "            self.compute_logits(&scratch.hidden, &mut scratch.logits, &mut scratch.norm_tmp)?;\n"
        "            let next = crate::sample::sample_token(\n"
        "                &mut scratch.logits,\n"
        "                &history,\n"
        "                sample,\n"
        "                &mut rng,\n"
        "                &mut probs,\n"
        "            );"
    )
    decode_new = (
        "        let mut generated = Vec::with_capacity(max_new);\n"
        "        let mut first_token_secs = 0.0f64;\n"
        "        let t_decode = Instant::now();\n"
        "        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));\n"
        "        for _ in 0..max_new {\n"
        "            let next = if sample.needs_full_logits() {\n"
        "                self.compute_logits(&scratch.hidden, &mut scratch.logits, &mut scratch.norm_tmp)?;\n"
        "                crate::sample::sample_token(\n"
        "                    &mut scratch.logits,\n"
        "                    &history,\n"
        "                    sample,\n"
        "                    &mut rng,\n"
        "                    &mut scratch.probs,\n"
        "                )\n"
        "            } else {\n"
        "                self.logits_argmax_greedy(&scratch.hidden, &mut scratch.norm_tmp)?\n"
        "            };"
    )
    if decode_old in fwd:
        fwd = fwd.replace(decode_old, decode_new, 1)
        changed = True
    elif "needs_full_logits()" in fwd:
        print("step 9: greedy argmax decode loop already patched", flush=True)
    else:
        print("step 9: decode loop anchor missing (skip greedy argmax loop)", flush=True)
    if changed:
        FWD.write_bytes(fwd.encode("utf-8"))
        print("step 9: greedy lm_head argmax wired", flush=True)


# Q6_K dequant: llama.cpp dequantize_row_q6_K scale layout (was sc_i += 4 / wrong is).
_Q6K_OLD_GGUF = """fn dequant_q6_k(packed: &[u8], n: usize) -> Result<Vec<f32>, GgufLoadError> {
    let mut out = vec![0.0f32; n];
    let n_blocks = (n + QK_K - 1) / QK_K;
    for ib in 0..n_blocks {
        let off = ib * Q6_K_BYTES;
        if off + Q6_K_BYTES > packed.len() {
            return Err(GgufLoadError::Message("Q6_K truncated".into()));
        }
        let block = &packed[off..off + Q6_K_BYTES];
        let ql = &block[0..128];
        let qh = &block[128..192];
        let scales = &block[192..208];
        let d = f16_to_f32(u16::from_le_bytes([block[208], block[209]]));
        let base = ib * QK_K;

        // Port of ggml-quants.c `dequantize_row_q6_K` / gguf.quants.Q6_K.
        // Two passes of 128 weights; each pass consumes 64 ql bytes + 32 qh bytes + 4 scales.
        let mut ql_i = 0usize;
        let mut qh_i = 0usize;
        let mut sc_i = 0usize;
        let mut out_i = base;
        for _ in 0..QK_K / 128 {
            for l in 0..32 {
                let is = sc_i + l / 16;
                let q0 = ((ql[ql_i + l] & 0x0F) | ((qh[qh_i + l] & 3) << 4)) as i8 - 32;
                let q1 = ((ql[ql_i + 32 + l] & 0x0F) | (((qh[qh_i + l] >> 2) & 3) << 4)) as i8 - 32;
                let q2 = (((ql[ql_i + l] >> 4) & 0x0F) | (((qh[qh_i + l] >> 4) & 3) << 4)) as i8 - 32;
                let q3 =
                    (((ql[ql_i + 32 + l] >> 4) & 0x0F) | (((qh[qh_i + l] >> 6) & 3) << 4)) as i8 - 32;
                let d1 = d * scales[is] as i8 as f32;
                let d2 = d * scales[is + 2] as i8 as f32;
                if out_i + l < n {
                    out[out_i + l] = d1 * q0 as f32;
                }
                if out_i + 32 + l < n {
                    out[out_i + 32 + l] = d1 * q1 as f32;
                }
                if out_i + 64 + l < n {
                    out[out_i + 64 + l] = d2 * q2 as f32;
                }
                if out_i + 96 + l < n {
                    out[out_i + 96 + l] = d2 * q3 as f32;
                }
            }
            out_i += 128;
            ql_i += 64;
            qh_i += 32;
            sc_i += 4;
        }
    }
    Ok(out)
}"""

_Q6K_NEW_GGUF = """fn dequant_q6_k(packed: &[u8], n: usize) -> Result<Vec<f32>, GgufLoadError> {
    let mut out = vec![0.0f32; n];
    let n_blocks = (n + QK_K - 1) / QK_K;
    for ib in 0..n_blocks {
        let off = ib * Q6_K_BYTES;
        if off + Q6_K_BYTES > packed.len() {
            return Err(GgufLoadError::Message("Q6_K truncated".into()));
        }
        let block = &packed[off..off + Q6_K_BYTES];
        let mut ql_i = 0usize;
        let mut qh_i = 128usize;
        let mut sc_i = 192usize;
        let d = f16_to_f32(u16::from_le_bytes([block[208], block[209]]));
        let mut out_i = ib * QK_K;
        for _ in 0..QK_K / 128 {
            for l in 0..32 {
                let is = l / 16;
                let q1 = ((block[ql_i + l] & 0x0F) | ((block[qh_i + l] & 3) << 4)) as i8 - 32;
                let q2 = ((block[ql_i + 32 + l] & 0x0F) | (((block[qh_i + l] >> 2) & 3) << 4)) as i8 - 32;
                let q3 = (((block[ql_i + l] >> 4) & 0x0F) | (((block[qh_i + l] >> 4) & 3) << 4)) as i8 - 32;
                let q4 = (((block[ql_i + 32 + l] >> 4) & 0x0F)
                    | (((block[qh_i + l] >> 6) & 3) << 4)) as i8
                    - 32;
                if out_i + l < n {
                    out[out_i + l] = d * block[sc_i + is] as i8 as f32 * q1 as f32;
                }
                if out_i + 32 + l < n {
                    out[out_i + 32 + l] = d * block[sc_i + is + 2] as i8 as f32 * q2 as f32;
                }
                if out_i + 64 + l < n {
                    out[out_i + 64 + l] = d * block[sc_i + is + 4] as i8 as f32 * q3 as f32;
                }
                if out_i + 96 + l < n {
                    out[out_i + 96 + l] = d * block[sc_i + is + 6] as i8 as f32 * q4 as f32;
                }
            }
            out_i += 128;
            ql_i += 64;
            qh_i += 32;
            sc_i += 8;
        }
    }
    Ok(out)
}"""

_Q6K_OLD_QM = """fn dequant_q6_k_block(block: &[u8], out: &mut [f32]) {
    let ql = &block[0..128];
    let qh = &block[128..192];
    let scales = &block[192..208];
    let d = f16_to_f32(u16::from_le_bytes([block[208], block[209]]));
    let mut ql_i = 0usize;
    let mut qh_i = 0usize;
    let mut sc_i = 0usize;
    let mut out_i = 0usize;
    for _ in 0..QK_K / 128 {
        for l in 0..32 {
            let is = sc_i + l / 16;
            let q0 = ((ql[ql_i + l] & 0x0F) | ((qh[qh_i + l] & 3) << 4)) as i8 - 32;
            let q1 = ((ql[ql_i + 32 + l] & 0x0F) | (((qh[qh_i + l] >> 2) & 3) << 4)) as i8 - 32;
            let q2 = (((ql[ql_i + l] >> 4) & 0x0F) | (((qh[qh_i + l] >> 4) & 3) << 4)) as i8 - 32;
            let q3 =
                (((ql[ql_i + 32 + l] >> 4) & 0x0F) | (((qh[qh_i + l] >> 6) & 3) << 4)) as i8 - 32;
            let d1 = d * scales[is] as i8 as f32;
            let d2 = d * scales[is + 2] as i8 as f32;
            out[out_i + l] = d1 * q0 as f32;
            out[out_i + 32 + l] = d1 * q1 as f32;
            out[out_i + 64 + l] = d2 * q2 as f32;
            out[out_i + 96 + l] = d2 * q3 as f32;
        }
        out_i += 128;
        ql_i += 64;
        qh_i += 32;
        sc_i += 4;
    }
}"""

_Q6K_NEW_QM = """fn dequant_q6_k_block(block: &[u8], out: &mut [f32]) {
    let d = f16_to_f32(u16::from_le_bytes([block[208], block[209]]));
    let mut ql_i = 0usize;
    let mut qh_i = 128usize;
    let mut sc_i = 192usize;
    let mut out_i = 0usize;
    for _ in 0..QK_K / 128 {
        for l in 0..32 {
            let is = l / 16;
            let q1 = ((block[ql_i + l] & 0x0F) | ((block[qh_i + l] & 3) << 4)) as i8 - 32;
            let q2 = ((block[ql_i + 32 + l] & 0x0F) | (((block[qh_i + l] >> 2) & 3) << 4)) as i8 - 32;
            let q3 = (((block[ql_i + l] >> 4) & 0x0F) | (((block[qh_i + l] >> 4) & 3) << 4)) as i8 - 32;
            let q4 = (((block[ql_i + 32 + l] >> 4) & 0x0F) | (((block[qh_i + l] >> 6) & 3) << 4)) as i8
                - 32;
            out[out_i + l] = d * block[sc_i + is] as i8 as f32 * q1 as f32;
            out[out_i + 32 + l] = d * block[sc_i + is + 2] as i8 as f32 * q2 as f32;
            out[out_i + 64 + l] = d * block[sc_i + is + 4] as i8 as f32 * q3 as f32;
            out[out_i + 96 + l] = d * block[sc_i + is + 6] as i8 as f32 * q4 as f32;
        }
        out_i += 128;
        ql_i += 64;
        qh_i += 32;
        sc_i += 8;
    }
}"""


def apply_q6k_dequant_fix() -> None:
    """Fix Q6_K embedding dequant scale indexing (gguf_load.rs + quant_mat.rs)."""
    for path, old, new, label in (
        (SRC / "gguf_load.rs", _Q6K_OLD_GGUF, _Q6K_NEW_GGUF, "gguf_load.rs"),
        (SRC / "quant_mat.rs", _Q6K_OLD_QM, _Q6K_NEW_QM, "quant_mat.rs"),
    ):
        text = path.read_text(encoding="utf-8")
        if new in text and old not in text:
            print(f"step 9b: {label} Q6_K dequant already fixed", flush=True)
            continue
        if old not in text:
            raise SystemExit(f"step 9b: {label} Q6_K dequant anchor missing")
        path.write_bytes(text.replace(old, new, 1).encode("utf-8"))
        print(f"step 9b: {label} Q6_K dequant fixed", flush=True)


def apply_forward_q8_q4_only_gate() -> None:
    """GE_GEMV_Q8 fast path only for Q4_0 weights (Q4_K/Q6_K need f32 activations)."""
    fwd = FWD.read_text(encoding="utf-8")
    changed = False
    replacements = [
        (
            "            if gemv_use_q8_act() {\n"
            "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
            "                project_qkv_quant_shared(",
            "            if gemv_use_q8_act() && layer.wq.ggml_type == crate::quant_mat::GGML_Q4_0 {\n"
            "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
            "                project_qkv_quant_shared(",
        ),
        (
            "            if gemv_use_q8_act() {\n"
            "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
            "                swiglu_quant_shared(",
            "            if gemv_use_q8_act() && layer.gate.ggml_type == crate::quant_mat::GGML_Q4_0 {\n"
            "                scratch.gemv_act.quantize_into(&scratch.attn.xn);\n"
            "                swiglu_quant_shared(",
        ),
    ]
    for old, new in replacements:
        if old in fwd:
            fwd = fwd.replace(old, new, 1)
            changed = True
    if "layer.wq.ggml_type == crate::quant_mat::GGML_Q4_0" in fwd and not changed:
        print("step 9b: forward.rs Q8-act Q4_0 gate already present", flush=True)
        return
    if not changed:
        raise SystemExit("step 9b: forward.rs Q8-act gate anchors missing")
    FWD.write_bytes(fwd.encode("utf-8"))
    print("step 9b: forward.rs Q8-act gated to Q4_0 weights", flush=True)


def apply_prefill_kv_fix() -> None:
    """prefill_logits must pass &mut kv into forward_token (&mut dyn KvStore)."""
    fwd = FWD.read_text(encoding="utf-8")
    old = (
        "            self.forward_token(token_pos, kv, &mut scratch)?;"
    )
    new = (
        "            self.forward_token(token_pos, &mut kv, &mut scratch)?;"
    )
    if old in fwd:
        fwd = fwd.replace(old, new)
        FWD.write_bytes(fwd.encode("utf-8"))
        print("step 9b: forward.rs prefill &mut kv fix", flush=True)
    elif new in fwd:
        print("step 9b: forward.rs prefill &mut kv already ok", flush=True)


def install_q4km_prefill_diag() -> None:
    """Install diag_q4km_prefill bin for Q4_K_M gravity-prefix quality gate."""
    bin_p = SRC / "bin" / "diag_q4km_prefill.rs"
    if not bin_p.is_file() or "prefill_logits_f32_kv" not in bin_p.read_text(encoding="utf-8"):
        bin_p.parent.mkdir(parents=True, exist_ok=True)
        bin_p.write_bytes(DIAG_Q4KM_PREFILL_RS.encode("utf-8"))
        print("q4km: diag_q4km_prefill.rs installed", flush=True)
    cargo_p = EC / "Cargo.toml"
    cargo = cargo_p.read_text(encoding="utf-8")
    if "diag_q4km_prefill" not in cargo:
        cargo = cargo.replace(
            "[[bin]]\nname = \"decode_1b_bench\"\npath = \"src/bin/decode_1b_bench.rs\"",
            "[[bin]]\nname = \"decode_1b_bench\"\npath = \"src/bin/decode_1b_bench.rs\"\n\n[[bin]]\n"
            'name = "diag_q4km_prefill"\npath = "src/bin/diag_q4km_prefill.rs"\n'
            "test = false\ndoc = false",
        )
        cargo_p.write_bytes(cargo.encode("utf-8"))
        print("q4km: Cargo.toml diag_q4km_prefill bin", flush=True)


def verify_q4km_logits(*, expected_top: int = 48590, min_cos: float = 0.9999) -> None:
    """Q4_K_M GGUF prefill: top token + logit cos vs numpy ref (Q6_K embedding sanity)."""
    install_q4km_prefill_diag()
    run(
        ["cargo", "build", "--release", "-p", "engine-core", "--bin", "diag_q4km_prefill"],
        label="cargo build diag_q4km_prefill",
    )
    exe = REPO / "target" / "release" / "diag_q4km_prefill.exe"
    if not exe.is_file():
        exe = REPO / "target" / "release" / "diag_q4km_prefill"
    out = subprocess.check_output([str(exe)], cwd=REPO, text=True, encoding="utf-8")
    print(out, end="", flush=True)
    m = re.search(r"^top (\d+)$", out, re.MULTILINE)
    if not m:
        raise SystemExit("diag_q4km_prefill: no top token line in output")
    top = int(m.group(1))
    if top != expected_top:
        raise SystemExit(f"Q4_K_M prefill top token {top} != expected {expected_top}")
    print(f"Q4_K_M prefill top token OK: {top}", flush=True)
    run([PY, str(REPO / "scripts" / "ref_forward_q4km.py")], label="ref_forward_q4km.py")
    home = Path(os.environ.get("USERPROFILE", str(Path.home())))
    ref_path = home / ".green" / "models" / "_py_ref_logits.f32"
    m_log = re.search(r"^logits (.+)$", out, re.MULTILINE)
    if not m_log or not ref_path.is_file():
        raise SystemExit("Q4_K_M verify: missing engine or ref logits file")
    import numpy as np

    ref_logits = np.fromfile(ref_path, dtype=np.float32)
    eng_logits = np.fromfile(m_log.group(1).strip(), dtype=np.float32)
    cos = float(np.dot(ref_logits, eng_logits) / (
        (float(np.dot(ref_logits, ref_logits)) * float(np.dot(eng_logits, eng_logits))) ** 0.5 + 1e-12
    ))
    print(f"logit cos={cos:.6f}", flush=True)
    if cos < min_cos:
        raise SystemExit(f"Q4_K_M logit cos {cos} < {min_cos}")
    print(f"Q4_K_M logit cos OK: {cos}", flush=True)


def fix_q4_0_fast_test() -> None:
    """q4_0_fast_matches_legacy must compare f32Ã—Q4 path, not Q8-act int path."""
    qm_p = SRC / "quant_mat.rs"
    qm = qm_p.read_text(encoding="utf-8")
    anchor = "    fn q4_0_fast_matches_legacy() {\n        let in_dim = 64usize;"
    fix = '    fn q4_0_fast_matches_legacy() {\n        std::env::set_var("GE_GEMV_Q8", "0");\n        let in_dim = 64usize;'
    if anchor in qm and 'set_var("GE_GEMV_Q8"' not in qm:
        qm_p.write_bytes(qm.replace(anchor, fix, 1).encode("utf-8"))
        print("quant_mat.rs: q4_0_fast test forces f32xQ4 path", flush=True)
    elif 'set_var("GE_GEMV_Q8"' in qm:
        print("quant_mat.rs: q4_0_fast test fix already present", flush=True)


def patch_atomic_merge() -> None:
    qk_p = SRC / "quant_kernels.rs"
    qk = qk_p.read_text(encoding="utf-8")
    if "pub fn quantize_into" not in qk:
        qk = qk_p.read_text(encoding="utf-8")
        qk = qk.replace(
            "impl ActQ8 {\n    pub fn quantize(x: &[f32]) -> Self {",
            "impl ActQ8 {\n    pub fn quantize_into(&mut self, x: &[f32]) {\n        let n_blocks = x.len() / Q4_0_BLOCK;\n        self.scales.clear();\n        self.qs.clear();\n        self.scales.reserve(n_blocks);\n        self.qs.reserve(n_blocks * Q4_0_BLOCK);\n        for b in 0..n_blocks {\n            let xb = &x[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];\n            let mut amax = 0.0f32;\n            for &v in xb { amax = amax.max(v.abs()); }\n            let d = amax / 127.0;\n            let id = if d > 1e-12 { 1.0 / d } else { 0.0 };\n            self.scales.push(d);\n            for &v in xb {\n                self.qs.push((v * id).round().clamp(-127.0, 127.0) as i8);\n            }\n        }\n    }\n\n    pub fn quantize(x: &[f32]) -> Self {",
        )
        qk_p.write_bytes(qk.encode("utf-8"))
    gen_p = SRC / "generate.rs"
    gen = gen_p.read_text(encoding="utf-8").replace("pub pub fn", "pub fn")
    for old, new in [
        ("fn load_tokenizer_cached(", "pub fn load_tokenizer_cached("),
        ("fn not_ready_message(", "pub fn not_ready_message("),
        ("fn require_can_generate(", "pub fn require_can_generate("),
        ("fn resolve_prompt_text(", "pub fn resolve_prompt_text("),
        ("fn stop_token_ids(", "pub fn stop_token_ids("),
        ("pub(crate) fn stop_token_ids", "pub fn stop_token_ids"),
    ]:
        if new not in gen:
            gen = gen.replace(old, new, 1)
    gen_p.write_bytes(gen.encode("utf-8"))
    lib_p = SRC / "lib.rs"
    lib = lib_p.read_text(encoding="utf-8")
    if "ResidentModel" not in lib:
        lib = lib.replace(
            "pub use generate::{",
            "pub use resident::{open_model_cached, ResidentModel};\npub use generate::{",
        )
    lib = lib.replace(
        "pub use forward::{DenseForward, ForwardError, GreedyGenerateOut};",
        "pub use forward::{DecodeSession, DenseForward, ForwardError, GreedyGenerateOut};",
    )
    if "pub mod resident" not in lib:
        lib = lib.replace("pub mod generate;\n", "pub mod generate;\npub mod resident;\n")
    lib_p.write_bytes(lib.encode("utf-8"))
    print("atomic merge: quant_kernels/generate/lib", flush=True)


def patch_parallel_support() -> None:
    import re
    sys_p = SRC / "sys.rs"
    sys = sys_p.read_text(encoding="utf-8")
    if "decode_pool" not in sys:
        if "use rayon::ThreadPool" not in sys:
            sys = sys.replace(
                "use std::sync::OnceLock;",
                "use std::sync::OnceLock;\n\nuse rayon::ThreadPool;\nuse rayon::ThreadPoolBuilder;",
            )
        sys = sys.replace(
            "    physical_cores()\n}\n\n#[cfg(target_os = \"windows\")]",
            "    physical_cores()\n}\n\npub fn decode_pool() -> &'static ThreadPool {\n    static POOL: OnceLock<ThreadPool> = OnceLock::new();\n    POOL.get_or_init(|| {\n        ThreadPoolBuilder::new()\n            .num_threads(decode_threads())\n            .thread_name(|i| format!(\"ge-decode-{i}\"))\n            .build()\n            .unwrap_or_else(|_| ThreadPoolBuilder::new().num_threads(1).build().unwrap())\n    })\n}\n\n#[cfg(target_os = \"windows\")]",
        )
        sys_p.write_bytes(sys.encode("utf-8"))
    qm_p = SRC / "quant_mat.rs"
    qm = qm_p.read_text(encoding="utf-8")
    if "gemv_pool()" in qm:
        qm = re.sub(r"/// Decode GEMV rayon pool.*?^}\n\n", "", qm, count=1, flags=re.MULTILINE | re.DOTALL)
    qm = qm.replace("gemv_pool().install", "sys::decode_pool().install")
    if "pub fn project_qkv_quant" not in qm:
        qm = qm.replace(
            "/// SiLU for SwiGLU.",
            "/// Parallel Q/K/V GEMV for decode.\npub fn project_qkv_quant(\n    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, x: &[f32],\n    q: &mut [f32], k: &mut [f32], v: &mut [f32],\n) {\n    sys::decode_pool().install(|| {\n        rayon::join(|| wq.gemv(x, q), || rayon::join(|| wk.gemv(x, k), || wv.gemv(x, v)));\n    });\n}\n\n/// SiLU for SwiGLU.",
        )
    if "rayon::join(|| gate.gemv" not in qm:
        qm = qm.replace(
            "    gate.gemv(x, g);\n    up.gemv(x, u);",
            "    if gate.out_dim >= 256 && up.out_dim >= 256 {\n        sys::decode_pool().install(|| { rayon::join(|| gate.gemv(x, g), || up.gemv(x, u)); });\n    } else {\n        gate.gemv(x, g);\n        up.gemv(x, u);\n    }",
        )
    qm_p.write_bytes(qm.encode("utf-8"))
    print("parallel support: sys/quant_mat", flush=True)


def patch_parallel_forward() -> None:
    fwd = FWD.read_text(encoding="utf-8")
    old_imports = "use crate::attention::{\n    append_kv, multi_head_attend_decode, recall_kv_f32, softmax_inplace,\n};"
    new_imports = "use crate::attention::{\n    append_kv_reuse, multi_head_attend_decode, multi_head_attend_decode_parallel,\n    recall_kv_f32, recall_kv_f32_into, MhaScratch,\n};"
    if old_imports in fwd:
        fwd = fwd.replace(old_imports, new_imports)
    fwd = fwd.replace(
        "use crate::kv::{KvKeyQuant, KvStore, PagedKvMetrics, PagedRamKvStore};",
        "use crate::kv::{F16, KvKeyQuant, KvStore, PagedKvMetrics, PagedRamKvStore};",
    )
    fwd = fwd.replace(
        "use crate::quant_mat::{swiglu_quant, QuantMat};",
        "use crate::quant_mat::{project_qkv_quant, swiglu_quant, QuantMat};\nuse crate::sys;",
    )
    fwd = fwd.replace(
        "            layer.wq.gemv(&scratch.attn.xn, &mut scratch.attn.q);\n"
        "            layer.wk.gemv(&scratch.attn.xn, &mut scratch.attn.k);\n"
        "            layer.wv.gemv(&scratch.attn.xn, &mut scratch.attn.v);",
        "            project_qkv_quant(\n"
        "                &layer.wq, &layer.wk, &layer.wv, &scratch.attn.xn,\n"
        "                &mut scratch.attn.q, &mut scratch.attn.k, &mut scratch.attn.v,\n"
        "            );",
    )
    old_attn = (
        "            append_kv(kv, layer_i, slot, &scratch.attn.new_k, &scratch.attn.new_v);\n"
        "            let resident = kv.seq_len();\n"
        "            let (past_k, past_v) = if resident <= 1 {\n"
        "                (Vec::new(), Vec::new())\n"
        "            } else {\n"
        "                // Attend over resident past only (StreamingLLM sinks + recent).\n"
        "                recall_kv_f32(kv, layer_i, 0..resident - 1)\n"
        "            };\n"
        "            scratch.attn.ensure_seq(resident);\n"
        "            multi_head_attend_decode(\n"
        "                &scratch.attn.q,\n"
        "                &scratch.attn.k,\n"
        "                &scratch.attn.v,\n"
        "                &past_k,\n"
        "                &past_v,\n"
        "                self.n_heads,\n"
        "                self.n_kv_heads,\n"
        "                self.head_dim,\n"
        "                &mut scratch.attn.scores,\n"
        "                &mut scratch.attn.attn,\n"
        "            );"
    )
    new_attn = (
        "            append_kv_reuse(\n"
        "                kv, layer_i, slot, &scratch.attn.new_k, &scratch.attn.new_v,\n"
        "                &mut scratch.kv_k16, &mut scratch.kv_v16,\n"
        "            );\n"
        "            let resident = kv.seq_len();\n"
        "            if resident <= 1 {\n"
        "                scratch.past_k.clear();\n"
        "                scratch.past_v.clear();\n"
        "            } else {\n"
        "                recall_kv_f32_into(kv, layer_i, 0..resident - 1, &mut scratch.past_k, &mut scratch.past_v);\n"
        "            }\n"
        "            scratch.attn.ensure_parallel(resident, self.head_dim);\n"
        "            let score_cap = scratch.attn.score_cap();\n"
        "            multi_head_attend_decode_parallel(\n"
        "                &scratch.attn.q, &scratch.attn.k, &scratch.attn.v,\n"
        "                &scratch.past_k, &scratch.past_v,\n"
        "                self.n_heads, self.n_kv_heads, self.head_dim,\n"
        "                &mut scratch.attn.mha_per_kv, &mut scratch.attn.head_scores,\n"
        "                score_cap, &mut scratch.attn.attn,\n"
        "            );"
    )
    if old_attn in fwd:
        fwd = fwd.replace(old_attn, new_attn)
    fwd = fwd.replace("        let _ = softmax_inplace;\n", "")
    if "    past_k: Vec<f32>,\n    past_v: Vec<f32>," not in fwd:
        fwd = fwd.replace(
            "    norm_tmp: Vec<f32>,\n    attn: AttnBufs,\n}",
            "    norm_tmp: Vec<f32>,\n    past_k: Vec<f32>,\n    past_v: Vec<f32>,\n"
            "    kv_k16: Vec<F16>,\n    kv_v16: Vec<F16>,\n    attn: AttnBufs,\n}",
        )
        fwd = fwd.replace(
            "            norm_tmp: vec![0.0; hidden],\n            attn: AttnBufs::new(hidden, n_heads, n_kv, head_dim),\n        }\n    }\n}",
            "            norm_tmp: vec![0.0; hidden],\n            past_k: Vec::new(),\n            past_v: Vec::new(),\n"
            "            kv_k16: Vec::with_capacity(n_kv * head_dim),\n            kv_v16: Vec::with_capacity(n_kv * head_dim),\n"
            "            attn: AttnBufs::new(hidden, n_heads, n_kv, head_dim),\n        }\n    }\n}",
        )
    if "sys::decode_pool().install" not in fwd:
        fwd = fwd.replace(
            "            use rayon::prelude::*;\n            logits.par_iter_mut().enumerate().for_each(|(v, out)| {",
            "            use rayon::prelude::*;\n            sys::decode_pool().install(|| {\n"
            "                logits.par_iter_mut().enumerate().for_each(|(v, out)| {",
        )
        fwd = fwd.replace(
            "                *out = s;\n            });\n        } else {",
            "                *out = s;\n                });\n            });\n        } else {",
        )
    if "mha_per_kv: Vec<MhaScratch>" not in fwd:
        old_bufs = """struct AttnBufs {
    xn: Vec<f32>,
    q: Vec<f32>,
    k: Vec<f32>,
    v: Vec<f32>,
    attn: Vec<f32>,
    proj: Vec<f32>,
    scores: Vec<f32>,
    new_k: Vec<f32>,
    new_v: Vec<f32>,
}

impl AttnBufs {
    fn new(hidden: usize, n_heads: usize, n_kv: usize, head_dim: usize) -> Self {
        AttnBufs {
            xn: vec![0.0; hidden],
            q: vec![0.0; n_heads * head_dim],
            k: vec![0.0; n_kv * head_dim],
            v: vec![0.0; n_kv * head_dim],
            attn: vec![0.0; n_heads * head_dim],
            proj: vec![0.0; hidden],
            scores: vec![0.0; 8],
            new_k: vec![0.0; n_kv * head_dim],
            new_v: vec![0.0; n_kv * head_dim],
        }
    }

    fn ensure_seq(&mut self, seq: usize) {
        if self.scores.len() < seq {
            self.scores.resize(seq, 0.0);
        }
    }
}"""
        new_bufs = """struct AttnBufs {
    n_heads: usize,
    n_kv: usize,
    xn: Vec<f32>,
    q: Vec<f32>,
    k: Vec<f32>,
    v: Vec<f32>,
    attn: Vec<f32>,
    proj: Vec<f32>,
    scores: Vec<f32>,
    new_k: Vec<f32>,
    new_v: Vec<f32>,
    mha_per_kv: Vec<MhaScratch>,
    head_scores: Vec<f32>,
    score_len: usize,
}

impl AttnBufs {
    fn new(hidden: usize, n_heads: usize, n_kv: usize, head_dim: usize) -> Self {
        AttnBufs {
            n_heads,
            n_kv,
            xn: vec![0.0; hidden],
            q: vec![0.0; n_heads * head_dim],
            k: vec![0.0; n_kv * head_dim],
            v: vec![0.0; n_kv * head_dim],
            attn: vec![0.0; n_heads * head_dim],
            proj: vec![0.0; hidden],
            scores: vec![0.0; 8],
            new_k: vec![0.0; n_kv * head_dim],
            new_v: vec![0.0; n_kv * head_dim],
            mha_per_kv: vec![MhaScratch::default(); n_kv],
            head_scores: vec![0.0; n_heads * 8],
            score_len: 8,
        }
    }

    fn score_cap(&self) -> usize {
        self.score_len
    }

    fn ensure_parallel(&mut self, seq: usize, head_dim: usize) {
        if self.score_len < seq {
            self.score_len = seq;
            self.scores.resize(seq, 0.0);
            self.head_scores.resize(self.n_heads * seq, 0.0);
        }
        for m in &mut self.mha_per_kv {
            m.ensure(seq, head_dim);
        }
    }

    fn ensure_seq(&mut self, seq: usize) {
        if self.scores.len() < seq {
            self.scores.resize(seq, 0.0);
        }
    }
}"""
        if old_bufs in fwd:
            fwd = fwd.replace(old_bufs, new_bufs)
    FWD.write_bytes(fwd.encode("utf-8"))
    print("parallel forward: forward.rs", flush=True)


def apply_perf_patches() -> None:
    """Pool nesting fix, decode profile, Q4 lm_head GEMV argmax."""
    qm_p = SRC / "quant_mat.rs"
    qm = qm_p.read_text(encoding="utf-8")
    old_pool = (
        "        sys::decode_pool().install(|| {\n"
        "            y.par_iter_mut().enumerate().for_each(|(o, yo)| f(o, yo));\n"
        "        });"
    )
    new_pool = (
        "        let mut par = || y.par_iter_mut().enumerate().for_each(|(o, yo)| f(o, yo));\n"
        "        if rayon::current_thread_index().is_some() {\n"
        "            par();\n"
        "        } else {\n"
        "            sys::decode_pool().install(par);\n"
        "        }"
    )
    if old_pool in qm:
        qm = qm.replace(old_pool, new_pool, 1)
        qm_p.write_bytes(qm.encode("utf-8"))
        print("perf: gemv_rows_parallel nested-pool fix", flush=True)

    lib_p = SRC / "lib.rs"
    lib = lib_p.read_text(encoding="utf-8")
    if "pub mod decode_profile" not in lib:
        lib = lib.replace("pub mod forward;\n", "pub mod decode_profile;\npub mod forward;\n")
        lib_p.write_bytes(lib.encode("utf-8"))
        print("perf: lib.rs decode_profile mod", flush=True)

    dp_p = SRC / "decode_profile.rs"
    if not dp_p.is_file() or "ProfileAccum" not in dp_p.read_text(encoding="utf-8"):
        dp_p.write_bytes(DECODE_PROFILE_RS.encode("utf-8"))
        print("perf: decode_profile.rs installed", flush=True)

    micro_p = SRC / "bin" / "gemv_dot_microbench.rs"
    micro_p.parent.mkdir(parents=True, exist_ok=True)
    if not micro_p.is_file() or "row_dots_per_s" not in micro_p.read_text(encoding="utf-8"):
        micro_p.write_bytes(GEMV_DOT_MICROBENCH_RS.encode("utf-8"))
        print("perf: gemv_dot_microbench.rs installed", flush=True)

    cargo_p = REPO / "crates" / "engine-core" / "Cargo.toml"
    cargo = cargo_p.read_text(encoding="utf-8")
    if "gemv_dot_microbench" not in cargo:
        cargo = cargo.replace(
            "[[bin]]\nname = \"decode_1b_bench\"\npath = \"src/bin/decode_1b_bench.rs\"",
            "[[bin]]\nname = \"decode_1b_bench\"\npath = \"src/bin/decode_1b_bench.rs\"\n\n[[bin]]\n"
            'name = "gemv_dot_microbench"\npath = "src/bin/gemv_dot_microbench.rs"\n'
            "test = false\ndoc = false",
        )
        cargo_p.write_bytes(cargo.encode("utf-8"))
        print("perf: Cargo.toml gemv_dot_microbench bin", flush=True)

    fwd = FWD.read_text(encoding="utf-8")
    changed = False
    if "emb_lm:" not in fwd:
        fwd = fwd.replace(
            "    emb: EmbeddingTable,\n    output: Vec<f32>,",
            "    emb: EmbeddingTable,\n    emb_lm: Option<QuantMat>,\n    output: Vec<f32>,",
        )
        changed = True
    if "emb_lm: Some(emb_lm)" not in fwd:
        fwd = fwd.replace(
            "        let emb = EmbeddingTable::from_rows(n_vocab, n_embd, emb_weight)\n"
            "            .map_err(|e| ForwardError::Message(e.to_string()))?;\n",
            "        let emb = EmbeddingTable::from_rows(n_vocab, n_embd, emb_weight)\n"
            "            .map_err(|e| ForwardError::Message(e.to_string()))?;\n"
            "        let mut emb_lm = emb_t.to_quant_mat().map_err(ForwardError::from)?;\n"
            "        emb_lm = orient_in(emb_lm, n_embd);\n"
            "        emb_lm = orient_out(emb_lm, n_vocab);\n",
        )
        fwd = fwd.replace(
            "            emb,\n            output:",
            "            emb,\n            emb_lm: Some(emb_lm),\n            output:",
        )
        changed = True
    if "use crate::decode_profile" not in fwd:
        fwd = fwd.replace(
            "use crate::embed::EmbeddingTable;",
            "use crate::decode_profile;\nuse crate::embed::EmbeddingTable;",
        )
        changed = True
    old_argmax = """    fn logits_argmax_greedy(
        &self,
        hidden: &[f32],
        norm_tmp: &mut [f32],
    ) -> Result<u32, ForwardError> {
        let h = if let Some(ref wn) = self.output_norm {
            rms_norm(hidden, wn, self.rms_eps, norm_tmp);
            norm_tmp.as_ref()
        } else {
            hidden
        };
        let table = if self.tied_output {
            self.emb.weight.as_slice()
        } else {
            self.output.as_slice()
        };
        let n_embd = self.n_embd;
        let n_vocab = self.n_vocab;
        let (_best_score, best_id) = sys::decode_pool().install(|| {
            use rayon::prelude::*;
            (0..n_vocab)
                .into_par_iter()
                .map(|v| {
                    let row = &table[v * n_embd..(v + 1) * n_embd];
                    let mut s = 0.0f32;
                    let mut i = 0usize;
                    while i + 4 <= n_embd {
                        s += h[i] * row[i]
                            + h[i + 1] * row[i + 1]
                            + h[i + 2] * row[i + 2]
                            + h[i + 3] * row[i + 3];
                        i += 4;
                    }
                    while i < n_embd {
                        s += h[i] * row[i];
                        i += 1;
                    }
                    (s, v as u32)
                })
                .reduce(
                    || (f32::NEG_INFINITY, 0u32),
                    |a, b| if a.0 > b.0 { a } else { b },
                )
        });
        Ok(best_id)
    }"""
    new_argmax = """    fn logits_argmax_greedy(
        &self,
        hidden: &[f32],
        norm_tmp: &mut [f32],
        logits: &mut [f32],
    ) -> Result<u32, ForwardError> {
        let h = if let Some(ref wn) = self.output_norm {
            rms_norm(hidden, wn, self.rms_eps, norm_tmp);
            norm_tmp.as_ref()
        } else {
            hidden
        };
        if let Some(ref lm) = self.emb_lm {
            if lm.ggml_type == crate::quant_mat::GGML_Q4_0 {
                let _ = logits;
                return Ok(lm.argmax_gemv(h));
            }
        }
        let table = if self.tied_output {
            self.emb.weight.as_slice()
        } else {
            self.output.as_slice()
        };
        let n_embd = self.n_embd;
        let n_vocab = self.n_vocab;
        let (_best_score, best_id) = sys::decode_pool().install(|| {
            use rayon::prelude::*;
            (0..n_vocab)
                .into_par_iter()
                .map(|v| {
                    let row = &table[v * n_embd..(v + 1) * n_embd];
                    let mut s = 0.0f32;
                    let mut i = 0usize;
                    while i + 4 <= n_embd {
                        s += h[i] * row[i]
                            + h[i + 1] * row[i + 1]
                            + h[i + 2] * row[i + 2]
                            + h[i + 3] * row[i + 3];
                        i += 4;
                    }
                    while i < n_embd {
                        s += h[i] * row[i];
                        i += 1;
                    }
                    (s, v as u32)
                })
                .reduce(
                    || (f32::NEG_INFINITY, 0u32),
                    |a, b| if a.0 > b.0 { a } else { b },
                )
        });
        Ok(best_id)
    }"""
    if old_argmax in fwd:
        fwd = fwd.replace(old_argmax, new_argmax, 1)
        changed = True
    fwd = fwd.replace(
        "self.logits_argmax_greedy(&scratch.hidden, &mut scratch.norm_tmp)?",
        "self.logits_argmax_greedy(&scratch.hidden, &mut scratch.norm_tmp, &mut scratch.logits)?",
    )
    if "decode_profile::reset()" not in fwd:
        fwd = fwd.replace(
            "        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));\n"
            "        for _ in 0..max_new {",
            "        let max_new = max_new.min(ctx.saturating_sub(prompt_ids.len()).max(1));\n"
            "        decode_profile::reset();\n"
            "        for _ in 0..max_new {\n"
            "            let t_lm = if decode_profile::enabled() { Some(std::time::Instant::now()) } else { None };",
        )
        fwd = fwd.replace(
            "            if stop_ids.iter().any(|&s| s == next) {",
            "            if let Some(t0) = t_lm {\n"
            "                decode_profile::add_ns(&decode_profile::accum().lm_head_ns, t0.elapsed().as_nanos());\n"
            "                decode_profile::accum().tokens.fetch_add(1, std::sync::atomic::Ordering::Relaxed);\n"
            "            }\n"
            "            if stop_ids.iter().any(|&s| s == next) {",
        )
        fwd = fwd.replace(
            "        let decode_secs = t_decode.elapsed().as_secs_f64();\n"
            "        Ok(GreedyGenerateOut {",
            "        let decode_secs = t_decode.elapsed().as_secs_f64();\n"
            "        decode_profile::report();\n"
            "        Ok(GreedyGenerateOut {",
        )
        changed = True
    if changed:
        FWD.write_bytes(fwd.encode("utf-8"))
        print("perf: forward emb_lm + Q4 lm_head argmax", flush=True)
    else:
        print("perf: forward perf already present", flush=True)


def run_gemv_microbench() -> None:
    out = run_capture(
        ["cargo", "run", "--release", "-p", "engine-core", "--bin", "gemv_dot_microbench"],
        label="gemv_dot_microbench",
    )
    ratio = None
    row_ratio = None
    for line in out.splitlines():
        if "block_dots_per_s:" in line and "ratio=" in line:
            try:
                ratio = float(line.split("ratio=")[1].split("x")[0])
            except ValueError:
                pass
        if "row_dots_per_s:" in line and "ratio=" in line:
            try:
                row_ratio = float(line.split("ratio=")[1].split("x")[0])
            except ValueError:
                pass
    if row_ratio is not None and row_ratio < 1.0:
        print(f"gemv_dot_microbench WARN: row q8 slower than f32 (row_ratio={row_ratio:.2f}x); keep GE_LM_HEAD_Q8=0", flush=True)
    if row_ratio is not None:
        print(f"gemv_dot_microbench OK row_ratio={row_ratio:.2f}x block_ratio={ratio}", flush=True)


# --- P0.1–P0.4: ggml-style row GEMV, pool nesting fix, shared-act lm_head ---

_GGML_SIMD_HELPERS = r'''
#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn sum_i16_pairs_float(dot: std::arch::x86_64::__m256i) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let ones = _mm256_set1_epi16(1);
    _mm256_cvtepi32_ps(_mm256_madd_epi16(ones, dot))
}

#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn mul_sum_i8_pairs_float_avx2(
    x: std::arch::x86_64::__m256i,
    y: std::arch::x86_64::__m256i,
) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let ax = _mm256_sign_epi8(x, x);
    let sy = _mm256_sign_epi8(y, x);
    let axl = _mm256_castsi256_si128(ax);
    let axh = _mm256_extracti128_si256(ax, 1);
    let syl = _mm256_castsi256_si128(sy);
    let syh = _mm256_extracti128_si256(sy, 1);
    let dotl = _mm_maddubs_epi16(axl, syl);
    let doth = _mm_maddubs_epi16(axh, syh);
    sum_i16_pairs_float(_mm256_set_m128i(doth, dotl))
}
'''


def strip_gemv_q4_orphan_tail(qk: str) -> tuple[str, bool]:
    """Drop duplicate inline tail after gemv_q4_0_row_f32_avx2 (speed-agent merge artifact)."""
    orphan = (
        r"(    hsum256_ps\(acc\) \+ gemv_q4_0_row_f32_tail\(packed_row, x, in_dim, off, n_blocks\)\n\})\n"
        r"    let rem = in_dim % Q4_0_BLOCK;\n"
        r"    if rem != 0 && off \+ Q4_0_BYTES <= packed_row\.len\(\) \{[\s\S]*?\n    sum\n\}\n"
        r"(\n/// Row GEMV:)"
    )
    qk2, n = re.subn(orphan, r"\1\2", qk, count=1)
    return qk2, n > 0


def ensure_avx2_partial_fn(qk: str) -> tuple[str, bool]:
    """Ensure q4_0_block_dot_f32_avx2_partial exists when gemv AVX2 row calls it."""
    if "unsafe fn q4_0_block_dot_f32_avx2_partial" in qk:
        return qk, False
    if "q4_0_block_dot_f32_avx2_partial(" not in qk:
        return qk, False
    partial_fn = (
        "#[cfg(target_arch = \"x86_64\")]\n"
        "#[target_feature(enable = \"avx2\", enable = \"fma\")]\n"
        "unsafe fn q4_0_block_dot_f32_avx2_partial(block: *const u8, x: *const f32) -> std::arch::x86_64::__m256 {\n"
        "    use std::arch::x86_64::*;\n"
        "    let scale = f16_to_f32(u16::from_le_bytes([*block, *block.add(1)]));\n"
        "    let vscale = _mm256_set1_ps(scale);\n"
        "    let qs = _mm_loadu_si128(block.add(2) as *const __m128i);\n"
        "    let low_mask = _mm_set1_epi8(0x0f);\n"
        "    let off8 = _mm_set1_epi8(8);\n"
        "    let q_lo = _mm_sub_epi8(_mm_and_si128(qs, low_mask), off8);\n"
        "    let q_hi = _mm_sub_epi8(_mm_and_si128(_mm_srli_epi16(qs, 4), low_mask), off8);\n"
        "    let mut acc = _mm256_setzero_ps();\n"
        "    let q0 = _mm256_cvtepi8_epi32(q_lo);\n"
        "    let x0 = _mm256_loadu_ps(x);\n"
        "    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q0), vscale), x0, acc);\n"
        "    let q1 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_lo, 8));\n"
        "    let x1 = _mm256_loadu_ps(x.add(8));\n"
        "    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q1), vscale), x1, acc);\n"
        "    let q2 = _mm256_cvtepi8_epi32(q_hi);\n"
        "    let x2 = _mm256_loadu_ps(x.add(16));\n"
        "    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q2), vscale), x2, acc);\n"
        "    let q3 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_hi, 8));\n"
        "    let x3 = _mm256_loadu_ps(x.add(24));\n"
        "    _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q3), vscale), x3, acc)\n"
        "}\n\n"
    )
    markers = [
        "/// Row GEMV: Σ blocks Q4_0 · x (f32 path)",
        "#[cfg(target_arch = \"x86_64\")]\n#[target_feature(enable = \"avx2\", enable = \"fma\")]\nunsafe fn gemv_q4_0_row_f32_avx2",
    ]
    for marker in markers:
        if marker in qk:
            return qk.replace(marker, partial_fn + marker, 1), True
    return qk, False


def apply_p0_gemv_kernels() -> None:
    """P0.2: ggml vec_dot_q4_0_q8_0 row accumulator; P0.1/P0.4 helpers in quant_mat."""
    qk_p = SRC / "quant_kernels.rs"
    qk = qk_p.read_text(encoding="utf-8")
    changed = False

    if "fn mul_sum_i8_pairs_float_avx2" not in qk:
        anchor = "#[cfg(target_arch = \"x86_64\")]\n#[target_feature(enable = \"avx2\", enable = \"fma\")]\nunsafe fn q4_0_block_dot_f32_avx2"
        if anchor not in qk:
            raise SystemExit("P0: quant_kernels.rs avx2 anchor missing")
        qk = qk.replace(anchor, _GGML_SIMD_HELPERS + "\n" + anchor)
        changed = True
        print("P0: quant_kernels ggml simd helpers", flush=True)
    else:
        # Strip broken runtime-detect helpers from prior pass if present.
        bad = (
            "#[cfg(target_arch = \"x86_64\")]\n"
            "#[inline]\n"
            "unsafe fn mul_sum_us8_pairs_float("
        )
        if bad in qk:
            qk = re.sub(
                r"#\[cfg\(target_arch = \"x86_64\"\)\]\n"
                r"#\[inline\]\n"
                r"unsafe fn mul_sum_us8_pairs_float\([\s\S]*?"
                r"unsafe fn mul_sum_i8_pairs_float\([\s\S]*?\n\}\n",
                "",
                qk,
                count=1,
            )
            changed = True
            print("P0: removed broken runtime-detect helpers", flush=True)

    old_vnni = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "avxvnni")]
unsafe fn q4_0_block_dot_q8_avxvnni(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let w = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let ax = _mm256_sign_epi8(qx, qx);
    let sy = _mm256_sign_epi8(qy, qx);
    hsum_epi32_256(_mm256_dpbusd_avx_epi32(_mm256_setzero_si256(), ax, sy)) as f32 * w
}"""
    new_vnni = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "avxvnni")]
unsafe fn q4_0_block_dot_q8_avxvnni(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let d = _mm256_set1_ps(f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale);
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let ax = _mm256_sign_epi8(qx, qx);
    let sy = _mm256_sign_epi8(qy, qx);
    let q = _mm256_cvtepi32_ps(_mm256_dpbusd_avx_epi32(_mm256_setzero_si256(), ax, sy));
    hsum256_ps(_mm256_mul_ps(d, q))
}"""
    if old_vnni in qk:
        qk = qk.replace(old_vnni, new_vnni, 1)
        changed = True

    old512 = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "avx512vnni", enable = "avx512vl")]
unsafe fn q4_0_block_dot_q8_avx512vnni(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let w = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let ax = _mm256_sign_epi8(qx, qx);
    let sy = _mm256_sign_epi8(qy, qx);
    hsum_epi32_256(_mm256_dpbusd_epi32(_mm256_setzero_si256(), ax, sy)) as f32 * w
}"""
    new512 = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "avx512vnni", enable = "avx512vl")]
unsafe fn q4_0_block_dot_q8_avx512vnni(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let d = _mm256_set1_ps(f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale);
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let ax = _mm256_sign_epi8(qx, qx);
    let sy = _mm256_sign_epi8(qy, qx);
    let q = _mm256_cvtepi32_ps(_mm256_dpbusd_epi32(_mm256_setzero_si256(), ax, sy));
    hsum256_ps(_mm256_mul_ps(d, q))
}"""
    if old512 in qk:
        qk = qk.replace(old512, new512, 1)
        changed = True

    old_avx2_q8 = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2")]
unsafe fn q4_0_block_dot_q8_avx2(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let w_scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let qx = _mm_loadu_si128(block.as_ptr().add(2) as *const __m128i);
    let low_mask = _mm_set1_epi8(0x0f);
    let off8 = _mm_set1_epi8(8);
    let q4_lo = _mm_sub_epi8(_mm_and_si128(qx, low_mask), off8);
    let q4_hi = _mm_sub_epi8(_mm_and_si128(_mm_srli_epi16(qx, 4), low_mask), off8);

    let q4_lo16 = _mm256_cvtepi8_epi16(q4_lo);
    let q4_hi16 = _mm256_cvtepi8_epi16(q4_hi);
    let q8_lo16 = _mm256_cvtepi8_epi16(_mm_loadu_si128(qs.as_ptr() as *const __m128i));
    let q8_hi16 = _mm256_cvtepi8_epi16(_mm_loadu_si128(qs.as_ptr().add(16) as *const __m128i));

    let p0 = _mm256_madd_epi16(q4_lo16, q8_lo16);
    let p1 = _mm256_madd_epi16(q4_hi16, q8_hi16);
    let sum = _mm256_add_epi32(p0, p1);

    // horizontal sum of 8×i32
    let sum128 = _mm_add_epi32(
        _mm256_castsi256_si128(sum),
        _mm256_extracti128_si256(sum, 1),
    );
    let sum64 = _mm_add_epi32(sum128, _mm_unpackhi_epi64(sum128, sum128));
    let sum32 = _mm_add_epi32(sum64, _mm_shuffle_epi32(sum64, 0x01));
    let isum = _mm_cvtsi128_si32(sum32);
    isum as f32 * w_scale
}"""
    new_avx2_q8 = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_q8_avx2(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let d = _mm256_set1_ps(f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale);
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let q = mul_sum_i8_pairs_float_avx2(qx, qy);
    hsum256_ps(_mm256_mul_ps(d, q))
}"""
    if old_avx2_q8 in qk:
        qk = qk.replace(old_avx2_q8, new_avx2_q8, 1)
        changed = True

    row_q8_old = """/// Row GEMV: Σ blocks Q4_0 · ActQ8.
pub fn gemv_q4_0_row_q8(packed_row: &[u8], act: &ActQ8) -> f32 {
    let n_blocks = act.n_blocks();
    let mut sum = 0.0f32;
    let mut off = 0usize;
    for b in 0..n_blocks {
        let qs = &act.qs[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
        sum += q4_0_block_dot_q8(
            &packed_row[off..off + Q4_0_BYTES],
            qs,
            act.scales[b],
        );
        off += Q4_0_BYTES;
    }
    sum
}"""
    row_q8_new = """/// Row GEMV: Σ blocks Q4_0 · ActQ8 (ggml vec_dot_q4_0_q8_0 FMA accumulate).
pub fn gemv_q4_0_row_q8(packed_row: &[u8], act: &ActQ8) -> f32 {
    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return gemv_q4_0_row_q8_avx2(packed_row, act);
            }
        }
    }
    let n_blocks = act.n_blocks();
    let mut sum = 0.0f32;
    let mut off = 0usize;
    for b in 0..n_blocks {
        let qs = &act.qs[b * Q4_0_BLOCK..(b + 1) * Q4_0_BLOCK];
        sum += q4_0_block_dot_q8(
            &packed_row[off..off + Q4_0_BYTES],
            qs,
            act.scales[b],
        );
        off += Q4_0_BYTES;
    }
    sum
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn gemv_q4_0_row_q8_avx2(packed_row: &[u8], act: &ActQ8) -> f32 {
    use std::arch::x86_64::*;
    let n_blocks = act.n_blocks();
    let mut acc = _mm256_setzero_ps();
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        for k in 0..4usize {
            let bi = b + k;
            let d = _mm256_set1_ps(
                f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]))
                    * act.scales[bi],
            );
            let qx = _mm256_sub_epi8(
                bytes_from_nibbles_32(packed_row.as_ptr().add(off + 2)),
                _mm256_set1_epi8(8),
            );
            let qy = _mm256_loadu_si256(
                act.qs.as_ptr().add(bi * Q4_0_BLOCK) as *const __m256i,
            );
            let q = mul_sum_i8_pairs_float_avx2(qx, qy);
            acc = _mm256_fmadd_ps(d, q, acc);
            off += Q4_0_BYTES;
        }
        b += 4;
    }
    while b < n_blocks {
        let d = _mm256_set1_ps(
            f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]))
                * act.scales[b],
        );
        let qx = _mm256_sub_epi8(
            bytes_from_nibbles_32(packed_row.as_ptr().add(off + 2)),
            _mm256_set1_epi8(8),
        );
        let qy = _mm256_loadu_si256(act.qs.as_ptr().add(b * Q4_0_BLOCK) as *const __m256i);
        let q = mul_sum_i8_pairs_float_avx2(qx, qy);
        acc = _mm256_fmadd_ps(d, q, acc);
        off += Q4_0_BYTES;
        b += 1;
    }
    hsum256_ps(acc)
}"""
    if row_q8_old in qk:
        qk = qk.replace(row_q8_old, row_q8_new, 1)
        changed = True

    row_f32_old = """/// Row GEMV: Σ blocks Q4_0 · x (f32 path). Block loop unrolled for decode rows.
pub fn gemv_q4_0_row_f32(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut sum = 0.0f32;
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        let x0 = b * Q4_0_BLOCK;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 2 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 3 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        b += 4;
    }
    while b < n_blocks {
        sum += q4_0_block_dot_f32(
            &packed_row[off..off + Q4_0_BYTES],
            &x[b * Q4_0_BLOCK..],
        );
        off += Q4_0_BYTES;
        b += 1;
    }"""
    row_f32_new = """/// Row GEMV: Σ blocks Q4_0 · x (f32 path). Block loop unrolled for decode rows.
pub fn gemv_q4_0_row_f32(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut sum = 0.0f32;
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        let x0 = b * Q4_0_BLOCK;
        #[cfg(target_arch = "x86_64")]
        {
            if b + 4 <= n_blocks.saturating_sub(1) {
                let p = packed_row.as_ptr().add(off);
                _mm_prefetch(p.add(4 * Q4_0_BYTES) as *const i8, _MM_HINT_T0);
            }
        }
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 2 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 3 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        b += 4;
    }
    while b < n_blocks {
        sum += q4_0_block_dot_f32(
            &packed_row[off..off + Q4_0_BYTES],
            &x[b * Q4_0_BLOCK..],
        );
        off += Q4_0_BYTES;
        b += 1;
    }"""
    row_f32_simple = """/// Row GEMV: Σ blocks Q4_0 · x (f32 path).
pub fn gemv_q4_0_row_f32(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut sum = 0.0f32;
    let mut off = 0usize;
    for b in 0..n_blocks {
        sum += q4_0_block_dot_f32(
            &packed_row[off..off + Q4_0_BYTES],
            &x[b * Q4_0_BLOCK..],
        );
        off += Q4_0_BYTES;
    }"""
    row_f32_unrolled = """/// Row GEMV: Σ blocks Q4_0 · x (f32 path). Block loop unrolled for decode rows.
pub fn gemv_q4_0_row_f32(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut sum = 0.0f32;
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        let x0 = b * Q4_0_BLOCK;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 2 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 3 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        b += 4;
    }
    while b < n_blocks {
        sum += q4_0_block_dot_f32(
            &packed_row[off..off + Q4_0_BYTES],
            &x[b * Q4_0_BLOCK..],
        );
        off += Q4_0_BYTES;
        b += 1;
    }"""
    row_f32_avx2 = r'''/// Row GEMV: Σ blocks Q4_0 · x (f32 path). AVX2 accumulates 4 blocks before hsum.
pub fn gemv_q4_0_row_f32(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return gemv_q4_0_row_f32_avx2(packed_row, x, in_dim);
            }
        }
    }
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut sum = 0.0f32;
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        let x0 = b * Q4_0_BLOCK;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 2 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        sum += q4_0_block_dot_f32(&packed_row[off..off + Q4_0_BYTES], &x[x0 + 3 * Q4_0_BLOCK..]);
        off += Q4_0_BYTES;
        b += 4;
    }
    while b < n_blocks {
        sum += q4_0_block_dot_f32(
            &packed_row[off..off + Q4_0_BYTES],
            &x[b * Q4_0_BLOCK..],
        );
        off += Q4_0_BYTES;
        b += 1;
    }
    sum + gemv_q4_0_row_f32_tail(packed_row, x, in_dim, off, n_blocks)
}

#[inline]
fn gemv_q4_0_row_f32_tail(
    packed_row: &[u8],
    x: &[f32],
    in_dim: usize,
    off: usize,
    n_blocks: usize,
) -> f32 {
    let rem = in_dim % Q4_0_BLOCK;
    if rem == 0 || off + Q4_0_BYTES > packed_row.len() {
        return 0.0;
    }
    let scale = f16_to_f32(u16::from_le_bytes([packed_row[off], packed_row[off + 1]]));
    let qs = &packed_row[off + 2..off + 18];
    let base = n_blocks * Q4_0_BLOCK;
    let mut sum = 0.0f32;
    for j in 0..rem.min(16) {
        let byte = qs[j];
        sum += ((byte & 0x0f) as i8 - 8) as f32 * scale * x[base + j];
    }
    for j in 0..rem.saturating_sub(16) {
        let byte = qs[j];
        sum += ((byte >> 4) as i8 - 8) as f32 * scale * x[base + 16 + j];
    }
    sum
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn gemv_q4_0_row_f32_avx2(packed_row: &[u8], x: &[f32], in_dim: usize) -> f32 {
    use std::arch::x86_64::*;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut acc = _mm256_setzero_ps();
    let mut off = 0usize;
    let mut b = 0usize;
    while b + 4 <= n_blocks {
        for k in 0..4usize {
            let bi = b + k;
            acc = _mm256_add_ps(
                acc,
                q4_0_block_dot_f32_avx2_partial(
                    packed_row.as_ptr().add(off),
                    x.as_ptr().add(bi * Q4_0_BLOCK),
                ),
            );
            off += Q4_0_BYTES;
        }
        b += 4;
    }
    while b < n_blocks {
        acc = _mm256_add_ps(
            acc,
            q4_0_block_dot_f32_avx2_partial(
                packed_row.as_ptr().add(off),
                x.as_ptr().add(b * Q4_0_BLOCK),
            ),
        );
        off += Q4_0_BYTES;
        b += 1;
    }
    hsum256_ps(acc) + gemv_q4_0_row_f32_tail(packed_row, x, in_dim, off, n_blocks)
}'''
    if "gemv_q4_0_row_f32_avx2" not in qk:
        start = qk.find("/// Row GEMV: Σ blocks Q4_0 · x (f32 path)")
        end = qk.find("/// Row GEMV: Σ blocks Q4_0 · ActQ8")
        if start >= 0 and end > start:
            qk = qk[:start] + row_f32_avx2 + "\n\n" + qk[end:]
            changed = True
            print("P0: gemv_q4_0_row_f32 AVX2 row kernel", flush=True)
        else:
            print("P0: gemv_q4_0_row_f32 AVX2 anchors missing", flush=True)

    avx2_partial_old = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_f32_avx2(block: &[u8], x: &[f32]) -> f32 {
    use std::arch::x86_64::*;
    let scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let vscale = _mm256_set1_ps(scale);
    let q8 = q4_nibbles_to_i8_32(block);
    let q_lo = _mm256_castsi256_si128(q8);
    let q_hi = _mm256_extracti128_si256(q8, 1);
    let mut acc = _mm256_setzero_ps();
    let q0 = _mm256_cvtepi8_epi32(q_lo);
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q0), vscale), _mm256_loadu_ps(x.as_ptr()), acc);
    let q1 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_lo, 8));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q1), vscale), _mm256_loadu_ps(x.as_ptr().add(8)), acc);
    let q2 = _mm256_cvtepi8_epi32(q_hi);
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q2), vscale), _mm256_loadu_ps(x.as_ptr().add(16)), acc);
    let q3 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_hi, 8));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q3), vscale), _mm256_loadu_ps(x.as_ptr().add(24)), acc);
    hsum256_ps(acc)
}"""
    avx2_partial_new = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_f32_avx2_partial(block: *const u8, x: *const f32) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let block = std::slice::from_raw_parts(block, Q4_0_BYTES);
    let scale = f16_to_f32(u16::from_le_bytes([block[0], block[1]]));
    let vscale = _mm256_set1_ps(scale);
    let q8 = q4_nibbles_to_i8_32(block);
    let q_lo = _mm256_castsi256_si128(q8);
    let q_hi = _mm256_extracti128_si256(q8, 1);
    let mut acc = _mm256_setzero_ps();
    let q0 = _mm256_cvtepi8_epi32(q_lo);
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q0), vscale), _mm256_loadu_ps(x), acc);
    let q1 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_lo, 8));
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q1), vscale), _mm256_loadu_ps(x.add(8)), acc);
    let q2 = _mm256_cvtepi8_epi32(q_hi);
    acc = _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q2), vscale), _mm256_loadu_ps(x.add(16)), acc);
    let q3 = _mm256_cvtepi8_epi32(_mm_srli_si128(q_hi, 8));
    _mm256_fmadd_ps(_mm256_mul_ps(_mm256_cvtepi32_ps(q3), vscale), _mm256_loadu_ps(x.add(24)), acc)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_f32_avx2(block: &[u8], x: &[f32]) -> f32 {
    hsum256_ps(q4_0_block_dot_f32_avx2_partial(block.as_ptr(), x.as_ptr()))
}"""
    if avx2_partial_old in qk and "unsafe fn q4_0_block_dot_f32_avx2_partial" not in qk:
        qk = qk.replace(avx2_partial_old, avx2_partial_new, 1)
        changed = True
        print("P0: q4_0_block_dot_f32_avx2 partial accumulator", flush=True)
    elif (
        "q4_0_block_dot_f32_avx2_partial(" in qk
        and "unsafe fn q4_0_block_dot_f32_avx2_partial" not in qk
    ):
        anchor = "#[cfg(target_arch = \"x86_64\")]\n#[target_feature(enable = \"avx2\", enable = \"fma\")]\nunsafe fn gemv_q4_0_row_f32_avx2"
        if anchor in qk:
            partial_only = avx2_partial_new.split(
                "#[cfg(target_arch = \"x86_64\")]\n#[target_feature(enable = \"avx2\", enable = \"fma\")]\nunsafe fn q4_0_block_dot_f32_avx2"
            )[0]
            qk = qk.replace(anchor, partial_only + anchor, 1)
            changed = True
            print("P0: q4_0_block_dot_f32_avx2_partial inserted (missing def)", flush=True)

    qk, stripped = strip_gemv_q4_orphan_tail(qk)
    if stripped:
        changed = True
        print("P0: stripped orphan gemv_q4_0_row_f32 tail", flush=True)

    if row_f32_old in qk:
        qk = qk.replace(row_f32_old, row_f32_new, 1)
        changed = True
        qk = qk.replace(
            "    while b + 4 <= n_blocks {\n        let x0 = b * Q4_0_BLOCK;\n        #[cfg(target_arch = \"x86_64\")]\n        {\n            if b + 4 <= n_blocks.saturating_sub(1) {\n                let p = packed_row.as_ptr().add(off);\n                _mm_prefetch(p.add(4 * Q4_0_BYTES) as *const i8, _MM_HINT_T0);\n            }\n        }",
            "    while b + 4 <= n_blocks {\n        let x0 = b * Q4_0_BLOCK;\n        #[cfg(target_arch = \"x86_64\")]\n        {\n            use std::arch::x86_64::_MM_HINT_T0;\n            if b + 4 <= n_blocks.saturating_sub(1) {\n                let p = packed_row.as_ptr().add(off);\n                std::arch::x86_64::_mm_prefetch(p.add(4 * Q4_0_BYTES) as *const i8, _MM_HINT_T0);\n            }\n        }",
            1,
        )

    if changed:
        qk_p.write_bytes(qk.encode("utf-8"))
        print("P0: quant_kernels row/dot ggml kernels", flush=True)
    else:
        print("P0: quant_kernels already patched", flush=True)


def apply_p0_quant_mat() -> None:
    """P0.1 chunked lm_head argmax; P0.4 no nested pool / global rayon oversubscribe."""
    qm_p = SRC / "quant_mat.rs"
    qm = qm_p.read_text(encoding="utf-8")
    changed = False

    if "use rayon::prelude::*;\n\nuse crate::" not in qm and "use rayon::prelude::*;\nuse crate::" not in qm:
        qm2, n = re.subn(
            r"use rayon::ThreadPool;\nuse rayon::ThreadPoolBuilder;\n+",
            "use rayon::prelude::*;\n\n",
            qm,
            count=1,
        )
        if n:
            qm = qm2
        else:
            qm = qm.replace(
                "use std::sync::OnceLock;\n\n",
                "use std::sync::OnceLock;\n\nuse rayon::prelude::*;\n\n",
                1,
            )
        changed = True
        print("P0: quant_mat rayon prelude import", flush=True)

    if "fn gemv_chunk_rows" not in qm:
        chunk_fn = """
/// Rows per rayon task (~4× threads) — cuts 128k-task overhead on lm_head argmax.
fn gemv_chunk_rows(out_dim: usize) -> usize {
    let threads = sys::decode_threads();
    let nchunks = threads.saturating_mul(4).max(1);
    ((out_dim + nchunks - 1) / nchunks).max(64)
}

"""
        anchor = "/// Parallelize over output rows when the matrix is large enough to amortize rayon.\nfn gemv_rows_parallel"
        if anchor not in qm:
            raise SystemExit("P0: gemv_rows_parallel anchor missing for chunk insert")
        qm = qm.replace(anchor, chunk_fn + anchor, 1)
        changed = True
        print("P0: gemv_chunk_rows inserted", flush=True)

        old_par = """/// Parallelize over output rows when the matrix is large enough to amortize rayon.
fn gemv_rows_parallel(out_dim: usize, y: &mut [f32], f: impl Fn(usize, &mut f32) + Sync) {
    // Gate/up are 8192 rows; attn projections ~2048. Parallel wins above ~512 rows.
    if out_dim >= 512 {
        use rayon::prelude::*;
        let mut par = || y.par_iter_mut().enumerate().for_each(|(o, yo)| f(o, yo));
        if rayon::current_thread_index().is_some() {
            par();
        } else {
            sys::decode_pool().install(par);
        }
    } else {
        for (o, yo) in y.iter_mut().enumerate() {
            f(o, yo);
        }
    }
}"""
        new_par_chunk = """/// Parallelize over output rows when the matrix is large enough to amortize rayon.
fn gemv_rows_parallel(out_dim: usize, y: &mut [f32], f: impl Fn(usize, &mut f32) + Sync) {
    if out_dim < 512 {
        for (o, yo) in y.iter_mut().enumerate() {
            f(o, yo);
        }
        return;
    }
    let chunk = gemv_chunk_rows(out_dim);
    let mut par = || {
        y.par_chunks_mut(chunk)
            .enumerate()
            .for_each(|(ci, chunk_y)| {
                let base = ci * chunk;
                for (i, yo) in chunk_y.iter_mut().enumerate() {
                    f(base + i, yo);
                }
            });
    };
    if rayon::current_thread_index().is_some() {
        par();
    } else {
        sys::decode_pool().install(par);
    }
}"""
        if old_par in qm:
            qm = qm.replace(old_par, new_par_chunk, 1)
            changed = True

    old_par = """/// Parallelize over output rows when the matrix is large enough to amortize rayon.
fn gemv_rows_parallel(out_dim: usize, y: &mut [f32], f: impl Fn(usize, &mut f32) + Sync) {
    if out_dim >= 512 {
        let chunk = gemv_chunk_rows(out_dim);
        let mut par = || {
            y.par_chunks_mut(chunk)
                .enumerate()
                .for_each(|(ci, chunk_y)| {
                    let base = ci * chunk;
                    for (i, yo) in chunk_y.iter_mut().enumerate() {
                        f(base + i, yo);
                    }
                });
        };
        if rayon::current_thread_index().is_some() {
            par();
        } else {
            sys::decode_pool().install(par);
        }
    } else {
        for (o, yo) in y.iter_mut().enumerate() {
            f(o, yo);
        }
    }
}"""
    new_par = """/// Parallelize over output rows when the matrix is large enough to amortize rayon.
fn gemv_rows_parallel(out_dim: usize, y: &mut [f32], f: impl Fn(usize, &mut f32) + Sync) {
    if out_dim < 512 {
        for (o, yo) in y.iter_mut().enumerate() {
            f(o, yo);
        }
        return;
    }
    let chunk = gemv_chunk_rows(out_dim);
    let mut par = || {
        y.par_chunks_mut(chunk)
            .enumerate()
            .for_each(|(ci, chunk_y)| {
                let base = ci * chunk;
                for (i, yo) in chunk_y.iter_mut().enumerate() {
                    f(base + i, yo);
                }
            });
    };
    if rayon::current_thread_index().is_some() {
        par();
    } else {
        sys::decode_pool().install(par);
    }
}"""
    if old_par in qm:
        qm = qm.replace(old_par, new_par, 1)
        changed = True
        print("P0: gemv_rows_parallel pool-nesting fix", flush=True)

    old_qkv = """pub fn project_qkv_quant(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, x: &[f32],
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    sys::decode_pool().install(|| {
        rayon::join(|| wq.gemv(x, q), || rayon::join(|| wk.gemv(x, k), || wv.gemv(x, v)));
    });
}"""
    new_qkv = """pub fn project_qkv_quant(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, x: &[f32],
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    if rayon::current_thread_index().is_some() {
        wq.gemv(x, q);
        wk.gemv(x, k);
        wv.gemv(x, v);
    } else {
        sys::decode_pool().install(|| {
            rayon::join(|| wq.gemv(x, q), || rayon::join(|| wk.gemv(x, k), || wv.gemv(x, v)));
        });
    }
}"""
    if old_qkv in qm:
        qm = qm.replace(old_qkv, new_qkv, 1)
        changed = True

    old_shared = """pub fn project_qkv_quant_shared(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, act: &GemvActScratch,
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    wq.gemv_with_act(act, q);
    sys::decode_pool().install(|| rayon::join(|| wk.gemv_with_act(act, k), || wv.gemv_with_act(act, v)));
}"""
    new_shared = """pub fn project_qkv_quant_shared(
    wq: &QuantMat, wk: &QuantMat, wv: &QuantMat, act: &GemvActScratch,
    q: &mut [f32], k: &mut [f32], v: &mut [f32],
) {
    if rayon::current_thread_index().is_some() {
        wq.gemv_with_act(act, q);
        wk.gemv_with_act(act, k);
        wv.gemv_with_act(act, v);
    } else {
        sys::decode_pool().install(|| {
            rayon::join(
                || wq.gemv_with_act(act, q),
                || rayon::join(|| wk.gemv_with_act(act, k), || wv.gemv_with_act(act, v)),
            );
        });
    }
}"""
    if old_shared in qm:
        qm = qm.replace(old_shared, new_shared, 1)
        changed = True

    old_swiglu = """pub fn swiglu_quant_shared(
    act: &GemvActScratch, gate: &QuantMat, up: &QuantMat, down: &QuantMat,
    g: &mut [f32], u: &mut [f32], out: &mut [f32],
) {
    sys::decode_pool().install(|| rayon::join(|| gate.gemv_with_act(act, g), || up.gemv_with_act(act, u)));
    for j in 0..g.len() { g[j] = silu(g[j]) * u[j]; }
    down.gemv(g, out);
}"""
    fused_helpers = """
/// Fused gate+up row parallel — one pool pass, no nested `join` oversubscribe.
fn gemv_rows_parallel2(
    out_dim: usize,
    g: &mut [f32],
    u: &mut [f32],
    f: impl Fn(usize, &mut f32, &mut f32) + Sync,
) {
    assert_eq!(g.len(), out_dim);
    assert_eq!(u.len(), out_dim);
    if out_dim < 512 {
        for o in 0..out_dim {
            f(o, &mut g[o], &mut u[o]);
        }
        return;
    }
    let chunk = gemv_chunk_rows(out_dim);
    let mut par = || {
        g.par_chunks_mut(chunk)
            .zip(u.par_chunks_mut(chunk))
            .enumerate()
            .for_each(|(ci, (cg, cu))| {
                let base = ci * chunk;
                for (i, (go, uo)) in cg.iter_mut().zip(cu.iter_mut()).enumerate() {
                    f(base + i, go, uo);
                }
            });
    };
    if rayon::current_thread_index().is_some() {
        par();
    } else {
        sys::decode_pool().install(par);
    }
}

fn swiglu_gate_up_f32(
    gate: &QuantMat,
    up: &QuantMat,
    x: &[f32],
    g: &mut [f32],
    u: &mut [f32],
) {
    if gate.ggml_type == GGML_Q4_0
        && up.ggml_type == GGML_Q4_0
        && gate.in_dim == up.in_dim
        && gate.out_dim == up.out_dim
        && gate.in_dim % Q4_0_BLOCK == 0
    {
        let in_dim = gate.in_dim;
        let out_dim = gate.out_dim;
        let rb = (in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let gp = gate.packed.as_slice();
        let upk = up.packed.as_slice();
        gemv_rows_parallel2(out_dim, g, u, |o, go, uo| {
            *go = quant_kernels::gemv_q4_0_row_f32(&gp[o * rb..(o + 1) * rb], x, in_dim);
            *uo = quant_kernels::gemv_q4_0_row_f32(&upk[o * rb..(o + 1) * rb], x, in_dim);
        });
    } else {
        gate.gemv(x, g);
        up.gemv(x, u);
    }
}

fn swiglu_gate_up_q8_shared(
    gate: &QuantMat,
    up: &QuantMat,
    act: &GemvActScratch,
    g: &mut [f32],
    u: &mut [f32],
) {
    if gate.ggml_type == GGML_Q4_0
        && up.ggml_type == GGML_Q4_0
        && gate.in_dim == up.in_dim
        && gate.out_dim == up.out_dim
        && gate.in_dim % Q4_0_BLOCK == 0
    {
        let out_dim = gate.out_dim;
        let rb = (gate.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let gp = gate.packed.as_slice();
        let upk = up.packed.as_slice();
        gemv_rows_parallel2(out_dim, g, u, |o, go, uo| {
            *go = quant_kernels::gemv_q4_0_row_q8(&gp[o * rb..(o + 1) * rb], &act.act);
            *uo = quant_kernels::gemv_q4_0_row_q8(&upk[o * rb..(o + 1) * rb], &act.act);
        });
    } else {
        gate.gemv_with_act(act, g);
        up.gemv_with_act(act, u);
    }
}

"""
    if "fn gemv_rows_parallel2" not in qm:
        anchor = "// --- Legacy flat paths (div/mod per element; used by GE_GEMV_LEGACY=1 and odd dims) ---"
        if anchor not in qm:
            raise SystemExit("P0: legacy anchor missing for fused gate+up insert")
        qm = qm.replace(anchor, fused_helpers + anchor, 1)
        changed = True
        print("P0: fused gate+up row parallel helpers", flush=True)

    new_swiglu = """pub fn swiglu_quant_shared(
    act: &GemvActScratch, gate: &QuantMat, up: &QuantMat, down: &QuantMat,
    g: &mut [f32], u: &mut [f32], out: &mut [f32],
) {
    swiglu_gate_up_q8_shared(gate, up, act, g, u);
    for j in 0..g.len() { g[j] = silu(g[j]) * u[j]; }
    down.gemv(g, out);
}"""
    if old_swiglu in qm:
        qm = qm.replace(old_swiglu, new_swiglu, 1)
        changed = True

    old_swiglu_f32 = """    if gate.out_dim >= 256 && up.out_dim >= 256 {
        sys::decode_pool().install(|| { rayon::join(|| gate.gemv(x, g), || up.gemv(x, u)); });
    } else {
        gate.gemv(x, g);
        up.gemv(x, u);
    }"""
    new_swiglu_f32 = """    swiglu_gate_up_f32(gate, up, x, g, u);"""
    old_swiglu_f32_nested = """    if gate.out_dim >= 256 && up.out_dim >= 256 {
        if rayon::current_thread_index().is_some() {
            gate.gemv(x, g);
            up.gemv(x, u);
        } else {
            sys::decode_pool().install(|| {
                rayon::join(|| gate.gemv(x, g), || up.gemv(x, u));
            });
        }
    } else {
        gate.gemv(x, g);
        up.gemv(x, u);
    }"""
    if old_swiglu_f32 in qm:
        qm = qm.replace(old_swiglu_f32, new_swiglu_f32, 1)
        changed = True
    elif old_swiglu_f32_nested in qm:
        qm = qm.replace(old_swiglu_f32_nested, new_swiglu_f32, 1)
        changed = True
    elif "swiglu_gate_up_f32(gate, up, x, g, u)" not in qm and "pub fn swiglu_quant(" in qm:
        print("P0: swiglu_quant f32 fused gate+up skip (anchor missing)", flush=True)

    if "pub fn argmax_gemv_act" in qm:
        # Drop broken argmax_gemv_act; keep single argmax_gemv with chunked parallel.
        qm = re.sub(
            r"\n    /// Greedy argmax with pre-quantized activation.*?^    \}\n\n    /// Greedy argmax over output rows",
            "\n\n    /// Greedy argmax over output rows",
            qm,
            count=1,
            flags=re.MULTILINE | re.DOTALL,
        )
        changed = True
        print("P0: removed broken argmax_gemv_act", flush=True)

    per_row_argmax = """    pub fn argmax_gemv(&self, x: &[f32]) -> u32 {
        assert_eq!(x.len(), self.in_dim);
        debug_assert_eq!(self.ggml_type, GGML_Q4_0);
        debug_assert_eq!(self.in_dim % Q4_0_BLOCK, 0);
        let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let packed = self.packed.as_slice();
        let in_dim = self.in_dim;
        let out_dim = self.out_dim;
        use rayon::prelude::*;
        let reduce = || (f32::NEG_INFINITY, 0u32);
        let cmp = |a: (f32, u32), b: (f32, u32)| if a.0 > b.0 || (a.0 == b.0 && a.1 > b.1) { a } else { b };
        let par = || {
            if gemv_use_q8_act() {
                let act = ActQ8::quantize(x);
                (0..out_dim)
                    .into_par_iter()
                    .map(|o| {
                        let s =
                            quant_kernels::gemv_q4_0_row_q8(&packed[o * rb..(o + 1) * rb], &act);
                        (s, o as u32)
                    })
                    .reduce(reduce, cmp)
                    .1
            } else {
                (0..out_dim)
                    .into_par_iter()
                    .map(|o| {
                        let s = quant_kernels::gemv_q4_0_row_f32(
                            &packed[o * rb..(o + 1) * rb],
                            x,
                            in_dim,
                        );
                        (s, o as u32)
                    })
                    .reduce(reduce, cmp)
                    .1
            }
        };
        if rayon::current_thread_index().is_some() {
            par()
        } else {
            sys::decode_pool().install(par)
        }
    }"""
    chunked_argmax = """    pub fn argmax_gemv(&self, x: &[f32]) -> u32 {
        assert_eq!(x.len(), self.in_dim);
        debug_assert_eq!(self.ggml_type, GGML_Q4_0);
        debug_assert_eq!(self.in_dim % Q4_0_BLOCK, 0);
        let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let packed = self.packed.as_slice();
        let in_dim = self.in_dim;
        let out_dim = self.out_dim;
        let reduce = || (f32::NEG_INFINITY, 0u32);
        let cmp = |a: (f32, u32), b: (f32, u32)| if a.0 > b.0 || (a.0 == b.0 && a.1 > b.1) { a } else { b };
        if out_dim < 512 {
            let mut best_s = f32::NEG_INFINITY;
            let mut best_o = 0u32;
            for o in 0..out_dim {
                let s = quant_kernels::gemv_q4_0_row_f32(&packed[o * rb..(o + 1) * rb], x, in_dim);
                if s >= best_s { best_s = s; best_o = o as u32; }
            }
            return best_o;
        }
        let par = || {
            let use_q8 = gemv_use_q8_act();
            let act = if use_q8 { Some(ActQ8::quantize(x)) } else { None };
            let chunk = gemv_chunk_rows(out_dim);
            let nchunks = (out_dim + chunk - 1) / chunk;
            (0..nchunks)
                .into_par_iter()
                .map(|ci| {
                    let start = ci * chunk;
                    let end = (start + chunk).min(out_dim);
                    let mut best_s = f32::NEG_INFINITY;
                    let mut best_o = start as u32;
                    for o in start..end {
                        let s = if use_q8 {
                            quant_kernels::gemv_q4_0_row_q8(
                                &packed[o * rb..(o + 1) * rb],
                                act.as_ref().unwrap(),
                            )
                        } else {
                            quant_kernels::gemv_q4_0_row_f32(
                                &packed[o * rb..(o + 1) * rb],
                                x,
                                in_dim,
                            )
                        };
                        if s >= best_s { best_s = s; best_o = o as u32; }
                    }
                    (best_s, best_o)
                })
                .reduce(reduce, cmp)
                .1
        };
        if rayon::current_thread_index().is_some() {
            par()
        } else {
            sys::decode_pool().install(par)
        }
    }"""
    if per_row_argmax in qm:
        qm = qm.replace(per_row_argmax, chunked_argmax, 1)
        changed = True
        print("P0: argmax_gemv chunked parallel", flush=True)
    elif "gemv_chunk_rows(out_dim)" not in qm and "pub fn argmax_gemv" in qm:
        raise SystemExit("P0: argmax_gemv anchor missing for chunk patch")

    # Remove stale delegate-only argmax bodies from prior passes.
    for stale in (
        """    pub fn argmax_gemv(&self, x: &[f32]) -> u32 {
        if gemv_use_q8_act() {
            let act = ActQ8::quantize(x);
            self.argmax_gemv_act(x, Some(&act))
        } else {
            self.argmax_gemv_act(x, None)
        }
    }""",
        """    pub fn argmax_gemv(&self, x: &[f32]) -> u32 {
        assert_eq!(x.len(), self.in_dim);
        debug_assert_eq!(self.ggml_type, GGML_Q4_0);
        debug_assert_eq!(self.in_dim % Q4_0_BLOCK, 0);
        let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let packed = self.packed.as_slice();
        let in_dim = self.in_dim;
        let out_dim = self.out_dim;
        let reduce = || (f32::NEG_INFINITY, 0u32);
        let cmp = |a: (f32, u32), b: (f32, u32)| if a.0 > b.0 || (a.0 == b.0 && a.1 > b.1) { a } else { b };
        self.argmax_gemv_act(x, None)
    }""",
    ):
        if stale in qm:
            qm = qm.replace(stale, chunked_argmax, 1)
            changed = True

    if "pub fn argmax_gemv_act" not in qm:
        insert_act = """
    /// Greedy argmax with a pre-quantized activation (lm_head shared Q8).
    pub fn argmax_gemv_act(&self, x: &[f32], act: &ActQ8) -> u32 {
        let _ = x;
        assert_eq!(self.in_dim % Q4_0_BLOCK, 0);
        let rb = (self.in_dim / Q4_0_BLOCK) * Q4_0_BYTES;
        let packed = self.packed.as_slice();
        let out_dim = self.out_dim;
        let reduce = || (f32::NEG_INFINITY, 0u32);
        let cmp = |a: (f32, u32), b: (f32, u32)| if a.0 > b.0 || (a.0 == b.0 && a.1 > b.1) { a } else { b };
        let par = || {
            let chunk = gemv_chunk_rows(out_dim);
            let nchunks = (out_dim + chunk - 1) / chunk;
            (0..nchunks)
                .into_par_iter()
                .map(|ci| {
                    let start = ci * chunk;
                    let end = (start + chunk).min(out_dim);
                    let mut best_s = f32::NEG_INFINITY;
                    let mut best_o = start as u32;
                    for o in start..end {
                        let s = quant_kernels::gemv_q4_0_row_q8(&packed[o * rb..(o + 1) * rb], act);
                        if s >= best_s { best_s = s; best_o = o as u32; }
                    }
                    (best_s, best_o)
                })
                .reduce(reduce, cmp)
                .1
        };
        if rayon::current_thread_index().is_some() { par() } else { sys::decode_pool().install(par) }
    }

"""
        anchor = "    /// Greedy argmax over output rows without materializing `out_dim` logits.\n    pub fn argmax_gemv"
        if anchor not in qm:
            raise SystemExit("P0: argmax_gemv anchor missing for argmax_gemv_act insert")
        qm = qm.replace(anchor, insert_act + anchor, 1)
        changed = True
        print("P0: argmax_gemv_act (prequant only) added", flush=True)

    # Tie-break: match Iterator::max_by (last index wins on equal logits).
    tie_cmp = "let cmp = |a: (f32, u32), b: (f32, u32)| if a.0 > b.0 || (a.0 == b.0 && a.1 > b.1) { a } else { b };"
    tie_row = "if s >= best_s { best_s = s; best_o = o as u32; }"
    old_cmp = "let cmp = |a: (f32, u32), b: (f32, u32)| if a.0 > b.0 { a } else { b };"
    old_row = "if s > best_s { best_s = s; best_o = o as u32; }"
    if old_cmp in qm:
        qm = qm.replace(old_cmp, tie_cmp)
        changed = True
    if old_row in qm:
        qm = qm.replace(old_row, tie_row)
        changed = True

    if changed:
        qm_p.write_bytes(qm.encode("utf-8"))
    else:
        print("P0: quant_mat already patched", flush=True)


def apply_p0_forward_lm_head() -> None:
    """P0.3: one Q8 quant for lm_head when GE_LM_HEAD_Q8=1 (FFN stays GE_GEMV_Q8=0)."""
    fwd = FWD.read_text(encoding="utf-8")
    old_sig = """    fn logits_argmax_greedy(
        &self,
        hidden: &[f32],
        norm_tmp: &mut [f32],
        logits: &mut [f32],
    ) -> Result<u32, ForwardError> {"""
    new_sig = """    fn logits_argmax_greedy(
        &self,
        hidden: &[f32],
        norm_tmp: &mut [f32],
        logits: &mut [f32],
        gemv_act: &mut GemvActScratch,
    ) -> Result<u32, ForwardError> {"""
    if old_sig in fwd:
        fwd = fwd.replace(old_sig, new_sig, 1)

    old_lm = """        if let Some(ref lm) = self.emb_lm {
            if lm.ggml_type == crate::quant_mat::GGML_Q4_0 {
                let _ = logits;
                return Ok(lm.argmax_gemv(h));
            }
        }"""
    new_lm = """        if let Some(ref lm) = self.emb_lm {
            if lm.ggml_type == crate::quant_mat::GGML_Q4_0 {
                let _ = logits;
                if lm_head_use_q8_act() {
                    gemv_act.quantize_into(h);
                    return Ok(lm.argmax_gemv_act(h, &gemv_act.act));
                }
                return Ok(lm.argmax_gemv(h));
            }
        }"""
    if old_lm in fwd:
        fwd = fwd.replace(old_lm, new_lm, 1)

    fwd = fwd.replace(
        "self.logits_argmax_greedy(&scratch.hidden, &mut scratch.norm_tmp, &mut scratch.logits)?",
        "self.logits_argmax_greedy(&scratch.hidden, &mut scratch.norm_tmp, &mut scratch.logits, &mut scratch.gemv_act)?",
    )
    if "gemv_act: &mut GemvActScratch" in fwd and "argmax_gemv_act" in fwd:
        fwd = fwd.replace(
            "use crate::quant_mat::{gemv_use_q8_act, project_qkv_quant",
            "use crate::quant_mat::{gemv_use_q8_act, lm_head_use_q8_act, project_qkv_quant",
            1,
        )
        FWD.write_bytes(fwd.encode("utf-8"))
        print("P0: forward lm_head shared Q8 quant path", flush=True)
    else:
        print("P0: forward lm_head skip (already wired or anchor missing)", flush=True)


def apply_lm_head_q8_quant_mat() -> None:
    """GE_LM_HEAD_Q8=1: lm_head-only Q4×Q8; FFN/attn remain on GE_GEMV_Q8 gate."""
    qm_p = SRC / "quant_mat.rs"
    qm = qm_p.read_text(encoding="utf-8")
    fn_body = """/// lm_head-only Q4×Q8 path (`GE_LM_HEAD_Q8=1`); FFN/attn stay on f32×Q4 unless `GE_GEMV_Q8=1`.
pub fn lm_head_use_q8_act() -> bool {
    if gemv_force_f32_env() { return false; }
    match std::env::var("GE_LM_HEAD_Q8") {
        Ok(v) if matches!(v.as_str(), "0" | "false" | "FALSE" | "no" | "NO") => false,
        Ok(v) if matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES") => {
            quant_kernels::has_avx_vnni()
        }
        Err(_) => gemv_use_q8_act(),
        _ => false,
    }
}

"""
    if "pub fn lm_head_use_q8_act" not in qm:
        end_anchor = """        Err(_) => false,
        _ => false,
    }
}

"""
        if end_anchor in qm:
            qm = qm.replace(end_anchor, end_anchor + fn_body, 1)
            qm_p.write_bytes(qm.encode("utf-8"))
            print("P0: lm_head_use_q8_act added", flush=True)
        else:
            anchor = "\n\nfn gemv_isa() -> GemvIsa {"
            if anchor in qm:
                qm = qm.replace(anchor, "\n\n" + fn_body + "fn gemv_isa() -> GemvIsa {", 1)
                qm_p.write_bytes(qm.encode("utf-8"))
                print("P0: lm_head_use_q8_act added (gemv_isa anchor)", flush=True)
            else:
                print("P0: lm_head_use_q8_act anchor missing", flush=True)
    else:
        bad = "}\n\n/// lm_head-only Q4×Q8 path"
        good = "/// lm_head-only Q4×Q8 path"
        if bad in qm:
            qm = qm.replace(bad, good, 1)
            qm_p.write_bytes(qm.encode("utf-8"))
            print("P0: lm_head_use_q8_act stray brace fixed", flush=True)


def apply_ggml_block_dot_final_fix() -> None:
    """ggml vec_dot_q4_0_q8_0: int hsum VNNI block dot + mul_sum_us8 in row path."""
    qk_p = SRC / "quant_kernels.rs"
    qk = qk_p.read_text(encoding="utf-8")
    changed = False

    dispatch_old = """    #[cfg(target_arch = "x86_64")] {
        if is_x86_feature_detected!("avxvnni") {
            unsafe { return q4_0_block_dot_q8_avxvnni(block, qs, act_scale); }
        }
        if is_x86_feature_detected!("avx512vnni") && is_x86_feature_detected!("avx512vl") {
            unsafe { return q4_0_block_dot_q8_avx512vnni(block, qs, act_scale); }
        }
        if is_x86_feature_detected!("avx2") {
            unsafe { return q4_0_block_dot_q8_avx2(block, qs, act_scale); }
        }
    }"""
    dispatch_new = """    #[cfg(target_arch = "x86_64")]
    {
        if is_x86_feature_detected!("avx2") && is_x86_feature_detected!("fma") {
            unsafe {
                return q4_0_block_dot_q8_avx2(block, qs, act_scale);
            }
        }
    }"""
    if dispatch_old in qk:
        qk = qk.replace(dispatch_old, dispatch_new, 1)
        changed = True

    us8_helper = """#[cfg(target_arch = "x86_64")]
#[inline]
unsafe fn mul_sum_us8_pairs_float_avx2(
    ax: std::arch::x86_64::__m256i,
    sy: std::arch::x86_64::__m256i,
) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    if is_x86_feature_detected!("avxvnni") {
        let zero = _mm256_setzero_si256();
        return _mm256_cvtepi32_ps(_mm256_dpbusd_avx_epi32(zero, ax, sy));
    }
    if is_x86_feature_detected!("avx512vnni") && is_x86_feature_detected!("avx512vl") {
        let zero = _mm256_setzero_si256();
        return _mm256_cvtepi32_ps(_mm256_dpbusd_epi32(zero, ax, sy));
    }
    let axl = _mm256_castsi256_si128(ax);
    let axh = _mm256_extracti128_si256(ax, 1);
    let syl = _mm256_castsi256_si128(sy);
    let syh = _mm256_extracti128_si256(sy, 1);
    let dotl = _mm_maddubs_epi16(axl, syl);
    let doth = _mm_maddubs_epi16(axh, syh);
    sum_i16_pairs_float(_mm256_set_m128i(doth, dotl))
}

"""
    if "fn mul_sum_us8_pairs_float_avx2" not in qk:
        anchor = "#[cfg(target_arch = \"x86_64\")]\n#[inline]\nunsafe fn mul_sum_i8_pairs_float_avx2"
        if anchor in qk:
            qk = qk.replace(anchor, us8_helper + anchor, 1)
            changed = True

    i8_old = """unsafe fn mul_sum_i8_pairs_float_avx2(
    x: std::arch::x86_64::__m256i,
    y: std::arch::x86_64::__m256i,
) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let ax = _mm256_sign_epi8(x, x);
    let sy = _mm256_sign_epi8(y, x);
    let axl = _mm256_castsi256_si128(ax);
    let axh = _mm256_extracti128_si256(ax, 1);
    let syl = _mm256_castsi256_si128(sy);
    let syh = _mm256_extracti128_si256(sy, 1);
    let dotl = _mm_maddubs_epi16(axl, syl);
    let doth = _mm_maddubs_epi16(axh, syh);
    sum_i16_pairs_float(_mm256_set_m128i(doth, dotl))
}"""
    i8_new = """unsafe fn mul_sum_i8_pairs_float_avx2(
    x: std::arch::x86_64::__m256i,
    y: std::arch::x86_64::__m256i,
) -> std::arch::x86_64::__m256 {
    use std::arch::x86_64::*;
    let ax = _mm256_sign_epi8(x, x);
    let sy = _mm256_sign_epi8(y, x);
    mul_sum_us8_pairs_float_avx2(ax, sy)
}"""
    if i8_old in qk:
        qk = qk.replace(i8_old, i8_new, 1)
        changed = True

    avx2_old = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_q8_avx2(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let d = _mm256_set1_ps(f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale);
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    let q = mul_sum_i8_pairs_float_avx2(qx, qy);
    hsum256_ps(_mm256_mul_ps(d, q))
}"""
    avx2_new = """#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2", enable = "fma")]
unsafe fn q4_0_block_dot_q8_avx2(block: &[u8], qs: &[i8], act_scale: f32) -> f32 {
    use std::arch::x86_64::*;
    let w = f16_to_f32(u16::from_le_bytes([block[0], block[1]])) * act_scale;
    let qx = _mm256_sub_epi8(bytes_from_nibbles_32(block.as_ptr().add(2)), _mm256_set1_epi8(8));
    let qy = _mm256_loadu_si256(qs.as_ptr() as *const __m256i);
    if is_x86_feature_detected!("avxvnni") {
        let ax = _mm256_sign_epi8(qx, qx);
        let sy = _mm256_sign_epi8(qy, qx);
        let isum = hsum_epi32_256(_mm256_dpbusd_avx_epi32(_mm256_setzero_si256(), ax, sy));
        return isum as f32 * w;
    }
    if is_x86_feature_detected!("avx512vnni") && is_x86_feature_detected!("avx512vl") {
        let ax = _mm256_sign_epi8(qx, qx);
        let sy = _mm256_sign_epi8(qy, qx);
        let isum = hsum_epi32_256(_mm256_dpbusd_epi32(_mm256_setzero_si256(), ax, sy));
        return isum as f32 * w;
    }
    let q = mul_sum_i8_pairs_float_avx2(qx, qy);
    hsum256_ps(_mm256_mul_ps(_mm256_set1_ps(w), q))
}"""
    if avx2_old in qk:
        qk = qk.replace(avx2_old, avx2_new, 1)
        changed = True

    # Drop standalone VNNI block-dot helpers (folded into avx2).
    qk2, n = re.subn(
        r"#\[cfg\(target_arch = \"x86_64\"\)\]\n"
        r"#\[target_feature\(enable = \"avx2\", enable = \"avxvnni\"\)\]\n"
        r"unsafe fn q4_0_block_dot_q8_avxvnni\([\s\S]*?\n\}\n",
        "",
        qk,
        count=1,
    )
    if n:
        qk = qk2
        changed = True
    qk2, n = re.subn(
        r"#\[cfg\(target_arch = \"x86_64\"\)\]\n"
        r"#\[target_feature\(enable = \"avx2\", enable = \"avx512vnni\", enable = \"avx512vl\"\)\]\n"
        r"unsafe fn q4_0_block_dot_q8_avx512vnni\([\s\S]*?\n\}\n",
        "",
        qk,
        count=1,
    )
    if n:
        qk = qk2
        changed = True

    qk, stripped = strip_gemv_q4_orphan_tail(qk)
    if stripped:
        changed = True
        print("P0: stripped orphan gemv_q4_0_row_f32 tail (final)", flush=True)

    if changed:
        qk_p.write_bytes(qk.encode("utf-8"))
        print("P0: ggml block-dot final fix", flush=True)
    else:
        print("P0: ggml block-dot already final", flush=True)


def apply_p0_lm_head_rows4_pin() -> None:
    """P0.5: 4-row lm_head GEMV, chunked argmax (not 128k tasks), thread pinning."""
    overlay = REPO / "scripts" / "overlays" / "engine-core"
    if not overlay.is_dir():
        print("P0.5: overlay dir missing (skip rows4/pin)", flush=True)
        return
    for name in ("quant_kernels.rs", "quant_mat.rs", "sys.rs"):
        src = overlay / name
        dst = SRC / name
        if src.is_file():
            dst.write_bytes(src.read_bytes())
            print(f"P0.5: overlay {name}", flush=True)
        else:
            print(f"P0.5: overlay missing {name}", flush=True)


def apply_p0_gemv_opt() -> None:
    """Step 10: P0.1–P0.4 decode GEMV + lm_head + pool fixes."""
    apply_p0_gemv_kernels()
    apply_ggml_block_dot_final_fix()
    apply_p0_quant_mat()
    apply_lm_head_q8_quant_mat()
    apply_p0_forward_lm_head()
    qk_p = SRC / "quant_kernels.rs"
    qk = qk_p.read_text(encoding="utf-8")
    qk, stripped = strip_gemv_q4_orphan_tail(qk)
    qk, ensured = ensure_avx2_partial_fn(qk)
    if stripped or ensured:
        qk_p.write_bytes(qk.encode("utf-8"))
        if stripped:
            print("P0: stripped orphan gemv_q4_0_row_f32 tail (post-opt)", flush=True)
        if ensured:
            print("P0: ensured q4_0_block_dot_f32_avx2_partial def (post-opt)", flush=True)
    apply_p0_lm_head_rows4_pin()
    print("step 10: P0 GEMV opt OK", flush=True)


def apply_patches() -> str:
    run(
        ["git", "checkout", BASE, "--", "crates/engine-core/src/"],
        label=f"git checkout {BASE} crates/engine-core/src/",
    )
    prune_orphan_src()
    prune_orphan_cargo_bins()
    patch_atomic_merge()
    run([PY, str(REPO / "patch_all.py")], label="patch_all.py")
    patch_parallel_support()
    patch_parallel_forward()
    run(
        [PY, str(REPO / "scripts" / "install_resident.py"), "--after-merge"],
        label="install_resident.py --after-merge",
    )
    patch_forward_decode_session()
    apply_score_cap_borrow_fix()
    apply_vnni_step8()
    apply_forward_token_shared_act()
    apply_greedy_argmax_p1()
    fix_q4_0_fast_test()
    apply_perf_patches()
    run(
        [PY, str(REPO / "scripts" / "_apply_q4_argmax_patch.py")],
        label="_apply_q4_argmax_patch.py",
    )
    apply_forward_q8_q4_only_gate()
    apply_q6k_dequant_fix()
    apply_prefill_kv_fix()
    install_q4km_prefill_diag()
    apply_p0_gemv_opt()
    run([PY, str(REPO / "scripts" / "patch_repack_gemv.py")], label="patch_repack_gemv.py")
    digest = normalize_forward_utf8()
    patch_bench_ignore_eos()
    return digest


def apply_gpu_integration_safe() -> None:
    """Wire GPU decode on recovered sources (no forward.rs git checkout)."""
    if os.environ.get("GE_RECOVER_SKIP_GPU", "").strip().lower() in ("1", "true", "yes"):
        print("skip GPU integration (GE_RECOVER_SKIP_GPU)", flush=True)
        return
    spec = importlib.util.spec_from_file_location(
        "apply_gpu_integration", REPO / "scripts" / "apply_gpu_integration.py"
    )
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    mod.apply_gpu_integration_safe()


def build_and_verify(*, skip_bench: bool, microbench: bool = True) -> None:
    normalize_forward_utf8()  # re-check after GPU patches / editor races
    if microbench:
        run(
            ["cargo", "build", "--release", "-p", "engine-core", "--bin", "gemv_dot_microbench"],
            label="cargo build gemv_dot_microbench",
        )
        run_gemv_microbench()
    # Prefer --features gpu when CUDA toolkit / kernels DLL is present (CPU-only hosts stay plain).
    run(
        [PY, str(REPO / "scripts" / "build_ge_release.py"), "--also-decode-bench"],
        label="build_ge_release.py (--features gpu when CUDA/kernels available)",
    )
    # Always exercise the gpu-feature compile path once (stub OK without DLL) so CI catches bitrot.
    if os.environ.get("GE_RECOVER_SKIP_GPU_STUB", "").strip().lower() not in (
        "1",
        "true",
        "yes",
    ):
        run(
            [
                "cargo",
                "build",
                "--release",
                "-p",
                "engine-core",
                "--features",
                "gpu",
                "--bin",
                "decode_1b_bench",
            ],
            label="cargo build decode_1b_bench --features gpu (stub/link check)",
        )
    run(
        ["cargo", "test", "-p", "engine-core", "--lib", "--release", "--", "--test-threads=1"],
        label="cargo test --lib",
    )
    run(
        ["cargo", "test", "-p", "engine-core", "--test", "native_generate_smoke", "--release"],
        label="native_generate_smoke",
    )
    verify_q4km_logits()
    if (EC / "tests" / "native_quality_regression.rs").is_file():
        run(
            ["cargo", "test", "-p", "engine-core", "--test", "native_quality_regression", "--release"],
            label="native_quality_regression",
        )
    else:
        print("native_quality_regression: skipped (test file absent)", flush=True)
    if skip_bench:
        return
    home = os.environ.get("USERPROFILE", str(Path.home()))
    model = Path(home) / ".green" / "models" / "Llama-3.2-1B.green"
    bench = REPO / "target" / "release" / "decode_1b_bench.exe"
    if not bench.is_file():
        bench = REPO / "target" / "release" / "decode_1b_bench"
    env = {**os.environ, "GE_BENCH_IGNORE_EOS": "1", "GE_GEMV_Q8": "0"}
    run(
        [str(bench), str(model), "32"],
        label="decode_1b_bench warm n=32 GE_GEMV_Q8=0 GE_LM_HEAD_Q8=0",
        env=env,
    )
    env_lm = {
        **os.environ,
        "GE_BENCH_IGNORE_EOS": "1",
        "GE_GEMV_Q8": "0",
        "GE_LM_HEAD_Q8": "1",
    }
    run(
        [str(bench), str(model), "32"],
        label="decode_1b_bench warm n=32 GE_LM_HEAD_Q8=1 (FFN f32)",
        env=env_lm,
    )
    env_prof = {**os.environ, "GE_BENCH_IGNORE_EOS": "1", "GE_DECODE_PROFILE": "1"}
    run(
        [str(bench), str(model), "32"],
        label="decode_1b_bench profile n=32",
        env=env_prof,
    )


def main() -> None:
    parser = argparse.ArgumentParser(description="Recover engine-core patches + build + bench")
    parser.add_argument("--patch-only", action="store_true", help="Apply patches only")
    parser.add_argument("--skip-bench", action="store_true", help="Build and test but skip decode_1b_bench")
    parser.add_argument(
        "--full",
        action="store_true",
        help="Apply all patches (steps 1-9) + build + test + bench",
    )
    args = parser.parse_args()
    digest = apply_patches()
    apply_gpu_integration_safe()
    if args.patch_only:
        print(f"\n=== PATCH ONLY OK forward.rs sha256={digest} ===", flush=True)
        return
    build_and_verify(skip_bench=args.skip_bench)
    print(f"\n=== ALL OK forward.rs sha256={digest} ===", flush=True)


if __name__ == "__main__":
    main()
