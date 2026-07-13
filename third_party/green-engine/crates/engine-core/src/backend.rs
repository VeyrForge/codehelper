//! Compute backend abstraction — the clean boundary between the Rust scheduler and the
//! actual expert math. A `CpuBackend` ships here; a GPU/C++ backend implements the same
//! trait over FFI (see the `gpu` feature stub below), so the engine is device-agnostic.

use crate::tensor::swiglu_ffn;
use crate::weights::ExpertWeights;

/// Reusable per-call scratch, so the hot path does not allocate.
pub struct Scratch {
    g: Vec<f32>,    // [inter]
    u: Vec<f32>,    // [inter]
    deq_a: Vec<f32>, // dequant buffer for gate/up [hidden*inter]
    deq_b: Vec<f32>, // dequant buffer for down    [inter*hidden]
}

impl Scratch {
    pub fn new(hidden: usize, inter: usize) -> Self {
        Scratch {
            g: vec![0.0; inter],
            u: vec![0.0; inter],
            deq_a: vec![0.0; hidden * inter],
            deq_b: vec![0.0; inter * hidden],
        }
    }
}

/// Computes one expert's FFN: `out = expert(x)`. Implementations may run on CPU, GPU, etc.
pub trait ExpertBackend {
    fn name(&self) -> &'static str;
    /// `x`:[hidden] -> `out`:[hidden].
    fn compute_expert(&self, w: &ExpertWeights, x: &[f32], scratch: &mut Scratch, out: &mut [f32]);
}

/// Reference CPU backend (pure Rust). Dequantizes on the fly when weights are Q8.
pub struct CpuBackend;

impl ExpertBackend for CpuBackend {
    fn name(&self) -> &'static str {
        "cpu"
    }
    fn compute_expert(&self, w: &ExpertWeights, x: &[f32], s: &mut Scratch, out: &mut [f32]) {
        // SAFETY of borrows: gate/up share deq_a sequentially (each consumed before next).
        let gate = w.gate.as_f32(&mut s.deq_a);
        // gate is borrowed from deq_a; compute g before touching deq_a again
        crate::tensor::matvec(x, gate, w.hidden, w.inter, &mut s.g);
        let up = w.up.as_f32(&mut s.deq_a);
        crate::tensor::matvec(x, up, w.hidden, w.inter, &mut s.u);
        for j in 0..w.inter {
            s.g[j] = crate::tensor::silu(s.g[j]) * s.u[j];
        }
        let down = w.down.as_f32(&mut s.deq_b);
        crate::tensor::matvec(&s.g, down, w.inter, w.hidden, out);
        let _ = swiglu_ffn; // kept as the documented reference path
    }
}

/// Native backend over the kernels C ABI (CPU reference today; CUDA/HIP/Metal in production).
/// Compiled only with `--features gpu`; the default build is CPU-only and needs no toolkit.
#[cfg(feature = "gpu")]
pub mod gpu {
    use super::*;
    use std::os::raw::c_void;

    extern "C" {
        fn ge_ctx_create(device_id: i32) -> *mut c_void;
        fn ge_ctx_destroy(ctx: *mut c_void);
        fn ge_gpu_compute_expert(
            ctx: *mut c_void,
            gate: *const f32,
            up: *const f32,
            down: *const f32,
            x: *const f32,
            y: *mut f32,
            hidden: u32,
            inter: u32,
        ) -> i32;
    }

