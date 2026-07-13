//! Heterogeneous CPU+GPU execution (KTransformers / Fiddler style). With one GPU and a many-core
//! CPU, the two are the "devices": hot/resident experts compute on the GPU while cold experts
//! compute on the CPU **concurrently**, so a token's decode time is `max(gpu_work, cpu_work)`
//! instead of the serial sum. The engine's job is to pick the split that balances them.

#[derive(Clone, Copy)]
pub struct Hetero {
    pub cpu_ms: f64,      // per-expert compute on CPU (measured, parallel-adjusted)
    pub gpu_ms: f64,      // per-expert compute on GPU
    pub transfer_ms: f64, // per-expert host->device transfer for a non-resident expert
}

impl Hetero {
    /// Split `n` active experts between GPU and CPU running concurrently; returns
    /// (experts_on_gpu, decode_ms) for the balance point that minimizes max(gpu, cpu).
    pub fn optimal_split(&self, n: usize) -> (usize, f64) {
        let mut best = (n, n as f64 * self.gpu_ms);
        for g in 0..=n {
            let c = n - g;
            let t = (g as f64 * self.gpu_ms).max(c as f64 * self.cpu_ms);
            if t < best.1 {
                best = (g, t);
            }
        }
        best
    }

    /// All experts on CPU (no GPU).
    pub fn cpu_only_ms(&self, n: usize) -> f64 {
        n as f64 * self.cpu_ms
    }

    /// GPU does everything, serially streaming the `1-resident_frac` cold experts over PCIe
    /// (the naive offload path the engine improves on).
    pub fn gpu_offload_ms(&self, n: usize, resident_frac: f64) -> f64 {
        let cold = n as f64 * (1.0 - resident_frac);
        let hot = n as f64 * resident_frac;
        hot * self.gpu_ms + cold * (self.transfer_ms + self.gpu_ms)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn concurrent_split_beats_either_device_alone() {
        let h = Hetero { cpu_ms: 0.11, gpu_ms: 0.02, transfer_ms: 0.14 };
        let n = 128;
        let (g, t) = h.optimal_split(n);
        assert!(g > 0 && g < n, "should use both devices, got {g}");
        assert!(t <= h.cpu_only_ms(n), "hetero {t} ≤ cpu-only {}", h.cpu_only_ms(n));
        assert!(t <= n as f64 * h.gpu_ms + 1e-9, "hetero {t} ≤ gpu-compute {}", n as f64 * h.gpu_ms);
    }

    #[test]
    fn concurrent_split_beats_naive_offload() {
        // when cold experts must stream, computing them on CPU concurrently is faster
        let h = Hetero { cpu_ms: 0.11, gpu_ms: 0.02, transfer_ms: 0.40 };
        let (_, t) = h.optimal_split(128);
        assert!(t < h.gpu_offload_ms(128, 0.25), "hetero {t} should beat naive offload {}", h.gpu_offload_ms(128, 0.25));
    }
}
