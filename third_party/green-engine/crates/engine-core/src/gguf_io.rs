//! Minimal dep-free GGUF reader for KV metadata and F32 tensors.
//!
//! Enough for `.green` `metadata.gguf` tokenizer keys and synthetic F32 embedding tables.
//! Quantized tensor decode is out of scope (loader / weights path owns dequant).

use std::fs::File;
use std::io::{self, Read, Seek, SeekFrom};
use std::path::Path;

const GGUF_MAGIC: [u8; 4] = *b"GGUF";
const ALIGNMENT: u64 = 32;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub enum GgufValueType {
    Uint8 = 0,
    Int8 = 1,
    Uint16 = 2,
    Int16 = 3,
    Uint32 = 4,
    Int32 = 5,
    Float32 = 6,
    Bool = 7,
    String = 8,
    Array = 9,
    Uint64 = 10,
    Int64 = 11,
    Float64 = 12,
}

impl GgufValueType {
    fn from_u32(v: u32) -> io::Result<Self> {
        Ok(match v {
            0 => Self::Uint8,
            1 => Self::Int8,
            2 => Self::Uint16,
            3 => Self::Int16,
            4 => Self::Uint32,
            5 => Self::Int32,
            6 => Self::Float32,
            7 => Self::Bool,
            8 => Self::String,
            9 => Self::Array,
            10 => Self::Uint64,
            11 => Self::Int64,
            12 => Self::Float64,
            _ => {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    format!("unknown GGUF value type {v}"),
                ))
            }
        })
    }
}

/// One KV entry from a GGUF header.
#[derive(Clone, Debug)]
pub enum GgufValue {
    Uint8(u8),
    Int8(i8),
    Uint16(u16),
    Int16(i16),
    Uint32(u32),
    Int32(i32),
    Float32(f32),
    Bool(bool),
    String(String),
    Uint64(u64),
    Int64(i64),
    Float64(f64),
    Array(Vec<GgufValue>),
}

impl GgufValue {
    pub fn as_str(&self) -> Option<&str> {
        match self {
            GgufValue::String(s) => Some(s),
            _ => None,
        }
    }

    pub fn as_u64(&self) -> Option<u64> {
        Some(match self {
            GgufValue::Uint8(v) => *v as u64,
            GgufValue::Int8(v) => *v as u64,
            GgufValue::Uint16(v) => *v as u64,
            GgufValue::Int16(v) => *v as u64,
            GgufValue::Uint32(v) => *v as u64,
            GgufValue::Int32(v) => *v as u64,
            GgufValue::Uint64(v) => *v,
            GgufValue::Int64(v) => *v as u64,
            GgufValue::Bool(v) => *v as u64,
            _ => return None,
        })
    }

    pub fn as_f64(&self) -> Option<f64> {
        Some(match self {
            GgufValue::Float32(v) => *v as f64,
            GgufValue::Float64(v) => *v,
            GgufValue::Uint32(v) => *v as f64,
            GgufValue::Uint64(v) => *v as f64,
            GgufValue::Int32(v) => *v as f64,
            GgufValue::Int64(v) => *v as f64,
            _ => return None,
        })
    }

    pub fn as_string_array(&self) -> Option<Vec<String>> {
        match self {
            GgufValue::Array(items) => {
                let mut out = Vec::with_capacity(items.len());
                for item in items {
                    out.push(item.as_str()?.to_string());
                }
                Some(out)
            }
            _ => None,
        }
    }

    pub fn as_f32_array(&self) -> Option<Vec<f32>> {
        match self {
            GgufValue::Array(items) => {
                let mut out = Vec::with_capacity(items.len());
                for item in items {
                    out.push(item.as_f64()? as f32);
                }
                Some(out)
            }
            _ => None,
        }
    }

    pub fn as_i32_array(&self) -> Option<Vec<i32>> {
        match self {
            GgufValue::Array(items) => {
                let mut out = Vec::with_capacity(items.len());
                for item in items {
                    out.push(match item {
                        GgufValue::Int32(v) => *v,
                        GgufValue::Uint32(v) => *v as i32,
                        GgufValue::Int64(v) => *v as i32,
                        GgufValue::Uint64(v) => *v as i32,
                        _ => return None,
                    });
                }
                Some(out)
            }
            _ => None,
        }
    }

    pub fn as_u32_array(&self) -> Option<Vec<u32>> {
        self.as_i32_array()
            .map(|v| v.into_iter().map(|x| x as u32).collect())
    }
}

