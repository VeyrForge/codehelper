use std::path::PathBuf;
use std::time::Instant;

use crate::error::{fail, Result};
use crate::io::{load_matrix, load_q4, load_repair, make_matrix, save_q4, save_repair};
use crate::matmul::matmul;
use crate::q4::{dequantize_q4, matmul_q4_repaired, quantize_q4, reconstruct, q4_value_at};
use crate::repair::{fit_repair, sparse_mode_from_string};
use crate::types::{Args, SparseScoreMode};
use crate::util::{
    elapsed, file_size, get_f32, get_optional_string, get_string, get_u32, max_abs_diff,
    print_size, rel_l2,
};

pub fn cmd_q4(args: &Args) -> Result<()> {
    let in_path = PathBuf::from(get_string(args, "in", "")?);
    let out_path = PathBuf::from(get_string(args, "out", "")?);
    let block = get_u32(args, "block", 32, false)?;
    let matrix = load_matrix(&in_path)?;
    let q4 = quantize_q4(&matrix, block);
    save_q4(&out_path, &q4)?;
    let restored = dequantize_q4(&q4);
    println!("wrote {}", out_path.display());
    println!("q4_rel_l2 {}", rel_l2(&matrix.data, &restored.data));
    print_size("fp32_matrix", matrix.data.len() as u64 * 4 + 16);
    print_size("q4_file", file_size(&out_path));
    Ok(())
}

pub fn cmd_repair(args: &Args) -> Result<()> {
    let in_path = PathBuf::from(get_string(args, "in", "")?);
    let q4_path = PathBuf::from(get_string(args, "q4", "")?);
    let out_path = PathBuf::from(get_string(args, "out", "")?);
    let rank = get_u32(args, "rank", 16, false)?;
    let iters = get_u32(args, "iters", 8, false)?;
    let seed = get_u32(args, "seed", 3, false)?;
    let sparse_frac = get_f32(args, "sparse-frac", 0.005, false)?;
    let fit_order = get_optional_string(args, "fit-order", "low-rank-first");
    let sparse_mode = get_optional_string(args, "sparse-mode", "magnitude");
    let activation_path = get_optional_string(args, "activations", "");
    let max_sparse_entries = get_u32(args, "max-sparse-entries", 0, false)? as usize;
    let greedy_sparse = get_u32(args, "greedy-sparse", 0, false)? != 0;
    let sparse_first = fit_order == "sparse-first";
    if !sparse_first && fit_order != "low-rank-first" {
        return Err(fail("fit-order must be low-rank-first or sparse-first"));
    }

    let original = load_matrix(&in_path)?;
    let q4 = load_q4(&q4_path)?;
    let residual = make_matrix_from_diff(&original, &q4)?;

    let (score_mode, row_weights, col_weights) = if sparse_mode == "magnitude" {
        (SparseScoreMode::Magnitude, Vec::new(), Vec::new())
    } else {
        if activation_path.is_empty() {
            return Err(fail("sparse-mode activation/output requires --activations"));
        }
        let activations = load_matrix(PathBuf::from(&activation_path).as_path())?;
        if activations.cols != original.rows {
            return Err(fail(
                "activation cols must match weight rows for activation sparse mode",
            ));
        }
        let (mode, row, col) = sparse_mode_from_string(
            &sparse_mode,
            &activations,
            original.rows,
            original.cols,
            Some(&original),
        )?;
        (mode, row, col)
    };

    let start_fit = Instant::now();
    let repair = fit_repair(
        residual,
        rank,
        iters,
        seed,
        sparse_frac,
        sparse_first,
        score_mode,
        if row_weights.is_empty() {
            None
        } else {
            Some(row_weights.as_slice())
        },
        max_sparse_entries,
        greedy_sparse,
        1,
        if col_weights.is_empty() {
            None
        } else {
            Some(col_weights.as_slice())
        },
    );
    let fit_seconds = elapsed(start_fit);

    save_repair(&out_path, &repair)?;
    let q4_only = dequantize_q4(&q4);
    let repaired = reconstruct(&q4, Some(&repair));

    println!("wrote {}", out_path.display());
    println!("fit_order {fit_order}");
    println!("sparse_mode {sparse_mode}");
    println!("actual_rank {}", repair.low_rank.len());
    println!("sparse_count {}", repair.sparse.len());
    println!("repair_fit_seconds {fit_seconds}");
    println!("q4_rel_l2 {}", rel_l2(&original.data, &q4_only.data));
    println!("repaired_rel_l2 {}", rel_l2(&original.data, &repaired.data));
    print_size("repair_file", file_size(&out_path));
    Ok(())
}

