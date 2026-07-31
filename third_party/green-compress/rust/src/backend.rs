/// Compute backend for layer inference / benchmarks.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum ComputeBackend {
    #[default]
    Cpu,
    #[cfg(feature = "gpu")]
    Gpu,
    #[cfg(feature = "gpu")]
    Auto,
}

/// Placeholder when built without `--features gpu`.
#[cfg(not(feature = "gpu"))]
#[derive(Default)]
pub struct GpuSession;

#[cfg(feature = "gpu")]
pub use crate::gpu::GpuSession;

impl ComputeBackend {
    pub fn parse(s: &str) -> Self {
        match s.trim().to_lowercase().as_str() {
            #[cfg(feature = "gpu")]
            "gpu" | "cuda" => ComputeBackend::Gpu,
            #[cfg(feature = "gpu")]
            "auto" => ComputeBackend::Auto,
            _ => ComputeBackend::Cpu,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            ComputeBackend::Cpu => "cpu",
            #[cfg(feature = "gpu")]
            ComputeBackend::Gpu => "gpu",
            #[cfg(feature = "gpu")]
            ComputeBackend::Auto => "auto",
        }
    }
}
