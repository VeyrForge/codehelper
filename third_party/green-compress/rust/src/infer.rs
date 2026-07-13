use std::collections::HashMap;
use std::io::{BufRead, Read, Write};
use std::path::Path;
use std::time::Instant;

use crate::error::{fail, Result};
use crate::io::{decode_f32_le, encode_f32_le, load_matrix, save_fused_cache, save_matrix};
use crate::matmul::{build_fused_weight_cache, matmul, matmul_q8_repaired};
use crate::repair::{build_outlier_row_cache, build_sparse_row_cache};
use crate::types::{LayerRuntime, Matrix, OutlierRowCache};
use crate::backend::{ComputeBackend, GpuSession};
#[cfg(feature = "gpu")]
use crate::gpu;
#[cfg(target_os = "linux")]
use crate::types::MappedShm;

pub fn load_layer_runtime(layer_dir: &Path) -> Result<LayerRuntime> {
    let q8_path = layer_dir.join("w.q8");
    let repair_path = layer_dir.join("w.rep");
    let outlier_path = layer_dir.join("w.out");
    let bias_path = layer_dir.join("w.bias");
    let sub_path = layer_dir.join("w.sub");
    let fused_path = layer_dir.join("w.fcw");

    let q8 = crate::io::load_q8(&q8_path)?;

    let fused_cache = if fused_path.exists() {
        Some(crate::io::load_fused_cache(&fused_path)?)
    } else {
        None
    };

    let mut repair = if repair_path.exists() {
        Some(crate::io::load_repair(&repair_path)?)
    } else {
        None
    };

    let outlier_row_cache = if outlier_path.exists() {
        let outliers = crate::io::load_outliers(&outlier_path)?;
        build_outlier_row_cache(&outliers, q8.cols)
    } else {
        OutlierRowCache::default()
    };

    let output_bias = if bias_path.exists() {
        Some(crate::io::load_output_bias(&bias_path)?)
    } else {
        None
    };

    let subspace = if sub_path.exists() {
        let sub = crate::io::load_subspace_adapter(&sub_path)?;
        if sub.rank > 0 {
            Some(sub)
        } else {
            None
        }
    } else {
        None
    };

    let repair_row_cache = repair
        .as_ref()
        .filter(|r| !r.sparse.is_empty())
        .map(|r| build_sparse_row_cache(r, q8.cols))
        .unwrap_or_default();

    if let Some(ref mut r) = repair {
        if !r.sparse.is_empty() {
            r.sparse.clear();
            r.sparse.shrink_to_fit();
        }
    }

    let fused_cache = if fused_cache.is_none()
        && (repair_row_cache.has_rows() || outlier_row_cache.has_rows())
    {
        Some(build_fused_weight_cache(
            &q8,
            if outlier_row_cache.has_rows() {
                Some(&outlier_row_cache)
            } else {
                None
            },
            if repair_row_cache.has_rows() {
                Some(&repair_row_cache)
            } else {
                None
            },
        ))
    } else {
        fused_cache
    };

    Ok(LayerRuntime {
        q8,
        repair,
        outlier_row_cache,
        output_bias,
        subspace,
        repair_row_cache,
        fused_cache,
    })
}

pub fn infer_layer_runtime(rt: &LayerRuntime, activations: &Matrix) -> Result<Matrix> {
    infer_layer_runtime_with_backend(rt, activations, ComputeBackend::Cpu, None, "")
}

pub fn infer_layer_runtime_with_backend(
    rt: &LayerRuntime,
    activations: &Matrix,
    backend: ComputeBackend,
    gpu: Option<&mut GpuSession>,
    cache_key: &str,
) -> Result<Matrix> {
    #[cfg(feature = "gpu")]
    if matches!(backend, ComputeBackend::Gpu | ComputeBackend::Auto) {
        if let Some(session) = gpu {
            match gpu::infer_layer_runtime_gpu(session, cache_key, rt, activations) {
                Ok(m) => return Ok(m),
                Err(e) if backend == ComputeBackend::Gpu => return Err(e),
                Err(_) => {}
            }
        } else if backend == ComputeBackend::Gpu {
            return Err(fail("GPU backend requested but CUDA session is unavailable"));
        }
    }
    #[cfg(not(feature = "gpu"))]
    if backend != ComputeBackend::Cpu {
        return Err(fail(format!(
            "backend {:?} requested but greencompress was built without --features gpu",
            backend
        )));
    }

    if activations.cols != rt.q8.rows {
        return Err(fail("activation cols must match q8 rows"));
    }
    Ok(matmul_q8_repaired(
        activations,
        &rt.q8,
        rt.repair.as_ref(),
        if rt.repair_row_cache.by_row.is_empty() {
            None
        } else {
            Some(&rt.repair_row_cache)
        },
        if rt.outlier_row_cache.has_rows() {
            Some(&rt.outlier_row_cache)
        } else {
            None
        },
        rt.output_bias.as_deref(),
        rt.subspace.as_ref(),
        rt.fused_cache.as_ref(),
    ))
}

