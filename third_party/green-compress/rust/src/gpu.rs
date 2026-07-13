//! Optional CUDA matmul for fused green weights (build with `--features gpu`).

use std::collections::HashMap;
use std::sync::Arc;

use cudarc::cublas::sys;
use cudarc::cublas::{CudaBlas, Gemm, GemmConfig};
use cudarc::cublaslt::{CudaBlasLT, Matmul, MatmulConfig};
use cudarc::driver::{CudaContext, CudaSlice, CudaStream};
use half::f16;
use rayon::prelude::*;

use crate::error::{fail, Result};
use crate::matmul::build_fused_weight_cache;
use crate::repair::apply_low_rank_repair_matmul;
use crate::types::{FusedWeightCache, LayerRuntime, Matrix, SubspaceAdapter};

pub struct GpuContext {
    pub ctx: Arc<CudaContext>,
    pub stream: Arc<CudaStream>,
    pub blas: CudaBlas,
    /// cuBLASLt handle — used for the f16 (f32-accumulate) low-VRAM matmul path.
    pub blas_lt: CudaBlasLT,
}

impl GpuContext {
    pub fn new(device_id: usize) -> std::result::Result<Self, String> {
        let ctx = CudaContext::new(device_id).map_err(|e| e.to_string())?;
        let stream = ctx.default_stream();
        let blas = CudaBlas::new(stream.clone()).map_err(|e| e.to_string())?;
        let blas_lt = CudaBlasLT::new(stream.clone()).map_err(|e| e.to_string())?;
        Ok(Self {
            ctx,
            stream,
            blas,
            blas_lt,
        })
    }

    pub fn try_default() -> Option<Self> {
        Self::new(0).ok()
    }
}

/// Device-resident fused weights (f32), plus the metadata the GEMM needs so the
/// caller never has to rebuild the CPU-side `FusedWeightCache` on repeat calls.
pub struct GpuFusedWeights {
    weights: CudaSlice<f32>,
    cols: u32,
    row_spin: Vec<f32>,
}

/// Device-resident fused weights as f16 (2 bytes/weight — half the f32 VRAM).
struct GpuFusedF16 {
    weights: CudaSlice<f16>,
    cols: u32,
    row_spin: Vec<f32>,
}

pub struct GpuSession {
    pub ctx: GpuContext,
    fused: HashMap<String, GpuFusedWeights>,
    fused_f16: HashMap<String, GpuFusedF16>,
}

impl GpuSession {
    pub fn try_new() -> Option<Self> {
        GpuContext::try_default().map(|ctx| Self {
            ctx,
            fused: HashMap::new(),
            fused_f16: HashMap::new(),
        })
    }

    pub fn has_fused(&self, key: &str) -> bool {
        self.fused.contains_key(key)
    }

    pub fn has_fused_f16(&self, key: &str) -> bool {
        self.fused_f16.contains_key(key)
    }

    fn ensure_fused(&mut self, key: &str, cache: &FusedWeightCache) -> Result<()> {
        if !self.fused.contains_key(key) {
            let weights = self
                .ctx
                .stream
                .clone_htod(&cache.weights)
                .map_err(|e| fail(e.to_string()))?;
            self.fused.insert(
                key.to_string(),
                GpuFusedWeights {
                    weights,
                    cols: cache.cols,
                    row_spin: cache.row_spin.clone(),
                },
            );
        }
        Ok(())
    }

    /// Upload the fused weights as f16 once (2× less VRAM than the f32 cache).
    fn ensure_fused_f16(&mut self, key: &str, cache: &FusedWeightCache) -> Result<()> {
        if !self.fused_f16.contains_key(key) {
            let half_w: Vec<f16> = cache.weights.par_iter().map(|&w| f16::from_f32(w)).collect();
            let weights = self
                .ctx
                .stream
                .clone_htod(&half_w)
                .map_err(|e| fail(e.to_string()))?;
            self.fused_f16.insert(
                key.to_string(),
                GpuFusedF16 {
                    weights,
                    cols: cache.cols,
                    row_spin: cache.row_spin.clone(),
                },
            );
        }
        Ok(())
    }
}

