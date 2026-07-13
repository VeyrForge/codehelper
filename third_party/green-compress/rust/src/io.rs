use std::fs::File;
use std::io::{Read, Seek, Write};
use std::path::Path;

use crate::error::{fail, Result};
use crate::types::{
    FusedWeightCache, Matrix, Q4Matrix, Q8Matrix, Repair, SparseTerm, SubspaceAdapter,
};

/// Decode little-endian f32 bytes into `out` (bulk copy on little-endian hosts).
pub(crate) fn decode_f32_le(bytes: &[u8], out: &mut [f32]) -> Result<()> {
    if bytes.len() != out.len() * 4 {
        return Err(fail(format!(
            "f32 decode size mismatch: {} bytes for {} floats",
            bytes.len(),
            out.len()
        )));
    }
    #[cfg(target_endian = "little")]
    {
        unsafe {
            std::ptr::copy_nonoverlapping(
                bytes.as_ptr(),
                out.as_mut_ptr() as *mut u8,
                bytes.len(),
            );
        }
        Ok(())
    }
    #[cfg(not(target_endian = "little"))]
    {
        for (chunk, slot) in bytes.chunks_exact(4).zip(out.iter_mut()) {
            *slot = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
        }
        Ok(())
    }
}

/// Encode f32 slice to little-endian bytes (bulk copy on little-endian hosts).
pub(crate) fn encode_f32_le(data: &[f32]) -> Vec<u8> {
    #[cfg(target_endian = "little")]
    {
        let mut bytes = vec![0u8; data.len() * 4];
        unsafe {
            std::ptr::copy_nonoverlapping(
                data.as_ptr() as *const u8,
                bytes.as_mut_ptr(),
                bytes.len(),
            );
        }
        bytes
    }
    #[cfg(not(target_endian = "little"))]
    {
        data.iter().flat_map(|f| f.to_le_bytes()).collect()
    }
}

fn write_exact(file: &mut File, data: &[u8]) -> Result<()> {
    file.write_all(data)
        .map_err(|e| fail(format!("write failed: {e}")))
}

pub fn read_exact(file: &mut File, data: &mut [u8]) -> Result<()> {
    file.read_exact(data)
        .map_err(|e| fail(format!("read failed: {e}")))
}

fn write_u32(file: &mut File, value: u32) -> Result<()> {
    write_exact(file, &value.to_le_bytes())
}

fn write_u64(file: &mut File, value: u64) -> Result<()> {
    write_exact(file, &value.to_le_bytes())
}

fn write_f32(file: &mut File, value: f32) -> Result<()> {
    write_exact(file, &value.to_le_bytes())
}

fn read_u32(file: &mut File) -> Result<u32> {
    let mut buf = [0u8; 4];
    read_exact(file, &mut buf)?;
    Ok(u32::from_le_bytes(buf))
}

fn read_u64(file: &mut File) -> Result<u64> {
    let mut buf = [0u8; 8];
    read_exact(file, &mut buf)?;
    Ok(u64::from_le_bytes(buf))
}

fn read_f32(file: &mut File) -> Result<f32> {
    let mut buf = [0u8; 4];
    read_exact(file, &mut buf)?;
    Ok(f32::from_le_bytes(buf))
}

fn write_magic(file: &mut File, magic: &[u8; 8]) -> Result<()> {
    write_exact(file, magic)
}

fn read_magic8(file: &mut File) -> Result<[u8; 8]> {
    let mut got = [0u8; 8];
    read_exact(file, &mut got)?;
    Ok(got)
}

/// Accept the current green-branded magic or the legacy `LCL*` one (pre-0.3.7 files).
fn check_magic_any(file: &mut File, primary: &[u8; 8], legacy: &[u8; 8]) -> Result<()> {
    let got = read_magic8(file)?;
    if &got == primary || &got == legacy {
        Ok(())
    } else {
        Err(fail("invalid file magic"))
    }
}

pub fn make_matrix(rows: u32, cols: u32) -> Matrix {
    Matrix {
        rows,
        cols,
        data: vec![0.0; rows as usize * cols as usize],
    }
}

