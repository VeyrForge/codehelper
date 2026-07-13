/* Green Engine — compute-kernel C ABI.
 *
 * This is the contract between the Rust engine (crates/engine-core, `backend::gpu`) and the
 * native expert-compute kernels (CPU reference here, CUDA/HIP/Metal in production). The Rust
 * side declares these exact symbols under `--features gpu`; implement them in C++/CUDA and
 * link the resulting library.
 *
 * All matrices are row-major f32 (the engine dequantizes Q8 weights before the call, or — in a
 * production build — uploads/keeps them device-resident and passes device pointers).
 */
#ifndef GREEN_ENGINE_KERNELS_H
#define GREEN_ENGINE_KERNELS_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Opaque backend context (device handle, streams, weight residency table, ...). */
typedef struct ge_ctx ge_ctx;

/* Create/destroy a backend context. device_id selects the GPU (ignored by the CPU ref). */
ge_ctx *ge_ctx_create(int device_id);
void    ge_ctx_destroy(ge_ctx *ctx);

/* Compute one expert's SwiGLU FFN:  y = down( silu(x @ gate) * (x @ up) ).
 *   gate,up : [hidden*inter] row-major
 *   down    : [inter*hidden] row-major
 *   x       : [hidden]
 *   y       : [hidden]  (output)
 * Returns 0 on success.
 */
int ge_gpu_compute_expert(ge_ctx *ctx,
                          const float *gate, const float *up, const float *down,
                          const float *x, float *y,
                          uint32_t hidden, uint32_t inter);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* GREEN_ENGINE_KERNELS_H */
