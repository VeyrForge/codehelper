use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};

use crate::error::{fail, Result};
use crate::types::Args;
use crate::util::get_string;

#[derive(Clone, Debug, Default)]
pub struct BenchmarkRow {
    pub method_id: String,
    pub method_type: String,
    pub quality_accuracy_pct: f64,
    pub memory_runtime_mib: f64,
    pub speed_inference_ms: f64,
    pub speed_matmul_ratio: f64,
    pub memory_runtime_ratio: f64,
    pub speed_fit_seconds: f64,
}

fn parse_benchmark_file(path: &Path) -> Result<HashMap<String, String>> {
    let content = fs::read_to_string(path)
        .map_err(|e| fail(format!("could not read {}: {e}", path.display())))?;
    let mut metrics = HashMap::new();
    for line in content.lines() {
        let Some((key, value)) = line.split_once(' ') else {
            continue;
        };
        metrics.insert(key.to_string(), value.trim().to_string());
    }
    Ok(metrics)
}

fn metric_f64(metrics: &HashMap<String, String>, key: &str) -> f64 {
    metrics
        .get(key)
        .and_then(|v| v.parse().ok())
        .unwrap_or(0.0)
}

fn metric_str(metrics: &HashMap<String, String>, key: &str) -> String {
    metrics.get(key).cloned().unwrap_or_default()
}

fn load_row(method_dir: &Path, metrics: HashMap<String, String>) -> BenchmarkRow {
    BenchmarkRow {
        method_id: method_dir
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("unknown")
            .to_string(),
        method_type: metric_str(&metrics, "benchmark_type"),
        quality_accuracy_pct: metric_f64(&metrics, "quality_accuracy_pct"),
        memory_runtime_mib: metric_f64(&metrics, "memory_runtime_mib"),
        speed_inference_ms: metric_f64(&metrics, "speed_inference_ms"),
        speed_matmul_ratio: metric_f64(&metrics, "speed_matmul_ratio"),
        memory_runtime_ratio: metric_f64(&metrics, "memory_runtime_ratio"),
        speed_fit_seconds: metric_f64(&metrics, "speed_fit_seconds"),
    }
}

fn type_rank(method_type: &str) -> u8 {
    match method_type {
        "fp32" => 0,
        "green_spqr_svd" => 1,
        "green_smart" => 2,
        "green_adaptive" => 3,
        "green_optimal" => 4,
        _ => 99,
    }
}

fn load_benchmark_dir(dir: &Path) -> Result<Vec<BenchmarkRow>> {
    let mut rows = Vec::new();
    for entry in fs::read_dir(dir).map_err(|e| fail(e.to_string()))? {
        let entry = entry.map_err(|e| fail(e.to_string()))?;
        if !entry.file_type().map_err(|e| fail(e.to_string()))?.is_dir() {
            continue;
        }
        let bench_path = entry.path().join("benchmark.txt");
        if !bench_path.is_file() {
            continue;
        }
        let metrics = parse_benchmark_file(&bench_path)?;
        rows.push(load_row(&entry.path(), metrics));
    }
    rows.sort_by(|a, b| {
        type_rank(&a.method_type)
            .cmp(&type_rank(&b.method_type))
            .then(a.method_id.cmp(&b.method_id))
    });
    Ok(rows)
}

pub fn cmd_compare_benchmark(args: &Args) -> Result<()> {
    let dir = PathBuf::from(get_string(args, "dir", "")?);
    if !dir.is_dir() {
        return Err(fail(format!("benchmark dir not found: {}", dir.display())));
    }

    let rows = load_benchmark_dir(&dir)?;
    if rows.is_empty() {
        return Err(fail(format!(
            "no benchmark.txt files under {}",
            dir.display()
        )));
    }

    let fp32_ms = rows
        .iter()
        .find(|r| r.method_type == "fp32")
        .map(|r| r.speed_inference_ms)
        .filter(|v| *v > 0.0)
        .unwrap_or(1.0);

    println!("benchmark_compare_dir {}", dir.display());
    println!("benchmark_compare_methods {}", rows.len());
    println!("matrix_note synthetic 512x512, 64 activation rows, bench-iters from config");
    println!();
    println!(
        "{:<22} {:<16} {:>10} {:>10} {:>10} {:>10} {:>8}",
        "method", "type", "quality%", "ram_mib", "infer_ms", "vs_fp32", "ram_x"
    );
    for row in &rows {
        let vs_fp32 = if row.method_type == "fp32" {
            1.0
        } else {
            fp32_ms / row.speed_inference_ms.max(1e-9)
        };
        println!(
            "{:<22} {:<16} {:>10.2} {:>10.3} {:>10.2} {:>10.2}x {:>8.1}x",
            row.method_id,
            row.method_type,
            row.quality_accuracy_pct,
            row.memory_runtime_mib,
            row.speed_inference_ms,
            vs_fp32,
            row.memory_runtime_ratio
        );
    }

    if let Some(best) = rows
        .iter()
        .filter(|r| r.method_type != "fp32")
        .max_by(|a, b| {
            a.quality_accuracy_pct
                .partial_cmp(&b.quality_accuracy_pct)
                .unwrap()
        })
    {
        println!();
        println!(
            "best_quality {} ({}) {:.2}%",
            best.method_id, best.method_type, best.quality_accuracy_pct
        );
    }

    if let Some(cheapest) = rows
        .iter()
        .filter(|r| r.method_type != "fp32")
        .min_by(|a, b| {
            a.memory_runtime_mib
                .partial_cmp(&b.memory_runtime_mib)
                .unwrap()
        })
    {
        println!(
            "best_ram {} ({}) {:.3} MiB",
            cheapest.method_id, cheapest.method_type, cheapest.memory_runtime_mib
        );
    }

    if let Some(fastest) = rows
        .iter()
        .filter(|r| r.method_type != "fp32")
        .min_by(|a, b| {
            a.speed_inference_ms
                .partial_cmp(&b.speed_inference_ms)
                .unwrap()
        })
    {
        println!(
            "best_speed {} ({}) {:.2} ms",
            fastest.method_id, fastest.method_type, fastest.speed_inference_ms
        );
    }

    Ok(())
}