pub fn save_matrix(path: &Path, matrix: &Matrix) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open matrix for write: {}: {e}", path.display())))?;
    write_magic(&mut file, b"GRNMX01\0")?;
    write_u32(&mut file, matrix.rows)?;
    write_u32(&mut file, matrix.cols)?;
    let bytes: Vec<u8> = matrix
        .data
        .iter()
        .flat_map(|f| f.to_le_bytes())
        .collect();
    write_exact(&mut file, &bytes)?;
    Ok(())
}

pub fn load_matrix(path: &Path) -> Result<Matrix> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open matrix for read: {}: {e}", path.display())))?;
    check_magic_any(&mut file, b"GRNMX01\0", b"LCLMX01\0")?;
    let rows = read_u32(&mut file)?;
    let cols = read_u32(&mut file)?;
    let n = rows as u64 * cols as u64;
    if n == 0 || n > 256 * 1024 * 1024 {
        return Err(fail(format!(
            "matrix dimensions out of range: {rows}x{cols}"
        )));
    }
    let mut data = vec![0.0f32; n as usize];
    let mut bytes = vec![0u8; data.len() * 4];
    read_exact(&mut file, &mut bytes)?;
    decode_f32_le(&bytes, &mut data)?;
    Ok(Matrix { rows, cols, data })
}

pub fn save_q4(path: &Path, matrix: &Q4Matrix) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open q4 for write: {}: {e}", path.display())))?;
    write_magic(&mut file, b"GRNQ401\0")?;
    write_u32(&mut file, matrix.rows)?;
    write_u32(&mut file, matrix.cols)?;
    write_u32(&mut file, matrix.block)?;
    write_u64(&mut file, matrix.scales.len() as u64)?;
    write_u64(&mut file, matrix.packed.len() as u64)?;
    let scale_bytes: Vec<u8> = matrix
        .scales
        .iter()
        .flat_map(|f| f.to_le_bytes())
        .collect();
    write_exact(&mut file, &scale_bytes)?;
    write_exact(&mut file, &matrix.packed)?;
    Ok(())
}

pub fn load_q4(path: &Path) -> Result<Q4Matrix> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open q4 for read: {}: {e}", path.display())))?;
    check_magic_any(&mut file, b"GRNQ401\0", b"LCLQ401\0")?;
    let rows = read_u32(&mut file)?;
    let cols = read_u32(&mut file)?;
    let block = read_u32(&mut file)?;
    let scale_count = read_u64(&mut file)? as usize;
    let packed_count = read_u64(&mut file)? as usize;
    let mut scales = vec![0.0f32; scale_count];
    let mut scale_bytes = vec![0u8; scale_count * 4];
    read_exact(&mut file, &mut scale_bytes)?;
    for (i, chunk) in scale_bytes.chunks_exact(4).enumerate() {
        scales[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
    }
    let mut packed = vec![0u8; packed_count];
    read_exact(&mut file, &mut packed)?;
    Ok(Q4Matrix {
        rows,
        cols,
        block,
        scales,
        packed,
    })
}

pub fn save_repair(path: &Path, repair: &Repair) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open repair for write: {}: {e}", path.display())))?;
    write_magic(&mut file, b"GRNRP01\0")?;
    write_u32(&mut file, repair.rows)?;
    write_u32(&mut file, repair.cols)?;
    write_u32(&mut file, repair.low_rank.len() as u32)?;
    write_u64(&mut file, repair.sparse.len() as u64)?;
    for term in &repair.low_rank {
        write_f32(&mut file, term.sigma)?;
        let u_bytes: Vec<u8> = term.u.iter().flat_map(|f| f.to_le_bytes()).collect();
        write_exact(&mut file, &u_bytes)?;
        let v_bytes: Vec<u8> = term.v.iter().flat_map(|f| f.to_le_bytes()).collect();
        write_exact(&mut file, &v_bytes)?;
    }
    for term in &repair.sparse {
        write_u64(&mut file, term.index)?;
        write_f32(&mut file, term.value)?;
    }
    Ok(())
}