pub fn matmul_fp32_with_backend(
    activations: &Matrix,
    weights: &Matrix,
    backend: ComputeBackend,
    gpu: Option<&mut GpuSession>,
) -> Result<Matrix> {
    #[cfg(feature = "gpu")]
    if matches!(backend, ComputeBackend::Gpu | ComputeBackend::Auto) {
        if let Some(session) = gpu {
            match gpu::matmul_f32_gpu(&session.ctx, activations, weights) {
                Ok(m) => return Ok(m),
                Err(e) if backend == ComputeBackend::Gpu => return Err(e),
                Err(_) => {}
            }
        } else if backend == ComputeBackend::Gpu {
            return Err(fail("GPU backend requested but CUDA session is unavailable"));
        }
    }
    Ok(matmul(activations, weights))
}

fn read_stdin_exact(data: &mut [u8]) -> Result<()> {
    let mut got = 0;
    while got < data.len() {
        let stdin = std::io::stdin();
        let mut handle = stdin.lock();
        let chunk = handle
            .read(&mut data[got..])
            .map_err(|e| fail(format!("stdin read failed: {e}")))?;
        if chunk == 0 {
            return Err(fail("unexpected stdin EOF"));
        }
        got += chunk;
    }
    Ok(())
}

/// Bounded LRU for infer-server: avoids unbounded RAM when many layer dirs are loaded.
struct LayerServerCache {
    max_entries: usize,
    order: Vec<String>,
    layers: HashMap<String, LayerRuntime>,
}

impl LayerServerCache {
    fn new(max_entries: usize) -> Self {
        Self {
            max_entries: max_entries.max(1),
            order: Vec::new(),
            layers: HashMap::new(),
        }
    }

    fn from_env() -> Self {
        let max = std::env::var("GREENCOMPRESS_MAX_LAYERS")
            .ok()
            .and_then(|s| s.parse().ok())
            .unwrap_or(8);
        Self::new(max)
    }

    fn touch(&mut self, key: &str) {
        self.order.retain(|k| k != key);
        self.order.push(key.to_string());
    }

    fn evict(&mut self) {
        while self.layers.len() > self.max_entries {
            let Some(victim) = self.order.first().cloned() else {
                break;
            };
            self.layers.remove(&victim);
            self.order.retain(|k| k != &victim);
        }
    }

    fn get_or_load(&mut self, layer_dir: &str) -> Result<&LayerRuntime> {
        if !self.layers.contains_key(layer_dir) {
            let rt = load_layer_runtime(Path::new(layer_dir))?;
            self.layers.insert(layer_dir.to_string(), rt);
            self.touch(layer_dir);
            self.evict();
        } else {
            self.touch(layer_dir);
        }
        self.layers
            .get(layer_dir)
            .ok_or_else(|| fail(format!("layer evicted under cache limit: {layer_dir}")))
    }
}

#[cfg(target_os = "linux")]
fn matrix_from_shm(shm: &MappedShm, rows: u32, cols: u32) -> Result<Matrix> {
    let nbytes = rows as usize * cols as usize * 4;
    if !shm.is_mapped() || shm.size < nbytes {
        return Err(fail("shm input too small"));
    }
    let mut matrix = crate::io::make_matrix(rows, cols);
    unsafe {
        std::ptr::copy_nonoverlapping(shm.ptr, matrix.data.as_mut_ptr() as *mut u8, nbytes);
    }
    Ok(matrix)
}

#[cfg(target_os = "linux")]
fn write_matrix_to_shm(matrix: &Matrix, shm: &MappedShm) -> Result<()> {
    let nbytes = matrix.data.len() * 4;
    if !shm.is_mapped() || shm.size < nbytes {
        return Err(fail("shm output too small"));
    }
    unsafe {
        std::ptr::copy_nonoverlapping(matrix.data.as_ptr() as *const u8, shm.ptr, nbytes);
    }
    Ok(())
}