/// Parsed GGUF file (KV map + optional named F32 tensors).
#[derive(Clone, Debug, Default)]
pub struct GgufFile {
    pub version: u32,
    pub kv: Vec<(String, GgufValue)>,
    pub tensors: Vec<GgufTensor>,
}

#[derive(Clone, Debug)]
pub struct GgufTensor {
    pub name: String,
    pub shape: Vec<u64>,
    pub ggml_type: u32,
    pub offset: u64,
    /// Present when ggml_type is F32 and data was loaded.
    pub f32_data: Option<Vec<f32>>,
}

impl GgufFile {
    pub fn get(&self, key: &str) -> Option<&GgufValue> {
        self.kv.iter().find(|(k, _)| k == key).map(|(_, v)| v)
    }

    pub fn tensor(&self, name: &str) -> Option<&GgufTensor> {
        self.tensors.iter().find(|t| t.name == name)
    }
}

/// Load KV + tensor metadata; optionally materialize F32 tensor payloads.
pub fn read_gguf(path: &Path, load_f32_tensors: bool) -> io::Result<GgufFile> {
    let mut f = File::open(path)?;
    let mut magic = [0u8; 4];
    f.read_exact(&mut magic)?;
    if magic != GGUF_MAGIC {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("not a GGUF file: {}", path.display()),
        ));
    }
    let version = read_u32(&mut f)?;
    if version < 2 || version > 3 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("unsupported GGUF version {version}"),
        ));
    }
    let tensor_count = read_u64(&mut f)?;
    let kv_count = read_u64(&mut f)?;

    let mut kv = Vec::with_capacity(kv_count as usize);
    for _ in 0..kv_count {
        let key = read_string(&mut f)?;
        let ty = GgufValueType::from_u32(read_u32(&mut f)?)?;
        let value = read_value(&mut f, ty)?;
        kv.push((key, value));
    }

    let mut tensors = Vec::with_capacity(tensor_count as usize);
    for _ in 0..tensor_count {
        let name = read_string(&mut f)?;
        let n_dims = read_u32(&mut f)? as usize;
        let mut shape = Vec::with_capacity(n_dims);
        for _ in 0..n_dims {
            shape.push(read_u64(&mut f)?);
        }
        let ggml_type = read_u32(&mut f)?;
        let offset = read_u64(&mut f)?;
        tensors.push(GgufTensor {
            name,
            shape,
            ggml_type,
            offset,
            f32_data: None,
        });
    }

    // Tensor data starts at the next alignment boundary after the info block.
    let info_end = f.stream_position()?;
    let data_base = align_up(info_end, ALIGNMENT);

    if load_f32_tensors {
        const GGML_F32: u32 = 0;
        for t in &mut tensors {
            if t.ggml_type != GGML_F32 {
                continue;
            }
            let n: u64 = t.shape.iter().product();
            let nbytes = n.saturating_mul(4);
            let mut buf = vec![0u8; nbytes as usize];
            f.seek(SeekFrom::Start(data_base + t.offset))?;
            f.read_exact(&mut buf)?;
            let mut data = Vec::with_capacity(n as usize);
            for chunk in buf.chunks_exact(4) {
                data.push(f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]));
            }
            t.f32_data = Some(data);
        }
    }

    Ok(GgufFile {
        version,
        kv,
        tensors,
    })
}

fn align_up(v: u64, align: u64) -> u64 {
    (v + align - 1) & !(align - 1)
}

fn read_u8(r: &mut impl Read) -> io::Result<u8> {
    let mut b = [0u8; 1];
    r.read_exact(&mut b)?;
    Ok(b[0])
}

fn read_u16(r: &mut impl Read) -> io::Result<u16> {
    let mut b = [0u8; 2];
    r.read_exact(&mut b)?;
    Ok(u16::from_le_bytes(b))
}

fn read_u32(r: &mut impl Read) -> io::Result<u32> {
    let mut b = [0u8; 4];
    r.read_exact(&mut b)?;
    Ok(u32::from_le_bytes(b))
}

fn read_u64(r: &mut impl Read) -> io::Result<u64> {
    let mut b = [0u8; 8];
    r.read_exact(&mut b)?;
    Ok(u64::from_le_bytes(b))
}

fn read_i8(r: &mut impl Read) -> io::Result<i8> {
    Ok(read_u8(r)? as i8)
}

fn read_i16(r: &mut impl Read) -> io::Result<i16> {
    Ok(read_u16(r)? as i16)
}

