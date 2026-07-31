use std::collections::HashMap;

pub use half::f16;

#[derive(Clone, Debug, Default)]
pub struct Matrix {
    pub rows: u32,
    pub cols: u32,
    pub data: Vec<f32>,
}

impl Matrix {
    pub fn at(&self, r: u32, c: u32) -> f32 {
        self.data[r as usize * self.cols as usize + c as usize]
    }

    pub fn at_mut(&mut self, r: u32, c: u32) -> &mut f32 {
        let idx = r as usize * self.cols as usize + c as usize;
        &mut self.data[idx]
    }
}

#[derive(Clone, Debug, Default)]
pub struct Q4Matrix {
    pub rows: u32,
    pub cols: u32,
    pub block: u32,
    pub scales: Vec<f32>,
    pub packed: Vec<u8>,
}

/// Uniform sub-8-bit block quant (5/6/7-bit), bit-packed. Symmetric per-block scale
/// (f16), like Q8 but fewer bits. Q7: +0.28% perplexity at −12% RAM vs Q8.
#[derive(Clone, Debug, Default)]
pub struct QnMatrix {
    pub rows: u32,
    pub cols: u32,
    pub bits: u8,   // 5, 6, or 7
    pub block: u32,
    pub scales: Vec<f16>,
    /// bit-packed unsigned codes (value + level), `bits` bits each, little-endian bit order
    pub packed: Vec<u8>,
}

/// llama.cpp-style Q4_K: 256-weight super-blocks of 8 sub-blocks (32 each),
/// asymmetric 4-bit (`w = scale*q - min`), with 6-bit quantized per-sub scales/mins
/// and fp16 super-scales `d`/`dmin`. ~4.5 bits/weight. Codec only for now (matmul TBD).
#[derive(Clone, Debug, Default)]
pub struct Q4KMatrix {
    pub rows: u32,
    pub cols: u32,
    /// super-block scale for the 6-bit sub-scales (one f16 per super-block)
    pub d: Vec<f16>,
    /// super-block scale for the 6-bit sub-mins (one f16 per super-block)
    pub dmin: Vec<f16>,
    /// 8 six-bit sub-scales per super-block (stored 0..63 in a u8)
    pub scales: Vec<u8>,
    /// 8 six-bit sub-mins per super-block (stored 0..63 in a u8). The affine offset is
    /// `min(block_min, 0)`, so it is always ≤ 0 and stored unsigned as its magnitude —
    /// optimal for real (zero-centred) weights and robust to all-positive blocks.
    pub mins: Vec<u8>,
    /// 4-bit quants (0..15), two per byte
    pub quants: Vec<u8>,
}

#[derive(Clone, Debug, Default)]
pub struct Q8Matrix {
    pub rows: u32,
    pub cols: u32,
    pub block: u32,
    /// Per-block dequant scales, stored f16 (2 B/block, quality-neutral vs f32 —
    /// −0.0004% on real ffn_down). Read once per block, converted to f32 in compute.
    pub scales: Vec<f16>,
    pub packed: Vec<i8>,
    pub awq_scales: Vec<f32>,
    pub row_spin: Vec<f32>,
}

#[derive(Clone, Debug)]
pub struct LowRankTerm {
    pub sigma: f32,
    pub u: Vec<f32>,
    pub v: Vec<f32>,
}

#[derive(Clone, Debug, Copy)]
pub struct SparseTerm {
    pub index: u64,
    pub value: f32,
}

#[derive(Clone, Debug, Default)]
pub struct Repair {
    pub rows: u32,
    pub cols: u32,
    pub low_rank: Vec<LowRankTerm>,
    pub sparse: Vec<SparseTerm>,
}

#[derive(Clone, Debug, Default)]
pub struct SubspaceAdapter {
    pub in_dim: u32,
    pub out_dim: u32,
    pub rank: u32,
    pub basis: Vec<f32>,
    pub coeff: Vec<f32>,
}

#[derive(Clone, Debug, Default)]
pub struct FusedWeightCache {
    pub rows: u32,
    pub cols: u32,
    pub weights: Vec<f32>,
    pub row_spin: Vec<f32>,
}

pub type RowColEntry = (u32, f32);

#[derive(Clone, Debug, Default)]
pub struct SparseRowCache {
    pub by_row: Vec<Vec<RowColEntry>>,
}

#[derive(Clone, Debug, Default)]
pub struct OutlierRowCache {
    pub by_row: Vec<Vec<RowColEntry>>,
}

