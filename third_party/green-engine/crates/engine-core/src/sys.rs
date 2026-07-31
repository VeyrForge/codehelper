//! Hardware detection + backend portability registry.
//!
//! The engine is device-agnostic: the scheduler is pure Rust, and expert compute goes through
//! the `ExpertBackend` trait. This module reports what the current build/host can use, and
//! enumerates the backends the engine is designed to drive (so "works on as many CPUs/GPUs as
//! possible" is explicit, not vague).

use std::sync::OnceLock;

use rayon::ThreadPool;
use rayon::ThreadPoolBuilder;

/// Detected CPU capabilities (used to pick the widest SIMD path; scalar always works).
pub struct CpuInfo {
    /// Logical processors (HT/SMT counted).
    pub cores: usize,
    /// Physical cores (preferred for bandwidth-bound decode GEMV).
    pub physical_cores: usize,
    pub arch: &'static str,
    pub simd: &'static str,
    pub features: Vec<&'static str>,
}

/// Logical parallelism from the OS (includes hyperthreads when enabled).
pub fn logical_cores() -> usize {
    std::thread::available_parallelism()
        .map(|n| n.get())
        .unwrap_or(1)
        .max(1)
}

/// Best-effort physical core count.
///
/// Decode GEMV is memory-bound; HT/SMT siblings usually hurt. Override with `GE_THREADS`.
/// Heuristic: when logical count is even and ≥4, assume 2-way SMT (common on desktop x86).
pub fn physical_cores() -> usize {
    static CACHED: OnceLock<usize> = OnceLock::new();
    *CACHED.get_or_init(|| {
        if let Ok(v) = std::env::var("GE_PHYSICAL_CORES") {
            if let Ok(n) = v.parse::<usize>() {
                return n.max(1);
            }
        }
        let logical = logical_cores();
        #[cfg(target_os = "windows")]
        {
            if let Some(n) = physical_cores_windows() {
                return n.max(1);
            }
        }
        // Fallback: even logical ≥4 → assume SMT on (llama.cpp / OpenBLAS style).
        if logical >= 4 && logical % 2 == 0 {
            logical / 2
        } else {
            logical
        }
    })
}

/// Thread count for decode GEMV.
///
/// Override: `GE_CPU_THREADS` then `GE_THREADS`. With `GE_GAME_MODE=1` and no override,
/// defaults to [`crate::game_mode::DEFAULT_GAME_THREADS`] (low) so games keep CPU headroom.
pub fn decode_threads() -> usize {
    if let Some(n) = crate::game_mode::decode_thread_override() {
        return n;
    }
    if crate::game_mode::enabled() {
        return crate::game_mode::game_decode_threads(physical_cores());
    }
    physical_cores()
}

fn thread_pin_enabled() -> bool {
    match std::env::var("GE_THREAD_PIN") {
        Ok(v) => matches!(v.as_str(), "1" | "true" | "TRUE" | "yes" | "YES"),
        Err(_) => true,
    }
}

#[cfg(target_os = "windows")]
fn pin_current_thread(core_id: usize) {
    extern "system" {
        fn GetCurrentThread() -> isize;
        fn SetThreadAffinityMask(hThread: isize, dwThreadAffinityMask: usize) -> usize;
    }
    let mask = 1usize << (core_id % 64);
    unsafe {
        SetThreadAffinityMask(GetCurrentThread(), mask);
    }
}

#[cfg(not(target_os = "windows"))]
fn pin_current_thread(_core_id: usize) {}

pub fn decode_pool() -> &'static ThreadPool {
    static POOL: OnceLock<ThreadPool> = OnceLock::new();
    POOL.get_or_init(|| {
        let pin = thread_pin_enabled();
        ThreadPoolBuilder::new()
            .num_threads(decode_threads())
            .thread_name(|i| format!("ge-decode-{i}"))
            .start_handler(move |i| {
                if pin {
                    pin_current_thread(i);
                }
            })
            .build()
            .unwrap_or_else(|_| ThreadPoolBuilder::new().num_threads(1).build().unwrap())
    })
}

#[cfg(target_os = "windows")]
fn physical_cores_windows() -> Option<usize> {
    // GetActiveProcessorCount(ALL_PROCESSOR_GROUPS) returns logical count.
    // RelationProcessorCore via GetLogicalProcessorInformationEx is the right API;
    // keep a light heuristic here to avoid extra FFI crates — GE_PHYSICAL_CORES overrides.
    None
}

pub fn detect_cpu() -> CpuInfo {
    let cores = logical_cores();
    let physical_cores = physical_cores();
    let mut features = Vec::new();
    let mut simd = "scalar";

    #[cfg(target_arch = "x86_64")]
    {
        // runtime detection — safe on any x86_64 host
        if std::is_x86_feature_detected!("avx512f") {
            features.push("avx512f");
            simd = "avx512";
        } else if std::is_x86_feature_detected!("avx2") {
            features.push("avx2");
            simd = "avx2";
        } else if std::is_x86_feature_detected!("sse4.2") {
            features.push("sse4.2");
            simd = "sse4.2";
        }
        if std::is_x86_feature_detected!("fma") {
            features.push("fma");
        }
        if std::is_x86_feature_detected!("avxvnni") {
            features.push("avxvnni");
            if simd == "avx2" {
                simd = "avx2+vnni";
            }
        }
        if std::is_x86_feature_detected!("avx512vnni") {
            features.push("avx512vnni");
        }
    }
    #[cfg(target_arch = "aarch64")]
    {
        features.push("neon"); // baseline on aarch64
        simd = "neon";
        if std::arch::is_aarch64_feature_detected!("dotprod") {
            features.push("dotprod");
        }
    }

    let arch = if cfg!(target_arch = "x86_64") {
        "x86_64"
    } else if cfg!(target_arch = "aarch64") {
        "aarch64"
    } else {
        "other"
    };
    CpuInfo {
        cores,
        physical_cores,
        arch,
        simd,
        features,
    }
}

/// Compute backends the engine targets. CPU ships; the rest implement the same C ABI
/// (`crates/kernels`) — `Ggml` is the pragmatic "runs on every vendor" path (it *is* what
/// llama.cpp uses: CUDA, HIP/ROCm, Metal, Vulkan, SYCL, CPU).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BackendKind {
    Cpu,
    Cuda,
    Hip,
    Metal,
    Vulkan,
    Ggml,
}

impl BackendKind {
    pub fn vendor(&self) -> &'static str {
        match self {
            BackendKind::Cpu => "any CPU (x86_64/aarch64)",
            BackendKind::Cuda => "NVIDIA",
            BackendKind::Hip => "AMD (ROCm)",
            BackendKind::Metal => "Apple",
            BackendKind::Vulkan => "any Vulkan GPU",
            BackendKind::Ggml => "all of the above (via ggml)",
        }
    }
}

/// (backend, compiled-in this build, status note).
pub fn available_backends() -> Vec<(BackendKind, bool, &'static str)> {
    let gpu = cfg!(feature = "gpu");
    vec![
        (BackendKind::Cpu, true, "reference impl (Rust); SIMD path selected at runtime"),
        (BackendKind::Cuda, gpu, "via kernels C ABI (expert_cuda.cu) when --features gpu"),
        (BackendKind::Hip, false, "same ABI; HIP kernel (port of expert_cuda.cu) — planned"),
        (BackendKind::Metal, false, "same ABI; Metal shader — planned"),
        (BackendKind::Vulkan, false, "same ABI; Vulkan compute — planned"),
        (BackendKind::Ggml, gpu, "bridge built + verified (expert_ggml.cpp); inherits all ggml vendor backends — recommended"),
    ]
}