pub fn cmd_infer_server() -> Result<()> {
    let mut cache = LayerServerCache::from_env();
    #[cfg(target_os = "linux")]
    let mut shm_cache: HashMap<String, MappedShm> = HashMap::new();

    let mut stdout = std::io::stdout().lock();
    writeln!(stdout, "READY").map_err(|e| fail(e.to_string()))?;
    stdout.flush().map_err(|e| fail(e.to_string()))?;

    let stdin = std::io::stdin();
    let mut lines = stdin.lock().lines();

    while let Some(Ok(line)) = lines.next() {
        if line == "QUIT" || line == "EXIT" {
            break;
        }
        if line == "PRELOAD" {
            let layer_dir = lines
                .next()
                .transpose()
                .map_err(|e| fail(e.to_string()))?
                .ok_or_else(|| fail("unexpected EOF during PRELOAD"))?;
            let rt = cache.get_or_load(&layer_dir)?;
            writeln!(stdout, "OK {} {}", rt.q8.rows, rt.q8.cols)
                .map_err(|e| fail(e.to_string()))?;
            stdout.flush().map_err(|e| fail(e.to_string()))?;
            continue;
        }

        #[cfg(target_os = "linux")]
        if line == "INFER_SHM" {
            let layer_dir = lines
                .next()
                .transpose()
                .map_err(|e| fail(e.to_string()))?
                .ok_or_else(|| fail("unexpected EOF during INFER_SHM"))?;
            let shape_line = lines
                .next()
                .transpose()
                .map_err(|e| fail(e.to_string()))?
                .ok_or_else(|| fail("unexpected EOF during INFER_SHM shape"))?;
            let parts: Vec<&str> = shape_line.split_whitespace().collect();
            if parts.len() < 4 {
                return Err(fail("INFER_SHM bad shape line"));
            }
            let rows: u32 = parts[0].parse().map_err(|_| fail("bad rows"))?;
            let cols: u32 = parts[1].parse().map_err(|_| fail("bad cols"))?;
            let in_shm_name = parts[2];
            let out_shm_name = parts[3];

            let q8_rows = {
                let rt = cache.get_or_load(&layer_dir)?;
                rt.q8.rows
            };
            if cols != q8_rows {
                writeln!(stdout, "ERR shape_mismatch").map_err(|e| fail(e.to_string()))?;
                stdout.flush().map_err(|e| fail(e.to_string()))?;
                continue;
            }

            let out_cols = cache.get_or_load(&layer_dir)?.q8.cols;
            let in_bytes = rows as usize * cols as usize * 4;
            let out_bytes = rows as usize * out_cols as usize * 4;

            {
                let entry = shm_cache.entry(in_shm_name.to_string()).or_insert_with(MappedShm::new);
                if entry.size < in_bytes {
                    entry.open(in_shm_name, in_bytes)?;
                }
            }
            {
                let entry = shm_cache.entry(out_shm_name.to_string()).or_insert_with(MappedShm::new);
                if entry.size < out_bytes {
                    entry.open(out_shm_name, out_bytes)?;
                }
            }

            let in_shm = shm_cache.get(in_shm_name).unwrap();
            let activations = matrix_from_shm(in_shm, rows, cols)?;
            let result = {
                let rt = cache.get_or_load(&layer_dir)?;
                infer_layer_runtime(rt, &activations)?
            };
            let out_shm = shm_cache.get_mut(out_shm_name).unwrap();
            write_matrix_to_shm(&result, out_shm)?;
            writeln!(stdout, "OK {}", result.cols).map_err(|e| fail(e.to_string()))?;
            stdout.flush().map_err(|e| fail(e.to_string()))?;
            continue;
        }

        if line != "INFER" {
            eprintln!("error: unknown command: {line}");
            writeln!(stdout, "ERR unknown_command").map_err(|e| fail(e.to_string()))?;
            stdout.flush().map_err(|e| fail(e.to_string()))?;
            continue;
        }

        let layer_dir = lines
            .next()
            .transpose()
            .map_err(|e| fail(e.to_string()))?
            .ok_or_else(|| fail("unexpected EOF during INFER"))?;
        let shape_line = lines
            .next()
            .transpose()
            .map_err(|e| fail(e.to_string()))?
            .ok_or_else(|| fail("unexpected EOF during INFER shape"))?;
        let shape_parts: Vec<&str> = shape_line.split_whitespace().collect();
        if shape_parts.len() < 2 {
            return Err(fail("INFER bad shape line"));
        }
        let rows: u32 = shape_parts[0].parse().map_err(|_| fail("bad rows"))?;
        let cols: u32 = shape_parts[1].parse().map_err(|_| fail("bad cols"))?;

        let mut activations = crate::io::make_matrix(rows, cols);
        let mut raw = vec![0u8; activations.data.len() * 4];
        read_stdin_exact(&mut raw)?;
        decode_f32_le(&raw, &mut activations.data)?;

        let result = {
            let rt = cache.get_or_load(&layer_dir)?;
            infer_layer_runtime(rt, &activations)?
        };
        writeln!(stdout, "OK {}", result.cols).map_err(|e| fail(e.to_string()))?;
        let out_bytes = encode_f32_le(&result.data);
        stdout
            .write_all(&out_bytes)
            .map_err(|e| fail(e.to_string()))?;
        stdout.flush().map_err(|e| fail(e.to_string()))?;
    }
    Ok(())
}

