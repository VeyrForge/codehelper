//! Green Engine — Green-Compress weight benchmark (manifest-driven).
//!
//! Loads a whole model compressed by Green Compress (every attn/ffn weight, any architecture) via
//! the `model_manifest.json` that `scripts/compress_model.py` emits, runs a decode loop through all
//! tensors, and reports tok/s, weight RAM, compression ratio, and quality vs the fp32 reference —
//! overall and broken down per tensor class. This is the path that lets Green Engine *run* compressed
//! weights directly, reusing the Green Compress decoder, instead of only loading GGUF via llama.cpp.
//!
//! Usage:
//!   green-weights-bench --manifest WORKDIR/model_manifest.json [--tokens 32] [--iters 20] [--backend cpu|gpu|auto]
//!
//! Legacy explicit mode (single tensor class) is still available:
//!   green-weights-bench --ref-dir DIR --bench-dir DIR --acts X.mx --layers 0,16,31 --methods ...

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::time::Instant;

use greencompress::backend::ComputeBackend;
use greencompress::infer::{infer_layer_runtime_with_backend, load_layer_runtime, matmul_fp32_with_backend};
use greencompress::io::load_matrix;
use greencompress::types::Matrix;
use greencompress::util::rel_l2;
#[cfg(feature = "gpu")]
use greencompress::GpuSession;
#[cfg(feature = "gpu")]
use greencompress::gpu_available;

fn arg(args: &[String], key: &str, default: &str) -> String {
    args.iter()
        .position(|a| a == key)
        .and_then(|i| args.get(i + 1))
        .cloned()
        .unwrap_or_else(|| default.to_string())
}

fn has_flag(args: &[String], key: &str) -> bool {
    args.iter().any(|a| a == key)
}

/// Sum the on-disk size of a compressed layer directory (w.q8, w.rep, w.out, ...).
fn dir_bytes(dir: &Path) -> u64 {
    fs::read_dir(dir)
        .map(|rd| {
            rd.filter_map(|e| e.ok())
                .filter_map(|e| e.metadata().ok())
                .filter(|m| m.is_file())
                .map(|m| m.len())
                .sum()
        })
        .unwrap_or(0)
}

fn median(mut v: Vec<f64>) -> f64 {
    v.sort_by(|a, b| a.partial_cmp(b).unwrap());
    if v.is_empty() {
        0.0
    } else {
        v[v.len() / 2]
    }
}

/// Deterministic activation matrix [tokens x in_dim] in roughly [-1, 1] (LCG, no deps).
fn gen_acts(tokens: usize, in_dim: usize) -> Matrix {
    let mut s: u64 = 0x9E3779B97F4A7C15;
    let mut data = vec![0f32; tokens * in_dim];
    for v in data.iter_mut() {
        s = s.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
        *v = ((s >> 33) as f32 / (1u64 << 31) as f32) - 1.0;
    }
    Matrix {
        rows: tokens as u32,
        cols: in_dim as u32,
        data,
    }
}

/// Tensor class from a GGUF tensor name: `blk.3.ffn_down.weight` -> `ffn_down`.
fn class_of(name: &str) -> String {
    let parts: Vec<&str> = name.split('.').collect();
    if name.starts_with("blk.") && parts.len() >= 3 {
        parts[2].to_string()
    } else {
        name.trim_end_matches(".weight").to_string()
    }
}

#[derive(Default, Clone)]
struct Agg {
    weight_bytes: u64,
    secs: f64,
    quality_sum: f64,
    count: usize,
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let iters: u32 = arg(&args, "--iters", "20").parse().unwrap_or(20);
    let tokens: usize = arg(&args, "--tokens", "32").parse().unwrap_or(32);
    let backend = ComputeBackend::parse(&arg(&args, "--backend", "cpu"));

    if !has_flag(&args, "--manifest") {
        eprintln!("error: --manifest WORKDIR/model_manifest.json is required");
        std::process::exit(2);
    }
    let manifest_path = PathBuf::from(arg(&args, "--manifest", ""));
    let raw = fs::read_to_string(&manifest_path).expect("read manifest");
    let mani: serde_json::Value = serde_json::from_str(&raw).expect("parse manifest");

    let model = mani["model"].as_str().unwrap_or("?");
    let archv = mani["arch"].as_str().unwrap_or("?");
    let methods: Vec<String> = mani["methods"]
        .as_array()
        .unwrap()
        .iter()
        .map(|m| m.as_str().unwrap().to_string())
        .collect();
    let tensors = mani["tensors"].as_array().unwrap();

    #[cfg(feature = "gpu")]
    let mut gpu_session = if matches!(backend, ComputeBackend::Gpu | ComputeBackend::Auto) {
        GpuSession::try_new()
    } else {
        None
    };

    // fp32 baseline aggregate + per-method aggregates, plus per-class/per-method breakdown.
    let mut fp32 = Agg::default();
    let mut per_method: BTreeMap<String, Agg> = BTreeMap::new();
    // class -> method -> Agg  (method "" == fp32)
    let mut per_class: BTreeMap<String, BTreeMap<String, Agg>> = BTreeMap::new();

