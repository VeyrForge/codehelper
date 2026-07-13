use std::path::Path;
use std::time::Instant;

use crate::error::{fail, Result};
use crate::expert_cache::{load_expert_manifest, ExpertEntry, ExpertLruCache};
use crate::io::{load_matrix, make_matrix, save_matrix};
use crate::matmul::matmul;
use crate::types::{Args, Matrix};
use crate::util::{elapsed, get_f32, get_string, get_u32, rel_l2};

fn softmax_top_k(logits: &[f32], k: usize) -> Vec<(usize, f32)> {
    let k = k.min(logits.len()).max(1);
    let max = logits.iter().copied().fold(f32::NEG_INFINITY, f32::max);
    let mut exp: Vec<(usize, f32)> = logits
        .iter()
        .enumerate()
        .map(|(i, &v)| (i, (v - max).exp()))
        .collect();
    exp.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));
    exp.truncate(k);
    let sum: f32 = exp.iter().map(|(_, w)| w).sum();
    if sum > 0.0 {
        for (_, w) in &mut exp {
            *w /= sum;
        }
    }
    exp
}

fn route_activations(activations: &Matrix, router: &Matrix, top_k: usize) -> Vec<Vec<(usize, f32)>> {
    let router_out = matmul(activations, router);
    let mut per_batch = Vec::with_capacity(activations.rows as usize);
    for b in 0..activations.rows {
        let base = b as usize * router.cols as usize;
        let logits: Vec<f32> = router_out.data[base..base + router.cols as usize].to_vec();
        per_batch.push(softmax_top_k(&logits, top_k));
    }
    per_batch
}

fn accumulate_weighted(out: &mut Matrix, expert_out: &Matrix, weight: f32) {
    assert_eq!(out.rows, expert_out.rows);
    assert_eq!(out.cols, expert_out.cols);
    for i in 0..out.data.len() {
        out.data[i] += weight * expert_out.data[i];
    }
}

pub fn moe_forward(
    activations: &Matrix,
    router: &Matrix,
    experts: &[ExpertEntry],
    cache: &mut ExpertLruCache,
    top_k: usize,
) -> Result<Matrix> {
    if router.rows != activations.cols {
        return Err(fail("router rows must match activation cols"));
    }
    if experts.is_empty() {
        return Err(fail("no experts in manifest"));
    }
    let routes = route_activations(activations, router, top_k);
    let out_dim = {
        let rt = cache.get_or_load(&experts[0])?;
        rt.q8.cols
    };
    let mut out = make_matrix(activations.rows, out_dim);

    for b in 0..activations.rows {
        let mut row = make_matrix(1, activations.cols);
        let base = b as usize * activations.cols as usize;
        row.data.copy_from_slice(&activations.data[base..base + activations.cols as usize]);

        for &(expert_idx, weight) in &routes[b as usize] {
            if expert_idx >= experts.len() {
                return Err(fail(format!("expert index {expert_idx} out of range")));
            }
            let expert_out = cache.infer_expert(&experts[expert_idx], &row)?;
            let out_base = b as usize * out.cols as usize;
            let out_row = &mut out.data[out_base..out_base + out.cols as usize];
            for (i, v) in expert_out.data.iter().enumerate() {
                out_row[i] += weight * v;
            }
        }
    }
    Ok(out)
}