impl OutlierRowCache {
    pub fn has_rows(&self) -> bool {
        self.by_row.iter().any(|r| !r.is_empty())
    }
}

impl SparseRowCache {
    pub fn has_rows(&self) -> bool {
        self.by_row.iter().any(|r| !r.is_empty())
    }
}

#[derive(Clone, Debug, Default)]
pub struct SparseColCache {
    pub by_col: Vec<Vec<RowColEntry>>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SparseScoreMode {
    Magnitude,
    Activation,
    Output,
    Imatrix,
}

#[derive(Clone, Debug, Default)]
pub struct Args {
    pub command: String,
    pub values: HashMap<String, String>,
}

#[derive(Clone, Debug, Default)]
pub struct SweepRow {
    pub name: String,
    pub rank: u32,
    pub sparse_frac: f32,
    pub q4_activation_drift: f64,
    pub repaired_activation_drift: f64,
    pub repaired_weight_rel_l2: f64,
    pub q4_plus_repair: u64,
}

/// Loaded layer weights for inference. Row caches hold outlier/sparse data; raw
/// `Repair::sparse` is cleared after load to save RAM when caches or fused weights exist.
#[derive(Clone, Debug)]
pub struct LayerRuntime {
    pub q8: Q8Matrix,
    pub repair: Option<Repair>,
    pub outlier_row_cache: OutlierRowCache,
    pub output_bias: Option<Vec<f32>>,
    pub subspace: Option<SubspaceAdapter>,
    pub repair_row_cache: SparseRowCache,
    pub fused_cache: Option<FusedWeightCache>,
}

impl LayerRuntime {
    /// Heap bytes after `load_layer_runtime` (row caches only — raw sparse/outlier vecs dropped).
    pub fn load_bytes(&self) -> u64 {
        let mut bytes = crate::q8::q8_runtime_bytes(&self.q8);
        if let Some(ref repair) = self.repair {
            bytes += crate::repair::repair_low_rank_bytes(repair);
        }
        bytes += crate::repair::sparse_row_cache_bytes(&self.repair_row_cache);
        bytes += crate::repair::outlier_row_cache_bytes(&self.outlier_row_cache);
        if let Some(ref bias) = self.output_bias {
            bytes += bias.len() as u64 * 4;
        }
        if let Some(ref sub) = self.subspace {
            bytes += crate::subspace::subspace_runtime_bytes(sub);
        }
        if let Some(ref fused) = self.fused_cache {
            bytes += crate::matmul::fused_cache_runtime_bytes(fused);
        }
        bytes
    }
}

#[cfg(target_os = "linux")]
pub struct MappedShm {
    pub ptr: *mut u8,
    pub size: usize,
}

#[cfg(target_os = "linux")]
impl MappedShm {
    pub fn new() -> Self {
        Self {
            ptr: std::ptr::null_mut(),
            size: 0,
        }
    }

    pub fn is_mapped(&self) -> bool {
        !self.ptr.is_null()
    }
}

#[cfg(target_os = "linux")]
impl Drop for MappedShm {
    fn drop(&mut self) {
        self.unmap();
    }
}

#[cfg(target_os = "linux")]
impl MappedShm {
    pub fn open(&mut self, name: &str, nbytes: usize) -> crate::Result<()> {
        use crate::error::fail;
        use std::ffi::CString;

        self.unmap();
        if nbytes == 0 {
            return Err(fail("shm map size is zero"));
        }
        let cname = CString::new(name).map_err(|_| fail("shm name contains null byte"))?;
        let fd = unsafe { libc::shm_open(cname.as_ptr(), libc::O_RDWR, 0) };
        if fd < 0 {
            return Err(fail(format!("shm_open failed: {name}")));
        }
        let ptr = unsafe {
            libc::mmap(
                std::ptr::null_mut(),
                nbytes,
                libc::PROT_READ | libc::PROT_WRITE,
                libc::MAP_SHARED,
                fd,
                0,
            )
        };
        unsafe { libc::close(fd) };
        if ptr == libc::MAP_FAILED {
            return Err(fail(format!("mmap failed: {name}")));
        }
        self.ptr = ptr as *mut u8;
        self.size = nbytes;
        Ok(())
    }

    pub fn unmap(&mut self) {
        if self.is_mapped() {
            unsafe {
                libc::munmap(self.ptr as *mut libc::c_void, self.size);
            }
            self.ptr = std::ptr::null_mut();
            self.size = 0;
        }
    }
}