    /// Owns a kernels context (device handle, streams, weight-residency table).
    ///
    /// The context has mutable device-side state (the residency LRU slots), so concurrent calls
    /// must serialize — the pointer lives behind a `Mutex`. That makes one `GpuBackend` **shareable
    /// across sessions** (`Arc<GpuBackend>`): N chat sessions time-share a single GPU and a single
    /// residency cache, so the working set costs 1× VRAM instead of N×. The GPU is one device, so
    /// serializing the per-expert calls is the correct model anyway.
    pub struct GpuBackend {
        ctx: std::sync::Mutex<*mut c_void>,
    }
    // Safe because every access to the raw ctx pointer goes through the Mutex (see compute_expert),
    // and the kernels context is only ever touched by one thread at a time as a result.
    unsafe impl Send for GpuBackend {}
    unsafe impl Sync for GpuBackend {}
    impl GpuBackend {
        pub fn new(device_id: i32) -> Self {
            GpuBackend { ctx: std::sync::Mutex::new(unsafe { ge_ctx_create(device_id) }) }
        }
    }
    impl Drop for GpuBackend {
        fn drop(&mut self) {
            unsafe { ge_ctx_destroy(*self.ctx.lock().unwrap()) }
        }
    }
    impl ExpertBackend for GpuBackend {
        fn name(&self) -> &'static str {
            "native-ffi"
        }
        fn compute_expert(&self, w: &ExpertWeights, x: &[f32], _s: &mut Scratch, out: &mut [f32]) {
            // For f32 weights, hand the kernel the store's own stable pointers (no copy) so the
            // native backend can key its device-residency cache on them and skip re-uploading a
            // resident expert. Q8 must be dequantized, so it falls back to an owned copy.
            let guard = self.ctx.lock().unwrap(); // held across the call → serializes sessions
            let ctx = *guard;
            let mut call = |gate: *const f32, up: *const f32, down: *const f32| unsafe {
                ge_gpu_compute_expert(
                    ctx,
                    gate,
                    up,
                    down,
                    x.as_ptr(),
                    out.as_mut_ptr(),
                    w.hidden as u32,
                    w.inter as u32,
                );
            };
            match (w.gate.as_f32_borrow(), w.up.as_f32_borrow(), w.down.as_f32_borrow()) {
                (Some(gate), Some(up), Some(down)) => call(gate.as_ptr(), up.as_ptr(), down.as_ptr()),
                _ => {
                    let (gate, up, down) = (w.gate.to_f32_vec(), w.up.to_f32_vec(), w.down.to_f32_vec());
                    call(gate.as_ptr(), up.as_ptr(), down.as_ptr());
                }
            }
        }
    }

    #[cfg(test)]
    mod tests {
        use super::*;
        use crate::weights::WeightStore;
        use std::sync::Arc;

        /// One `GpuBackend` shared by N threads (sessions) over one residency cache: each session's
        /// output must match the CPU reference — proving concurrent sessions share the GPU safely
        /// and at 1× VRAM (one context, not N).
        #[test]
        fn gpu_backend_shared_across_sessions() {
            let (h, i, experts) = (256usize, 128usize, 4u16);
            let store = Arc::new(WeightStore::synthetic(1, experts as usize, h, i, false, 3));
            let backend = Arc::new(GpuBackend::new(0));

            let cpu = CpuBackend;
            let x: Vec<f32> = (0..h).map(|k| (k * 7 % 13) as f32 / 13.0 - 0.5).collect();
            let mut refs = Vec::new();
            for e in 0..experts {
                let (mut y, mut sc) = (vec![0.0f32; h], Scratch::new(h, i));
                cpu.compute_expert(store.get(0, e), &x, &mut sc, &mut y);
                refs.push(y);
            }
            let refs = Arc::new(refs);

            let handles: Vec<_> = (0..experts).map(|e| {
                let (b, st, xx, rf) = (backend.clone(), store.clone(), x.clone(), refs.clone());
                std::thread::spawn(move || {
                    let (mut y, mut sc) = (vec![0.0f32; h], Scratch::new(h, i));
                    for _ in 0..40 {
                        b.compute_expert(st.get(0, e), &xx, &mut sc, &mut y);
                    }
                    rf[e as usize].iter().zip(&y).map(|(a, c)| (a - c).abs()).fold(0.0f32, f32::max)
                })
            }).collect();
            for hd in handles {
                assert!(hd.join().unwrap() < 1e-4, "shared-GPU session output must match CPU");
            }
        }
    }
}