fn read_i32(r: &mut impl Read) -> io::Result<i32> {
    Ok(read_u32(r)? as i32)
}

fn read_i64(r: &mut impl Read) -> io::Result<i64> {
    Ok(read_u64(r)? as i64)
}

fn read_f32(r: &mut impl Read) -> io::Result<f32> {
    let mut b = [0u8; 4];
    r.read_exact(&mut b)?;
    Ok(f32::from_le_bytes(b))
}

fn read_f64(r: &mut impl Read) -> io::Result<f64> {
    let mut b = [0u8; 8];
    r.read_exact(&mut b)?;
    Ok(f64::from_le_bytes(b))
}

fn read_string(r: &mut impl Read) -> io::Result<String> {
    let len = read_u64(r)? as usize;
    let mut buf = vec![0u8; len];
    r.read_exact(&mut buf)?;
    String::from_utf8(buf).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))
}

fn read_value(r: &mut impl Read, ty: GgufValueType) -> io::Result<GgufValue> {
    Ok(match ty {
        GgufValueType::Uint8 => GgufValue::Uint8(read_u8(r)?),
        GgufValueType::Int8 => GgufValue::Int8(read_i8(r)?),
        GgufValueType::Uint16 => GgufValue::Uint16(read_u16(r)?),
        GgufValueType::Int16 => GgufValue::Int16(read_i16(r)?),
        GgufValueType::Uint32 => GgufValue::Uint32(read_u32(r)?),
        GgufValueType::Int32 => GgufValue::Int32(read_i32(r)?),
        GgufValueType::Float32 => GgufValue::Float32(read_f32(r)?),
        GgufValueType::Bool => GgufValue::Bool(read_u8(r)? != 0),
        GgufValueType::String => GgufValue::String(read_string(r)?),
        GgufValueType::Uint64 => GgufValue::Uint64(read_u64(r)?),
        GgufValueType::Int64 => GgufValue::Int64(read_i64(r)?),
        GgufValueType::Float64 => GgufValue::Float64(read_f64(r)?),
        GgufValueType::Array => {
            let elem_ty = GgufValueType::from_u32(read_u32(r)?)?;
            let n = read_u64(r)? as usize;
            let mut items = Vec::with_capacity(n);
            for _ in 0..n {
                items.push(read_value(r, elem_ty)?);
            }
            GgufValue::Array(items)
        }
    })
}

/// Test tensor spec for [`write_test_gguf`].
#[cfg(test)]
pub struct TestGgufTensor<'a> {
    pub name: &'a str,
    pub shape: &'a [u64],
    pub ggml_type: u32,
    pub f32_data: Option<&'a [f32]>,
}

#[cfg(test)]
fn test_tensor_payload_bytes(t: &TestGgufTensor<'_>) -> u64 {
    if let Some(data) = t.f32_data {
        return (data.len() as u64) * 4;
    }
    let n: u64 = t.shape.iter().product();
    let elem_bytes = match t.ggml_type {
        0 => 4,
        11 => 8,
        _ => 1,
    };
    n.saturating_mul(elem_bytes)
}