    for t in tensors {
        let name = t["name"].as_str().unwrap();
        let rows = t["rows"].as_u64().unwrap() as usize;
        let class = class_of(name);
        let acts = gen_acts(tokens, rows);

        // fp32 reference
        let refm = load_matrix(Path::new(t["ref"].as_str().unwrap())).expect("load ref");
        let ref_bytes = (refm.rows as u64) * (refm.cols as u64) * 4;
        let mut times = Vec::new();
        for _ in 0..iters.max(1) {
            let s = Instant::now();
            #[cfg(feature = "gpu")]
            let _ = matmul_fp32_with_backend(&acts, &refm, backend, gpu_session.as_mut()).expect("fp32 matmul");
            #[cfg(not(feature = "gpu"))]
            let _ = matmul_fp32_with_backend(&acts, &refm, backend, None).expect("fp32 matmul");
            times.push(s.elapsed().as_secs_f64());
        }
        let fp32_secs = median(times);
        #[cfg(feature = "gpu")]
        let ref_out = matmul_fp32_with_backend(&acts, &refm, backend, gpu_session.as_mut()).expect("fp32 matmul");
        #[cfg(not(feature = "gpu"))]
        let ref_out = matmul_fp32_with_backend(&acts, &refm, backend, None).expect("fp32 matmul");

        fp32.weight_bytes += ref_bytes;
        fp32.secs += fp32_secs;
        fp32.quality_sum += 100.0;
        fp32.count += 1;
        let cf = per_class.entry(class.clone()).or_default().entry(String::new()).or_default();
        cf.weight_bytes += ref_bytes;
        cf.secs += fp32_secs;
        cf.quality_sum += 100.0;
        cf.count += 1;

        // each compressed method
        let dirs = t["dirs"].as_object().unwrap();
        for m in &methods {
            let dir = PathBuf::from(dirs[m].as_str().unwrap());
            let rt = load_layer_runtime(&dir).expect("load compressed layer");
            let cache_key = dir.to_string_lossy().to_string();
            let bytes = dir_bytes(&dir);
            let mut times = Vec::new();
            let mut out = None;
            for _ in 0..iters.max(1) {
                let s = Instant::now();
                #[cfg(feature = "gpu")]
                let y = infer_layer_runtime_with_backend(
                    &rt, &acts, backend, gpu_session.as_mut(), &cache_key,
                )
                .expect("decode");
                #[cfg(not(feature = "gpu"))]
                let y = infer_layer_runtime_with_backend(&rt, &acts, backend, None, &cache_key)
                    .expect("decode");
                times.push(s.elapsed().as_secs_f64());
                out = Some(y);
            }
            let secs = median(times);
            let q = (1.0 - rel_l2(&ref_out.data, &out.unwrap().data)).max(0.0) * 100.0;

            let a = per_method.entry(m.clone()).or_default();
            a.weight_bytes += bytes;
            a.secs += secs;
            a.quality_sum += q;
            a.count += 1;
            let c = per_class.entry(class.clone()).or_default().entry(m.clone()).or_default();
            c.weight_bytes += bytes;
            c.secs += secs;
            c.quality_sum += q;
            c.count += 1;
        }
    }

    // ---- report ----
    let mib = |b: u64| b as f64 / (1024.0 * 1024.0);
    let row = |label: &str, a: &Agg, fp32_secs: f64, fp32_bytes: u64| {
        let tok_s = tokens as f64 / a.secs;
        println!(
            "{:<18} {:>9.3}% {:>11.2} {:>8.2}x {:>9.0} {:>10.2}x {:>11.3}",
            label,
            a.quality_sum / a.count.max(1) as f64,
            mib(a.weight_bytes),
            fp32_bytes as f64 / a.weight_bytes.max(1) as f64,
            tok_s,
            fp32_secs / a.secs,
            a.secs * 1000.0,
        );
    };

    println!(
        "Green Engine — whole-model Green-Compress bench\n  model={model}  arch={archv}  \
         tensors={}  tokens/pass={tokens}  iters={iters}  backend={}\n",
        tensors.len(),
        backend.as_str(),
    );
    #[cfg(feature = "gpu")]
    if matches!(backend, ComputeBackend::Gpu | ComputeBackend::Auto) {
        println!(
            "  cuda_available={}  session={}",
            gpu_available(),
            if gpu_session.is_some() { "yes" } else { "no (fallback cpu)" }
        );
    }
    println!(
        "{:<18} {:>10} {:>11} {:>9} {:>9} {:>11} {:>11}",
        "Method", "Quality", "RAM(MiB)", "Compr", "tok/s", "Speedup", "Pass(ms)"
    );
    println!("{}", "-".repeat(84));
    row("fp32_uncompressed", &fp32, fp32.secs, fp32.weight_bytes);
    for m in &methods {
        row(m, &per_method[m], fp32.secs, fp32.weight_bytes);
    }

    // per-class quality + compression (per method)
    println!("\nPer tensor class (quality% | compression x):");
    print!("  {:<14}", "class");
    for m in &methods {
        print!("  {:>22}", m);
    }
    println!();
    for (cls, mm) in &per_class {
        let fpb = mm.get("").map(|a| a.weight_bytes).unwrap_or(0);
        print!("  {:<14}", cls);
        for m in &methods {
            if let Some(a) = mm.get(m) {
                let q = a.quality_sum / a.count.max(1) as f64;
                let c = fpb as f64 / a.weight_bytes.max(1) as f64;
                print!("  {:>11.2}% {:>8.2}x", q, c);
            } else {
                print!("  {:>22}", "-");
            }
        }
        println!();
    }

    let best = methods.last().cloned().unwrap_or_default();
    if let Some(a) = per_method.get(&best) {
        println!(
            "\nWeight RAM: fp32 {:.1} MiB -> {} {:.1} MiB  ({:.0}% saved)",
            mib(fp32.weight_bytes),
            best,
            mib(a.weight_bytes),
            100.0 * (1.0 - a.weight_bytes as f64 / fp32.weight_bytes as f64),
        );
    }
}