fn make_matrix_from_diff(
    original: &crate::types::Matrix,
    q4: &crate::types::Q4Matrix,
) -> Result<crate::types::Matrix> {
    let mut residual = make_matrix(original.rows, original.cols);
    for i in 0..original.data.len() {
        residual.data[i] = original.data[i] - q4_value_at(q4, i);
    }
    Ok(residual)
}

pub fn cmd_eval(args: &Args) -> Result<()> {
    let in_path = PathBuf::from(get_string(args, "in", "")?);
    let q4_path = PathBuf::from(get_string(args, "q4", "")?);
    let repair_path = get_optional_string(args, "repair", "");
    let activation_path = PathBuf::from(get_string(args, "activations", "")?);

    let original = load_matrix(&in_path)?;
    let activations = load_matrix(&activation_path)?;
    let q4 = load_q4(&q4_path)?;
    let repair = if repair_path.is_empty() {
        None
    } else {
        Some(load_repair(PathBuf::from(&repair_path).as_path())?)
    };

    let q4_only = dequantize_q4(&q4);
    let repaired = reconstruct(&q4, repair.as_ref());

    let start_full = Instant::now();
    let full_out = matmul(&activations, &original);
    let full_seconds = elapsed(start_full);

    let start_q4 = Instant::now();
    let q4_out = matmul_q4_repaired(&activations, &q4, None, None);
    let q4_seconds = elapsed(start_q4);

    let start_repaired_direct = Instant::now();
    let repaired_direct_out = matmul_q4_repaired(&activations, &q4, repair.as_ref(), None);
    let repaired_direct_seconds = elapsed(start_repaired_direct);

    let start_reconstructed = Instant::now();
    let repaired_reconstructed_out = matmul(&activations, &repaired);
    let reconstructed_seconds = elapsed(start_reconstructed);

    println!("matrix_rows {}", original.rows);
    println!("matrix_cols {}", original.cols);
    println!("activation_rows {}", activations.rows);
    println!("q4_weight_rel_l2 {}", rel_l2(&original.data, &q4_only.data));
    println!("repaired_weight_rel_l2 {}", rel_l2(&original.data, &repaired.data));
    println!("q4_activation_drift {}", rel_l2(&full_out.data, &q4_out.data));
    println!(
        "repaired_activation_drift {}",
        rel_l2(&full_out.data, &repaired_direct_out.data)
    );
    println!(
        "direct_vs_reconstructed_drift {}",
        rel_l2(&repaired_reconstructed_out.data, &repaired_direct_out.data)
    );
    println!(
        "direct_vs_reconstructed_max_abs {}",
        max_abs_diff(&repaired_reconstructed_out.data, &repaired_direct_out.data)
    );
    println!("full_matmul_seconds {full_seconds}");
    println!("q4_direct_matmul_seconds {q4_seconds}");
    println!("repaired_direct_matmul_seconds {repaired_direct_seconds}");
    println!("reconstructed_full_matmul_seconds {reconstructed_seconds}");
    print_size("fp32_matrix", original.data.len() as u64 * 4 + 16);
    print_size("q4_file", file_size(&q4_path));
    if !repair_path.is_empty() {
        let rep = PathBuf::from(&repair_path);
        print_size("repair_file", file_size(rep.as_path()));
        print_size("q4_plus_repair", file_size(&q4_path) + file_size(rep.as_path()));
    }
    Ok(())
}
