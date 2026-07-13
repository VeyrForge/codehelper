use std::path::Path;
use std::time::Instant;

use crate::error::{fail, Result};
use crate::backend::{ComputeBackend, GpuSession};
use crate::infer::{infer_layer_runtime_with_backend, matmul_fp32_with_backend};
use crate::io::{
    load_matrix, make_matrix, save_fused_cache, save_outliers, save_output_bias, save_q8,
    save_repair, save_subspace_adapter,
};
use crate::matmul::{
    build_fused_weight_cache, fused_cache_runtime_bytes, matmul, matmul_q8_repaired,
};
use crate::q8::{
    apply_row_spin, dequantize_q8, generate_row_spin, pick_best_row_spin, q8_runtime_bytes,
    q8_value_unspun, quantize_q8, quantize_q8_awq, quantize_q8_awq_spin, reconstruct_q8,
};
use crate::repair::{
    activation_row_rms, build_outlier_row_cache, build_sparse_row_cache, fit_fp16_outliers,
    fit_low_rank, fit_output_bias, fit_repair, fit_sparse, fit_sparse_greedy,
    outlier_runtime_bytes, repair_runtime_bytes, sparse_mode_from_string,
};
use crate::subspace::{fit_subspace_adapter, subspace_runtime_bytes};
use crate::types::{
    Args, FusedWeightCache, LayerRuntime, OutlierRowCache, Repair, SparseRowCache, SparseScoreMode,
    SparseTerm, SubspaceAdapter,
};
use crate::util::{
    elapsed, file_size, get_f32, get_optional_string, get_string, get_u32, print_benchmark_line,
    print_benchmark_line_str, print_benchmark_line_u64, rel_l2,
};

/// Core compression methods only.
fn is_core_method(method_type: &str) -> bool {
    matches!(
        method_type,
        "fp32" | "green_smart" | "green_adaptive" | "green_optimal" | "green_spqr_svd"
    )
}

