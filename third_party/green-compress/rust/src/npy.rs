use std::fs::File;
use std::path::Path;

use regex::Regex;

use crate::error::{fail, Result};
use crate::io::{make_matrix, read_exact};
use crate::types::Matrix;

pub struct NpyHeader {
    pub little_endian: bool,
    pub fortran_order: bool,
    pub shape: Vec<u32>,
}

pub fn parse_npy_header(header: &str) -> Result<NpyHeader> {
    let descr_re = Regex::new(r"'descr':\s*'([^']+)'").unwrap();
    let fortran_re = Regex::new(r"'fortran_order':\s*(True|False)").unwrap();
    let shape_re = Regex::new(r"'shape':\s*\(([^)]*)\)").unwrap();

    let caps = descr_re
        .captures(header)
        .ok_or_else(|| fail("npy header missing descr"))?;
    let descr = caps.get(1).unwrap().as_str();
    if descr != "<f4" && descr != ">f4" {
        return Err(fail(format!("npy must be float32 (<f4 or >f4), got: {descr}")));
    }
    let little_endian = descr.starts_with('<');

    let fortran_order = fortran_re
        .captures(header)
        .map(|c| c.get(1).unwrap().as_str() == "True")
        .unwrap_or(false);
    if fortran_order {
        return Err(fail("npy fortran_order tensors are not supported"));
    }

    let shape_caps = shape_re
        .captures(header)
        .ok_or_else(|| fail("npy header missing shape"))?;
    let shape_str = shape_caps.get(1).unwrap().as_str();
    let mut shape = Vec::new();
    for item in shape_str.split(',') {
        let item = item.trim();
        if item.is_empty() {
            continue;
        }
        shape.push(item.parse::<u32>().map_err(|_| fail("bad npy shape"))?);
    }
    if shape.len() != 2 {
        return Err(fail("npy must be 2D float32 matrix"));
    }

    Ok(NpyHeader {
        little_endian,
        fortran_order,
        shape,
    })
}

pub fn load_npy_matrix(path: &Path) -> Result<Matrix> {
    let mut file = File::open(path)
        .map_err(|e| fail(format!("could not open npy for read: {}: {e}", path.display())))?;

    let mut magic = [0u8; 6];
    read_exact(&mut file, &mut magic)?;
    if &magic != b"\x93NUMPY" {
        return Err(fail("invalid npy magic"));
    }

    let mut major = [0u8; 1];
    let mut minor = [0u8; 1];
    read_exact(&mut file, &mut major)?;
    read_exact(&mut file, &mut minor)?;
    let _ = minor;

    let header_len = match major[0] {
        1 => {
            let mut len16 = [0u8; 2];
            read_exact(&mut file, &mut len16)?;
            u32::from_le_bytes([len16[0], len16[1], 0, 0])
        }
        2 | 3 => {
            let mut len32 = [0u8; 4];
            read_exact(&mut file, &mut len32)?;
            u32::from_le_bytes(len32)
        }
        _ => return Err(fail("unsupported npy version")),
    };

    let mut header = vec![0u8; header_len as usize];
    read_exact(&mut file, &mut header)?;
    let header_str = String::from_utf8_lossy(&header);
    let parsed = parse_npy_header(&header_str)?;

    let mut matrix = make_matrix(parsed.shape[0], parsed.shape[1]);
    let mut bytes = vec![0u8; matrix.data.len() * 4];
    read_exact(&mut file, &mut bytes)?;
    for (i, chunk) in bytes.chunks_exact(4).enumerate() {
        matrix.data[i] = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
    }

    if !parsed.little_endian {
        for value in &mut matrix.data {
            let bits = value.to_bits();
            let swapped = bits.swap_bytes();
            *value = f32::from_bits(swapped);
        }
    }

    Ok(matrix)
}