/// Write a minimal GGUF (KV + optional tensors) for tests.
#[cfg(test)]
pub fn write_test_gguf(
    path: &Path,
    kv: &[(&str, TestKv)],
    tensors: &[TestGgufTensor<'_>],
) -> io::Result<()> {
    use std::io::Write;
    let mut f = File::create(path)?;
    f.write_all(&GGUF_MAGIC)?;
    f.write_all(&3u32.to_le_bytes())?;
    f.write_all(&(tensors.len() as u64).to_le_bytes())?;
    f.write_all(&(kv.len() as u64).to_le_bytes())?;
    for (k, v) in kv {
        write_string(&mut f, k)?;
        write_test_kv(&mut f, v)?;
    }
    let info_pos = f.stream_position()?;
    for t in tensors {
        write_string(&mut f, t.name)?;
        f.write_all(&(t.shape.len() as u32).to_le_bytes())?;
        for d in t.shape {
            f.write_all(&d.to_le_bytes())?;
        }
        f.write_all(&t.ggml_type.to_le_bytes())?;
        f.write_all(&0u64.to_le_bytes())?;
    }
    let info_end = f.stream_position()?;
    let data_base = align_up(info_end, ALIGNMENT);
    let pad = (data_base - info_end) as usize;
    f.write_all(&vec![0u8; pad])?;

    let mut offsets = Vec::with_capacity(tensors.len());
    let mut cursor = 0u64;
    for t in tensors {
        offsets.push(cursor);
        if let Some(data) = t.f32_data {
            for &x in data {
                f.write_all(&x.to_le_bytes())?;
            }
            cursor += (data.len() as u64) * 4;
        } else {
            let nbytes = test_tensor_payload_bytes(t);
            if nbytes > 0 {
                f.write_all(&vec![0u8; nbytes as usize])?;
                cursor += nbytes;
            }
        }
    }

    f.seek(SeekFrom::Start(info_pos))?;
    for (t, off) in tensors.iter().zip(offsets) {
        write_string(&mut f, t.name)?;
        f.write_all(&(t.shape.len() as u32).to_le_bytes())?;
        for d in t.shape {
            f.write_all(&d.to_le_bytes())?;
        }
        f.write_all(&t.ggml_type.to_le_bytes())?;
        f.write_all(&off.to_le_bytes())?;
    }
    Ok(())
}

#[cfg(test)]
#[derive(Clone, Debug)]
#[allow(dead_code)]
pub enum TestKv {
    String(&'static str),
    U32(u32),
    F32(f32),
    Bool(bool),
    StringArray(&'static [&'static str]),
    F32Array(&'static [f32]),
    I32Array(&'static [i32]),
}

#[cfg(test)]
fn write_string(w: &mut impl io::Write, s: &str) -> io::Result<()> {
    w.write_all(&(s.len() as u64).to_le_bytes())?;
    w.write_all(s.as_bytes())
}

#[cfg(test)]
fn write_test_kv(w: &mut impl io::Write, v: &TestKv) -> io::Result<()> {
    match v {
        TestKv::String(s) => {
            w.write_all(&(GgufValueType::String as u32).to_le_bytes())?;
            write_string(w, s)
        }
        TestKv::U32(x) => {
            w.write_all(&(GgufValueType::Uint32 as u32).to_le_bytes())?;
            w.write_all(&x.to_le_bytes())
        }
        TestKv::F32(x) => {
            w.write_all(&(GgufValueType::Float32 as u32).to_le_bytes())?;
            w.write_all(&x.to_le_bytes())
        }
        TestKv::Bool(b) => {
            w.write_all(&(GgufValueType::Bool as u32).to_le_bytes())?;
            w.write_all(&[u8::from(*b)])
        }
        TestKv::StringArray(items) => {
            w.write_all(&(GgufValueType::Array as u32).to_le_bytes())?;
            w.write_all(&(GgufValueType::String as u32).to_le_bytes())?;
            w.write_all(&(items.len() as u64).to_le_bytes())?;
            for s in *items {
                write_string(w, s)?;
            }
            Ok(())
        }
        TestKv::F32Array(items) => {
            w.write_all(&(GgufValueType::Array as u32).to_le_bytes())?;
            w.write_all(&(GgufValueType::Float32 as u32).to_le_bytes())?;
            w.write_all(&(items.len() as u64).to_le_bytes())?;
            for x in *items {
                w.write_all(&x.to_le_bytes())?;
            }
            Ok(())
        }
        TestKv::I32Array(items) => {
            w.write_all(&(GgufValueType::Array as u32).to_le_bytes())?;
            w.write_all(&(GgufValueType::Int32 as u32).to_le_bytes())?;
            w.write_all(&(items.len() as u64).to_le_bytes())?;
            for x in *items {
                w.write_all(&x.to_le_bytes())?;
            }
            Ok(())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn roundtrip_kv_and_f32_tensor() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("t.gguf");
        let data = [1.0f32, 2.0, 3.0, 4.0];
        write_test_gguf(
            &path,
            &[
                ("general.architecture", TestKv::String("llama")),
                ("tokenizer.ggml.tokens", TestKv::StringArray(&["a", "b"])),
            ],
            &[TestGgufTensor {
                name: "token_embd.weight",
                shape: &[2u64, 2],
                ggml_type: 0,
                f32_data: Some(&data),
            }],
        )
        .unwrap();
        let g = read_gguf(&path, true).unwrap();
        assert_eq!(g.get("general.architecture").unwrap().as_str(), Some("llama"));
        let toks = g.get("tokenizer.ggml.tokens").unwrap().as_string_array().unwrap();
        assert_eq!(toks, ["a", "b"]);
        let t = g.tensor("token_embd.weight").unwrap();
        assert_eq!(t.f32_data.as_ref().unwrap(), &data);
    }
}