pub fn cmd_infer(
    layer_dir: &Path,
    activation_path: &Path,
    out_path: Option<&Path>,
    reference_path: Option<&Path>,
    bench_iters: u32,
    backend: ComputeBackend,
) -> Result<()> {
    let activations = load_matrix(activation_path)?;
    let rt = load_layer_runtime(layer_dir)?;
    let cache_key = layer_dir.to_string_lossy().to_string();

    #[cfg(feature = "gpu")]
    let mut gpu_session = if matches!(backend, ComputeBackend::Gpu | ComputeBackend::Auto) {
        GpuSession::try_new()
    } else {
        None
    };

    let mut infer_seconds = 0.0;
    for _ in 0..bench_iters.max(1) {
        let start = Instant::now();
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
        infer_seconds += start.elapsed().as_secs_f64();
    }
    infer_seconds /= bench_iters.max(1) as f64;

    #[cfg(feature = "gpu")]
    let result = infer_layer_runtime_with_backend(
        &rt,
        &activations,
        backend,
        gpu_session.as_mut(),
        &cache_key,
    )?;
    #[cfg(not(feature = "gpu"))]
    let result = infer_layer_runtime_with_backend(&rt, &activations, backend, None, &cache_key)?;

    if let Some(out) = out_path {
        save_matrix(out, &result)?;
    }

    println!("infer_layer_dir {}", layer_dir.display());
    println!("infer_batch {}", activations.rows);
    println!("infer_in_dim {}", rt.q8.rows);
    println!("infer_out_dim {}", rt.q8.cols);
    println!("benchmark_backend {}", backend.as_str());
    println!("speed_inference_ms {:.10}", infer_seconds * 1000.0);

    if let Some(ref_path) = reference_path {
        let reference = load_matrix(ref_path)?;
        let ref_out = matmul(&activations, &reference);
        let drift = crate::util::rel_l2(&ref_out.data, &result.data);
        println!("quality_activation_drift {:.10}", drift);
        println!("quality_accuracy_pct {:.10}", (1.0 - drift).max(0.0) * 100.0);
    }
    Ok(())
}

pub fn cmd_prepack(layer_dir: &Path) -> Result<()> {
    let q8_path = layer_dir.join("w.q8");
    let repair_path = layer_dir.join("w.rep");
    let outlier_path = layer_dir.join("w.out");
    let fused_path = layer_dir.join("w.fcw");

    let q8 = crate::io::load_q8(&q8_path)?;
    let repair = if repair_path.exists() {
        Some(crate::io::load_repair(&repair_path)?)
    } else {
        None
    };
    let outliers = if outlier_path.exists() {
        crate::io::load_outliers(&outlier_path)?
    } else {
        Vec::new()
    };
    let outlier_row_cache = build_outlier_row_cache(&outliers, q8.cols);
    let repair_row_cache = repair
        .as_ref()
        .filter(|r| !r.sparse.is_empty())
        .map(|r| build_sparse_row_cache(r, q8.cols))
        .unwrap_or_default();

    let fused_cache = build_fused_weight_cache(
        &q8,
        if outlier_row_cache.has_rows() {
            Some(&outlier_row_cache)
        } else {
            None
        },
        if repair_row_cache.has_rows() {
            Some(&repair_row_cache)
        } else {
            None
        },
    );
    save_fused_cache(&fused_path, &fused_cache)?;
    let bytes = (fused_cache.weights.len() + fused_cache.row_spin.len()) * 4;
    println!("prepack_fcw {}", fused_path.display());
    println!("prepack_rows {}", fused_cache.rows);
    println!("prepack_cols {}", fused_cache.cols);
    println!("prepack_bytes {bytes}");
    Ok(())
}
