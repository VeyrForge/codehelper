//! Loader for the dependency-free expert-trace binary (see `crates/engine-core/testdata/`).
//!
//! Layout (little-endian): magic:u32, tokens:u32, layers:u32, top_k:u32, experts:u32,
//! then tokens*layers*top_k i16 expert ids, row-major as (token, layer, k).

use std::fs;
use std::io;
use std::path::Path;

const MAGIC: u32 = 0x4F45_4E47;

/// A captured MoE routing trace: which experts each (token, layer) selected.
pub struct Trace {
    pub tokens: usize,
    pub layers: usize,
    pub top_k: usize,
    pub experts: usize,
    data: Vec<u16>, // expert ids, already validated < experts
}

impl Trace {
    pub fn load<P: AsRef<Path>>(path: P) -> io::Result<Trace> {
        let bytes = fs::read(path)?;
        if bytes.len() < 20 {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "trace too short"));
        }
        let u32_at = |o: usize| u32::from_le_bytes(bytes[o..o + 4].try_into().unwrap());
        if u32_at(0) != MAGIC {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "bad magic"));
        }
        let tokens = u32_at(4) as usize;
        let layers = u32_at(8) as usize;
        let top_k = u32_at(12) as usize;
        let experts = u32_at(16) as usize;
        let n = tokens * layers * top_k;
        let expected = 20 + n * 2;
        if bytes.len() != expected {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("size mismatch: got {}, expected {}", bytes.len(), expected),
            ));
        }
        let mut data = Vec::with_capacity(n);
        for i in 0..n {
            let o = 20 + i * 2;
            let v = i16::from_le_bytes([bytes[o], bytes[o + 1]]);
            if v < 0 || v as usize >= experts {
                return Err(io::Error::new(io::ErrorKind::InvalidData, "expert id out of range"));
            }
            data.push(v as u16);
        }
        Ok(Trace { tokens, layers, top_k, experts, data })
    }

    /// The `top_k` expert ids selected at (token `t`, layer `l`).
    #[inline]
    pub fn experts_at(&self, t: usize, l: usize) -> &[u16] {
        let base = (t * self.layers + l) * self.top_k;
        &self.data[base..base + self.top_k]
    }
}