pub fn load_repair(path: &Path) -> Result<Repair> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open repair for read: {}: {e}", path.display())))?;
    check_magic_any(&mut file, b"GRNRP01\0", b"LCLRP01\0")?;
    let rows = read_u32(&mut file)?;
    let cols = read_u32(&mut file)?;
    let rank = read_u32(&mut file)? as usize;
    let sparse_count = read_u64(&mut file)? as usize;
    let mut low_rank = Vec::with_capacity(rank);
    for _ in 0..rank {
        let sigma = read_f32(&mut file)?;
        let mut u = vec![0.0f32; rows as usize];
        let mut v = vec![0.0f32; cols as usize];
        let mut u_bytes = vec![0u8; u.len() * 4];
        read_exact(&mut file, &mut u_bytes)?;
        for (i, chunk) in u_bytes.chunks_exact(4).enumerate() {
            u[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
        }
        let mut v_bytes = vec![0u8; v.len() * 4];
        read_exact(&mut file, &mut v_bytes)?;
        for (i, chunk) in v_bytes.chunks_exact(4).enumerate() {
            v[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
        }
        low_rank.push(crate::types::LowRankTerm { sigma, u, v });
    }
    let mut sparse = Vec::with_capacity(sparse_count);
    for _ in 0..sparse_count {
        sparse.push(SparseTerm {
            index: read_u64(&mut file)?,
            value: read_f32(&mut file)?,
        });
    }
    Ok(Repair {
        rows,
        cols,
        low_rank,
        sparse,
    })
}

pub fn save_q8(path: &Path, matrix: &Q8Matrix) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open q8 for write: {}: {e}", path.display())))?;
    // LCLQ802 stores per-block scales as f16 (2 B/block); LCLQ801 was f32.
    write_magic(&mut file, b"GRNQ802\0")?;
    write_u32(&mut file, matrix.rows)?;
    write_u32(&mut file, matrix.cols)?;
    write_u32(&mut file, matrix.block)?;
    write_u64(&mut file, matrix.scales.len() as u64)?;
    write_u64(&mut file, matrix.packed.len() as u64)?;
    let scale_bytes: Vec<u8> = matrix
        .scales
        .iter()
        .flat_map(|f| f.to_le_bytes())
        .collect();
    write_exact(&mut file, &scale_bytes)?;
    let packed_bytes: Vec<u8> = matrix.packed.iter().map(|&b| b as u8).collect();
    write_exact(&mut file, &packed_bytes)?;
    write_u64(&mut file, matrix.awq_scales.len() as u64)?;
    if !matrix.awq_scales.is_empty() {
        let awq_bytes: Vec<u8> = matrix
            .awq_scales
            .iter()
            .flat_map(|f| f.to_le_bytes())
            .collect();
        write_exact(&mut file, &awq_bytes)?;
    }
    write_u64(&mut file, matrix.row_spin.len() as u64)?;
    if !matrix.row_spin.is_empty() {
        let spin_bytes: Vec<u8> = matrix
            .row_spin
            .iter()
            .flat_map(|f| f.to_le_bytes())
            .collect();
        write_exact(&mut file, &spin_bytes)?;
    }
    Ok(())
}

