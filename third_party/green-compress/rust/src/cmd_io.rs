use std::fs::File;
use std::io::Read;
use std::path::PathBuf;

use rand::prelude::*;
use rand::rngs::StdRng;
use rand::SeedableRng;

use crate::error::{fail, Result};
use crate::io::{load_matrix, make_matrix, save_matrix};
use crate::npy::load_npy_matrix;
use crate::types::Args;
use crate::util::{get_string, get_u32, sample_standard_normal};

pub fn cmd_import_npy(args: &Args) -> Result<()> {
    let in_path = PathBuf::from(get_string(args, "in", "")?);
    let out_path = PathBuf::from(get_string(args, "out", "")?);
    let matrix = load_npy_matrix(&in_path)?;
    save_matrix(&out_path, &matrix)?;
    println!("wrote {}", out_path.display());
    println!("rows {}", matrix.rows);
    println!("cols {}", matrix.cols);
    Ok(())
}

pub fn cmd_import_f32(args: &Args) -> Result<()> {
    let in_path = PathBuf::from(get_string(args, "in", "")?);
    let out_path = PathBuf::from(get_string(args, "out", "")?);
    let rows = get_u32(args, "rows", 0, true)?;
    let cols = get_u32(args, "cols", 0, true)?;
    let mut matrix = make_matrix(rows, cols);
    let mut file = File::open(&in_path)
        .map_err(|e| fail(format!("could not open raw f32 for read: {e}")))?;
    let mut bytes = vec![0u8; matrix.data.len() * 4];
    file.read_exact(&mut bytes)
        .map_err(|e| fail(format!("read failed: {e}")))?;
    for (i, chunk) in bytes.chunks_exact(4).enumerate() {
        matrix.data[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
    }
    save_matrix(&out_path, &matrix)?;
    println!("wrote {}", out_path.display());
    Ok(())
}

pub fn cmd_export_f32(args: &Args) -> Result<()> {
    let in_path = PathBuf::from(get_string(args, "in", "")?);
    let out_path = PathBuf::from(get_string(args, "out", "")?);
    let matrix = load_matrix(&in_path)?;
    let bytes: Vec<u8> = matrix.data.iter().flat_map(|f| f.to_le_bytes()).collect();
    std::fs::write(&out_path, bytes).map_err(|e| fail(format!("write failed: {e}")))?;
    println!("wrote {}", out_path.display());
    Ok(())
}

pub fn cmd_gen_matrix(args: &Args) -> Result<()> {
    let out_path = PathBuf::from(get_string(args, "out", "")?);
    let rows = get_u32(args, "rows", 0, true)?;
    let cols = get_u32(args, "cols", 0, true)?;
    let seed = get_u32(args, "seed", 1, false)?;
    let mut matrix = make_matrix(rows, cols);
    let mut rng = StdRng::seed_from_u64(seed as u64);
    let uniform = rand::distr::Uniform::new(0.0f32, 1.0f32).unwrap();

    for r in 0..rows {
        for c in 0..cols {
            let low_rank_signal =
                0.7 * (r as f32 * 0.017).sin() * (c as f32 * 0.013).cos();
            let local_pattern = 0.2 * ((r + c) as f32 * 0.031).sin();
            let noise = 0.08 * sample_standard_normal(&mut rng);
            let outlier = if uniform.sample(&mut rng) < 0.0025 {
                sample_standard_normal(&mut rng) * 3.0
            } else {
                0.0
            };
            *matrix.at_mut(r, c) = low_rank_signal + local_pattern + noise + outlier;
        }
    }
    save_matrix(&out_path, &matrix)?;
    println!("wrote {}", out_path.display());
    Ok(())
}

pub fn cmd_gen_activations(args: &Args) -> Result<()> {
    let out_path = PathBuf::from(get_string(args, "out", "")?);
    let rows = get_u32(args, "rows", 0, true)?;
    let cols = get_u32(args, "cols", 0, true)?;
    let seed = get_u32(args, "seed", 2, false)?;
    let mut matrix = make_matrix(rows, cols);
    let mut rng = StdRng::seed_from_u64(seed as u64);
    for v in &mut matrix.data {
        *v = sample_standard_normal(&mut rng);
    }
    save_matrix(&out_path, &matrix)?;
    println!("wrote {}", out_path.display());
    Ok(())
}