pub fn cmd_moe_infer(args: &Args) -> Result<()> {
    let router_str = get_string(args, "router", "")?;
    let manifest_str = get_string(args, "experts-manifest", "")?;
    let activations_str = get_string(args, "activations", "")?;
    let router_path = Path::new(&router_str);
    let manifest_path = Path::new(&manifest_str);
    let activations_path = Path::new(&activations_str);
    let out_path = get_string(args, "out", "").ok();
    let reference_path = get_string(args, "reference", "").ok();
    let top_k = get_u32(args, "top-k", 2, false)? as usize;
    let cache_mb = get_f32(args, "cache-budget-mb", 64.0, false)?;
    let bench_iters = get_u32(args, "bench-iters", 3, false)?.max(1);

    let router = load_matrix(router_path)?;
    let activations = load_matrix(activations_path)?;
    let experts = load_expert_manifest(manifest_path)?;
    let mut cache = ExpertLruCache::new((cache_mb * 1024.0 * 1024.0) as u64);

    let mut seconds = 0.0f64;
    for _ in 0..bench_iters {
        let start = Instant::now();
        let _ = moe_forward(&activations, &router, &experts, &mut cache, top_k)?;
        seconds += elapsed(start);
    }
    seconds /= bench_iters as f64;

    let result = moe_forward(&activations, &router, &experts, &mut cache, top_k)?;
    if let Some(ref out) = out_path {
        if !out.is_empty() {
            save_matrix(Path::new(out), &result)?;
        }
    }

    println!("moe_experts {}", experts.len());
    println!("moe_top_k {top_k}");
    println!("moe_cache_budget_mb {cache_mb}");
    println!("moe_cache_hits {}", cache.hits);
    println!("moe_cache_misses {}", cache.misses);
    println!("moe_out_dim {}", result.cols);
    println!("speed_inference_ms {:.10}", seconds * 1000.0);

    if let Some(ref_path) = reference_path {
        if !ref_path.is_empty() {
            let reference = load_matrix(Path::new(&ref_path))?;
            let drift = rel_l2(&reference.data, &result.data);
            println!("quality_activation_drift {:.10}", drift);
            println!("quality_accuracy_pct {:.10}", (1.0 - drift).max(0.0) * 100.0);
        }
    }
    Ok(())
}

/// Synthetic MoE bench: generate router + expert dirs from weight slices.
pub fn cmd_moe_synth(args: &Args) -> Result<()> {
    let weight_str = get_string(args, "in", "")?;
    let activations_str = get_string(args, "activations", "")?;
    let out_dir_str = get_string(args, "out-dir", "")?;
    let weight_path = Path::new(&weight_str);
    let activations_path = Path::new(&activations_str);
    let out_dir = Path::new(&out_dir_str);
    let num_experts = get_u32(args, "num-experts", 4, false)? as usize;
    let top_k = get_u32(args, "top-k", 2, false)? as usize;
    let cache_mb = get_f32(args, "cache-budget-mb", 32.0, false)?;

    let weight = load_matrix(weight_path)?;
    let activations = load_matrix(activations_path)?;
    if weight.rows % num_experts as u32 != 0 {
        return Err(fail("weight rows must divide num-experts"));
    }
    let expert_rows = weight.rows / num_experts as u32;

    let mut router = make_matrix(weight.rows, num_experts as u32);
    for i in 0..router.data.len() {
        let expert = (i % num_experts) as f32;
        router.data[i] = if (i / num_experts) as f32 == expert {
            1.0
        } else {
            0.01
        };
    }

    let manifest_path = out_dir.join("experts.json");
    std::fs::create_dir_all(out_dir).map_err(|e| fail(e.to_string()))?;

    let mut expert_entries = Vec::new();
    for e in 0..num_experts {
        let edir = out_dir.join(format!("expert_{e}"));
        std::fs::create_dir_all(&edir).map_err(|e| fail(e.to_string()))?;
        let mut sub = make_matrix(expert_rows, weight.cols);
        let r0 = e as u32 * expert_rows;
        for r in 0..expert_rows {
            for c in 0..weight.cols {
                let src = (r0 + r) as usize * weight.cols as usize + c as usize;
                sub.data[r as usize * weight.cols as usize + c as usize] = weight.data[src];
            }
        }
        let mx = edir.join("w.mx");
        save_matrix(&mx, &sub)?;
        expert_entries.push(ExpertEntry {
            id: format!("expert_{e}"),
            path: edir.clone(),
            bytes: 0,
        });
    }

    let manifest = serde_json::json!({
        "experts": expert_entries.iter().map(|e| serde_json::json!({
            "id": e.id,
            "path": e.path.to_string_lossy(),
        })).collect::<Vec<_>>()
    });
    std::fs::write(&manifest_path, manifest.to_string()).map_err(|e| fail(e.to_string()))?;

    println!("moe_synth_experts {num_experts}");
    println!("moe_synth_manifest {}", manifest_path.display());
    println!("moe_synth_note compress each expert dir with benchmark before moe-infer");
    let _ = (activations, top_k, cache_mb, router);
    Ok(())
}
