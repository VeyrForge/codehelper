//! Weight manifest — the integration seam with **Green Compress**
//! (<https://github.com/VeyrForge/GreenCompress>).
//!
//! Green Compress compresses each layer/expert (formats `green_ultra` / `green_spqr` / `green_turbo` /
//! `green_q8`, ~45-47% less RAM than FP32 at ~99.8-99.9% quality) and reports its compressed size and
//! quality. Green Engine schedules experts and counts bytes moved. The manifest is the contract
//! between them: per (layer, expert) → compressed `bytes`, `bits`, and `drift` (quality cost). With
//! it the engine accounts for *real* compressed sizes (and can later make quality-aware eviction
//! decisions) instead of assuming a uniform expert size.
//!
//! Text format (whitespace-separated, `#` comments), one row per expert — trivially emitted from
//! Green Compress's per-expert eval output:
//!
//! ```text
//! # layer expert bits bytes drift
//! 0 0 4 3670016 0.088
//! 0 1 3 2752512 0.183
//! ```

use std::fs;
use std::io;
use std::path::Path;

pub struct WeightManifest {
    pub layers: usize,
    pub experts: usize,
    bytes: Vec<u64>, // layers*experts, indexed (l*experts + e)
    bits: Vec<u8>,
    drift: Vec<f32>,
}

impl WeightManifest {
    /// Every expert the same size — the baseline when no compression manifest is supplied.
    pub fn uniform(layers: usize, experts: usize, bytes_per_expert: u64, bits: u8) -> Self {
        let n = layers * experts;
        WeightManifest {
            layers,
            experts,
            bytes: vec![bytes_per_expert; n],
            bits: vec![bits; n],
            drift: vec![0.0; n],
        }
    }

    pub fn load<P: AsRef<Path>>(path: P, layers: usize, experts: usize) -> io::Result<Self> {
        let text = fs::read_to_string(path)?;
        let n = layers * experts;
        let mut m = WeightManifest {
            layers,
            experts,
            bytes: vec![0; n],
            bits: vec![0; n],
            drift: vec![0.0; n],
        };
        let mut seen = vec![false; n];
        for (lineno, raw) in text.lines().enumerate() {
            let line = raw.split('#').next().unwrap_or("").trim();
            if line.is_empty() {
                continue;
            }
            let f: Vec<&str> = line.split_whitespace().collect();
            if f.len() < 4 {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    format!("manifest line {}: need `layer expert bits bytes [drift]`", lineno + 1),
                ));
            }
            let parse = |s: &str| s.parse::<f64>().map_err(|_| {
                io::Error::new(io::ErrorKind::InvalidData, format!("bad number on line {}", lineno + 1))
            });
            let (l, e) = (parse(f[0])? as usize, parse(f[1])? as usize);
            if l >= layers || e >= experts {
                return Err(io::Error::new(io::ErrorKind::InvalidData, "layer/expert out of range"));
            }
            let idx = l * experts + e;
            m.bits[idx] = parse(f[2])? as u8;
            m.bytes[idx] = parse(f[3])? as u64;
            m.drift[idx] = if f.len() > 4 { parse(f[4])? as f32 } else { 0.0 };
            seen[idx] = true;
        }
        if let Some(i) = seen.iter().position(|&s| !s) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("manifest missing expert (layer {}, expert {})", i / experts, i % experts),
            ));
        }
        Ok(m)
    }

    #[inline]
    pub fn expert_bytes(&self, layer: usize, expert: u16) -> u64 {
        self.bytes[layer * self.experts + expert as usize]
    }

    #[inline]
    pub fn expert_drift(&self, layer: usize, expert: u16) -> f32 {
        self.drift[layer * self.experts + expert as usize]
    }

    #[inline]
    pub fn expert_bits(&self, layer: usize, expert: u16) -> u8 {
        self.bits[layer * self.experts + expert as usize]
    }
}