pub fn load_q8(path: &Path) -> Result<Q8Matrix> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open q8 for read: {}: {e}", path.display())))?;
    let magic = read_magic8(&mut file)?;
    let scales_are_f16 = match &magic {
        b"GRNQ802\0" => true,  // current: green-branded, f16 scales
        b"LCLQ802\0" => true,  // legacy magic, f16 scales
        b"LCLQ801\0" => false, // legacy: f32 scales, upconverted to f16 on load
        _ => return Err(fail("invalid q8 file magic")),
    };
    let rows = read_u32(&mut file)?;
    let cols = read_u32(&mut file)?;
    let block = read_u32(&mut file)?;
    let scale_count = read_u64(&mut file)? as usize;
    let packed_count = read_u64(&mut file)? as usize;
    let mut scales = vec![crate::types::f16::from_f32(0.0); scale_count];
    if scales_are_f16 {
        let mut scale_bytes = vec![0u8; scale_count * 2];
        read_exact(&mut file, &mut scale_bytes)?;
        for (i, chunk) in scale_bytes.chunks_exact(2).enumerate() {
            scales[i] = crate::types::f16::from_le_bytes([chunk[0], chunk[1]]);
        }
    } else {
        let mut scale_bytes = vec![0u8; scale_count * 4];
        read_exact(&mut file, &mut scale_bytes)?;
        for (i, chunk) in scale_bytes.chunks_exact(4).enumerate() {
            let s = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
            scales[i] = crate::types::f16::from_f32(s);
        }
    }
    let mut packed_u8 = vec![0u8; packed_count];
    read_exact(&mut file, &mut packed_u8)?;
    let packed: Vec<i8> = packed_u8.iter().map(|&b| b as i8).collect();

    let pos = file.stream_position().map_err(|e| fail(e.to_string()))?;
    let end = file.seek(std::io::SeekFrom::End(0)).map_err(|e| fail(e.to_string()))?;
    file.seek(std::io::SeekFrom::Start(pos))
        .map_err(|e| fail(e.to_string()))?;

    let mut awq_scales = Vec::new();
    let mut row_spin = Vec::new();
    if end > pos {
        let awq_count = read_u64(&mut file)? as usize;
        if awq_count > 0 {
            awq_scales = vec![0.0f32; awq_count];
            let mut awq_bytes = vec![0u8; awq_count * 4];
            read_exact(&mut file, &mut awq_bytes)?;
            for (i, chunk) in awq_bytes.chunks_exact(4).enumerate() {
                awq_scales[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
            }
        }
        let pos2 = file.stream_position().map_err(|e| fail(e.to_string()))?;
        let end2 = file.seek(std::io::SeekFrom::End(0)).map_err(|e| fail(e.to_string()))?;
        file.seek(std::io::SeekFrom::Start(pos2))
            .map_err(|e| fail(e.to_string()))?;
        if end2 > pos2 {
            let spin_count = read_u64(&mut file)? as usize;
            if spin_count > 0 {
                row_spin = vec![0.0f32; spin_count];
                let mut spin_bytes = vec![0u8; spin_count * 4];
                read_exact(&mut file, &mut spin_bytes)?;
                for (i, chunk) in spin_bytes.chunks_exact(4).enumerate() {
                    row_spin[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
                }
            }
        }
    }

    Ok(Q8Matrix {
        rows,
        cols,
        block,
        scales,
        packed,
        awq_scales,
        row_spin,
    })
}

pub fn load_fused_cache(path: &Path) -> Result<FusedWeightCache> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open fused cache for read: {}: {e}", path.display())))?;
    check_magic_any(&mut file, b"GRNFCW01", b"LCLFCW01")?;
    let rows = read_u32(&mut file)?;
    let cols = read_u32(&mut file)?;
    let weight_count = read_u64(&mut file)? as usize;
    let mut weights = vec![0.0f32; weight_count];
    let mut weight_bytes = vec![0u8; weight_count * 4];
    read_exact(&mut file, &mut weight_bytes)?;
    for (i, chunk) in weight_bytes.chunks_exact(4).enumerate() {
        weights[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
    }
    let pos = file.stream_position().map_err(|e| fail(e.to_string()))?;
    let end = file.seek(std::io::SeekFrom::End(0)).map_err(|e| fail(e.to_string()))?;
    file.seek(std::io::SeekFrom::Start(pos))
        .map_err(|e| fail(e.to_string()))?;
    let mut row_spin = Vec::new();
    if end > pos {
        let spin_count = read_u64(&mut file)? as usize;
        if spin_count > 0 {
            row_spin = vec![0.0f32; spin_count];
            let mut spin_bytes = vec![0u8; spin_count * 4];
            read_exact(&mut file, &mut spin_bytes)?;
            for (i, chunk) in spin_bytes.chunks_exact(4).enumerate() {
                row_spin[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
            }
        }
    }
    Ok(FusedWeightCache {
        rows,
        cols,
        weights,
        row_spin,
    })
}

pub fn save_fused_cache(path: &Path, cache: &FusedWeightCache) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open fused cache for write: {}: {e}", path.display())))?;
    write_magic(&mut file, b"GRNFCW01")?;
    write_u32(&mut file, cache.rows)?;
    write_u32(&mut file, cache.cols)?;
    write_u64(&mut file, cache.weights.len() as u64)?;
    let weight_bytes: Vec<u8> = cache
        .weights
        .iter()
        .flat_map(|f| f.to_le_bytes())
        .collect();
    write_exact(&mut file, &weight_bytes)?;
    write_u64(&mut file, cache.row_spin.len() as u64)?;
    if !cache.row_spin.is_empty() {
        let spin_bytes: Vec<u8> = cache
            .row_spin
            .iter()
            .flat_map(|f| f.to_le_bytes())
            .collect();
        write_exact(&mut file, &spin_bytes)?;
    }
    Ok(())
}

pub fn load_output_bias(path: &Path) -> Result<Vec<f32>> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open bias for read: {}: {e}", path.display())))?;
    check_magic_any(&mut file, b"GRNBIAS1", b"LCLBIAS1")?;
    let count = read_u64(&mut file)? as usize;
    let mut bias = vec![0.0f32; count];
    let mut bytes = vec![0u8; count * 4];
    read_exact(&mut file, &mut bytes)?;
    for (i, chunk) in bytes.chunks_exact(4).enumerate() {
        bias[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
    }
    Ok(bias)
}

pub fn load_subspace_adapter(path: &Path) -> Result<SubspaceAdapter> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open subspace for read: {}: {e}", path.display())))?;
    check_magic_any(&mut file, b"GRNSUB01", b"LCLSUB01")?;
    let in_dim = read_u32(&mut file)?;
    let out_dim = read_u32(&mut file)?;
    let rank = read_u32(&mut file)?;
    let basis_count = read_u64(&mut file)? as usize;
    let mut basis = vec![0.0f32; basis_count];
    let mut basis_bytes = vec![0u8; basis_count * 4];
    read_exact(&mut file, &mut basis_bytes)?;
    for (i, chunk) in basis_bytes.chunks_exact(4).enumerate() {
        basis[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
    }
    let coeff_count = read_u64(&mut file)? as usize;
    let mut coeff = vec![0.0f32; coeff_count];
    let mut coeff_bytes = vec![0u8; coeff_count * 4];
    read_exact(&mut file, &mut coeff_bytes)?;
    for (i, chunk) in coeff_bytes.chunks_exact(4).enumerate() {
        coeff[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
    }
    Ok(SubspaceAdapter {
        in_dim,
        out_dim,
        rank,
        basis,
        coeff,
    })
}

pub fn load_outliers(path: &Path) -> Result<Vec<SparseTerm>> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open outliers for read: {}: {e}", path.display())))?;
    check_magic_any(&mut file, b"GRNOUT01", b"LCLOUT01")?;
    let count = read_u64(&mut file)? as usize;
    let mut outliers = Vec::with_capacity(count);
    for _ in 0..count {
        outliers.push(SparseTerm {
            index: read_u64(&mut file)?,
            value: read_f32(&mut file)?,
        });
    }
    Ok(outliers)
}

pub fn save_output_bias(path: &Path, bias: &[f32]) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open bias for write: {}: {e}", path.display())))?;
    write_magic(&mut file, b"GRNBIAS1")?;
    write_u64(&mut file, bias.len() as u64)?;
    let bytes: Vec<u8> = bias.iter().flat_map(|f| f.to_le_bytes()).collect();
    write_exact(&mut file, &bytes)?;
    Ok(())
}

