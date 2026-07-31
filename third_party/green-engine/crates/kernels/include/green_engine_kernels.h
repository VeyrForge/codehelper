/* Green Engine — compute-kernel C ABI.
 *
 * This is the contract between the Rust engine (crates/engine-core, `backend::gpu`) and the
 * native expert-compute kernels (CPU reference here, CUDA/HIP/Metal in production). The Rust
 * side declares these exact symbols under `--features gpu`; implement them in C++/CUDA and
 * link the resulting library.
 *
 * All matrices below are row-major f32 (the engine may dequantize Q8 weights before the call, or
 * — in a production build — upload/keep them device-resident and pass device pointers).
 *
 * Green-quant (Q4/Q8 + repair) matvec/matmul without full reconstruct lives in engine-core under
 * `--features green` (wrapping greencompress CPU SIMD/ref). No CUDA Green-quant ABI here until a
 * GPU toolkit is present and a device decode path is added.
 */
#ifndef GREEN_ENGINE_KERNELS_H
#define GREEN_ENGINE_KERNELS_H

#include <stddef.h>
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

/* Dense decode Q4_0 / F32 GEMV (see decode_q4_gemv.cu). Opaque ctx is ge_decode_ctx. */
typedef struct ge_decode_ctx ge_decode_ctx;
ge_decode_ctx *ge_decode_ctx_create(int device_id);
void           ge_decode_ctx_destroy(ge_decode_ctx *ctx);
int            ge_decode_device_count(void);
int            ge_decode_upload(ge_decode_ctx *ctx, const void *host, size_t nbytes, void **d_out);
/* Arena helpers: one cudaMalloc for many tensors (cuts ~1 MiB/alloc WDDM bin waste). */
int            ge_decode_malloc(ge_decode_ctx *ctx, size_t nbytes, void **d_out);
int            ge_decode_upload_into(ge_decode_ctx *ctx, const void *host, size_t nbytes, void *d_dst);
/* Q4_0: host ggml 18B blocks → device split scales[]+qs[] (same footprint, coalesced). */
int            ge_decode_upload_q4_0(ge_decode_ctx *ctx, const void *host, size_t nbytes,
                                    uint32_t in_dim, uint32_t out_dim, void **d_out);
int            ge_decode_upload_q4_0_into(ge_decode_ctx *ctx, const void *host, size_t nbytes,
                                         uint32_t in_dim, uint32_t out_dim, void *d_dst);
void           ge_decode_free(ge_decode_ctx *ctx, void *d);
/* NCU microbench: tag wraps kernel launch inside ge_decode_q4_gemv (see ge_q4_gemv_bench). */
void           ge_decode_set_nvtx_tag(ge_decode_ctx *ctx, const char *tag);
int            ge_decode_q4_gemv(ge_decode_ctx *ctx, const void *d_packed, const float *x_host,
                                 float *y_host, uint32_t in_dim, uint32_t out_dim,
                                 uint32_t ggml_type);

/* Fused QKV: one x upload, three GEMVs, one sync. */
int ge_decode_q4_gemv3(ge_decode_ctx *ctx,
                       const void *d_wq, const void *d_wk, const void *d_wv,
                       const float *x_host,
                       float *q_host, float *k_host, float *v_host,
                       uint32_t in_dim,
                       uint32_t q_out, uint32_t k_out, uint32_t v_out,
                       uint32_t ggml_type);

/* Fused SwiGLU on device: y = down(silu(x@gate)*(x@up)); intermediates stay in VRAM. */
int ge_decode_q4_swiglu(ge_decode_ctx *ctx,
                        const void *d_gate, const void *d_up, const void *d_down,
                        const float *x_host, float *y_host,
                        uint32_t hidden, uint32_t inter, uint32_t ggml_type);

/* lm_head greedy: device GEMV + argmax; writes winning vocab id to *best_out. */
int ge_decode_q4_argmax(ge_decode_ctx *ctx, const void *d_packed, const float *x_host,
                        uint32_t *best_out, uint32_t in_dim, uint32_t out_dim,
                        uint32_t ggml_type);

/* Device-resident KV for GPU attention layers. Call once after weight upload.
 * max_seq = *initial* staging size (M5.4 grow-on-demand). Ceiling via ge_decode_kv_set_grow_cap.
 * kv_dtype: 0 = F16, 1 = Q8 (symmetric K+V), 2 = Q8V4 (K=Q8, V=Q4 packed).
 * Q8V4 V scales are group-wise (GE_KV_Q4_V_GROUP), matching host KV_Q4_V_GROUP. */