/// f16-weight GEMM via cuBLASLt with **f32 accumulation** (`CUBLAS_COMPUTE_32F`).
///
/// Weights live in VRAM at 2 bytes/element — half the f32 path — while the dot
/// products still reduce in f32, so the large-K reduction stays accurate. Only
/// the storage/read precision is f16; drift on transformer layers is well inside
/// the 0.5% budget (see the parity test and `docs/BENCHMARK_REPORT.md`).
fn matmul_prepacked_gpu_f16(
    session: &GpuSession,
    cache_key: &str,
    activations: &Matrix,
) -> Result<Matrix> {
    let entry = &session.fused_f16[cache_key];
    let m = activations.rows as usize;
    let k = activations.cols as usize;
    let n = entry.cols as usize;

    let spun = spun_activation_data(activations, &entry.row_spin);
    let a_half: Vec<f16> = spun.par_iter().map(|&v| f16::from_f32(v)).collect();
    let a_dev = session
        .ctx
        .stream
        .clone_htod(&a_half)
        .map_err(|e| fail(e.to_string()))?;
    let mut c_dev = session
        .ctx
        .stream
        .alloc_zeros::<f16>(m * n)
        .map_err(|e| fail(e.to_string()))?;
    let w_dev = &entry.weights;

    // Row-major C[m,n] = A[m,k] @ W[k,n] via the column-major swap (same convention
    // as `gemm_row_major`): compute C^T = W^T·A^T by passing (W, A) with m/n swapped.
    unsafe {
        session
            .ctx
            .blas_lt
            .matmul(
                MatmulConfig {
                    transa: false,
                    transb: false,
                    transc: false,
                    m: n as u64,
                    n: m as u64,
                    k: k as u64,
                    alpha: 1.0,
                    lda: n as i64,
                    ldb: k as i64,
                    beta: 0.0,
                    ldc: n as i64,
                    stride_a: None,
                    stride_b: None,
                    stride_c: None,
                    stride_bias: None,
                    batch_size: None,
                },
                w_dev,
                &a_dev,
                &mut c_dev,
                None,
                None,
            )
            .map_err(|e| fail(e.to_string()))?;
    }

    let c_half = session
        .ctx
        .stream
        .clone_dtoh(&c_dev)
        .map_err(|e| fail(e.to_string()))?;
    let data = c_half.par_iter().map(|&h| h.to_f32()).collect();
    Ok(Matrix {
        rows: activations.rows,
        cols: entry.cols,
        data,
    })
}

/// Row-major GEMM: C[m,n] = A[m,k] @ B[k,n] (matches `crate::matmul::matmul`).
fn gemm_row_major(
    ctx: &GpuContext,
    a: &Matrix,
    b: &Matrix,
) -> Result<Matrix> {
    let m = a.rows as usize;
    let k = a.cols as usize;
    let n = b.cols as usize;
    if a.cols != b.rows {
        return Err(fail("matmul dimension mismatch"));
    }

    let a_dev = ctx
        .stream
        .clone_htod(&a.data)
        .map_err(|e| fail(e.to_string()))?;
    let b_dev = ctx
        .stream
        .clone_htod(&b.data)
        .map_err(|e| fail(e.to_string()))?;
    let mut c_dev = ctx
        .stream
        .alloc_zeros::<f32>(m * n)
        .map_err(|e| fail(e.to_string()))?;

    unsafe {
        ctx.blas
            .gemm(
                GemmConfig {
                    transa: sys::cublasOperation_t::CUBLAS_OP_N,
                    transb: sys::cublasOperation_t::CUBLAS_OP_N,
                    m: n as i32,
                    n: m as i32,
                    k: k as i32,
                    alpha: 1.0,
                    lda: n as i32,
                    ldb: k as i32,
                    beta: 0.0,
                    ldc: n as i32,
                },
                &b_dev,
                &a_dev,
                &mut c_dev,
            )
            .map_err(|e| fail(e.to_string()))?;
    }

    let data = ctx
        .stream
        .clone_dtoh(&c_dev)
        .map_err(|e| fail(e.to_string()))?;
    Ok(Matrix {
        rows: a.rows,
        cols: b.cols,
        data,
    })
}

pub fn matmul_f32_gpu(ctx: &GpuContext, a: &Matrix, b: &Matrix) -> Result<Matrix> {
    gemm_row_major(ctx, a, b)
}