pub fn cmd_benchmark(args: &Args) -> Result<()> {
    let method_id = get_string(args, "method-id", "")?;
    let method_type = get_string(args, "type", "")?;
    if !is_core_method(&method_type) {
        return Err(fail(format!(
            "unsupported type {method_type}; core: fp32, green_optimal, green_adaptive, green_smart, green_spqr_svd"
        )));
    }
    let weight_path_str = get_string(args, "in", "")?;
    let activation_path_str = get_string(args, "activations", "")?;
    let out_dir_str = get_string(args, "out-dir", "")?;
    let weight_path = Path::new(&weight_path_str);
    let activation_path = Path::new(&activation_path_str);
    let out_dir = Path::new(&out_dir_str);
    let block = get_u32(args, "block", 32, false)?;
    let rank = get_u32(args, "rank", 0, false)?;
    let iters = get_u32(args, "iters", 8, false)?;
    let seed = get_u32(args, "seed", 3, false)?;
    let sparse_frac = get_f32(args, "sparse-frac", 0.0, false)?;
    let fit_order = get_optional_string(args, "fit-order", "sparse-first");
    let sparse_mode = get_optional_string(args, "sparse-mode", "magnitude");
    let bench_iters = get_u32(args, "bench-iters", 3, false)?.max(1);
    let max_sparse_entries = get_u32(args, "max-sparse-entries", 0, false)?;
    let sparse_first = fit_order == "sparse-first";
    let use_awq = get_u32(args, "awq", 0, false)? != 0;
    let greedy_sparse = get_u32(args, "greedy-sparse", 0, false)? != 0;
    let is_spqr_svd = method_type == "green_spqr_svd";
    let is_optimal = method_type == "green_optimal";
    let is_smart = method_type == "green_smart" || method_type == "green_adaptive" || is_optimal;
    let is_adaptive = method_type == "green_adaptive" || is_optimal;
    let is_repair_family =
        matches!(method_type.as_str(), "green_spqr_svd" | "green_smart" | "green_adaptive" | "green_optimal");
    let repair_passes = get_u32(
        args,
        "repair-passes",
        if is_smart {
            8
        } else if is_spqr_svd {
            6
        } else {
            4
        },
        false,
    )?
    .max(1);
    let max_repair_passes_arg = get_u32(args, "max-repair-passes", 0, false)?;
    let max_repair_passes = if max_repair_passes_arg == 0 {
        repair_passes
    } else {
        repair_passes.max(max_repair_passes_arg)
    };
    let drift_target = get_f32(args, "drift-target", 0.0, false)?;
    let outlier_frac = get_f32(
        args,
        "outlier-frac",
        if is_repair_family { 0.0002 } else { 0.0 },
        false,
    )?;
    let use_output_bias = get_u32(args, "output-bias", if is_repair_family { 1 } else { 0 }, false)? != 0;
    let subspace_rank = get_u32(args, "subspace-rank", if is_spqr_svd { 4 } else { 0 }, false)?;
    let effective_rank = if rank > 0 {
        rank
    } else if is_smart || is_spqr_svd {
        4
    } else {
        0
    };
    let svd_peel_rank = get_u32(args, "svd-peel-rank", if is_spqr_svd { 4 } else { 0 }, false)?;
    let use_spin = get_u32(args, "spin", if is_repair_family { 1 } else { 0 }, false)? != 0;
    let spin_search = get_u32(args, "spin-search", if is_smart { 4 } else { 0 }, false)?;
    let use_prepack = get_u32(args, "prepack", 0, false)? != 0;
    let skip_repair_quality_pct = get_f32(
        args,
        "skip-repair-quality-pct",
        if is_adaptive || is_optimal { 99.50 } else { 0.0 },
        false,
    )?;
    let min_quality_pct = get_f32(
        args,
        "min-quality-pct",
        if is_optimal || is_adaptive { 99.50 } else { 0.0 },
        false,
    )?;
    let backend = ComputeBackend::parse(&get_optional_string(args, "backend", "cpu"));
    #[cfg(feature = "gpu")]
    let mut gpu_session = if matches!(backend, ComputeBackend::Gpu | ComputeBackend::Auto) {
        GpuSession::try_new()
    } else {
        None
    };
    let cache_key = out_dir_str.clone();

    std::fs::create_dir_all(out_dir)
        .map_err(|e| fail(format!("could not create out-dir: {e}")))?;

    let repair_path = out_dir.join("w.rep");

    let original = load_matrix(weight_path)?;
    let activations = load_matrix(activation_path)?;
    if activations.cols != original.rows {
        return Err(fail("activation cols must match weight rows"));
    }

    let fp32_bytes = original.data.len() as u64 * 4 + 16;
    let mut fit_seconds = 0.0f64;
    let mut q4_weight_rel_l2 = 0.0f64;
    let mut repaired_weight_rel_l2 = 0.0f64;
    let mut q4_activation_drift = 0.0f64;
    let mut activation_drift = 0.0f64;
    let mut full_matmul_seconds = 0.0f64;
    let mut inference_matmul_seconds = 0.0f64;
    let mut compressed_bytes = 0u64;
    let mut runtime_bytes = 0u64;

    let mut repair = Repair::default();

    let full_out = matmul(&activations, &original);

    let mut total_full = 0.0f64;
    for _ in 0..bench_iters {
        let start_full = Instant::now();
        #[cfg(feature = "gpu")]
        let _ = matmul_fp32_with_backend(&activations, &original, backend, gpu_session.as_mut())?;
        #[cfg(not(feature = "gpu"))]
        let _ = matmul_fp32_with_backend(&activations, &original, backend, None)?;
        total_full += elapsed(start_full);
    }
    full_matmul_seconds = total_full / bench_iters as f64;

    if method_type == "fp32" {
        activation_drift = 0.0;
        q4_activation_drift = 0.0;
        q4_weight_rel_l2 = 0.0;
        repaired_weight_rel_l2 = 0.0;
        inference_matmul_seconds = full_matmul_seconds;
        compressed_bytes = fp32_bytes;
        runtime_bytes = fp32_bytes;
    } else if matches!(
        method_type.as_str(),
        "green_spqr_svd" | "green_smart" | "green_adaptive" | "green_optimal"
    ) {
        let is_repair = true;
        let awq_quant = is_repair_family || use_awq;
        let quant_block = if is_repair_family && !is_smart && block == 32 {
            16
        } else {
            block
        };

        let mut quant_target = original.clone();
        let peel_terms = if svd_peel_rank > 0 {
            fit_low_rank(&mut quant_target, svd_peel_rank, iters, seed + 991)
        } else {
            Vec::new()
        };

        let act_rms = if awq_quant {
            activation_row_rms(&activations, quant_target.rows)
        } else {
            Vec::new()
        };

        let row_spin = if use_spin && spin_search > 0 && !act_rms.is_empty() {
            pick_best_row_spin(
                &quant_target,
                &activations,
                &full_out,
                quant_block,
                &act_rms,
                seed,
                spin_search,
            )
        } else if use_spin {
            generate_row_spin(quant_target.rows, seed + 7)
        } else {
            Vec::new()
        };

        let q8 = if awq_quant {
            if use_spin {
                quantize_q8_awq_spin(&quant_target, quant_block, &act_rms, &row_spin, 0.5)
            } else {
                quantize_q8_awq(&quant_target, quant_block, &act_rms, 0.5)
            }
        } else if use_spin {
            let mut spun = quant_target.clone();
            apply_row_spin(&mut spun, &row_spin);
            let mut q8 = quantize_q8(&spun, quant_block);
            q8.row_spin = row_spin;
            q8
        } else {
            quantize_q8(&quant_target, quant_block)
        };

        let q8_path = out_dir.join("w.q8");
        save_q8(&q8_path, &q8)?;
        let q8_only = dequantize_q8(&q8);
        q4_weight_rel_l2 = rel_l2(&original.data, &q8_only.data);
        compressed_bytes = file_size(&q8_path);
        runtime_bytes = q8_runtime_bytes(&q8);

        let q8_out = matmul_q8_repaired(&activations, &q8, None, None, None, None, None, None);
        q4_activation_drift = rel_l2(&full_out.data, &q8_out.data);

        let mut q8_total = 0.0f64;
        for _ in 0..bench_iters {
            let start_q8 = Instant::now();
            let _ = matmul_q8_repaired(&activations, &q8, None, None, None, None, None, None);
            q8_total += elapsed(start_q8);
        }
        let q8_matmul_seconds = q8_total / bench_iters as f64;

        let q8_quality_pct = (1.0 - q4_activation_drift).max(0.0) * 100.0;
        let skip_repair = is_repair
            && skip_repair_quality_pct > 0.0
            && q8_quality_pct >= skip_repair_quality_pct as f64;

        let mut total_repair_passes = repair_passes;
        let mut fp16_outliers: Vec<SparseTerm> = Vec::new();
        let mut output_bias_vec: Vec<f32> = Vec::new();
        let mut subspace_adapter = SubspaceAdapter::default();
        let mut fused_cache = FusedWeightCache::default();
        let mut repair_row_cache = SparseRowCache::default();
        let mut outlier_row_cache = OutlierRowCache::default();

        if !is_repair || skip_repair {
            if skip_repair {
                print_benchmark_line_str("benchmark_repair_skipped", "q8_quality_gate");
                print_benchmark_line("benchmark_skip_repair_quality_pct", skip_repair_quality_pct as f64);
            }
            activation_drift = q4_activation_drift;
            repaired_weight_rel_l2 = q4_weight_rel_l2;
            inference_matmul_seconds = q8_matmul_seconds;
        } else {
            if !sparse_first && fit_order != "low-rank-first" {
                return Err(fail("fit-order must be low-rank-first or sparse-first"));
            }

            let default_sparse = if is_smart { "imatrix" } else { "output" };
            let effective_sparse_mode = if sparse_mode == "magnitude" {
                default_sparse
            } else {
                sparse_mode.as_str()
            };
            let (score_mode, row_weights, col_weights) = sparse_mode_from_string(
                effective_sparse_mode,
                &activations,
                original.rows,
                original.cols,
                Some(&original),
            )?;
            let row_ptr = if row_weights.is_empty() {
                None
            } else {
                Some(row_weights.as_slice())
            };
            let col_ptr = if col_weights.is_empty() {
                None
            } else {
                Some(col_weights.as_slice())
            };

            if outlier_frac > 0.0 {
                let outlier_base = if peel_terms.is_empty() {
                    &original
                } else {
                    &quant_target
                };
                fp16_outliers = fit_fp16_outliers(
                    outlier_base,
                    &q8,
                    outlier_frac,
                    score_mode,
                    row_ptr,
                    col_ptr,
                );
                outlier_row_cache = build_outlier_row_cache(&fp16_outliers, q8.cols);
            }

            let mut residual = make_matrix(original.rows, original.cols);
            let mut is_outlier = vec![0u8; original.data.len()];
            for term in &fp16_outliers {
                if (term.index as usize) < is_outlier.len() {
                    is_outlier[term.index as usize] = 1;
                }
            }
            for i in 0..original.data.len() {
                residual.data[i] = if is_outlier[i] != 0 {
                    0.0
                } else {
                    quant_target.data[i] - q8_value_unspun(&q8, i)
                };
            }

            let start_fit = Instant::now();
            repair = fit_repair(
                residual,
                effective_rank,
                iters,
                seed,
                sparse_frac,
                sparse_first,
                score_mode,
                row_ptr,
                max_sparse_entries as usize,
                greedy_sparse,
                repair_passes,
                col_ptr,
            );
            if !peel_terms.is_empty() {
                let mut combined = peel_terms;
                combined.append(&mut repair.low_rank);
                repair.low_rank = combined;
            }
            total_repair_passes = repair_passes;

            let outlier_cache_ptr = if fp16_outliers.is_empty() {
                None
            } else {
                Some(&outlier_row_cache)
            };

            let mut repair_cache_ptr: Option<&SparseRowCache> = None;
            if !repair.sparse.is_empty() {
                repair_row_cache = build_sparse_row_cache(&repair, q8.cols);
                repair_cache_ptr = Some(&repair_row_cache);
            }

            let mut output_bias_ptr: Option<&[f32]> = None;
            let mut subspace_ptr: Option<&SubspaceAdapter> = None;
            let mut fused_cache_ptr: Option<&FusedWeightCache> = None;

            activation_drift = rel_l2(
                &full_out.data,
                &matmul_q8_repaired(
                    &activations,
                    &q8,
                    Some(&repair),
                    repair_cache_ptr,
                    outlier_cache_ptr,
                    output_bias_ptr,
                    subspace_ptr,
                    fused_cache_ptr,
                )
                .data,
            );

            while drift_target > 0.0
                && activation_drift > drift_target as f64
                && total_repair_passes < max_repair_passes
            {
                let current_w = reconstruct_q8(&q8, Some(&repair));
                let mut refine_residual = make_matrix(original.rows, original.cols);
                for i in 0..original.data.len() {
                    refine_residual.data[i] = original.data[i] - current_w.data[i];
                }
                let more_sparse = if greedy_sparse {
                    let keep = ((refine_residual.data.len() as f64) * sparse_frac as f64).ceil() as usize;
                    fit_sparse_greedy(&mut refine_residual, keep, row_ptr)
                } else {
                    fit_sparse(
                        &mut refine_residual,
                        sparse_frac,
                        score_mode,
                        row_ptr,
                        max_sparse_entries as usize,
                        col_ptr,
                    )
                };
                if more_sparse.is_empty() {
                    break;
                }
                repair.sparse.extend(more_sparse);
                total_repair_passes += 1;
                repair_cache_ptr = if !repair.sparse.is_empty() {
                    repair_row_cache = build_sparse_row_cache(&repair, q8.cols);
                    Some(&repair_row_cache)
                } else {
                    None
                };
                runtime_bytes = q8_runtime_bytes(&q8)
                    + repair_runtime_bytes(&repair)
                    + outlier_runtime_bytes(&fp16_outliers);
                if output_bias_ptr.is_some() {
                    runtime_bytes += output_bias_vec.len() as u64 * 4;
                }
                if subspace_ptr.is_some() {
                    runtime_bytes += subspace_runtime_bytes(&subspace_adapter);
                }
                activation_drift = rel_l2(
                    &full_out.data,
                    &matmul_q8_repaired(
                        &activations,
                        &q8,
                        Some(&repair),
                        repair_cache_ptr,
                        outlier_cache_ptr,
                        output_bias_ptr,
                        subspace_ptr,
                        fused_cache_ptr,
                    )
                    .data,
                );
            }

            if use_output_bias {
                let repaired_out = matmul_q8_repaired(
                    &activations,
                    &q8,
                    Some(&repair),
                    repair_cache_ptr,
                    outlier_cache_ptr,
                    None,
                    subspace_ptr,
                    fused_cache_ptr,
                );
                output_bias_vec = fit_output_bias(&full_out, &repaired_out);
                output_bias_ptr = Some(&output_bias_vec);
                activation_drift = rel_l2(
                    &full_out.data,
                    &matmul_q8_repaired(
                        &activations,
                        &q8,
                        Some(&repair),
                        repair_cache_ptr,
                        outlier_cache_ptr,
                        output_bias_ptr,
                        subspace_ptr,
                        fused_cache_ptr,
                    )
                    .data,
                );
            }

            if subspace_rank > 0 {
                let repaired_out = matmul_q8_repaired(
                    &activations,
                    &q8,
                    Some(&repair),
                    repair_cache_ptr,
                    outlier_cache_ptr,
                    output_bias_ptr,
                    None,
                    fused_cache_ptr,
                );
                subspace_adapter =
                    fit_subspace_adapter(&activations, &full_out, &repaired_out, subspace_rank);
                subspace_ptr = if subspace_adapter.rank > 0 {
                    Some(&subspace_adapter)
                } else {
                    None
                };
                activation_drift = rel_l2(
                    &full_out.data,
                    &matmul_q8_repaired(
                        &activations,
                        &q8,
                        Some(&repair),
                        repair_cache_ptr,
                        outlier_cache_ptr,
                        output_bias_ptr,
                        subspace_ptr,
                        fused_cache_ptr,
                    )
                    .data,
                );
            }

            if use_prepack {
                fused_cache = build_fused_weight_cache(&q8, outlier_cache_ptr, repair_cache_ptr);
                fused_cache_ptr = Some(&fused_cache);
                save_fused_cache(&out_dir.join("w.fcw"), &fused_cache)?;
                activation_drift = rel_l2(
                    &full_out.data,
                    &matmul_q8_repaired(
                        &activations,
                        &q8,
                        Some(&repair),
                        repair_cache_ptr,
                        outlier_cache_ptr,
                        output_bias_ptr,
                        subspace_ptr,
                        fused_cache_ptr,
                    )
                    .data,
                );
            }

            fit_seconds = elapsed(start_fit);
            save_repair(&repair_path, &repair)?;
            if !fp16_outliers.is_empty() {
                save_outliers(&out_dir.join("w.out"), &fp16_outliers)?;
            }
            if use_output_bias && !output_bias_vec.is_empty() {
                save_output_bias(&out_dir.join("w.bias"), &output_bias_vec)?;
            }
            if subspace_ptr.is_some() {
                save_subspace_adapter(&out_dir.join("w.sub"), &subspace_adapter)?;
            }
            compressed_bytes = file_size(&q8_path) + file_size(&repair_path);
            if !fp16_outliers.is_empty() {
                compressed_bytes += file_size(&out_dir.join("w.out"));
            }
            if use_output_bias && !output_bias_vec.is_empty() {
                compressed_bytes += file_size(&out_dir.join("w.bias"));
            }
            if subspace_ptr.is_some() {
                compressed_bytes += file_size(&out_dir.join("w.sub"));
            }
            if use_prepack && fused_cache_ptr.is_some() {
                compressed_bytes += file_size(&out_dir.join("w.fcw"));
            }
            repaired_weight_rel_l2 =
                rel_l2(&original.data, &reconstruct_q8(&q8, Some(&repair)).data);

            let mut repair_loaded = repair.clone();
            if !repair_row_cache.by_row.is_empty() {
                repair_loaded.sparse.clear();
                repair_loaded.sparse.shrink_to_fit();
            }
            let rt = LayerRuntime {
                q8: q8.clone(),
                repair: Some(repair_loaded),
                outlier_row_cache: outlier_row_cache.clone(),
                output_bias: if output_bias_vec.is_empty() {
                    None
                } else {
                    Some(output_bias_vec.clone())
                },
                subspace: subspace_ptr.map(|s| (*s).clone()),
                repair_row_cache: repair_row_cache.clone(),
                fused_cache: fused_cache_ptr.map(|f| (*f).clone()),
            };
            runtime_bytes = rt.load_bytes();

            let mut inference_total = 0.0f64;
            for _ in 0..bench_iters {
                let start_inference = Instant::now();
                #[cfg(feature = "gpu")]
                let _ = infer_layer_runtime_with_backend(
                    &rt,
                    &activations,
                    backend,
                    gpu_session.as_mut(),
                    &cache_key,
                )?;
                #[cfg(not(feature = "gpu"))]
                let _ = infer_layer_runtime_with_backend(&rt, &activations, backend, None, &cache_key)?;
                inference_total += elapsed(start_inference);
            }
            inference_matmul_seconds = inference_total / bench_iters as f64;
        }

        print_benchmark_line("benchmark_awq", if awq_quant { 1.0 } else { 0.0 });
        print_benchmark_line("benchmark_greedy_sparse", if greedy_sparse { 1.0 } else { 0.0 });
        print_benchmark_line("benchmark_repair_passes", total_repair_passes as f64);
        print_benchmark_line("benchmark_drift_target", drift_target as f64);
        print_benchmark_line("benchmark_outlier_frac", outlier_frac as f64);
        print_benchmark_line("benchmark_outlier_count", fp16_outliers.len() as f64);
        print_benchmark_line("benchmark_output_bias", if use_output_bias { 1.0 } else { 0.0 });
        print_benchmark_line("benchmark_spin", if use_spin { 1.0 } else { 0.0 });
        print_benchmark_line("benchmark_spin_search", spin_search as f64);
        print_benchmark_line("benchmark_prepack", if use_prepack { 1.0 } else { 0.0 });
        print_benchmark_line("benchmark_subspace_rank", subspace_rank as f64);
        print_benchmark_line("benchmark_svd_peel_rank", svd_peel_rank as f64);
        print_benchmark_line("benchmark_block", quant_block as f64);
    }

    let compression_ratio = if fp32_bytes > 0 {
        fp32_bytes as f64 / compressed_bytes.max(1) as f64
    } else {
        1.0
    };
    let speed_ratio = if inference_matmul_seconds > 0.0 {
        full_matmul_seconds / inference_matmul_seconds
    } else {
        0.0
    };
    let drift_improvement_pct = if q4_activation_drift > 0.0 {
        (1.0 - activation_drift / q4_activation_drift) * 100.0
    } else {
        0.0
    };
    let runtime_compression_ratio = if fp32_bytes > 0 {
        fp32_bytes as f64 / runtime_bytes.max(1) as f64
    } else {
        1.0
    };

    print_benchmark_line_str("benchmark_method", &method_id);
    print_benchmark_line_str("benchmark_type", &method_type);
    print_benchmark_line_str("benchmark_backend", backend.as_str());
    print_benchmark_line("benchmark_block", block as f64);
    print_benchmark_line("benchmark_rank", rank as f64);
    print_benchmark_line("benchmark_sparse_frac", sparse_frac as f64);
    print_benchmark_line_str("benchmark_fit_order", &fit_order);
    print_benchmark_line_str("benchmark_sparse_mode", &sparse_mode);
    print_benchmark_line("quality_activation_drift", activation_drift);
    print_benchmark_line("quality_vs_fp32_drift", activation_drift);
    print_benchmark_line("quality_error_pct", activation_drift * 100.0);
    print_benchmark_line(
        "quality_accuracy_pct",
        (1.0 - activation_drift).max(0.0) * 100.0,
    );
    print_benchmark_line("quality_q4_activation_drift", q4_activation_drift);
    let quality_weight_rel_l2 = if method_type == "fp32" {
        0.0
    } else {
        repaired_weight_rel_l2
    };
    print_benchmark_line("quality_weight_rel_l2", quality_weight_rel_l2);
    print_benchmark_line("quality_q4_weight_rel_l2", q4_weight_rel_l2);
    print_benchmark_line("quality_drift_improvement_pct", drift_improvement_pct);
    print_benchmark_line_u64("memory_fp32_bytes", fp32_bytes);
    print_benchmark_line_u64("memory_storage_bytes", compressed_bytes);
    print_benchmark_line_u64("memory_compressed_bytes", compressed_bytes);
    print_benchmark_line_u64("memory_runtime_bytes", runtime_bytes);
    print_benchmark_line(
        "memory_runtime_mib",
        runtime_bytes as f64 / (1024.0 * 1024.0),
    );
    print_benchmark_line("memory_compression_ratio", compression_ratio);
    print_benchmark_line("memory_runtime_ratio", runtime_compression_ratio);
    print_benchmark_line("speed_fit_seconds", fit_seconds);
    print_benchmark_line("speed_fp32_matmul_seconds", full_matmul_seconds);
    print_benchmark_line("speed_inference_matmul_seconds", inference_matmul_seconds);
    print_benchmark_line("speed_inference_ms", inference_matmul_seconds * 1000.0);
    print_benchmark_line("speed_matmul_ratio", speed_ratio);
    print_benchmark_line("matrix_rows", original.rows as f64);
    print_benchmark_line("matrix_cols", original.cols as f64);

    let final_quality_pct = (1.0 - activation_drift).max(0.0) * 100.0;
    print_benchmark_line("benchmark_min_quality_pct", min_quality_pct as f64);
    let gate_pass = min_quality_pct <= 0.0 || final_quality_pct >= min_quality_pct as f64;
    print_benchmark_line("benchmark_quality_gate_pass", if gate_pass { 1.0 } else { 0.0 });
    if !gate_pass {
        return Err(fail(format!(
            "quality {final_quality_pct:.4}% below minimum {min_quality_pct:.2}%"
        )));
    }

    Ok(())
}