#ifndef GE_KV_DTYPE_F16
#define GE_KV_DTYPE_F16 0u
#endif
#ifndef GE_KV_DTYPE_Q8
#define GE_KV_DTYPE_Q8 1u
#endif
#ifndef GE_KV_DTYPE_Q8V4
#define GE_KV_DTYPE_Q8V4 2u
#endif
#ifndef GE_KV_Q4_V_GROUP
#define GE_KV_Q4_V_GROUP 32u
#endif
int  ge_decode_kv_setup(ge_decode_ctx *ctx, uint32_t n_layers, uint32_t max_seq, uint32_t kv_dim,
                        uint32_t kv_dtype);
void ge_decode_kv_set_grow_cap(ge_decode_ctx *ctx, uint32_t grow_cap);
/* MC.2 / StreamingLLM: cap resident KV at hot_cap (sinks ∪ recent). hot_cap=0 disables.
 * Also clamps grow ceiling so peak allocated MiB stays ≈ window. */
void ge_decode_kv_set_window(ge_decode_ctx *ctx, uint32_t hot_cap, uint32_t sinks);
int  ge_decode_kv_ensure(ge_decode_ctx *ctx, uint32_t need_seq);
size_t ge_decode_kv_nbytes(const ge_decode_ctx *ctx);
uint32_t ge_decode_kv_hard_cap(const ge_decode_ctx *ctx);
uint32_t ge_decode_kv_dtype(const ge_decode_ctx *ctx);
uint32_t ge_decode_kv_seq_len(const ge_decode_ctx *ctx);
uint64_t ge_decode_kv_evictions(const ge_decode_ctx *ctx);
void ge_decode_kv_clear(ge_decode_ctx *ctx);
/* Speculative rewind: clamp resident length to new_len (drop suffix). No-op if already ≤. */
void ge_decode_kv_truncate(ge_decode_ctx *ctx, uint32_t new_len);

/* Decode-only CUDA graph arm (call after prefill when GE_CUDA_GRAPH=1). No-op if graphs off. */
void ge_decode_graph_arm(ge_decode_ctx *ctx);

/* Fused QKV GEMV + RoPE (Norm/NeoX) + KV append + GQA attn + WO GEMV.
 * Activations stay on device between QKV and WO (one H2D of x, one D2H of proj).
 * rope_mode: 0 = Norm (Llama GGUF), 1 = NeoX. Supports theta + freq_scale only.
 */
int ge_decode_q4_attn_block(ge_decode_ctx *ctx,
                            const void *d_wq, const void *d_wk, const void *d_wv, const void *d_wo,
                            const float *x_host, float *proj_host,
                            uint32_t layer, uint32_t pos,
                            uint32_t hidden, uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim,
                            float rope_theta, float rope_freq_scale, int rope_mode,
                            uint32_t ggml_type);

/* Device-resident residual across GPU layers (one H2D / one D2H per token). */
int ge_decode_residual_begin(ge_decode_ctx *ctx, const float *residual_host, uint32_t hidden);
int ge_decode_residual_end(ge_decode_ctx *ctx, float *residual_host, uint32_t hidden);
/* Device embed: dequant one Q4_0 emb/lm_head row into d_res (R27.1 / no host GET_ROWS). */
int ge_decode_residual_from_embed(ge_decode_ctx *ctx, const void *d_emb, uint32_t token_id,
                                  uint32_t hidden, uint32_t n_vocab, uint32_t ggml_type);

/* Full dense layer on device residual: RMSNorm→attn→+res→RMSNorm→SwiGLU→+res.
 * Call between residual_begin/end. Norm weights are device f32[hidden]. */
int ge_decode_q4_layer_fused(
    ge_decode_ctx *ctx, const void *d_wq, const void *d_wk, const void *d_wv, const void *d_wo,
    const void *d_gate, const void *d_up, const void *d_down, const float *d_attn_norm,
    const float *d_ffn_norm, uint32_t layer, uint32_t pos, uint32_t hidden, uint32_t inter,
    uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim, float rope_theta,
    float rope_freq_scale, int rope_mode, uint32_t ggml_type, float rms_eps);

/* Greedy token from device residual (optional output RMSNorm + lm_head + argmax). */
int ge_decode_residual_argmax(ge_decode_ctx *ctx, const void *d_lm, const float *d_output_norm,
                              uint32_t *best_out, uint32_t hidden, uint32_t n_vocab,
                              uint32_t ggml_type, float rms_eps);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* GREEN_ENGINE_KERNELS_H */