fn apply_output_bias(out: &mut Matrix, output_bias: Option<&[f32]>) {
    if let Some(bias) = output_bias {
        if bias.is_empty() {
            return;
        }
        let cols = out.cols as usize;
        for b in 0..out.rows {
            let obase = b as usize * cols;
            for j in 0..out.cols.min(bias.len() as u32) {
                out.data[obase + j as usize] += bias[j as usize];
            }
        }
    }
}

fn apply_subspace(out: &mut Matrix, activations: &Matrix, subspace: Option<&SubspaceAdapter>) {
    let Some(sub) = subspace else {
        return;
    };
    if sub.rank == 0 {
        return;
    }
    for b in 0..out.rows {
        let mut proj = vec![0.0f32; sub.rank as usize];
        for r in 0..sub.rank {
            let mut sum = 0.0f64;
            for j in 0..sub.in_dim {
                sum += activations.at(b, j) as f64
                    * sub.basis[r as usize * sub.in_dim as usize + j as usize] as f64;
            }
            proj[r as usize] = sum as f32;
        }
        let obase = b as usize * out.cols as usize;
        for j in 0..out.cols.min(sub.out_dim) {
            let mut sum = 0.0f64;
            for r in 0..sub.rank {
                sum += proj[r as usize] as f64
                    * sub.coeff[r as usize * sub.out_dim as usize + j as usize] as f64;
            }
            out.data[obase + j as usize] += sum as f32;
        }
    }
}

fn fused_for_runtime(rt: &LayerRuntime) -> FusedWeightCache {
    if let Some(cache) = rt.fused_cache.as_ref() {
        if cache.weights.len() == rt.q8.rows as usize * rt.q8.cols as usize {
            return cache.clone();
        }
    }
    build_fused_weight_cache(
        &rt.q8,
        if rt.outlier_row_cache.has_rows() {
            Some(&rt.outlier_row_cache)
        } else {
            None
        },
        if rt.repair_row_cache.by_row.is_empty() {
            None
        } else {
            Some(&rt.repair_row_cache)
        },
    )
}

/// Apply per-input-row SpinQuant signs to the activations, matching the CPU
/// `matmul_prepacked` path (`x = act * row_spin[k]`). Returns an owned f32 buffer;
/// with no spin this is just a copy. Without this, the GPU GEMM (which reads the
/// unspun fused weights) produces garbage on any spun layer.
fn spun_activation_data(activations: &Matrix, row_spin: &[f32]) -> Vec<f32> {
    let mut out = activations.data.clone();
    if row_spin.is_empty() {
        return out;
    }
    let k_dim = activations.cols as usize;
    for b in 0..activations.rows as usize {
        let base = b * k_dim;
        for k in 0..k_dim {
            if let Some(&s) = row_spin.get(k) {
                out[base + k] *= s;
            }
        }
    }
    out
}

fn matmul_prepacked_gpu(
    session: &GpuSession,
    cache_key: &str,
    activations: &Matrix,
) -> Result<Matrix> {
    let entry = &session.fused[cache_key];
    let w_dev = &entry.weights;
    let m = activations.rows as usize;
    let k = activations.cols as usize;
    let n = entry.cols as usize;

    let spun = spun_activation_data(activations, &entry.row_spin);
    let a_dev = session
        .ctx
        .stream
        .clone_htod(&spun)
        .map_err(|e| fail(e.to_string()))?;
    let mut c_dev = session
        .ctx
        .stream
        .alloc_zeros::<f32>(m * n)
        .map_err(|e| fail(e.to_string()))?;

    unsafe {
        session
            .ctx
            .blas
            .gemm(
                GemmConfig {
                    transa: sys::cublasOperation_t::CUBLAS_OP_N,
                    transb: sys::cublasOperation_t::CUBLAS_OP_N,
                    m: n as i32,
                    n: m as i32,
                    k: k as i32,
                    alpha: 1.0,
                    lda: n as i32,
                    ldb: k as i32,
                    beta: 0.0,
                    ldc: n as i32,
                },
                w_dev,
                &a_dev,
                &mut c_dev,
            )
            .map_err(|e| fail(e.to_string()))?;
    }

    let data = session
        .ctx
        .stream
        .clone_dtoh(&c_dev)
        .map_err(|e| fail(e.to_string()))?;
    Ok(Matrix {
        rows: activations.rows,
        cols: entry.cols,
        data,
    })
}

