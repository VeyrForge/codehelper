# green-engine kernels

Native expert-compute kernels behind the engine's `ExpertBackend` FFI. This is the **C++/CUDA
half** of the engine — Rust owns scheduling/memory; this owns raw expert math.

- `include/green_engine_kernels.h` — the C ABI (the contract). Stable.
- `src/expert_cpu_ref.cpp` — portable C++ reference; matches the Rust `CpuBackend`. Needs no GPU.
- `src/expert_cuda.cu` — CUDA implementation sketch (same ABI). Replace the naive GEMV with
  cuBLAS / a fused SwiGLU kernel for production throughput.

## Build & link

```bash
make cpu          # or: make cuda   (needs nvcc)
GREEN_ENGINE_KERNELS_DIR=$(pwd) LD_LIBRARY_PATH=$(pwd) \
  cargo test -p engine-core --features gpu     # runs the Rust↔native parity test
```

The default `cargo build` (no `--features gpu`) is **CPU-only and needs none of this** — the
toolkit is optional.

## Why this split

`engine-core` decides *which experts are resident and when to fetch them*; these kernels just
*compute one expert*. On a cache **hit** the engine passes a **device pointer** to already-resident
VRAM weights, so there is no per-token weight copy — the offload win. On a **miss** the engine
uploads the expert once (overlapped on a copy stream) before calling in. Keeping the two concerns
separate is what lets the same scheduler drive CPU, CUDA, HIP, or Metal unchanged.
