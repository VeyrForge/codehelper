//! Hardware detection + backend portability registry.
//!
//! The engine is device-agnostic: the scheduler is pure Rust, and expert compute goes through
//! the `ExpertBackend` trait. This module reports what the current build/host can use, and
//! enumerates the backends the engine is designed to drive (so "works on as many CPUs/GPUs as
//! possible" is explicit, not vague).

/// Detected CPU capabilities (used to pick the widest SIMD path; scalar always works).
pub struct CpuInfo {
    pub cores: usize,
    pub arch: &'static str,
    pub simd: &'static str,
    pub features: Vec<&'static str>,
}

pub fn detect_cpu() -> CpuInfo {
    let cores = std::thread::available_parallelism().map(|n| n.get()).unwrap_or(1);
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
    CpuInfo { cores, arch, simd, features }
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
