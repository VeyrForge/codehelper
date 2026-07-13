use std::collections::HashMap;
use std::fs::File;
use std::io::{Seek, SeekFrom};
use std::path::Path;
use std::time::Instant;

use crate::error::{fail, Result};
use crate::types::Args;

pub fn parse_args(args: &[String]) -> Result<Args> {
    if args.len() < 2 {
        return Err(fail("missing command"));
    }
    let command = args[1].clone();
    let mut values = HashMap::new();
    let mut i = 2;
    while i < args.len() {
        let key = &args[i];
        if !key.starts_with("--") {
            return Err(fail(format!("expected --key, got: {key}")));
        }
        if i + 1 >= args.len() {
            return Err(fail(format!("missing value for: {key}")));
        }
        values.insert(key[2..].to_string(), args[i + 1].clone());
        i += 2;
    }
    Ok(Args { command, values })
}

pub fn get_optional_string(args: &Args, key: &str, fallback: &str) -> String {
    args.values.get(key).cloned().unwrap_or_else(|| fallback.to_string())
}

pub fn get_string(args: &Args, key: &str, fallback: &str) -> Result<String> {
    match args.values.get(key) {
        Some(v) => Ok(v.clone()),
        None if !fallback.is_empty() => Ok(fallback.to_string()),
        None => Err(fail(format!("missing --{key}"))),
    }
}

pub fn get_u32(args: &Args, key: &str, fallback: u32, required: bool) -> Result<u32> {
    match args.values.get(key) {
        Some(v) => v
            .parse()
            .map_err(|_| fail(format!("invalid u32 for --{key}"))),
        None if required => Err(fail(format!("missing --{key}"))),
        None => Ok(fallback),
    }
}

pub fn get_f32(args: &Args, key: &str, fallback: f32, required: bool) -> Result<f32> {
    match args.values.get(key) {
        Some(v) => v
            .parse()
            .map_err(|_| fail(format!("invalid f32 for --{key}"))),
        None if required => Err(fail(format!("missing --{key}"))),
        None => Ok(fallback),
    }
}

pub fn get_u32_flag(args: &Args, key: &str, default: u32) -> u32 {
    args.values
        .get(key)
        .and_then(|v| v.parse().ok())
        .unwrap_or(default)
}

pub fn print_size(label: &str, bytes: u64) {
    let mb = bytes as f64 / (1024.0 * 1024.0);
    println!("{label:<28}{bytes} bytes / {mb:.3} MiB");
}

pub fn file_size(path: &Path) -> u64 {
    File::open(path)
        .and_then(|mut f| {
            f.seek(SeekFrom::End(0))
        })
        .unwrap_or(0)
}

pub fn elapsed(start: Instant) -> f64 {
    start.elapsed().as_secs_f64()
}

pub fn rel_l2(a: &[f32], b: &[f32]) -> f64 {
    assert_eq!(a.len(), b.len(), "rel_l2 size mismatch");
    let mut num = 0.0f64;
    let mut den = 0.0f64;
    for i in 0..a.len() {
        let diff = a[i] as f64 - b[i] as f64;
        num += diff * diff;
        den += a[i] as f64 * a[i] as f64;
    }
    (num / den.max(1e-30)).sqrt()
}

pub fn max_abs_diff(a: &[f32], b: &[f32]) -> f64 {
    assert_eq!(a.len(), b.len(), "max_abs_diff size mismatch");
    let mut max_diff = 0.0f64;
    for i in 0..a.len() {
        max_diff = max_diff.max((a[i] - b[i]).abs() as f64);
    }
    max_diff
}

pub fn dot(a: &[f32], b: &[f32]) -> f32 {
    let mut sum = 0.0f64;
    for i in 0..a.len() {
        sum += a[i] as f64 * b[i] as f64;
    }
    sum as f32
}

pub fn norm2(x: &[f32]) -> f32 {
    (dot(x, x).max(0.0)).sqrt()
}

pub fn normalize(x: &mut [f32]) {
    let n = norm2(x);
    if n <= 1e-20 {
        return;
    }
    let inv = 1.0 / n;
    for v in x.iter_mut() {
        *v *= inv;
    }
}

pub fn sample_standard_normal(rng: &mut impl rand::Rng) -> f32 {
    let u1 = rng.random::<f32>().max(1e-30f32);
    let u2 = rng.random::<f32>();
    (-2.0f32 * u1.ln()).sqrt() * (std::f32::consts::TAU * u2).cos()
}

pub fn print_benchmark_line(key: &str, value: f64) {
    println!("{key} {value:.10}");
}

pub fn print_benchmark_line_u64(key: &str, value: u64) {
    println!("{key} {value}");
}

pub fn print_benchmark_line_str(key: &str, value: &str) {
    println!("{key} {value}");
}

pub fn print_help() {
    println!("Green Compress — weight compression and layer inference");
    println!();
    println!("Commands:");
    println!("  import-f32 --in raw.bin --out W.mx --rows N --cols N");
    println!("  import-npy --in tensor.npy --out W.mx");
    println!("  export-f32 --in W.mx --out raw.bin");
    println!("  gen-matrix --out PATH --rows N --cols N --seed N");
    println!("  gen-activations --out PATH --rows N --cols N --seed N");
    println!("  q4 --in PATH --out PATH --block 32");
    println!("  repair --in W.mx --q4 W.q4 --out W.rep --rank 16 --iters 8 --sparse-frac 0.005 --fit-order low-rank-first [--sparse-mode magnitude|activation|output|imatrix] [--activations X.mx]");
    println!("  eval --in W.mx --q4 W.q4 --repair W.rep --activations X.mx");
    println!("  benchmark --method-id ID --type fp32|green_smart|green_adaptive|green_optimal|green_spqr_svd --in W.mx --activations X.mx --out-dir DIR [--backend cpu|gpu|auto] ...");
    println!("  infer --layer-dir DIR --activations X.mx [--out Y.mx] [--reference W.mx] [--bench-iters N] [--backend cpu|gpu|auto]");
    println!("  moe-infer --router R.mx --experts-manifest experts.json --activations X.mx [--top-k 2] [--cache-budget-mb 64]");
    println!("  moe-synth --in W.mx --activations X.mx --out-dir DIR [--num-experts 4]");
    println!("  infer-server   persistent stdin/stdout server (env GREENCOMPRESS_MAX_LAYERS, default 8)");
    println!("  prepack --layer-dir DIR");
    println!("  compare-sweep --dir out/sweep [--dir-b out/real_sweep] [--label synthetic] [--label-b real]");
    println!("  compare-benchmark --dir out/benchmark/synthetic");
}