pub fn infer_layer_runtime_gpu(
    session: &mut GpuSession,
    cache_key: &str,
    rt: &LayerRuntime,
    activations: &Matrix,
) -> Result<Matrix> {
    if activations.cols != rt.q8.rows {
        return Err(fail("activation cols must match q8 rows"));
    }
    // Default to the f16 (f32-accumulate) path for 2× less VRAM; set
    // GREENCOMPRESS_GPU_F32=1 to force the full-f32 upload.
    let force_f32 = std::env::var("GREENCOMPRESS_GPU_F32")
        .map(|v| v == "1" || v.eq_ignore_ascii_case("true"))
        .unwrap_or(false);
    // Build + upload the fused weights only on the first call for this key.
    // Repeat calls (infer-server, whole-model forward) reuse the device buffer
    // instead of re-dequantizing the whole matrix every time.
    let mut out = if force_f32 {
        if !session.has_fused(cache_key) {
            let fused = fused_for_runtime(rt);
            session.ensure_fused(cache_key, &fused)?;
        }
        matmul_prepacked_gpu(session, cache_key, activations)?
    } else {
        if !session.has_fused_f16(cache_key) {
            let fused = fused_for_runtime(rt);
            session.ensure_fused_f16(cache_key, &fused)?;
        }
        matmul_prepacked_gpu_f16(session, cache_key, activations)?
    };
    if let Some(repair) = rt.repair.as_ref() {
        if !repair.low_rank.is_empty() {
            apply_low_rank_repair_matmul(&mut out, activations, repair, rt.q8.rows, rt.q8.cols);
        }
    }
    apply_output_bias(&mut out, rt.output_bias.as_deref());
    apply_subspace(&mut out, activations, rt.subspace.as_ref());
    Ok(out)
}

pub fn gpu_available() -> bool {
    GpuContext::try_default().is_some()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::Matrix;

    #[test]
    fn gpu_matmul_matches_cpu_when_available() {
        let Some(mut session) = GpuSession::try_new() else {
            eprintln!("skip gpu_matmul_matches_cpu_when_available: no CUDA device");
            return;
        };
        let a = Matrix {
            rows: 4,
            cols: 8,
            data: (0..32).map(|i| (i as f32) * 0.01).collect(),
        };
        let b = Matrix {
            rows: 8,
            cols: 16,
            data: (0..128).map(|i| ((i % 17) as f32) * 0.02).collect(),
        };
        let cpu = crate::matmul::matmul(&a, &b);
        let gpu = matmul_f32_gpu(&session.ctx, &a, &b).expect("gpu matmul");
        let drift = crate::util::rel_l2(&cpu.data, &gpu.data);
        assert!(drift < 1e-3, "gpu/cpu drift {drift}");
        let _ = &mut session;
    }

    #[test]
    fn gpu_f16_prepacked_matches_cpu_when_available() {
        let Some(mut session) = GpuSession::try_new() else {
            eprintln!("skip gpu_f16_prepacked_matches_cpu_when_available: no CUDA device");
            return;
        };
        // Moderate K exercises f32 accumulation over many terms (where naive f16
        // accumulate would drift). Weights stored on-device as f16 (2× less VRAM).
        let k = 512u32;
        let n = 256u32;
        let batch = 4u32;
        let weights: Vec<f32> = (0..(k * n)).map(|i| ((i % 13) as f32 - 6.0) * 0.01).collect();
        let cache = FusedWeightCache {
            rows: k,
            cols: n,
            weights: weights.clone(),
            row_spin: Vec::new(),
        };
        let acts = Matrix {
            rows: batch,
            cols: k,
            data: (0..(batch * k)).map(|i| ((i % 7) as f32 - 3.0) * 0.02).collect(),
        };
        let w_mat = Matrix {
            rows: k,
            cols: n,
            data: weights,
        };
        let cpu = crate::matmul::matmul(&acts, &w_mat);
        session
            .ensure_fused_f16("test_f16", &cache)
            .expect("upload f16 weights");
        let gpu = matmul_prepacked_gpu_f16(&session, "test_f16", &acts).expect("f16 gpu matmul");
        let drift = crate::util::rel_l2(&cpu.data, &gpu.data);
        // f16 storage → ~1e-3 rel error; f32 accumulate keeps it well under budget.
        assert!(drift < 5e-3, "f16 gpu/cpu drift {drift}");
    }
}