pub fn save_subspace_adapter(path: &Path, adapter: &SubspaceAdapter) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open subspace for write: {}: {e}", path.display())))?;
    write_magic(&mut file, b"GRNSUB01")?;
    write_u32(&mut file, adapter.in_dim)?;
    write_u32(&mut file, adapter.out_dim)?;
    write_u32(&mut file, adapter.rank)?;
    write_u64(&mut file, adapter.basis.len() as u64)?;
    let basis_bytes: Vec<u8> = adapter.basis.iter().flat_map(|f| f.to_le_bytes()).collect();
    write_exact(&mut file, &basis_bytes)?;
    write_u64(&mut file, adapter.coeff.len() as u64)?;
    let coeff_bytes: Vec<u8> = adapter.coeff.iter().flat_map(|f| f.to_le_bytes()).collect();
    write_exact(&mut file, &coeff_bytes)?;
    Ok(())
}

pub fn save_outliers(path: &Path, outliers: &[SparseTerm]) -> Result<()> {
    let mut file = File::create(path)
        .map_err(|e| fail(format!("could not open outliers for write: {}: {e}", path.display())))?;
    write_magic(&mut file, b"GRNOUT01")?;
    write_u64(&mut file, outliers.len() as u64)?;
    for term in outliers {
        write_u64(&mut file, term.index)?;
        write_f32(&mut file, term.value)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    #[test]
    fn f32_le_roundtrip() {
        let data = vec![0.0f32, 1.25, -3.5, f32::NAN];
        let bytes = encode_f32_le(&data);
        let mut out = vec![0.0f32; data.len()];
        decode_f32_le(&bytes, &mut out).expect("decode");
        assert_eq!(out[0], data[0]);
        assert_eq!(out[1], data[1]);
        assert_eq!(out[2], data[2]);
        assert!(out[3].is_nan());
    }

    #[test]
    fn matrix_save_load_roundtrip() {
        let dir = std::env::temp_dir().join(format!(
            "greencompress_io_test_{}",
            std::process::id()
        ));
        let _ = std::fs::create_dir_all(&dir);
        let path = dir.join("m.mx");
        let matrix = Matrix {
            rows: 2,
            cols: 3,
            data: vec![1.0, 2.0, 3.0, 4.0, 5.0, 6.0],
        };
        save_matrix(&path, &matrix).expect("save");
        let loaded = load_matrix(&path).expect("load");
        assert_eq!(loaded.rows, matrix.rows);
        assert_eq!(loaded.cols, matrix.cols);
        assert_eq!(loaded.data, matrix.data);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn q8_save_load_roundtrip_f16_scales() {
        let dir = std::env::temp_dir().join(format!("greencompress_q8_test_{}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        let path = dir.join("w.q8");
        let m = Matrix {
            rows: 4,
            cols: 8,
            data: (0..32).map(|i| (i as f32 - 15.0) * 0.03).collect(),
        };
        let q8 = crate::q8::quantize_q8(&m, 8);
        save_q8(&path, &q8).expect("save q8");
        let loaded = load_q8(&path).expect("load q8");
        assert_eq!(loaded.rows, q8.rows);
        assert_eq!(loaded.cols, q8.cols);
        assert_eq!(loaded.block, q8.block);
        assert_eq!(loaded.scales, q8.scales); // f16 <-> f16 is lossless
        assert_eq!(loaded.packed, q8.packed);
        // reconstruction stays close to the original
        let rec = crate::q8::dequantize_q8(&loaded);
        let drift = crate::util::rel_l2(&m.data, &rec.data);
        assert!(drift < 0.02, "q8 roundtrip drift {drift}");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn load_matrix_rejects_huge_dims() {
        let dir = std::env::temp_dir().join(format!(
            "greencompress_io_bad_{}",
            std::process::id()
        ));
        let _ = std::fs::create_dir_all(&dir);
        let path = dir.join("bad.mx");
        let mut file = File::create(&path).unwrap();
        file.write_all(b"LCLMX01\0").unwrap();
        file.write_all(&65536u32.to_le_bytes()).unwrap();
        file.write_all(&65536u32.to_le_bytes()).unwrap();
        assert!(load_matrix(&path).is_err());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
