// Green Engine — CUDA Q4_0 / F32 GEMV for dense decode (native .green path).
//
// Uses synchronous cudaMalloc/cudaMemcpy for weight upload (Windows WDDM reliability).
// Per-token path: async pageable DMA, shared-memory x, warp-split GEMV, fused
// QKV (one H2D / three kernels / one sync) and fused SwiGLU (intermediates stay on device).
// GEMV: warp-cooperative split-K (32 lanes/row) — not naive 1-thread-per-row.
//
// GPU Q4_0 layout: host ggml blocks are 18 bytes (half d + 16 qs). At upload we
// split into contiguous scales[out*n_blocks] + qs[out*n_blocks*16] (same footprint)
// so warp split-K qs loads are 16-byte-strided/int-aligned for DP4A coalescing.
// Optional CUDA graphs via GE_CUDA_GRAPH=1 (default off). WDDM recipe (W5.3 / R11):
// decode-only (arm after prefill) + warmup×2 stream + one capture + replay via
// device token args (no per-token BeginCapture — that path lost on this host).
// Matches llama.cpp #19754 warmup gate; stay opt-in (never WDDM default-on).
#include "green_engine_kernels.h"
#include <cuda_runtime.h>
#include <cuda_fp16.h>
#include <nvtx3/nvToolsExt.h>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <cstdlib>
#include <vector>

struct ge_nvtx_scope {
    bool active;
    ge_nvtx_scope(const char *tag) : active(tag && tag[0]) {
        if (active) nvtxRangePushA(tag);
    }
    ~ge_nvtx_scope() {
        if (active) nvtxRangePop();
    }
};

/* Host-updated each token; baked pointer in captured graph → replay w/o SetParams. */
struct ge_graph_tok_args {
    uint32_t pos;
    uint32_t t;
    uint32_t seq;
};
static constexpr uint32_t GE_GRAPH_WARMUP = 2;

/* Host ggml Q4_0 block size. GPU upload keeps same footprint: scales[] + qs[] split
 * so qs are 16-byte-strided (coalesced) and int-aligned for DP4A — no 18→32 pad tax. */
static constexpr uint32_t GE_Q4_HOST_BLOCK = 18;
static constexpr uint32_t GE_Q4_QS_BYTES = 16;
static constexpr uint32_t GE_MAX_GRAPH_LAYERS = 64;
/* Device KV dtype enums live in green_engine_kernels.h (F16 / Q8 / Q8V4). */

/* Device Q4 matrix: scales[out*n_blocks] then qs[out*n_blocks*16] in one allocation. */
struct ge_q4_gpu_view {
    const half *scales; /* [out_dim * n_blocks] */
    const uint8_t *qs;  /* [out_dim * n_blocks * 16] */
    uint32_t n_blocks;
    uint32_t out_dim;
};

static __host__ __device__ __forceinline__ ge_q4_gpu_view q4_view(const void *base, uint32_t in_dim,
                                                                  uint32_t out_dim) {
    const uint32_t n_blocks = in_dim / 32;
    const half *scales = (const half *)base;
    const uint8_t *qs = (const uint8_t *)(scales + (size_t)out_dim * n_blocks);
    return ge_q4_gpu_view{scales, qs, n_blocks, out_dim};
}

struct ge_layer_graph {
    cudaGraphExec_t exec = nullptr;
    cudaGraphNode_t rope_node = nullptr;
    cudaGraphNode_t attn_node = nullptr;
    const void *d_wq = nullptr;
    const void *d_wk = nullptr;
    const void *d_wv = nullptr;
    const void *d_wo = nullptr;
    const void *d_gate = nullptr;
    const void *d_up = nullptr;
    const void *d_down = nullptr;
    const float *d_attn_norm = nullptr;
    const float *d_ffn_norm = nullptr;
    uint32_t hidden = 0;
    uint32_t inter = 0;
    uint32_t n_heads = 0;
    uint32_t n_kv_heads = 0;
    uint32_t head_dim = 0;
    uint32_t ggml_type = 0;
    float rope_theta = 0.0f;
    float rope_freq_scale = 0.0f;
    float rms_eps = 0.0f;
    int rope_mode = 0;
    uint32_t warmup_hits = 0;
    bool ready = false;
};

struct ge_decode_ctx {
    int device_id;
    cudaStream_t stream;
    float *d_x = nullptr;
    float *d_y = nullptr;
    float *d_y2 = nullptr;
    float *d_y3 = nullptr;
    size_t cap_x = 0;
    size_t cap_y = 0;
    size_t cap_y2 = 0;
    size_t cap_y3 = 0;
    /* Attn fusion scratch (Q/K/V/attn out). */
    float *d_q = nullptr;
    float *d_k = nullptr;
    float *d_v = nullptr;
    float *d_attn = nullptr;
    size_t cap_q = 0;
    size_t cap_k = 0;
    size_t cap_v = 0;
    size_t cap_attn = 0;
    /* Per-layer F16 KV cache (pointers into contiguous arenas — one cudaMalloc each). */
    half **d_k_cache = nullptr;
    half **d_v_cache = nullptr;
    half *d_k_arena = nullptr;
    half *d_v_arena = nullptr;
    /* Symmetric Q8 K+V: per-token absmax scale (half) + i8[kv_dim] (matches CPU Q8LaneStore).
     * Q8V4: same K layout; V qs = packed u8[kv_dim/2]; V scales = half[t*ngroups+g]
     * with GE_KV_Q4_V_GROUP (matches host KV_Q4_V_GROUP). */
    int8_t **d_k_qs = nullptr;
    int8_t **d_v_qs = nullptr;
    half **d_k_scales = nullptr;
    half **d_v_scales = nullptr;
    int8_t *d_k_qs_arena = nullptr;
    int8_t *d_v_qs_arena = nullptr;
    half *d_k_scale_arena = nullptr;
    half *d_v_scale_arena = nullptr;
    uint32_t *kv_len = nullptr; /* host-side per-layer length */
    uint32_t n_kv_layers = 0;
    uint32_t max_seq = 0;
    uint32_t kv_dim = 0;
    uint32_t kv_dtype = GE_KV_DTYPE_F16;
    uint32_t kv_hard_cap = 4096u; /* from device smem; GE_GPU_KV_HARD_CAP overrides */
    uint32_t kv_grow_cap = 0;     /* M5.4 soft ceiling; 0 → use hard_cap */
    /* MC.2 StreamingLLM: sinks ∪ recent window. 0 = disabled (legacy unbounded grow). */
    uint32_t kv_hot_cap = 0;
    uint32_t kv_sinks = 4;
    uint64_t kv_evictions = 0;
    /* One-layer compact scratch (F16 elems or Q8 qs bytes; scales use host staging). */
    void *d_kv_scratch = nullptr;
    size_t kv_scratch_bytes = 0;
    /* Device-resident residual across GPU layers (one H2D / D2H per token). */
    float *d_res = nullptr;
    size_t cap_res = 0;
    /* Once-per-GEMV Q8_1 activations (AoS) — avoid re-quant in every vocab CTA. */
    void *d_q8_act = nullptr;
    size_t cap_q8_act_blocks = 0;
    /* Host-precomputed RoPE inv_freq[head_dim/2] as f64 (matches CPU rope_pair). */
    double *d_inv_freq = nullptr;
    size_t cap_inv_freq = 0;
    uint32_t rope_head_dim = 0;
    float rope_theta = 0.0f;
    /* CUDA graphs for fused layers (WDDM launch amortization). */
    int use_graphs = 0;
    int graphs_armed = 0; /* 0 until ge_decode_graph_arm after prefill */
    /* Only pass d_graph_tok into kernels during capture (pointer baked into graph).
     * Eager launches must use grid args — stale/zero tok overrides poison ids when
     * GE_CUDA_GRAPH=1 but try_graph is idle (W53 LEAVE_OFF "isotope" failure). */
    int use_graph_tok = 0;
    ge_graph_tok_args *d_graph_tok = nullptr;
    ge_layer_graph layer_graphs[GE_MAX_GRAPH_LAYERS];
    /* NCU / NVTX: set via ge_decode_set_nvtx_tag before ge_decode_q4_gemv (kernel launch only). */
    const char *nvtx_tag = nullptr;
};

static void invalidate_layer_graphs(ge_decode_ctx *ctx) {
    if (!ctx) return;
    for (uint32_t i = 0; i < GE_MAX_GRAPH_LAYERS; ++i) {
        ge_layer_graph *lg = &ctx->layer_graphs[i];
        if (lg->exec) {
            cudaGraphExecDestroy(lg->exec);
            lg->exec = nullptr;
        }
        lg->ready = false;
        lg->warmup_hits = 0;
        lg->rope_node = nullptr;
        lg->attn_node = nullptr;
    }
}

static bool set_dev(ge_decode_ctx *ctx) {
    return ctx && cudaSetDevice(ctx->device_id) == cudaSuccess;
}

static void free_kv_locked(ge_decode_ctx *ctx);
static int ensure_kv_cap(ge_decode_ctx *ctx, uint32_t need_seq);

/* Scores live in dynamic smem (seq × f32). Lift past 4096 when the device allows. */
static uint32_t query_kv_hard_cap(int device_id) {
    int smem = 0;
    if (cudaDeviceGetAttribute(&smem, cudaDevAttrMaxSharedMemoryPerBlockOptin, device_id)
            != cudaSuccess
        || smem <= 0) {
        if (cudaDeviceGetAttribute(&smem, cudaDevAttrMaxSharedMemoryPerBlock, device_id)
            != cudaSuccess)
            smem = 48 * 1024;
    }
    /* Prefer default block smem when large enough for 8k (32 KiB) so we skip opt-in. */
    int def_smem = 0;
    if (cudaDeviceGetAttribute(&def_smem, cudaDevAttrMaxSharedMemoryPerBlock, device_id)
            == cudaSuccess
        && def_smem >= (int)(8192u * sizeof(float)) && def_smem < smem) {
        /* Still allow opt-in headroom up to device max, but floor usable at def when tiny. */
    }
    if (smem < (int)sizeof(float)) smem = (int)sizeof(float);
    uint32_t cap = (uint32_t)(smem / (int)sizeof(float));
    if (cap < 4096u) cap = 4096u;
    if (cap > 32768u) cap = 32768u;
    const char *e = std::getenv("GE_GPU_KV_HARD_CAP");
    if (e && e[0]) {
        unsigned long v = strtoul(e, nullptr, 10);
        if (v >= 512ul && v <= 65536ul) cap = (uint32_t)v;
    }
    return cap;
}

static size_t attn_smem_bytes(const ge_decode_ctx *ctx) {
    uint32_t cap = ctx && ctx->max_seq ? ctx->max_seq : 1u;
    uint32_t hard = (ctx && ctx->kv_hard_cap) ? ctx->kv_hard_cap : 4096u;
    if (cap > hard) cap = hard;
    return (size_t)cap * sizeof(float);
}

/* Opt-in dynamic smem when scores exceed the default per-block budget. */
static int ensure_attn_dyn_smem(ge_decode_ctx *ctx, size_t smem) {
    if (!ctx || smem == 0) return 0;
    int def_smem = 48 * 1024;
    cudaDeviceGetAttribute(&def_smem, cudaDevAttrMaxSharedMemoryPerBlock, ctx->device_id);
    if ((int)smem <= def_smem) return 0;
    /* Attributes set below when Q8 kernel is declared — F16 path here; Q8 in launch helpers. */
    return 0;
}

static bool attn_seq_ok(const ge_decode_ctx *ctx, uint32_t seq) {
    return (size_t)seq * sizeof(float) <= attn_smem_bytes(ctx);
}

/* Eager decode: size dynamic smem to *seq* (not max_seq). Launching with 16 KiB when
 * seq≈20–40 (GE_GPU_KV_MAX_SEQ=4096) tanks occupancy for no benefit. CUDA-graph path
 * keeps the fixed max_seq footprint so topology stays stable across tokens. */
static size_t attn_launch_smem(const ge_decode_ctx *ctx, uint32_t seq) {
    size_t cap = attn_smem_bytes(ctx);
    if (ctx && ctx->use_graphs) return cap;
    size_t need = (size_t)seq * sizeof(float);
    return need <= cap ? need : cap;
}

static bool ensure_dev(float **p, size_t *cap, size_t need) {
    if (*cap >= need && *p) return true;
    if (*p) {
        cudaFree(*p);
        *p = nullptr;
        *cap = 0;
    }
    if (need == 0) return true;
    if (cudaMalloc((void **)p, need * sizeof(float)) != cudaSuccess) {
        *p = nullptr;
        *cap = 0;
        return false;
    }
    *cap = need;
    return true;
}

static bool ensure_dev_f64(double **p, size_t *cap, size_t need) {
    if (*cap >= need && *p) return true;
    if (*p) {
        cudaFree(*p);
        *p = nullptr;
        *cap = 0;
    }
    if (need == 0) return true;
    if (cudaMalloc((void **)p, need * sizeof(double)) != cudaSuccess) {
        *p = nullptr;
        *cap = 0;
        return false;
    }
    *cap = need;
    return true;
}

static __device__ __forceinline__ float ggml_f16_to_f32(uint16_t h) {
    return __half2float(__ushort_as_half(h));
}

// llama.cpp mmvq-style decode GEMV:
//   1) Quantize float x → Q8_1 in shared memory (once per block)
//   2) Q4_0 × Q8_1 via __dp4a (integer SIMD), not float MAC
//   3) Adaptive rows/warp: 1 for huge outs (lm_head coalescing), 2/4 for FFN/attn
//   4) Optional fused 2-/3-output kernels (gate+up, QKV) — one x quant, N mats
__device__ __forceinline__ float warp_reduce_sum(float v) {
#pragma unroll
    for (int offset = 16; offset > 0; offset >>= 1)
        v += __shfl_down_sync(0xffffffffu, v, offset);
    return v;
}

/* R17-gate-up-silu-micro: SiLU(g)*u in registers — __expf + __fdividef + __fmul_rn (expert_cuda parity). */
__device__ __forceinline__ float ge_silu_mul_rn(float g, float u) {
    return __fmul_rn(__fmul_rn(g, __fdividef(1.0f, 1.0f + __expf(-g))), u);
}

#ifndef GE_Q4_ROWS_PER_WARP
#define GE_Q4_ROWS_PER_WARP 4
#endif

/* Q8 act for DP4A (1B/3B). f32 d + i32 sumq avoids ggml half2 (d,d*sum) bias. */
struct alignas(8) ge_block_q8_1 {
    float d;
    int sumq;
    int8_t qs[32];
};

/* Wide models (Llama-3.1-8B hidden=4096 / inter=14336): Q8-act + F16 current-KV
 * drift exceeds greedy margin by ~layer 11. Use f32×Q4 for those in_dims.
 * Keep DP4A for Llama-3.2-1B/3B (hidden 2048/3072, inter 8192) — preserves ~294 tok/s.
 * Override: GE_GPU_ACT_F32=0|1. */
#ifndef GE_GPU_F32_ACT_IN_DIM
#define GE_GPU_F32_ACT_IN_DIM 4096u
#endif

static bool gemv_use_f32_act(uint32_t in_dim) {
    const char *e = getenv("GE_GPU_ACT_F32");
    if (e && e[0]) {
        if (e[0] == '0') return false;
        if (e[0] == '1') return true;
    }
    (void)in_dim;
    /* Default Q8 DP4A: with fixed CUDA RoPE, 8B IGNORE_EOS n=16 greedy matches CPU at ~58 tok/s.
     * Auto f32 act on hidden=4096 regressed ~58→~13 tok/s. Opt in: GE_GPU_ACT_F32=1. */
    return false;
}

__device__ __forceinline__ void quantize_q8_1_smem(const float *__restrict__ x, uint32_t in_dim,
                                                   ge_block_q8_1 *__restrict__ out) {
    const uint32_t n_blocks = in_dim / 32;
    const uint32_t warp = threadIdx.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t nwarps = blockDim.x >> 5;
    for (uint32_t b = warp; b < n_blocks; b += nwarps) {
        const float xv = x[b * 32 + lane];
        float amax = fabsf(xv);
#pragma unroll
        for (int offset = 16; offset > 0; offset >>= 1)
            amax = fmaxf(amax, __shfl_xor_sync(0xffffffffu, amax, offset));
        const float d = amax / 127.0f;
        const float id = (d > 1e-12f) ? (1.0f / d) : 0.0f;
        int q = __float2int_rn(xv * id);
        q = max(-127, min(127, q));
        int sumq = q;
#pragma unroll
        for (int offset = 16; offset > 0; offset >>= 1)
            sumq += __shfl_xor_sync(0xffffffffu, sumq, offset);
        out[b].qs[lane] = (int8_t)q;
        if (lane == 0) {
            out[b].d = d;
            out[b].sumq = sumq;
        }
    }
}

/* One 16B load (LD.128) — same math as 4×int, fewer global load ops. */

/* Device-global Q8_1 quant (once per GEMV launch). Same math as smem path. */
__global__ void quantize_q8_1_global_kernel(const float *__restrict__ x, uint32_t in_dim,
                                            ge_block_q8_1 *__restrict__ out) {
    quantize_q8_1_smem(x, in_dim, out);
}

static bool ensure_q8_act(ge_decode_ctx *ctx, uint32_t n_blocks) {
    if (!ctx || n_blocks == 0) return false;
    if (ctx->d_q8_act && ctx->cap_q8_act_blocks >= n_blocks) return true;
    if (ctx->d_q8_act) {
        cudaFree(ctx->d_q8_act);
        ctx->d_q8_act = nullptr;
        ctx->cap_q8_act_blocks = 0;
    }
    if (cudaMalloc(&ctx->d_q8_act, (size_t)n_blocks * sizeof(ge_block_q8_1)) != cudaSuccess) {
        ctx->d_q8_act = nullptr;
        return false;
    }
    ctx->cap_q8_act_blocks = n_blocks;
    return true;
}

static bool launch_quantize_q8_act(ge_decode_ctx *ctx, const float *d_x, uint32_t in_dim) {
    if (!ctx || !d_x || in_dim == 0 || (in_dim % 32u) != 0) return false;
    const uint32_t n_blocks = in_dim / 32u;
    if (!ensure_q8_act(ctx, n_blocks)) return false;
    const int threads = 256;
    quantize_q8_1_global_kernel<<<1, threads, 0, ctx->stream>>>(
        d_x, in_dim, (ge_block_q8_1 *)ctx->d_q8_act);
    return cudaGetLastError() == cudaSuccess;
}

__device__ __forceinline__ float vec_dot_q4_0_q8_1(half d4_h, const uint8_t *__restrict__ qs,
                                                   const ge_block_q8_1 *__restrict__ bq8) {
    const float d4 = __half2float(d4_h);
    const float d8 = bq8->d;
    const int *__restrict__ u = (const int *)bq8->qs;
    const int4 qv = *reinterpret_cast<const int4 *>(qs);
    const int q[4] = {qv.x, qv.y, qv.z, qv.w};
    int sumi = 0;
#pragma unroll
    for (int j = 0; j < 4; j++) {
        const int v = q[j];
        const int v0 = v & 0x0F0F0F0F;
        const int v1 = (v >> 4) & 0x0F0F0F0F;
        sumi = __dp4a(v0, u[j], sumi);
        sumi = __dp4a(v1, u[4 + j], sumi);
    }
    return d4 * d8 * ((float)sumi - 8.0f * (float)bq8->sumq);
}

/* ExL angle: __ldg scale + LDG.128 qs only — same math, no software pipeline. */
__device__ __forceinline__ float vec_dot_q4_0_q8_1_ldg(const half *__restrict__ scale_ptr,
                                                      const uint8_t *__restrict__ qs,
                                                      const ge_block_q8_1 *__restrict__ bq8) {
    const float d4 = __half2float(__ldg(scale_ptr));
    const float d8 = bq8->d;
    const int *__restrict__ u = (const int *)bq8->qs;
    const int4 qv = __ldg(reinterpret_cast<const int4 *>(qs));
    const int q[4] = {qv.x, qv.y, qv.z, qv.w};
    int sumi = 0;
#pragma unroll
    for (int j = 0; j < 4; j++) {
        const int v = q[j];
        const int v0 = v & 0x0F0F0F0F;
        const int v1 = (v >> 4) & 0x0F0F0F0F;
        sumi = __dp4a(v0, u[j], sumi);
        sumi = __dp4a(v1, u[4 + j], sumi);
    }
    return d4 * d8 * ((float)sumi - 8.0f * (float)bq8->sumq);
}

/* R28.4-v3-deep-vocab-bw: deep-vocab bandwidth — ExL shared Q8 act tile + R26.1 named accums
 * + act-reuse across ROWS + LDG.128 weights.
 * Same math as vec_dot_q4_0_q8_1_ldg. Not coalesce outer-row / nwarps / cp.async.
 * Note: L2::evict_first needs v8.b32/v4.b64 on sm_120 — keep 16B via __ldg. */
__device__ __forceinline__ int4 r28_ld_qs_stream(const uint8_t *__restrict__ qs) {
    return __ldg(reinterpret_cast<const int4 *>(qs));
}

__device__ __forceinline__ float r28_dot_q4_q8_pre(
    half d4_h, int4 qv, float d8, int sumq, const int *__restrict__ u) {
    const float d4 = __half2float(d4_h);
    const int q[4] = {qv.x, qv.y, qv.z, qv.w};
    int sumi = 0;
#pragma unroll
    for (int j = 0; j < 4; j++) {
        const int v = q[j];
        const int v0 = v & 0x0F0F0F0F;
        const int v1 = (v >> 4) & 0x0F0F0F0F;
        sumi = __dp4a(v0, u[j], sumi);
        sumi = __dp4a(v1, u[4 + j], sumi);
    }
    return d4 * d8 * ((float)sumi - 8.0f * (float)sumq);
}

template <int ROWS>
__global__ void q4_0_gemv_q8_vocab_bw_kernel(const half *__restrict__ scales,
                                             const uint8_t *__restrict__ qs,
                                             const ge_block_q8_1 *__restrict__ bq8,
                                             float *__restrict__ y, uint32_t in_dim,
                                             uint32_t out_dim, uint32_t n_blocks,
                                             float *__restrict__ res_add) {
    (void)in_dim;
    extern __shared__ char r28_smem_raw[];
    ge_block_q8_1 *sm_q8 = reinterpret_cast<ge_block_q8_1 *>(r28_smem_raw);
    for (uint32_t b = threadIdx.x; b < n_blocks; b += blockDim.x) {
        sm_q8[b] = bq8[b];
    }
    __syncthreads();

    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * (uint32_t)ROWS;
    if (row0 >= out_dim) return;

    if constexpr (ROWS == 1) {
        float s0 = 0.0f;
        for (uint32_t b = lane; b < n_blocks; b += 32) {
            const ge_block_q8_1 &ab = sm_q8[b];
            const float d8 = ab.d;
            const int sumq = ab.sumq;
            const int *__restrict__ u = (const int *)ab.qs;
            const size_t idx = (size_t)row0 * n_blocks + b;
            const half sc = __ldg(scales + idx);
            const int4 qv = r28_ld_qs_stream(qs + idx * GE_Q4_QS_BYTES);
            s0 += r28_dot_q4_q8_pre(sc, qv, d8, sumq, u);
        }
        s0 = warp_reduce_sum(s0);
        if (lane == 0) {
            if (y) y[row0] = s0;
            if (res_add) res_add[row0] += s0;
        }
    } else if constexpr (ROWS == 2) {
        float s0 = 0.0f, s1 = 0.0f;
        const uint32_t o1 = row0 + 1u;
        const bool row1_ok = o1 < out_dim;
        for (uint32_t b = lane; b < n_blocks; b += 32) {
            const ge_block_q8_1 &ab = sm_q8[b];
            const float d8 = ab.d;
            const int sumq = ab.sumq;
            const int *__restrict__ u = (const int *)ab.qs;
            {
                const size_t idx = (size_t)row0 * n_blocks + b;
                const half sc = __ldg(scales + idx);
                const int4 qv = r28_ld_qs_stream(qs + idx * GE_Q4_QS_BYTES);
                s0 += r28_dot_q4_q8_pre(sc, qv, d8, sumq, u);
            }
            if (row1_ok) {
                const size_t idx = (size_t)o1 * n_blocks + b;
                const half sc = __ldg(scales + idx);
                const int4 qv = r28_ld_qs_stream(qs + idx * GE_Q4_QS_BYTES);
                s1 += r28_dot_q4_q8_pre(sc, qv, d8, sumq, u);
            }
        }
        s0 = warp_reduce_sum(s0);
        s1 = warp_reduce_sum(s1);
        if (lane == 0) {
            if (y) y[row0] = s0;
            if (res_add) res_add[row0] += s0;
            if (row1_ok) {
                if (y) y[o1] = s1;
                if (res_add) res_add[o1] += s1;
            }
        }
    } else {
        float s0 = 0.0f, s1 = 0.0f, s2 = 0.0f, s3 = 0.0f;
        const uint32_t o1 = row0 + 1u, o2 = row0 + 2u, o3 = row0 + 3u;
        const bool r1 = o1 < out_dim, r2 = o2 < out_dim, r3 = o3 < out_dim;
        for (uint32_t b = lane; b < n_blocks; b += 32) {
            const ge_block_q8_1 &ab = sm_q8[b];
            const float d8 = ab.d;
            const int sumq = ab.sumq;
            const int *__restrict__ u = (const int *)ab.qs;
            {
                const size_t idx = (size_t)row0 * n_blocks + b;
                s0 += r28_dot_q4_q8_pre(__ldg(scales + idx),
                                        r28_ld_qs_stream(qs + idx * GE_Q4_QS_BYTES), d8, sumq, u);
            }
            if (r1) {
                const size_t idx = (size_t)o1 * n_blocks + b;
                s1 += r28_dot_q4_q8_pre(__ldg(scales + idx),
                                        r28_ld_qs_stream(qs + idx * GE_Q4_QS_BYTES), d8, sumq, u);
            }
            if (r2) {
                const size_t idx = (size_t)o2 * n_blocks + b;
                s2 += r28_dot_q4_q8_pre(__ldg(scales + idx),
                                        r28_ld_qs_stream(qs + idx * GE_Q4_QS_BYTES), d8, sumq, u);
            }
            if (r3) {
                const size_t idx = (size_t)o3 * n_blocks + b;
                s3 += r28_dot_q4_q8_pre(__ldg(scales + idx),
                                        r28_ld_qs_stream(qs + idx * GE_Q4_QS_BYTES), d8, sumq, u);
            }
        }
        s0 = warp_reduce_sum(s0);
        s1 = warp_reduce_sum(s1);
        s2 = warp_reduce_sum(s2);
        s3 = warp_reduce_sum(s3);
        if (lane == 0) {
            if (y) y[row0] = s0;
            if (res_add) res_add[row0] += s0;
            if (r1) {
                if (y) y[o1] = s1;
                if (res_add) res_add[o1] += s1;
            }
            if (r2) {
                if (y) y[o2] = s2;
                if (res_add) res_add[o2] += s2;
            }
            if (r3) {
                if (y) y[o3] = s3;
                if (res_add) res_add[o3] += s3;
            }
        }
    }
}


__device__ __forceinline__ float vec_dot_q4_0_f32(half d4_h, const uint8_t *__restrict__ qs,
                                                  const float *__restrict__ x) {
    const float d4 = __half2float(d4_h);
    float sum = 0.0f;
#pragma unroll
    for (int j = 0; j < 16; j++) {
        const int byte = (int)qs[j];
        sum += (float)((byte & 0x0f) - 8) * x[j];
        sum += (float)(((byte >> 4) & 0x0f) - 8) * x[j + 16];
    }
    return sum * d4;
}

template <int ROWS, bool USE_LDG = false>
__global__ void q4_0_gemv_q8_kernel(const half *__restrict__ scales, const uint8_t *__restrict__ qs,
                                    const ge_block_q8_1 *__restrict__ bq8, float *__restrict__ y,
                                    uint32_t in_dim, uint32_t out_dim, uint32_t n_blocks,
                                    float *__restrict__ res_add) {
    (void)in_dim;
    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * (uint32_t)ROWS;
    if (row0 >= out_dim) return;

    float sums[ROWS];
#pragma unroll
    for (int r = 0; r < ROWS; r++) sums[r] = 0.0f;

    for (uint32_t b = lane; b < n_blocks; b += 32) {
#pragma unroll
        for (int r = 0; r < ROWS; r++) {
            const uint32_t o = row0 + (uint32_t)r;
            if (o >= out_dim) break;
            const size_t idx = (size_t)o * n_blocks + b;
            if constexpr (USE_LDG) {
                sums[r] += vec_dot_q4_0_q8_1_ldg(scales + idx, qs + idx * GE_Q4_QS_BYTES, bq8 + b);
            } else {
                sums[r] += vec_dot_q4_0_q8_1(scales[idx], qs + idx * GE_Q4_QS_BYTES, bq8 + b);
            }
        }
    }
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        const uint32_t o = row0 + (uint32_t)r;
        if (o >= out_dim) break;
        float sum = warp_reduce_sum(sums[r]);
        if (lane == 0) {
            if (y) y[o] = sum;
            if (res_add) res_add[o] += sum;
        }
    }
}

/* Q4_0 x f32 activations (CPU-matching). x from global — L1 reuse across warps. */
template <int ROWS>
__global__ void q4_0_gemv_f32_kernel(const half *__restrict__ scales, const uint8_t *__restrict__ qs,
                                     const float *__restrict__ x, float *__restrict__ y,
                                     uint32_t in_dim, uint32_t out_dim, uint32_t n_blocks,
                                     float *__restrict__ res_add) {
    (void)in_dim;
    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * (uint32_t)ROWS;
    if (row0 >= out_dim) return;

    float sums[ROWS];
#pragma unroll
    for (int r = 0; r < ROWS; r++) sums[r] = 0.0f;

    for (uint32_t b = lane; b < n_blocks; b += 32) {
        const float *xb = x + b * 32;
#pragma unroll
        for (int r = 0; r < ROWS; r++) {
            const uint32_t o = row0 + (uint32_t)r;
            if (o >= out_dim) break;
            const size_t idx = (size_t)o * n_blocks + b;
            sums[r] += vec_dot_q4_0_f32(scales[idx], qs + idx * GE_Q4_QS_BYTES, xb);
        }
    }
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        const uint32_t o = row0 + (uint32_t)r;
        if (o >= out_dim) break;
        float sum = warp_reduce_sum(sums[r]);
        if (lane == 0) {
            if (y) y[o] = sum;
            if (res_add) res_add[o] += sum;
        }
    }
}

template <int ROWS, bool FUSE_SILU>
__global__ void q4_0_gemv2_f32_kernel(const half *__restrict__ s0, const uint8_t *__restrict__ q0,
                                      const half *__restrict__ s1, const uint8_t *__restrict__ q1,
                                      const float *__restrict__ x, float *__restrict__ y0,
                                      float *__restrict__ y1, uint32_t in_dim, uint32_t out_dim,
                                      uint32_t n_blocks) {
    (void)in_dim;
    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * (uint32_t)ROWS;
    if (row0 >= out_dim) return;

    float a0[ROWS], a1[ROWS];
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        a0[r] = 0.0f;
        a1[r] = 0.0f;
    }
    for (uint32_t b = lane; b < n_blocks; b += 32) {
        const float *xb = x + b * 32;
#pragma unroll
        for (int r = 0; r < ROWS; r++) {
            const uint32_t o = row0 + (uint32_t)r;
            if (o >= out_dim) break;
            const size_t idx = (size_t)o * n_blocks + b;
            a0[r] += vec_dot_q4_0_f32(s0[idx], q0 + idx * GE_Q4_QS_BYTES, xb);
            a1[r] += vec_dot_q4_0_f32(s1[idx], q1 + idx * GE_Q4_QS_BYTES, xb);
        }
    }
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        const uint32_t o = row0 + (uint32_t)r;
        if (o >= out_dim) break;
        float ga = warp_reduce_sum(a0[r]);
        float up = warp_reduce_sum(a1[r]);
        if (lane == 0) {
            if (FUSE_SILU) {
                y0[o] = ge_silu_mul_rn(ga, up);
            } else {
                y0[o] = ga;
                y1[o] = up;
            }
        }
    }
}

template <int ROWS>
__global__ void q4_0_gemv3_f32_kernel(const half *__restrict__ sq, const uint8_t *__restrict__ qq,
                                      const half *__restrict__ sk, const uint8_t *__restrict__ qk,
                                      const half *__restrict__ sv, const uint8_t *__restrict__ qv,
                                      const float *__restrict__ x, float *__restrict__ yq,
                                      float *__restrict__ yk, float *__restrict__ yv,
                                      uint32_t in_dim, uint32_t q_out, uint32_t kv_out,
                                      uint32_t n_blocks) {
    (void)in_dim;
    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * (uint32_t)ROWS;
    if (row0 >= q_out) return;

    float tq[ROWS], tk[ROWS], tv[ROWS];
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        tq[r] = 0.0f;
        tk[r] = 0.0f;
        tv[r] = 0.0f;
    }
    for (uint32_t b = lane; b < n_blocks; b += 32) {
        const float *xb = x + b * 32;
#pragma unroll
        for (int r = 0; r < ROWS; r++) {
            const uint32_t o = row0 + (uint32_t)r;
            if (o >= q_out) break;
            const size_t idx = (size_t)o * n_blocks + b;
            tq[r] += vec_dot_q4_0_f32(sq[idx], qq + idx * GE_Q4_QS_BYTES, xb);
            if (o < kv_out) {
                tk[r] += vec_dot_q4_0_f32(sk[idx], qk + idx * GE_Q4_QS_BYTES, xb);
                tv[r] += vec_dot_q4_0_f32(sv[idx], qv + idx * GE_Q4_QS_BYTES, xb);
            }
        }
    }
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        const uint32_t o = row0 + (uint32_t)r;
        if (o >= q_out) break;
        float aq = warp_reduce_sum(tq[r]);
        float ak = warp_reduce_sum(tk[r]);
        float av = warp_reduce_sum(tv[r]);
        if (lane == 0) {
            yq[o] = aq;
            if (o < kv_out) {
                yk[o] = ak;
                yv[o] = av;
            }
        }
    }
}

/* Same x, two weight matrices (gate+up). Optional fused silu(g)*u into y0. */
template <int ROWS, bool FUSE_SILU>
__global__ void q4_0_gemv2_q8_kernel(const half *__restrict__ s0, const uint8_t *__restrict__ q0,
                                     const half *__restrict__ s1, const uint8_t *__restrict__ q1,
                                     const ge_block_q8_1 *__restrict__ bq8, float *__restrict__ y0,
                                     float *__restrict__ y1, uint32_t in_dim, uint32_t out_dim,
                                     uint32_t n_blocks) {
    (void)in_dim;
    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * (uint32_t)ROWS;
    if (row0 >= out_dim) return;

    float a0[ROWS], a1[ROWS];
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        a0[r] = 0.0f;
        a1[r] = 0.0f;
    }
    for (uint32_t b = lane; b < n_blocks; b += 32) {
        const ge_block_q8_1 *qb = bq8 + b;
#pragma unroll
        for (int r = 0; r < ROWS; r++) {
            const uint32_t o = row0 + (uint32_t)r;
            if (o >= out_dim) break;
            const size_t idx = (size_t)o * n_blocks + b;
            a0[r] += vec_dot_q4_0_q8_1(s0[idx], q0 + idx * GE_Q4_QS_BYTES, qb);
            a1[r] += vec_dot_q4_0_q8_1(s1[idx], q1 + idx * GE_Q4_QS_BYTES, qb);
        }
    }
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        const uint32_t o = row0 + (uint32_t)r;
        if (o >= out_dim) break;
        float ga = warp_reduce_sum(a0[r]);
        float up = warp_reduce_sum(a1[r]);
        if (lane == 0) {
            if (FUSE_SILU) {
                y0[o] = ge_silu_mul_rn(ga, up);
            } else {
                y0[o] = ga;
                y1[o] = up;
            }
        }
    }
}

/* Same x, three weight matrices (Q/K/V). K/V may be narrower than Q. */
template <int ROWS>
__global__ void q4_0_gemv3_q8_kernel(const half *__restrict__ sq, const uint8_t *__restrict__ qq,
                                     const half *__restrict__ sk, const uint8_t *__restrict__ qk,
                                     const half *__restrict__ sv, const uint8_t *__restrict__ qv,
                                     const ge_block_q8_1 *__restrict__ bq8, float *__restrict__ yq,
                                     float *__restrict__ yk, float *__restrict__ yv,
                                     uint32_t in_dim, uint32_t q_out, uint32_t kv_out,
                                     uint32_t n_blocks) {
    (void)in_dim;
    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * (uint32_t)ROWS;
    if (row0 >= q_out) return;

    float tq[ROWS], tk[ROWS], tv[ROWS];
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        tq[r] = 0.0f;
        tk[r] = 0.0f;
        tv[r] = 0.0f;
    }
    for (uint32_t b = lane; b < n_blocks; b += 32) {
        const ge_block_q8_1 *qb = bq8 + b;
#pragma unroll
        for (int r = 0; r < ROWS; r++) {
            const uint32_t o = row0 + (uint32_t)r;
            if (o >= q_out) break;
            const size_t idx = (size_t)o * n_blocks + b;
            tq[r] += vec_dot_q4_0_q8_1(sq[idx], qq + idx * GE_Q4_QS_BYTES, qb);
            if (o < kv_out) {
                tk[r] += vec_dot_q4_0_q8_1(sk[idx], qk + idx * GE_Q4_QS_BYTES, qb);
                tv[r] += vec_dot_q4_0_q8_1(sv[idx], qv + idx * GE_Q4_QS_BYTES, qb);
            }
        }
    }
#pragma unroll
    for (int r = 0; r < ROWS; r++) {
        const uint32_t o = row0 + (uint32_t)r;
        if (o >= q_out) break;
        float aq = warp_reduce_sum(tq[r]);
        float ak = warp_reduce_sum(tk[r]);
        float av = warp_reduce_sum(tv[r]);
        if (lane == 0) {
            yq[o] = aq;
            if (o < kv_out) {
                yk[o] = ak;
                yv[o] = av;
            }
        }
    }
}

__global__ void f32_gemv_kernel(const float *__restrict__ W, const float *__restrict__ x,
                                float *__restrict__ y, uint32_t in_dim, uint32_t out_dim) {
    extern __shared__ float sx[];
    const bool use_smem = in_dim * sizeof(float) <= (size_t)48 * 1024;
    if (use_smem) {
        for (uint32_t i = threadIdx.x; i < in_dim; i += blockDim.x) sx[i] = x[i];
        __syncthreads();
    }
    const float *__restrict__ xv = use_smem ? sx : x;

    const uint32_t warps_per_block = blockDim.x >> 5;
    const uint32_t lane = threadIdx.x & 31;
    const uint32_t warp_in_block = threadIdx.x >> 5;
    const uint32_t row0 = (blockIdx.x * warps_per_block + warp_in_block) * GE_Q4_ROWS_PER_WARP;
    if (row0 >= out_dim) return;

    float sums[GE_Q4_ROWS_PER_WARP];
#pragma unroll
    for (int r = 0; r < GE_Q4_ROWS_PER_WARP; r++) sums[r] = 0.0f;
    for (uint32_t i = lane; i < in_dim; i += 32) {
        float xv_i = xv[i];
#pragma unroll
        for (int r = 0; r < GE_Q4_ROWS_PER_WARP; r++) {
            const uint32_t o = row0 + (uint32_t)r;
            if (o >= out_dim) break;
            sums[r] += W[(size_t)o * in_dim + i] * xv_i;
        }
    }
#pragma unroll
    for (int r = 0; r < GE_Q4_ROWS_PER_WARP; r++) {
        const uint32_t o = row0 + (uint32_t)r;
        if (o >= out_dim) break;
        float sum = warp_reduce_sum(sums[r]);
        if (lane == 0) y[o] = sum;
    }
}

__global__ void silu_mul_kernel(float *__restrict__ g, const float *__restrict__ u, uint32_t n) {
    uint32_t i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i >= n) return;
    float v = g[i];
    g[i] = ge_silu_mul_rn(v, u[i]);
}

/* Single-block RMSNorm for hidden dims up to ~16k (Llama 1B/3B/8B). */
__global__ void rmsnorm_kernel(const float *__restrict__ x, const float *__restrict__ w,
                               float *__restrict__ out, uint32_t n, float eps) {
    extern __shared__ float sm[];
    float local = 0.0f;
    for (uint32_t i = threadIdx.x; i < n; i += blockDim.x) local += x[i] * x[i];
    sm[threadIdx.x] = local;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) sm[threadIdx.x] += sm[threadIdx.x + s];
        __syncthreads();
    }
    float inv = rsqrtf(sm[0] / (float)n + eps);
    for (uint32_t i = threadIdx.x; i < n; i += blockDim.x) out[i] = w[i] * (x[i] * inv);
}

__global__ void residual_add_inplace_kernel(float *__restrict__ res, const float *__restrict__ delta,
                                            uint32_t n) {
    uint32_t i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i < n) res[i] += delta[i];
}

static bool launch_rmsnorm(ge_decode_ctx *ctx, const float *d_x, const float *d_w, float *d_out,
                           uint32_t n, float eps) {
    if (n == 0 || n > 16384u) return false;
    const int threads = 256;
    size_t smem = (size_t)threads * sizeof(float);
    rmsnorm_kernel<<<1, threads, smem, ctx->stream>>>(d_x, d_w, d_out, n, eps);
    return cudaGetLastError() == cudaSuccess;
}

static bool launch_residual_add_inplace(ge_decode_ctx *ctx, float *d_res, const float *d_delta,
                                        uint32_t n) {
    const int threads = 256;
    const int blocks = (int)((n + threads - 1) / threads);
    residual_add_inplace_kernel<<<blocks, threads, 0, ctx->stream>>>(d_res, d_delta, n);
    return cudaGetLastError() == cudaSuccess;
}

/* Adaptive tiling: FFN/attn stay 1 row/warp on wide outs for Q4 coalescing.
 * Vocab lm_head (>=32k): batch 2 adjacent rows/warp to amortize act quant + x loads.
 * Override: GE_LM_GEMV_ROWS=1|2|4 (A/B). Not Q8 weight prequant. */
static int gemv_rows_for(uint32_t out_dim) {
    if (out_dim >= 32000u) {
        const char *e = getenv("GE_LM_GEMV_ROWS");
        if (e && e[0] == '1') return 1;
        if (e && e[0] == '4') return 4;
        return 2;
    }
    if (out_dim >= 4096u) return 1;
    if (out_dim >= 1024u) return 2;
    return GE_Q4_ROWS_PER_WARP;
}

static int gemv_threads_for(uint32_t out_dim, bool use_f32) {
    if (out_dim >= 32768u && !use_f32) return 256;
    if (out_dim >= 32768u) return 512;
    return 256;
}

static int launch_gemv_ex(ge_decode_ctx *ctx, const void *d_packed, float *d_x, float *d_y,
                          uint32_t in_dim, uint32_t out_dim, uint32_t ggml_type,
                          float *res_add) {
    ge_nvtx_scope nvtx(ctx ? ctx->nvtx_tag : nullptr);
    const int threads = gemv_threads_for(out_dim, gemv_use_f32_act(in_dim));
    const int rows = (ggml_type == 2) ? gemv_rows_for(out_dim) : GE_Q4_ROWS_PER_WARP;
    const int warps_per_block = threads / 32;
    const uint32_t rows_per_block = (uint32_t)warps_per_block * (uint32_t)rows;
    const int blocks = (int)((out_dim + rows_per_block - 1) / rows_per_block);
    if (ggml_type == 2) {
        if (in_dim % 32 != 0) return -1;
        const uint32_t n_blocks = in_dim / 32;
        ge_q4_gpu_view v = q4_view(d_packed, in_dim, out_dim);
        if (gemv_use_f32_act(in_dim)) {
            if (rows == 1)
                q4_0_gemv_f32_kernel<1><<<blocks, threads, 0, ctx->stream>>>(
                    v.scales, v.qs, d_x, d_y, in_dim, out_dim, n_blocks, res_add);
            else if (rows == 2)
                q4_0_gemv_f32_kernel<2><<<blocks, threads, 0, ctx->stream>>>(
                    v.scales, v.qs, d_x, d_y, in_dim, out_dim, n_blocks, res_add);
            else
                q4_0_gemv_f32_kernel<4><<<blocks, threads, 0, ctx->stream>>>(
                    v.scales, v.qs, d_x, d_y, in_dim, out_dim, n_blocks, res_add);
        } else {
            if (!launch_quantize_q8_act(ctx, d_x, in_dim)) return -1;
            const ge_block_q8_1 *bq8 = (const ge_block_q8_1 *)ctx->d_q8_act;
            /* R28.4-v3-deep-vocab-bw: ExL shared-act + named accums + stream weights (opt-out GE_R28_4=0). */
            if (out_dim >= 32768u) {
                const char *r284 = getenv("GE_R28_4");
                const bool use_v3 = !(r284 && r284[0] == '0');
                if (use_v3) {
                    const size_t smem = (size_t)n_blocks * sizeof(ge_block_q8_1);
                    if (rows == 1)
                        q4_0_gemv_q8_vocab_bw_kernel<1><<<blocks, threads, smem, ctx->stream>>>(
                            v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
                    else if (rows == 2)
                        q4_0_gemv_q8_vocab_bw_kernel<2><<<blocks, threads, smem, ctx->stream>>>(
                            v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
                    else
                        q4_0_gemv_q8_vocab_bw_kernel<4><<<blocks, threads, smem, ctx->stream>>>(
                            v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
                } else if (rows == 1)
                    q4_0_gemv_q8_kernel<1, true><<<blocks, threads, 0, ctx->stream>>>(
                        v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
                else if (rows == 2)
                    q4_0_gemv_q8_kernel<2, true><<<blocks, threads, 0, ctx->stream>>>(
                        v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
                else
                    q4_0_gemv_q8_kernel<4, true><<<blocks, threads, 0, ctx->stream>>>(
                        v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
            } else if (rows == 1)
                q4_0_gemv_q8_kernel<1><<<blocks, threads, 0, ctx->stream>>>(
                    v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
            else if (rows == 2)
                q4_0_gemv_q8_kernel<2><<<blocks, threads, 0, ctx->stream>>>(
                    v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
            else
                q4_0_gemv_q8_kernel<4><<<blocks, threads, 0, ctx->stream>>>(
                    v.scales, v.qs, bq8, d_y, in_dim, out_dim, n_blocks, res_add);
        }
    } else if (ggml_type == 0) {
        if (res_add) return -1;
        size_t smem = (size_t)in_dim * sizeof(float);
        if (smem > (size_t)48 * 1024) smem = 0;
        f32_gemv_kernel<<<blocks, threads, smem, ctx->stream>>>(
            (const float *)d_packed, d_x, d_y, in_dim, out_dim);
    } else {
        return -1;
    }
    return cudaGetLastError() == cudaSuccess ? 0 : -1;
}

static int launch_gemv(ge_decode_ctx *ctx, const void *d_packed, float *d_x, float *d_y,
                       uint32_t in_dim, uint32_t out_dim, uint32_t ggml_type) {
    return launch_gemv_ex(ctx, d_packed, d_x, d_y, in_dim, out_dim, ggml_type, nullptr);
}

static int launch_gemv2(ge_decode_ctx *ctx, const void *d0, const void *d1, float *d_x, float *d_y0,
                        float *d_y1, uint32_t in_dim, uint32_t out_dim, uint32_t ggml_type,
                        bool fuse_silu) {
    if (ggml_type != 2 || in_dim % 32 != 0) {
        if (launch_gemv(ctx, d0, d_x, d_y0, in_dim, out_dim, ggml_type) != 0) return -1;
        if (launch_gemv(ctx, d1, d_x, d_y1, in_dim, out_dim, ggml_type) != 0) return -1;
        if (fuse_silu) {
            const int threads = 256;
            const int blocks = (int)((out_dim + threads - 1) / threads);
            silu_mul_kernel<<<blocks, threads, 0, ctx->stream>>>(d_y0, d_y1, out_dim);
            return cudaGetLastError() == cudaSuccess ? 0 : -1;
        }
        return 0;
    }
    const int threads = (out_dim >= 32768u) ? 512 : 256;
    const int rows = gemv_rows_for(out_dim);
    const int warps_per_block = threads / 32;
    const uint32_t rows_per_block = (uint32_t)warps_per_block * (uint32_t)rows;
    const int blocks = (int)((out_dim + rows_per_block - 1) / rows_per_block);
    const uint32_t n_blocks = in_dim / 32;
    ge_q4_gpu_view v0 = q4_view(d0, in_dim, out_dim);
    ge_q4_gpu_view v1 = q4_view(d1, in_dim, out_dim);
    if (gemv_use_f32_act(in_dim)) {
        if (fuse_silu) {
            if (rows == 1)
                q4_0_gemv2_f32_kernel<1, true><<<blocks, threads, 0, ctx->stream>>>(
                    v0.scales, v0.qs, v1.scales, v1.qs, d_x, d_y0, d_y1, in_dim, out_dim, n_blocks);
            else if (rows == 2)
                q4_0_gemv2_f32_kernel<2, true><<<blocks, threads, 0, ctx->stream>>>(
                    v0.scales, v0.qs, v1.scales, v1.qs, d_x, d_y0, d_y1, in_dim, out_dim, n_blocks);
            else
                q4_0_gemv2_f32_kernel<4, true><<<blocks, threads, 0, ctx->stream>>>(
                    v0.scales, v0.qs, v1.scales, v1.qs, d_x, d_y0, d_y1, in_dim, out_dim, n_blocks);
        } else {
            if (rows == 1)
                q4_0_gemv2_f32_kernel<1, false><<<blocks, threads, 0, ctx->stream>>>(
                    v0.scales, v0.qs, v1.scales, v1.qs, d_x, d_y0, d_y1, in_dim, out_dim, n_blocks);
            else if (rows == 2)
                q4_0_gemv2_f32_kernel<2, false><<<blocks, threads, 0, ctx->stream>>>(
                    v0.scales, v0.qs, v1.scales, v1.qs, d_x, d_y0, d_y1, in_dim, out_dim, n_blocks);
            else
                q4_0_gemv2_f32_kernel<4, false><<<blocks, threads, 0, ctx->stream>>>(
                    v0.scales, v0.qs, v1.scales, v1.qs, d_x, d_y0, d_y1, in_dim, out_dim, n_blocks);
        }
        return cudaGetLastError() == cudaSuccess ? 0 : -1;
    }
    if (!launch_quantize_q8_act(ctx, d_x, in_dim)) return -1;
    const ge_block_q8_1 *bq8 = (const ge_block_q8_1 *)ctx->d_q8_act;
    if (fuse_silu) {
        if (rows == 1)
            q4_0_gemv2_q8_kernel<1, true><<<blocks, threads, 0, ctx->stream>>>(
                v0.scales, v0.qs, v1.scales, v1.qs, bq8, d_y0, d_y1, in_dim, out_dim, n_blocks);
        else if (rows == 2)
            q4_0_gemv2_q8_kernel<2, true><<<blocks, threads, 0, ctx->stream>>>(
                v0.scales, v0.qs, v1.scales, v1.qs, bq8, d_y0, d_y1, in_dim, out_dim, n_blocks);
        else
            q4_0_gemv2_q8_kernel<4, true><<<blocks, threads, 0, ctx->stream>>>(
                v0.scales, v0.qs, v1.scales, v1.qs, bq8, d_y0, d_y1, in_dim, out_dim, n_blocks);
    } else {
        if (rows == 1)
            q4_0_gemv2_q8_kernel<1, false><<<blocks, threads, 0, ctx->stream>>>(
                v0.scales, v0.qs, v1.scales, v1.qs, bq8, d_y0, d_y1, in_dim, out_dim, n_blocks);
        else if (rows == 2)
            q4_0_gemv2_q8_kernel<2, false><<<blocks, threads, 0, ctx->stream>>>(
                v0.scales, v0.qs, v1.scales, v1.qs, bq8, d_y0, d_y1, in_dim, out_dim, n_blocks);
        else
            q4_0_gemv2_q8_kernel<4, false><<<blocks, threads, 0, ctx->stream>>>(
                v0.scales, v0.qs, v1.scales, v1.qs, bq8, d_y0, d_y1, in_dim, out_dim, n_blocks);
    }
    return cudaGetLastError() == cudaSuccess ? 0 : -1;
}

static int launch_gemv3(ge_decode_ctx *ctx, const void *dq, const void *dk, const void *dv,
                        float *d_x, float *d_q, float *d_k, float *d_v, uint32_t in_dim,
                        uint32_t q_out, uint32_t kv_out, uint32_t ggml_type) {
    if (ggml_type != 2 || in_dim % 32 != 0 || kv_out > q_out) {
        if (launch_gemv(ctx, dq, d_x, d_q, in_dim, q_out, ggml_type) != 0) return -1;
        if (launch_gemv(ctx, dk, d_x, d_k, in_dim, kv_out, ggml_type) != 0) return -1;
        return launch_gemv(ctx, dv, d_x, d_v, in_dim, kv_out, ggml_type);
    }
    const int threads = (q_out >= 32768u) ? 512 : 256;
    const int rows = gemv_rows_for(q_out);
    const int warps_per_block = threads / 32;
    const uint32_t rows_per_block = (uint32_t)warps_per_block * (uint32_t)rows;
    const int blocks = (int)((q_out + rows_per_block - 1) / rows_per_block);
    const uint32_t n_blocks = in_dim / 32;
    ge_q4_gpu_view vq = q4_view(dq, in_dim, q_out);
    ge_q4_gpu_view vk = q4_view(dk, in_dim, kv_out);
    ge_q4_gpu_view vv = q4_view(dv, in_dim, kv_out);
    if (gemv_use_f32_act(in_dim)) {
        if (rows == 1)
            q4_0_gemv3_f32_kernel<1><<<blocks, threads, 0, ctx->stream>>>(
                vq.scales, vq.qs, vk.scales, vk.qs, vv.scales, vv.qs, d_x, d_q, d_k, d_v, in_dim,
                q_out, kv_out, n_blocks);
        else if (rows == 2)
            q4_0_gemv3_f32_kernel<2><<<blocks, threads, 0, ctx->stream>>>(
                vq.scales, vq.qs, vk.scales, vk.qs, vv.scales, vv.qs, d_x, d_q, d_k, d_v, in_dim,
                q_out, kv_out, n_blocks);
        else
            q4_0_gemv3_f32_kernel<4><<<blocks, threads, 0, ctx->stream>>>(
                vq.scales, vq.qs, vk.scales, vk.qs, vv.scales, vv.qs, d_x, d_q, d_k, d_v, in_dim,
                q_out, kv_out, n_blocks);
        return cudaGetLastError() == cudaSuccess ? 0 : -1;
    }
    if (!launch_quantize_q8_act(ctx, d_x, in_dim)) return -1;
    const ge_block_q8_1 *bq8 = (const ge_block_q8_1 *)ctx->d_q8_act;
    if (rows == 1)
        q4_0_gemv3_q8_kernel<1><<<blocks, threads, 0, ctx->stream>>>(
            vq.scales, vq.qs, vk.scales, vk.qs, vv.scales, vv.qs, bq8, d_q, d_k, d_v, in_dim,
            q_out, kv_out, n_blocks);
    else if (rows == 2)
        q4_0_gemv3_q8_kernel<2><<<blocks, threads, 0, ctx->stream>>>(
            vq.scales, vq.qs, vk.scales, vk.qs, vv.scales, vv.qs, bq8, d_q, d_k, d_v, in_dim,
            q_out, kv_out, n_blocks);
    else
        q4_0_gemv3_q8_kernel<4><<<blocks, threads, 0, ctx->stream>>>(
            vq.scales, vq.qs, vk.scales, vk.qs, vv.scales, vv.qs, bq8, d_q, d_k, d_v, in_dim,
            q_out, kv_out, n_blocks);
    return cudaGetLastError() == cudaSuccess ? 0 : -1;
}

// Direct pageable H2D (same as pre-fusion path). Avoids an extra host memcpy into
// pinned staging — that copy dominated small-vector transfer cost on this WDDM host.
static bool stage_h2d_x(ge_decode_ctx *ctx, const float *x_host, uint32_t in_dim) {
    if (!ensure_dev(&ctx->d_x, &ctx->cap_x, in_dim)) return false;
    return cudaMemcpyAsync(ctx->d_x, x_host, (size_t)in_dim * sizeof(float),
                           cudaMemcpyHostToDevice, ctx->stream) == cudaSuccess;
}

static bool stage_d2h(ge_decode_ctx *ctx, const float *d_src, float *y_host, uint32_t n) {
    return cudaMemcpyAsync(y_host, d_src, (size_t)n * sizeof(float),
                           cudaMemcpyDeviceToHost, ctx->stream) == cudaSuccess;
}

__global__ void argmax_blocks_kernel(const float *__restrict__ y, uint32_t n,
                                     float *__restrict__ blk_max, uint32_t *__restrict__ blk_idx) {
    extern __shared__ float sm[];
    float *sval = sm;
    uint32_t *sidx = (uint32_t *)(sm + blockDim.x);
    uint32_t i = blockIdx.x * blockDim.x + threadIdx.x;
    float v = (i < n) ? y[i] : -INFINITY;
    uint32_t id = i;
    sval[threadIdx.x] = v;
    sidx[threadIdx.x] = id;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) {
            float ov = sval[threadIdx.x + s];
            uint32_t oi = sidx[threadIdx.x + s];
            if (ov > sval[threadIdx.x] || (ov == sval[threadIdx.x] && oi > sidx[threadIdx.x])) {
                sval[threadIdx.x] = ov;
                sidx[threadIdx.x] = oi;
            }
        }
        __syncthreads();
    }
    if (threadIdx.x == 0) {
        blk_max[blockIdx.x] = sval[0];
        blk_idx[blockIdx.x] = sidx[0];
    }
}

/* Single-block finalize: reduce block maxima → one index (avoids per-token host malloc). */
__global__ void argmax_finalize_kernel(const float *__restrict__ blk_max,
                                       const uint32_t *__restrict__ blk_idx, uint32_t n_blocks,
                                       uint32_t *__restrict__ best_out) {
    extern __shared__ float sm[];
    float *sval = sm;
    uint32_t *sidx = (uint32_t *)(sm + blockDim.x);
    float best = -INFINITY;
    uint32_t best_i = 0;
    for (uint32_t i = threadIdx.x; i < n_blocks; i += blockDim.x) {
        float v = blk_max[i];
        uint32_t id = blk_idx[i];
        if (v > best || (v == best && id > best_i)) {
            best = v;
            best_i = id;
        }
    }
    sval[threadIdx.x] = best;
    sidx[threadIdx.x] = best_i;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) {
            float ov = sval[threadIdx.x + s];
            uint32_t oi = sidx[threadIdx.x + s];
            if (ov > sval[threadIdx.x] || (ov == sval[threadIdx.x] && oi > sidx[threadIdx.x])) {
                sval[threadIdx.x] = ov;
                sidx[threadIdx.x] = oi;
            }
        }
        __syncthreads();
    }
    if (threadIdx.x == 0) best_out[0] = sidx[0];
}

/* GEMV → block-argmax → device finalize → D2H of one u32. */
static int launch_argmax_from_y(ge_decode_ctx *ctx, uint32_t out_dim, uint32_t *best_out) {
    const int threads = 256;
    const int blocks = (int)((out_dim + threads - 1) / threads);
    if (!ensure_dev(&ctx->d_y2, &ctx->cap_y2, (size_t)blocks)) return -1;
    if (!ensure_dev(&ctx->d_y3, &ctx->cap_y3, (size_t)blocks + 1)) return -1;
    size_t smem = threads * (sizeof(float) + sizeof(uint32_t));
    argmax_blocks_kernel<<<blocks, threads, smem, ctx->stream>>>(
        ctx->d_y, out_dim, ctx->d_y2, (uint32_t *)ctx->d_y3);
    if (cudaGetLastError() != cudaSuccess) return -1;
    uint32_t *d_best = (uint32_t *)ctx->d_y3 + blocks;
    argmax_finalize_kernel<<<1, threads, smem, ctx->stream>>>(
        ctx->d_y2, (uint32_t *)ctx->d_y3, (uint32_t)blocks, d_best);
    if (cudaGetLastError() != cudaSuccess) return -1;
    if (cudaMemcpyAsync(best_out, d_best, sizeof(uint32_t), cudaMemcpyDeviceToHost, ctx->stream)
        != cudaSuccess)
        return -1;
    return cudaStreamSynchronize(ctx->stream) == cudaSuccess ? 0 : -1;
}

extern "C" ge_decode_ctx *ge_decode_ctx_create(int device_id) {
    int count = 0;
    if (cudaGetDeviceCount(&count) != cudaSuccess || count == 0) return nullptr;
    if (device_id < 0 || device_id >= count) device_id = 0;
    if (cudaSetDevice(device_id) != cudaSuccess) return nullptr;
    auto *ctx = new ge_decode_ctx();
    ctx->device_id = device_id;
    if (cudaStreamCreateWithFlags(&ctx->stream, cudaStreamNonBlocking) != cudaSuccess) {
        delete ctx;
        return nullptr;
    }
    const char *g = std::getenv("GE_CUDA_GRAPH");
    /* Default OFF. GE_CUDA_GRAPH=1 = decode-only opt-in (arm after prefill + warmup×2). */
    ctx->use_graphs = (g && g[0] == '1' && g[1] == '\0');
    ctx->graphs_armed = 0;
    ctx->use_graph_tok = 0;
    if (ctx->use_graphs) {
        if (cudaMalloc((void **)&ctx->d_graph_tok, sizeof(ge_graph_tok_args)) != cudaSuccess) {
            ctx->d_graph_tok = nullptr;
            ctx->use_graphs = 0;
        } else {
            /* Zero so a mistaken eager override cannot read garbage. */
            cudaMemset(ctx->d_graph_tok, 0, sizeof(ge_graph_tok_args));
        }
    }
    ctx->kv_hard_cap = query_kv_hard_cap(device_id);
    ctx->kv_grow_cap = ctx->kv_hard_cap;
    ctx->kv_dtype = GE_KV_DTYPE_F16;
    return ctx;
}

extern "C" void ge_decode_graph_arm(ge_decode_ctx *ctx) {
    if (ctx && ctx->use_graphs) ctx->graphs_armed = 1;
}

extern "C" void ge_decode_ctx_destroy(ge_decode_ctx *ctx) {
    if (!ctx) return;
    if (!set_dev(ctx)) {
        delete ctx;
        return;
    }
    invalidate_layer_graphs(ctx);
    if (ctx->d_graph_tok) {
        cudaFree(ctx->d_graph_tok);
        ctx->d_graph_tok = nullptr;
    }
    if (ctx->d_x) cudaFree(ctx->d_x);
    if (ctx->d_y) cudaFree(ctx->d_y);
    if (ctx->d_y2) cudaFree(ctx->d_y2);
    if (ctx->d_y3) cudaFree(ctx->d_y3);
    if (ctx->d_q) cudaFree(ctx->d_q);
    if (ctx->d_k) cudaFree(ctx->d_k);
    if (ctx->d_v) cudaFree(ctx->d_v);
    if (ctx->d_attn) cudaFree(ctx->d_attn);
    if (ctx->d_res) cudaFree(ctx->d_res);
    if (ctx->d_q8_act) cudaFree(ctx->d_q8_act);
    if (ctx->d_inv_freq) cudaFree(ctx->d_inv_freq);
    free_kv_locked(ctx);
    cudaStreamSynchronize(ctx->stream);
    cudaStreamDestroy(ctx->stream);
    delete ctx;
}

extern "C" int ge_decode_device_count() {
    int n = 0;
    return cudaGetDeviceCount(&n) == cudaSuccess ? n : 0;
}

/* Q4_0 host (18B ggml blocks) → GPU split layout: half scales[N] + uint8 qs[N*16] (same bytes). */
static int upload_q4_0_repack(const void *host, size_t nbytes, uint32_t in_dim, uint32_t out_dim,
                              void *d_dst) {
    if (!host || !d_dst || nbytes == 0 || in_dim == 0 || out_dim == 0) return -1;
    if (in_dim % 32 != 0) return -1;
    const uint32_t n_blocks = in_dim / 32;
    const size_t n_total = (size_t)out_dim * (size_t)n_blocks;
    const size_t expect = n_total * (size_t)GE_Q4_HOST_BLOCK;
    if (nbytes != expect) return -1;

    const size_t scales_bytes = n_total * sizeof(half);
    const size_t qs_bytes = n_total * (size_t)GE_Q4_QS_BYTES;
    const size_t gpu_bytes = scales_bytes + qs_bytes; /* == expect */
    std::vector<uint8_t> staged;
    try {
        staged.resize(gpu_bytes);
    } catch (...) {
        return -1;
    }
    half *scales = (half *)staged.data();
    uint8_t *qs = staged.data() + scales_bytes;
    const uint8_t *src = (const uint8_t *)host;
    for (size_t i = 0; i < n_total; ++i) {
        const uint8_t *b = src + i * GE_Q4_HOST_BLOCK;
        memcpy(&scales[i], b, sizeof(half));
        memcpy(qs + i * GE_Q4_QS_BYTES, b + 2, GE_Q4_QS_BYTES);
    }
    if (cudaMemcpy(d_dst, staged.data(), gpu_bytes, cudaMemcpyHostToDevice) != cudaSuccess)
        return -1;
    return 0;
}

extern "C" int ge_decode_malloc(ge_decode_ctx *ctx, size_t nbytes, void **d_out) {
    if (!ctx || !d_out || nbytes == 0) return -1;
    if (!set_dev(ctx)) return -1;
    void *d = nullptr;
    if (cudaMalloc(&d, nbytes) != cudaSuccess) return -1;
    *d_out = d;
    return 0;
}

extern "C" int ge_decode_upload_into(ge_decode_ctx *ctx, const void *host, size_t nbytes,
                                     void *d_dst) {
    if (!ctx || !host || !d_dst || nbytes == 0) return -1;
    if (!set_dev(ctx)) return -1;
    return cudaMemcpy(d_dst, host, nbytes, cudaMemcpyHostToDevice) == cudaSuccess ? 0 : -1;
}

extern "C" int ge_decode_upload_q4_0_into(ge_decode_ctx *ctx, const void *host, size_t nbytes,
                                          uint32_t in_dim, uint32_t out_dim, void *d_dst) {
    if (!ctx || !d_dst) return -1;
    if (!set_dev(ctx)) return -1;
    return upload_q4_0_repack(host, nbytes, in_dim, out_dim, d_dst);
}

extern "C" int ge_decode_upload(ge_decode_ctx *ctx, const void *host, size_t nbytes, void **d_out) {
    if (!ctx || !host || !d_out || nbytes == 0) return -1;
    if (!set_dev(ctx)) return -1;
    void *d = nullptr;
    if (cudaMalloc(&d, nbytes) != cudaSuccess) return -1;
    if (cudaMemcpy(d, host, nbytes, cudaMemcpyHostToDevice) != cudaSuccess) {
        cudaFree(d);
        return -1;
    }
    *d_out = d;
    return 0;
}

/* Q4_0 host (18B ggml blocks) → GPU split layout: half scales[N] + uint8 qs[N*16] (same bytes). */
extern "C" int ge_decode_upload_q4_0(ge_decode_ctx *ctx, const void *host, size_t nbytes,
                                     uint32_t in_dim, uint32_t out_dim, void **d_out) {
    if (!ctx || !d_out) return -1;
    if (!set_dev(ctx)) return -1;
    if (in_dim == 0 || out_dim == 0 || in_dim % 32 != 0) return -1;
    const uint32_t n_blocks = in_dim / 32;
    const size_t n_total = (size_t)out_dim * (size_t)n_blocks;
    const size_t expect = n_total * (size_t)GE_Q4_HOST_BLOCK;
    if (nbytes != expect) return -1;
    void *d = nullptr;
    if (cudaMalloc(&d, expect) != cudaSuccess) return -1;
    if (upload_q4_0_repack(host, nbytes, in_dim, out_dim, d) != 0) {
        cudaFree(d);
        return -1;
    }
    *d_out = d;
    return 0;
}

extern "C" void ge_decode_free(ge_decode_ctx *ctx, void *d) {
    if (!ctx || !d) return;
    if (!set_dev(ctx)) return;
    cudaFree(d);
}

extern "C" void ge_decode_set_nvtx_tag(ge_decode_ctx *ctx, const char *tag) {
    if (ctx) ctx->nvtx_tag = tag;
}

extern "C" int ge_decode_q4_gemv(ge_decode_ctx *ctx, const void *d_packed, const float *x_host,
                                 float *y_host, uint32_t in_dim, uint32_t out_dim,
                                 uint32_t ggml_type) {
    if (!ctx || !d_packed || !x_host || !y_host || in_dim == 0 || out_dim == 0) return -1;
    if (!set_dev(ctx)) return -1;
    if (!stage_h2d_x(ctx, x_host, in_dim)) return -1;
    if (!ensure_dev(&ctx->d_y, &ctx->cap_y, out_dim)) return -1;
    if (launch_gemv(ctx, d_packed, ctx->d_x, ctx->d_y, in_dim, out_dim, ggml_type) != 0) return -1;
    if (!stage_d2h(ctx, ctx->d_y, y_host, out_dim)) return -1;
    return cudaStreamSynchronize(ctx->stream) == cudaSuccess ? 0 : -1;
}

// lm_head greedy: GEMV then on-device block-argmax; returns only winning vocab id.
extern "C" int ge_decode_q4_argmax(ge_decode_ctx *ctx, const void *d_packed, const float *x_host,
                                    uint32_t *best_out, uint32_t in_dim, uint32_t out_dim,
                                    uint32_t ggml_type) {
    if (!ctx || !d_packed || !x_host || !best_out || in_dim == 0 || out_dim == 0) return -1;
    if (!set_dev(ctx)) return -1;
    if (!stage_h2d_x(ctx, x_host, in_dim)) return -1;
    if (!ensure_dev(&ctx->d_y, &ctx->cap_y, out_dim)) return -1;
    if (launch_gemv(ctx, d_packed, ctx->d_x, ctx->d_y, in_dim, out_dim, ggml_type) != 0) return -1;
    return launch_argmax_from_y(ctx, out_dim, best_out);
}


// Fused QKV: upload x once, three GEMVs, one sync. Cuts 3→1 H2D and 3→1 stream syncs.
extern "C" int ge_decode_q4_gemv3(ge_decode_ctx *ctx,
                                  const void *d_wq, const void *d_wk, const void *d_wv,
                                  const float *x_host,
                                  float *q_host, float *k_host, float *v_host,
                                  uint32_t in_dim,
                                  uint32_t q_out, uint32_t k_out, uint32_t v_out,
                                  uint32_t ggml_type) {
    if (!ctx || !d_wq || !d_wk || !d_wv || !x_host || !q_host || !k_host || !v_host) return -1;
    if (in_dim == 0 || q_out == 0 || k_out == 0 || v_out == 0) return -1;
    if (!set_dev(ctx)) return -1;
    if (!stage_h2d_x(ctx, x_host, in_dim)) return -1;
    if (!ensure_dev(&ctx->d_y, &ctx->cap_y, q_out)) return -1;
    if (!ensure_dev(&ctx->d_y2, &ctx->cap_y2, k_out)) return -1;
    if (!ensure_dev(&ctx->d_y3, &ctx->cap_y3, v_out)) return -1;
    if (launch_gemv3(ctx, d_wq, d_wk, d_wv, ctx->d_x, ctx->d_y, ctx->d_y2, ctx->d_y3, in_dim, q_out,
                     k_out, ggml_type)
        != 0)
        return -1;
    if (!stage_d2h(ctx, ctx->d_y, q_host, q_out)) return -1;
    if (!stage_d2h(ctx, ctx->d_y2, k_host, k_out)) return -1;
    if (!stage_d2h(ctx, ctx->d_y3, v_host, v_out)) return -1;
    return cudaStreamSynchronize(ctx->stream) == cudaSuccess ? 0 : -1;
}

// Fused SwiGLU: gate+up+silu* + down on device; only x H2D and y D2H (no inter host round-trip).
extern "C" int ge_decode_q4_swiglu(ge_decode_ctx *ctx,
                                   const void *d_gate, const void *d_up, const void *d_down,
                                   const float *x_host, float *y_host,
                                   uint32_t hidden, uint32_t inter, uint32_t ggml_type) {
    if (!ctx || !d_gate || !d_up || !d_down || !x_host || !y_host) return -1;
    if (hidden == 0 || inter == 0) return -1;
    if (!set_dev(ctx)) return -1;
    if (!stage_h2d_x(ctx, x_host, hidden)) return -1;
    if (!ensure_dev(&ctx->d_y2, &ctx->cap_y2, inter)) return -1; // gate / activated
    if (!ensure_dev(&ctx->d_y3, &ctx->cap_y3, inter)) return -1; // up
    if (!ensure_dev(&ctx->d_y, &ctx->cap_y, hidden)) return -1;  // down out
    if (launch_gemv2(ctx, d_gate, d_up, ctx->d_x, ctx->d_y2, ctx->d_y3, hidden, inter, ggml_type,
                     true)
        != 0)
        return -1;
    if (launch_gemv(ctx, d_down, ctx->d_y2, ctx->d_y, inter, hidden, ggml_type) != 0) return -1;
    if (!stage_d2h(ctx, ctx->d_y, y_host, hidden)) return -1;
    return cudaStreamSynchronize(ctx->stream) == cudaSuccess ? 0 : -1;
}

// ---- RoPE + KV + GQA attention (decode) --------------------------------------------------------

/* Match engine-core `rope::rope_pair`: f64 inv_freq + f64 angle, cast cos/sin to f32. */
__device__ __forceinline__ void rope_cs_table(uint32_t pos, uint32_t i, float freq_scale,
                                              const double *__restrict__ inv_freq, float *c_out,
                                              float *s_out) {
    const double angle = (double)pos * (double)freq_scale * inv_freq[i];
    *c_out = (float)cos(angle);
    *s_out = (float)sin(angle);
}

static int ensure_rope_inv_freq(ge_decode_ctx *ctx, float theta, uint32_t head_dim) {
    if (!ctx || head_dim == 0 || (head_dim & 1u) != 0) return -1;
    const uint32_t half = head_dim / 2;
    if (ctx->d_inv_freq && ctx->rope_head_dim == head_dim && ctx->rope_theta == theta) return 0;
    if (!ensure_dev_f64(&ctx->d_inv_freq, &ctx->cap_inv_freq, half)) return -1;
    std::vector<double> host(half);
    const double dim = (double)head_dim;
    const double th = (double)theta;
    for (uint32_t i = 0; i < half; ++i) {
        host[i] = 1.0 / pow(th, (2.0 * (double)i) / dim);
    }
    if (cudaMemcpyAsync(ctx->d_inv_freq, host.data(), (size_t)half * sizeof(double),
                        cudaMemcpyHostToDevice, ctx->stream) != cudaSuccess)
        return -1;
    ctx->rope_head_dim = head_dim;
    ctx->rope_theta = theta;
    return 0;
}

/* FP32 QK / VKQ accumulate (llama.cpp FA practice — avoid FP16 MAC drift on long heads). */
__device__ __forceinline__ float dot_qk_f32_f32(const float *__restrict__ q,
                                               const float *__restrict__ k, uint32_t head_dim) {
    float dot = 0.0f;
    uint32_t d = 0;
    for (; d + 3 < head_dim; d += 4) {
        dot = __fmaf_rn(q[d], k[d], dot);
        dot = __fmaf_rn(q[d + 1], k[d + 1], dot);
        dot = __fmaf_rn(q[d + 2], k[d + 2], dot);
        dot = __fmaf_rn(q[d + 3], k[d + 3], dot);
    }
    for (; d < head_dim; ++d) dot = __fmaf_rn(q[d], k[d], dot);
    return dot;
}

__device__ __forceinline__ float dot_qk_f32_f16(const float *__restrict__ q,
                                               const half *__restrict__ k, uint32_t head_dim) {
    float dot = 0.0f;
    uint32_t d = 0;
    for (; d + 3 < head_dim; d += 4) {
        dot = __fmaf_rn(q[d], __half2float(k[d]), dot);
        dot = __fmaf_rn(q[d + 1], __half2float(k[d + 1]), dot);
        dot = __fmaf_rn(q[d + 2], __half2float(k[d + 2]), dot);
        dot = __fmaf_rn(q[d + 3], __half2float(k[d + 3]), dot);
    }
    for (; d < head_dim; ++d) dot = __fmaf_rn(q[d], __half2float(k[d]), dot);
    return dot;
}

__device__ __forceinline__ float dot_qk_f32_q8(const float *__restrict__ q, const int8_t *__restrict__ k,
                                              float scale, uint32_t head_dim) {
    float dot = 0.0f;
    uint32_t d = 0;
    for (; d + 3 < head_dim; d += 4) {
        dot = __fmaf_rn(q[d], (float)k[d] * scale, dot);
        dot = __fmaf_rn(q[d + 1], (float)k[d + 1] * scale, dot);
        dot = __fmaf_rn(q[d + 2], (float)k[d + 2] * scale, dot);
        dot = __fmaf_rn(q[d + 3], (float)k[d + 3] * scale, dot);
    }
    for (; d < head_dim; ++d) dot = __fmaf_rn(q[d], (float)k[d] * scale, dot);
    return dot;
}

__global__ void rope_kv_append_kernel(float *__restrict__ q, float *__restrict__ k,
                                      float *__restrict__ v, half *__restrict__ k_cache,
                                      half *__restrict__ v_cache, uint32_t n_heads,
                                      uint32_t n_kv_heads, uint32_t head_dim, uint32_t pos,
                                      const double *__restrict__ inv_freq, float freq_scale,
                                      int mode, uint32_t t,
                                      const ge_graph_tok_args *__restrict__ tok) {
    if (tok) {
        pos = tok->pos;
        t = tok->t;
    }
    // Block 0..n_heads-1: RoPE Q. Block n_heads..n_heads+n_kv-1: RoPE K + optional F16 append.
    uint32_t bid = blockIdx.x;
    if (bid < n_heads) {
        uint32_t h = bid;
        uint32_t half = head_dim / 2;
        uint32_t base = h * head_dim;
        for (uint32_t i = threadIdx.x; i < half; i += blockDim.x) {
            float c, s;
            rope_cs_table(pos, i, freq_scale, inv_freq, &c, &s);
            uint32_t i0 = (mode == 0) ? (base + 2 * i) : (base + i);
            uint32_t i1 = (mode == 0) ? (base + 2 * i + 1) : (base + i + half);
            float x0 = q[i0], x1 = q[i1];
            q[i0] = x0 * c - x1 * s;
            q[i1] = x0 * s + x1 * c;
        }
        return;
    }
    uint32_t h = bid - n_heads;
    if (h < n_kv_heads) {
        uint32_t half = head_dim / 2;
        uint32_t base = h * head_dim;
        for (uint32_t i = threadIdx.x; i < half; i += blockDim.x) {
            float c, s;
            rope_cs_table(pos, i, freq_scale, inv_freq, &c, &s);
            uint32_t i0 = (mode == 0) ? (base + 2 * i) : (base + i);
            uint32_t i1 = (mode == 0) ? (base + 2 * i + 1) : (base + i + half);
            float x0 = k[i0], x1 = k[i1];
            k[i0] = x0 * c - x1 * s;
            k[i1] = x0 * s + x1 * c;
        }
        /* RoPE and append use different thread→dim maps; sync before reading K. */
        __syncthreads();
        if (!k_cache || !v_cache) return; /* Q8 path: RoPE only; append via kv_append_q8. */
        uint32_t kv_dim = n_kv_heads * head_dim;
        for (uint32_t i = threadIdx.x; i < head_dim; i += blockDim.x) {
            uint32_t idx = base + i;
            size_t off = (size_t)t * kv_dim + idx;
            k_cache[off] = __float2half(k[idx]);
            v_cache[off] = __float2half(v[idx]);
        }
    }
}

__global__ void kv_append_f16_kernel(const float *__restrict__ k, const float *__restrict__ v,
                                     half *__restrict__ k_cache, half *__restrict__ v_cache,
                                     uint32_t kv_dim, uint32_t t) {
    uint32_t i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i >= kv_dim) return;
    size_t off = (size_t)t * kv_dim + i;
    k_cache[off] = __float2half(k[i]);
    v_cache[off] = __float2half(v[i]);
}

/* Per-token absmax Q8 append for K (block 0) and V (block 1) — matches CPU Q8LaneStore. */
__global__ void kv_append_q8_kernel(const float *__restrict__ k, const float *__restrict__ v,
                                    int8_t *__restrict__ k_qs, half *__restrict__ k_scales,
                                    int8_t *__restrict__ v_qs, half *__restrict__ v_scales,
                                    uint32_t kv_dim, uint32_t t,
                                    const ge_graph_tok_args *__restrict__ tok) {
    if (tok) t = tok->t;
    extern __shared__ float red_q8[];
    const float *src = (blockIdx.x == 0) ? k : v;
    int8_t *qs = (blockIdx.x == 0) ? k_qs : v_qs;
    half *scales = (blockIdx.x == 0) ? k_scales : v_scales;
    float amax = 0.0f;
    for (uint32_t i = threadIdx.x; i < kv_dim; i += blockDim.x)
        amax = fmaxf(amax, fabsf(src[i]));
    red_q8[threadIdx.x] = amax;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) red_q8[threadIdx.x] = fmaxf(red_q8[threadIdx.x], red_q8[threadIdx.x + s]);
        __syncthreads();
    }
    float scale = (red_q8[0] > 0.0f) ? (red_q8[0] / 127.0f) : 1.0f;
    if (threadIdx.x == 0) scales[t] = __float2half(scale);
    __syncthreads();
    for (uint32_t i = threadIdx.x; i < kv_dim; i += blockDim.x) {
        float q = rintf(src[i] / scale);
        q = fminf(fmaxf(q, -127.0f), 127.0f);
        qs[(size_t)t * kv_dim + i] = (int8_t)q;
    }
}

/* MC.3: K=Q8 (block 0), V=Q4 packed nibbles (block 1).
 * V scales are group-wise (GE_KV_Q4_V_GROUP) — matches host Q4LaneStore / KV_Q4_V_GROUP. */
__global__ void kv_append_q8v4_kernel(const float *__restrict__ k, const float *__restrict__ v,
                                      int8_t *__restrict__ k_qs, half *__restrict__ k_scales,
                                      uint8_t *__restrict__ v_q4, half *__restrict__ v_scales,
                                      uint32_t kv_dim, uint32_t t) {
    extern __shared__ float red_q8v4[];
    if (blockIdx.x == 0) {
        float amax = 0.0f;
        for (uint32_t i = threadIdx.x; i < kv_dim; i += blockDim.x)
            amax = fmaxf(amax, fabsf(k[i]));
        red_q8v4[threadIdx.x] = amax;
        __syncthreads();
        for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
            if (threadIdx.x < s)
                red_q8v4[threadIdx.x] = fmaxf(red_q8v4[threadIdx.x], red_q8v4[threadIdx.x + s]);
            __syncthreads();
        }
        float scale = (red_q8v4[0] > 0.0f) ? (red_q8v4[0] / 127.0f) : 1.0f;
        if (threadIdx.x == 0) k_scales[t] = __float2half(scale);
        __syncthreads();
        for (uint32_t i = threadIdx.x; i < kv_dim; i += blockDim.x) {
            float q = rintf(k[i] / scale);
            q = fminf(fmaxf(q, -127.0f), 127.0f);
            k_qs[(size_t)t * kv_dim + i] = (int8_t)q;
        }
        return;
    }
    /* V Q4 packed — one absmax scale per GE_KV_Q4_V_GROUP elems */
    const uint32_t gsz = GE_KV_Q4_V_GROUP;
    const uint32_t groups = kv_dim / gsz;
    const uint32_t gpack = gsz / 2u;
    const uint32_t npack = kv_dim / 2u;
    for (uint32_t g = 0; g < groups; ++g) {
        const uint32_t base = g * gsz;
        float amax = 0.0f;
        for (uint32_t i = threadIdx.x; i < gsz; i += blockDim.x)
            amax = fmaxf(amax, fabsf(v[base + i]));
        red_q8v4[threadIdx.x] = amax;
        __syncthreads();
        for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
            if (threadIdx.x < s)
                red_q8v4[threadIdx.x] = fmaxf(red_q8v4[threadIdx.x], red_q8v4[threadIdx.x + s]);
            __syncthreads();
        }
        float scale = (red_q8v4[0] > 0.0f) ? (red_q8v4[0] / 7.0f) : 1.0f;
        if (threadIdx.x == 0) v_scales[(size_t)t * groups + g] = __float2half(scale);
        __syncthreads();
        for (uint32_t p = threadIdx.x; p < gpack; p += blockDim.x) {
            const uint32_t i0 = base + p * 2u;
            float q0 = rintf(v[i0] / scale);
            float q1 = rintf(v[i0 + 1u] / scale);
            q0 = fminf(fmaxf(q0, -7.0f), 7.0f);
            q1 = fminf(fmaxf(q1, -7.0f), 7.0f);
            uint8_t b = ((uint8_t)((int8_t)q0) & 0x0fu) | ((((uint8_t)((int8_t)q1)) & 0x0fu) << 4);
            v_q4[(size_t)t * npack + g * gpack + p] = b;
        }
        __syncthreads();
    }
}

/* Q8 K + Q4 V past-KV attention — dequant on read. Current token stays f32. */
__device__ __forceinline__ int8_t ge_unpack_i4(uint8_t byte, int high) {
    uint8_t n = high ? ((byte >> 4) & 0x0fu) : (byte & 0x0fu);
    /* Cast before ASR so 4-bit two's complement sign-extends (n=9 → -7). */
    return (int8_t)((int8_t)(n << 4) >> 4);
}

__global__ void gqa_attn_decode_q8v4_kernel(const float *__restrict__ q,
                                            const int8_t *__restrict__ k_qs,
                                            const half *__restrict__ k_scales,
                                            const uint8_t *__restrict__ v_q4,
                                            const half *__restrict__ v_scales,
                                            const float *__restrict__ k_cur,
                                            const float *__restrict__ v_cur,
                                            float *__restrict__ out, uint32_t n_heads,
                                            uint32_t n_kv_heads, uint32_t head_dim, uint32_t seq,
                                            float scale) {
    extern __shared__ float sm[];
    float *scores = sm;
    __shared__ float red[256];
    uint32_t h = blockIdx.x;
    if (h >= n_heads || seq == 0) return;
    uint32_t reps = n_heads / n_kv_heads;
    uint32_t kv_h = h / reps;
    const float *qh = q + h * head_dim;
    const uint32_t cur = seq - 1;
    const uint32_t kv_dim = n_kv_heads * head_dim;
    const uint32_t npack = kv_dim / 2u;
    const uint32_t v_groups = kv_dim / GE_KV_Q4_V_GROUP;

    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) {
        float dot;
        if (k_cur && t == cur) {
            const float *kh = k_cur + kv_h * head_dim;
            dot = dot_qk_f32_f32(qh, kh, head_dim);
        } else {
            const float sc = __half2float(k_scales[t]);
            const int8_t *kh = k_qs + (size_t)t * kv_dim + kv_h * head_dim;
            dot = dot_qk_f32_q8(qh, kh, sc, head_dim);
        }
        scores[t] = dot * scale;
    }
    __syncthreads();

    float lv = -INFINITY;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) lv = fmaxf(lv, scores[t]);
    red[threadIdx.x] = lv;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) red[threadIdx.x] = fmaxf(red[threadIdx.x], red[threadIdx.x + s]);
        __syncthreads();
    }
    float m = red[0];
    __syncthreads();

    float sum = 0.0f;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) {
        float e = expf(scores[t] - m);
        scores[t] = e;
        sum += e;
    }
    red[threadIdx.x] = sum;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) red[threadIdx.x] += red[threadIdx.x + s];
        __syncthreads();
    }
    float inv = (red[0] > 0.0f) ? (1.0f / red[0]) : 0.0f;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) scores[t] *= inv;
    __syncthreads();

    for (uint32_t d = threadIdx.x; d < head_dim; d += blockDim.x) {
        float acc = 0.0f;
        const uint32_t elem = kv_h * head_dim + d;
        const uint32_t vg = elem / GE_KV_Q4_V_GROUP;
        for (uint32_t t = 0; t < seq; ++t) {
            float vv;
            if (v_cur && t == cur) {
                vv = v_cur[kv_h * head_dim + d];
            } else {
                const float sc = __half2float(v_scales[(size_t)t * v_groups + vg]);
                const uint8_t byte = v_q4[(size_t)t * npack + (elem / 2u)];
                vv = (float)ge_unpack_i4(byte, (elem & 1u) != 0u) * sc;
            }
            acc = __fmaf_rn(scores[t], vv, acc);
        }
        out[h * head_dim + d] = acc;
    }
}

/* One block per Q head. Scores in shared mem; stable softmax then value mix.
 * Current token K/V stay f32 (match CPU attend_one_head); past tokens from F16 cache. */
__global__ void gqa_attn_decode_kernel(const float *__restrict__ q,
                                       const half *__restrict__ k_cache,
                                       const half *__restrict__ v_cache,
                                       const float *__restrict__ k_cur,
                                       const float *__restrict__ v_cur,
                                       float *__restrict__ out,
                                       uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim,
                                       uint32_t seq, float scale,
                                       const ge_graph_tok_args *__restrict__ tok) {
    if (tok) seq = tok->seq;
    extern __shared__ float sm[];
    float *scores = sm; // [seq]
    __shared__ float red[256];
    uint32_t h = blockIdx.x;
    if (h >= n_heads || seq == 0) return;
    uint32_t reps = n_heads / n_kv_heads;
    uint32_t kv_h = h / reps;
    const float *qh = q + h * head_dim;
    const uint32_t cur = seq - 1;

    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) {
        float dot;
        if (k_cur && t == cur) {
            const float *kh = k_cur + kv_h * head_dim;
            dot = dot_qk_f32_f32(qh, kh, head_dim);
        } else {
            const half *kh = k_cache + (size_t)t * n_kv_heads * head_dim + kv_h * head_dim;
            dot = dot_qk_f32_f16(qh, kh, head_dim);
        }
        scores[t] = dot * scale;
    }
    __syncthreads();

    float lv = -INFINITY;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) lv = fmaxf(lv, scores[t]);
    red[threadIdx.x] = lv;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) red[threadIdx.x] = fmaxf(red[threadIdx.x], red[threadIdx.x + s]);
        __syncthreads();
    }
    float m = red[0];
    __syncthreads();

    float sum = 0.0f;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) {
        float e = expf(scores[t] - m);
        scores[t] = e;
        sum += e;
    }
    red[threadIdx.x] = sum;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) red[threadIdx.x] += red[threadIdx.x + s];
        __syncthreads();
    }
    float inv = (red[0] > 0.0f) ? (1.0f / red[0]) : 0.0f;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) scores[t] *= inv;
    __syncthreads();

    for (uint32_t d = threadIdx.x; d < head_dim; d += blockDim.x) {
        float acc = 0.0f;
        for (uint32_t t = 0; t < seq; ++t) {
            float vv;
            if (v_cur && t == cur) {
                vv = v_cur[kv_h * head_dim + d];
            } else {
                const half *vh = v_cache + (size_t)t * n_kv_heads * head_dim + kv_h * head_dim;
                vv = __half2float(vh[d]);
            }
            acc = __fmaf_rn(scores[t], vv, acc);
        }
        out[h * head_dim + d] = acc;
    }
}

/* Q8 past-KV attention — dequant on read (scale[t] * i8). Current token stays f32. */
__global__ void gqa_attn_decode_q8_kernel(const float *__restrict__ q,
                                          const int8_t *__restrict__ k_qs,
                                          const half *__restrict__ k_scales,
                                          const int8_t *__restrict__ v_qs,
                                          const half *__restrict__ v_scales,
                                          const float *__restrict__ k_cur,
                                          const float *__restrict__ v_cur,
                                          float *__restrict__ out, uint32_t n_heads,
                                          uint32_t n_kv_heads, uint32_t head_dim, uint32_t seq,
                                          float scale,
                                          const ge_graph_tok_args *__restrict__ tok) {
    if (tok) seq = tok->seq;
    extern __shared__ float sm[];
    float *scores = sm;
    __shared__ float red[256];
    uint32_t h = blockIdx.x;
    if (h >= n_heads || seq == 0) return;
    uint32_t reps = n_heads / n_kv_heads;
    uint32_t kv_h = h / reps;
    const float *qh = q + h * head_dim;
    const uint32_t cur = seq - 1;
    const uint32_t kv_dim = n_kv_heads * head_dim;

    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) {
        float dot;
        if (k_cur && t == cur) {
            const float *kh = k_cur + kv_h * head_dim;
            dot = dot_qk_f32_f32(qh, kh, head_dim);
        } else {
            const float sc = __half2float(k_scales[t]);
            const int8_t *kh = k_qs + (size_t)t * kv_dim + kv_h * head_dim;
            dot = dot_qk_f32_q8(qh, kh, sc, head_dim);
        }
        scores[t] = dot * scale;
    }
    __syncthreads();

    float lv = -INFINITY;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) lv = fmaxf(lv, scores[t]);
    red[threadIdx.x] = lv;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) red[threadIdx.x] = fmaxf(red[threadIdx.x], red[threadIdx.x + s]);
        __syncthreads();
    }
    float m = red[0];
    __syncthreads();

    float sum = 0.0f;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) {
        float e = expf(scores[t] - m);
        scores[t] = e;
        sum += e;
    }
    red[threadIdx.x] = sum;
    __syncthreads();
    for (uint32_t s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s) red[threadIdx.x] += red[threadIdx.x + s];
        __syncthreads();
    }
    float inv = (red[0] > 0.0f) ? (1.0f / red[0]) : 0.0f;
    for (uint32_t t = threadIdx.x; t < seq; t += blockDim.x) scores[t] *= inv;
    __syncthreads();

    for (uint32_t d = threadIdx.x; d < head_dim; d += blockDim.x) {
        float acc = 0.0f;
        for (uint32_t t = 0; t < seq; ++t) {
            float vv;
            if (v_cur && t == cur) {
                vv = v_cur[kv_h * head_dim + d];
            } else {
                const float sc = __half2float(v_scales[t]);
                const int8_t *vh = v_qs + (size_t)t * kv_dim + kv_h * head_dim;
                vv = (float)vh[d] * sc;
            }
            acc = __fmaf_rn(scores[t], vv, acc);
        }
        out[h * head_dim + d] = acc;
    }
}

static void free_kv_locked(ge_decode_ctx *ctx) {
    if (ctx->d_k_arena) {
        cudaFree(ctx->d_k_arena);
        ctx->d_k_arena = nullptr;
    }
    if (ctx->d_v_arena) {
        cudaFree(ctx->d_v_arena);
        ctx->d_v_arena = nullptr;
    }
    if (ctx->d_k_qs_arena) {
        cudaFree(ctx->d_k_qs_arena);
        ctx->d_k_qs_arena = nullptr;
    }
    if (ctx->d_v_qs_arena) {
        cudaFree(ctx->d_v_qs_arena);
        ctx->d_v_qs_arena = nullptr;
    }
    if (ctx->d_k_scale_arena) {
        cudaFree(ctx->d_k_scale_arena);
        ctx->d_k_scale_arena = nullptr;
    }
    if (ctx->d_v_scale_arena) {
        cudaFree(ctx->d_v_scale_arena);
        ctx->d_v_scale_arena = nullptr;
    }
    if (ctx->d_kv_scratch) {
        cudaFree(ctx->d_kv_scratch);
        ctx->d_kv_scratch = nullptr;
    }
    ctx->kv_scratch_bytes = 0;
    delete[] ctx->d_k_cache;
    delete[] ctx->d_v_cache;
    delete[] ctx->d_k_qs;
    delete[] ctx->d_v_qs;
    delete[] ctx->d_k_scales;
    delete[] ctx->d_v_scales;
    ctx->d_k_cache = nullptr;
    ctx->d_v_cache = nullptr;
    ctx->d_k_qs = nullptr;
    ctx->d_v_qs = nullptr;
    ctx->d_k_scales = nullptr;
    ctx->d_v_scales = nullptr;
    delete[] ctx->kv_len;
    ctx->kv_len = nullptr;
    ctx->n_kv_layers = 0;
    ctx->max_seq = 0;
    ctx->kv_dim = 0;
}

/* Device KV hard cap defaults to 4096; raised at ctx create from device smem (often ≥8192). */
#ifndef GE_GPU_KV_GROW_STEP
#define GE_GPU_KV_GROW_STEP 64u
#endif

static void kv_bind_layer_ptrs(ge_decode_ctx *ctx) {
    if (ctx->kv_dtype == GE_KV_DTYPE_Q8 || ctx->kv_dtype == GE_KV_DTYPE_Q8V4) {
        const size_t k_qs_stride = (size_t)ctx->max_seq * (size_t)ctx->kv_dim;
        const size_t v_qs_stride = (ctx->kv_dtype == GE_KV_DTYPE_Q8V4)
                                       ? ((size_t)ctx->max_seq * ((size_t)ctx->kv_dim / 2u))
                                       : k_qs_stride;
        const size_t k_sc_stride = (size_t)ctx->max_seq;
        const size_t v_groups = (ctx->kv_dtype == GE_KV_DTYPE_Q8V4)
                                    ? ((size_t)ctx->kv_dim / (size_t)GE_KV_Q4_V_GROUP)
                                    : 1u;
        const size_t v_sc_stride = (size_t)ctx->max_seq * v_groups;
        for (uint32_t i = 0; i < ctx->n_kv_layers; ++i) {
            ctx->d_k_qs[i] = ctx->d_k_qs_arena + i * k_qs_stride;
            ctx->d_v_qs[i] = ctx->d_v_qs_arena + i * v_qs_stride;
            ctx->d_k_scales[i] = ctx->d_k_scale_arena + i * k_sc_stride;
            ctx->d_v_scales[i] = ctx->d_v_scale_arena + i * v_sc_stride;
        }
        return;
    }
    const size_t layer_stride = (size_t)ctx->max_seq * (size_t)ctx->kv_dim;
    for (uint32_t i = 0; i < ctx->n_kv_layers; ++i) {
        ctx->d_k_cache[i] = ctx->d_k_arena + i * layer_stride;
        ctx->d_v_cache[i] = ctx->d_v_arena + i * layer_stride;
    }
}

/* Ensure compact scratch holds one layer of current max_seq (F16 elems or Q8 qs). */
static int ensure_kv_scratch(ge_decode_ctx *ctx) {
    if (!ctx || ctx->max_seq == 0 || ctx->kv_dim == 0) return -1;
    size_t need = (size_t)ctx->max_seq * (size_t)ctx->kv_dim;
    if (ctx->kv_dtype == GE_KV_DTYPE_Q8 || ctx->kv_dtype == GE_KV_DTYPE_Q8V4)
        need = need * sizeof(int8_t); /* Q8V4 V is half; K size is the upper bound */
    else
        need = need * sizeof(half);
    if (need <= ctx->kv_scratch_bytes && ctx->d_kv_scratch) return 0;
    if (ctx->d_kv_scratch) {
        cudaFree(ctx->d_kv_scratch);
        ctx->d_kv_scratch = nullptr;
        ctx->kv_scratch_bytes = 0;
    }
    if (cudaMalloc(&ctx->d_kv_scratch, need) != cudaSuccess) return -1;
    ctx->kv_scratch_bytes = need;
    return 0;
}

/* StreamingLLM compact one layer: keep [0..sinks) + last `recent` tokens. */
static int compact_layer_sinks_recent(ge_decode_ctx *ctx, uint32_t layer, uint32_t sinks,
                                      uint32_t recent) {
    if (!ctx || !ctx->kv_len || layer >= ctx->n_kv_layers) return -1;
    uint32_t n = ctx->kv_len[layer];
    if (sinks > n) sinks = n;
    if (recent > n - sinks) recent = n - sinks;
    if (sinks + recent >= n) return 0;
    uint32_t keep_from = n - recent;
    uint32_t new_n = sinks + recent;
    if (ensure_kv_scratch(ctx) != 0) return -1;
    if (ctx->stream && cudaStreamSynchronize(ctx->stream) != cudaSuccess) return -1;

    if (ctx->kv_dtype == GE_KV_DTYPE_Q8 || ctx->kv_dtype == GE_KV_DTYPE_Q8V4) {
        int8_t *kq = ctx->d_k_qs[layer];
        int8_t *vq = ctx->d_v_qs[layer];
        half *ks = ctx->d_k_scales[layer];
        half *vs = ctx->d_v_scales[layer];
        int8_t *scratch = (int8_t *)ctx->d_kv_scratch;
        size_t dim = (size_t)ctx->kv_dim;
        size_t v_stride = (ctx->kv_dtype == GE_KV_DTYPE_Q8V4) ? (dim / 2u) : dim;
        if (sinks > 0) {
            if (cudaMemcpy(scratch, kq, sinks * dim, cudaMemcpyDeviceToDevice) != cudaSuccess)
                return -1;
        }
        if (recent > 0) {
            if (cudaMemcpy(scratch + sinks * dim, kq + keep_from * dim, recent * dim,
                           cudaMemcpyDeviceToDevice)
                != cudaSuccess)
                return -1;
        }
        if (new_n > 0
            && cudaMemcpy(kq, scratch, new_n * dim, cudaMemcpyDeviceToDevice) != cudaSuccess)
            return -1;
        if (sinks > 0) {
            if (cudaMemcpy(scratch, vq, sinks * v_stride, cudaMemcpyDeviceToDevice) != cudaSuccess)
                return -1;
        }
        if (recent > 0) {
            if (cudaMemcpy(scratch + sinks * v_stride, vq + keep_from * v_stride, recent * v_stride,
                           cudaMemcpyDeviceToDevice)
                != cudaSuccess)
                return -1;
        }
        if (new_n > 0
            && cudaMemcpy(vq, scratch, new_n * v_stride, cudaMemcpyDeviceToDevice) != cudaSuccess)
            return -1;
        const size_t v_ng = (ctx->kv_dtype == GE_KV_DTYPE_Q8V4)
                                ? (dim / (size_t)GE_KV_Q4_V_GROUP)
                                : 1u;
        std::vector<half> sc(n ? n : 1);
        if (n > 0) {
            if (cudaMemcpy(sc.data(), ks, n * sizeof(half), cudaMemcpyDeviceToHost) != cudaSuccess)
                return -1;
            for (uint32_t i = 0; i < recent; ++i) sc[sinks + i] = sc[keep_from + i];
            if (cudaMemcpy(ks, sc.data(), new_n * sizeof(half), cudaMemcpyHostToDevice)
                != cudaSuccess)
                return -1;
        }
        std::vector<half> vsc(n ? ((size_t)n * v_ng) : 1u);
        if (n > 0) {
            if (cudaMemcpy(vsc.data(), vs, (size_t)n * v_ng * sizeof(half), cudaMemcpyDeviceToHost)
                != cudaSuccess)
                return -1;
            for (uint32_t i = 0; i < recent; ++i) {
                for (size_t g = 0; g < v_ng; ++g)
                    vsc[((size_t)sinks + i) * v_ng + g] =
                        vsc[((size_t)keep_from + i) * v_ng + g];
            }
            if (cudaMemcpy(vs, vsc.data(), (size_t)new_n * v_ng * sizeof(half),
                           cudaMemcpyHostToDevice)
                != cudaSuccess)
                return -1;
        }
    } else {
        half *k = ctx->d_k_cache[layer];
        half *v = ctx->d_v_cache[layer];
        half *scratch = (half *)ctx->d_kv_scratch;
        size_t dim = (size_t)ctx->kv_dim;
        if (sinks > 0) {
            if (cudaMemcpy(scratch, k, sinks * dim * sizeof(half), cudaMemcpyDeviceToDevice)
                != cudaSuccess)
                return -1;
        }
        if (recent > 0) {
            if (cudaMemcpy(scratch + sinks * dim, k + keep_from * dim,
                           recent * dim * sizeof(half), cudaMemcpyDeviceToDevice)
                != cudaSuccess)
                return -1;
        }
        if (new_n > 0
            && cudaMemcpy(k, scratch, new_n * dim * sizeof(half), cudaMemcpyDeviceToDevice)
                != cudaSuccess)
            return -1;
        if (sinks > 0) {
            if (cudaMemcpy(scratch, v, sinks * dim * sizeof(half), cudaMemcpyDeviceToDevice)
                != cudaSuccess)
                return -1;
        }
        if (recent > 0) {
            if (cudaMemcpy(scratch + sinks * dim, v + keep_from * dim,
                           recent * dim * sizeof(half), cudaMemcpyDeviceToDevice)
                != cudaSuccess)
                return -1;
        }
        if (new_n > 0
            && cudaMemcpy(v, scratch, new_n * dim * sizeof(half), cudaMemcpyDeviceToDevice)
                != cudaSuccess)
            return -1;
    }
    ctx->kv_len[layer] = new_n;
    return 0;
}

/* Before append: if window full, drop oldest non-sink so one slot frees (peak stays hot_cap). */
static int kv_prepare_append(ge_decode_ctx *ctx, uint32_t layer) {
    if (!ctx || !ctx->kv_len || layer >= ctx->n_kv_layers) return -1;
    uint32_t t = ctx->kv_len[layer];
    if (ctx->kv_hot_cap > 0 && t >= ctx->kv_hot_cap) {
        uint32_t sinks = ctx->kv_sinks;
        if (sinks >= ctx->kv_hot_cap) sinks = ctx->kv_hot_cap > 1u ? ctx->kv_hot_cap - 1u : 0u;
        uint32_t keep = ctx->kv_hot_cap - 1u;
        uint32_t recent = keep > sinks ? keep - sinks : 0u;
        if (compact_layer_sinks_recent(ctx, layer, sinks, recent) != 0) return -1;
        ctx->kv_evictions += 1;
        /* Compaction rewrites KV slots — captured graphs bake absolute t indices. */
        invalidate_layer_graphs(ctx);
        t = ctx->kv_len[layer];
    }
    if (t >= ctx->max_seq && ensure_kv_cap(ctx, t + 1) != 0) return -1;
    return 0;
}

static size_t kv_nbytes_for(const ge_decode_ctx *ctx, uint32_t seq) {
    if (!ctx || ctx->n_kv_layers == 0 || ctx->kv_dim == 0 || seq == 0) return 0;
    if (ctx->kv_dtype == GE_KV_DTYPE_Q8) {
        const size_t qs = (size_t)ctx->n_kv_layers * (size_t)seq * (size_t)ctx->kv_dim;
        const size_t sc = (size_t)ctx->n_kv_layers * (size_t)seq * sizeof(half);
        return qs * 2u + sc * 2u; /* K+V qs + K+V scales */
    }
    if (ctx->kv_dtype == GE_KV_DTYPE_Q8V4) {
        const size_t groups = (size_t)ctx->kv_dim / (size_t)GE_KV_Q4_V_GROUP;
        const size_t k_qs = (size_t)ctx->n_kv_layers * (size_t)seq * (size_t)ctx->kv_dim;
        const size_t v_qs = (size_t)ctx->n_kv_layers * (size_t)seq * ((size_t)ctx->kv_dim / 2u);
        const size_t k_sc = (size_t)ctx->n_kv_layers * (size_t)seq * sizeof(half);
        const size_t v_sc = (size_t)ctx->n_kv_layers * (size_t)seq * groups * sizeof(half);
        return k_qs + v_qs + k_sc + v_sc;
    }
    return (size_t)ctx->n_kv_layers * (size_t)seq * (size_t)ctx->kv_dim * sizeof(half) * 2u;
}

/* Grow device K/V arenas to cover need_seq (step-rounded). One alloc pair — not per-layer. */
static int ensure_kv_cap(ge_decode_ctx *ctx, uint32_t need_seq) {
    if (!ctx || ctx->n_kv_layers == 0 || ctx->kv_dim == 0) return -1;
    const bool q8 = ctx->kv_dtype == GE_KV_DTYPE_Q8;
    const bool q8v4 = ctx->kv_dtype == GE_KV_DTYPE_Q8V4;
    const bool qkeys = q8 || q8v4;
    if (qkeys) {
        if (!ctx->d_k_qs_arena || !ctx->d_v_qs_arena || !ctx->d_k_scale_arena
            || !ctx->d_v_scale_arena)
            return -1;
    } else if (!ctx->d_k_arena || !ctx->d_v_arena) {
        return -1;
    }
    if (need_seq <= ctx->max_seq) return 0;
    /* Arena realloc moves device pointers baked into captured graphs. */
    invalidate_layer_graphs(ctx);
    const uint32_t hard = ctx->kv_hard_cap ? ctx->kv_hard_cap : 4096u;
    uint32_t ceiling = ctx->kv_grow_cap ? ctx->kv_grow_cap : hard;
    if (ceiling > hard) ceiling = hard;
    if (need_seq > ceiling) return -1;
    const uint32_t step = GE_GPU_KV_GROW_STEP ? GE_GPU_KV_GROW_STEP : 64u;
    uint32_t new_cap = ((need_seq + step - 1u) / step) * step;
    if (new_cap < need_seq || new_cap > ceiling) new_cap = ceiling;
    if (new_cap <= ctx->max_seq) return 0;
    if (ctx->stream && cudaStreamSynchronize(ctx->stream) != cudaSuccess) return -1;

    if (qkeys) {
        const size_t old_k_qs = (size_t)ctx->max_seq * (size_t)ctx->kv_dim;
        const size_t new_k_qs = (size_t)new_cap * (size_t)ctx->kv_dim;
        const size_t old_v_qs = q8v4 ? ((size_t)ctx->max_seq * ((size_t)ctx->kv_dim / 2u)) : old_k_qs;
        const size_t new_v_qs = q8v4 ? ((size_t)new_cap * ((size_t)ctx->kv_dim / 2u)) : new_k_qs;
        const size_t v_groups = q8v4 ? ((size_t)ctx->kv_dim / (size_t)GE_KV_Q4_V_GROUP) : 1u;
        const size_t k_qs_bytes = new_k_qs * (size_t)ctx->n_kv_layers;
        const size_t v_qs_bytes = new_v_qs * (size_t)ctx->n_kv_layers;
        const size_t k_sc_bytes = (size_t)new_cap * (size_t)ctx->n_kv_layers * sizeof(half);
        const size_t v_sc_bytes =
            (size_t)new_cap * v_groups * (size_t)ctx->n_kv_layers * sizeof(half);
        int8_t *nkq = nullptr;
        int8_t *nvq = nullptr;
        half *nks = nullptr;
        half *nvs = nullptr;
        if (cudaMalloc((void **)&nkq, k_qs_bytes) != cudaSuccess) return -1;
        if (cudaMalloc((void **)&nvq, v_qs_bytes) != cudaSuccess) {
            cudaFree(nkq);
            return -1;
        }
        if (cudaMalloc((void **)&nks, k_sc_bytes) != cudaSuccess) {
            cudaFree(nkq);
            cudaFree(nvq);
            return -1;
        }
        if (cudaMalloc((void **)&nvs, v_sc_bytes) != cudaSuccess) {
            cudaFree(nkq);
            cudaFree(nvq);
            cudaFree(nks);
            return -1;
        }
        for (uint32_t i = 0; i < ctx->n_kv_layers; ++i) {
            uint32_t len = ctx->kv_len ? ctx->kv_len[i] : 0;
            size_t copy_k = (size_t)len * (size_t)ctx->kv_dim;
            size_t copy_v = q8v4 ? ((size_t)len * ((size_t)ctx->kv_dim / 2u)) : copy_k;
            size_t copy_k_sc = (size_t)len * sizeof(half);
            size_t copy_v_sc = (size_t)len * v_groups * sizeof(half);
            if (copy_k > old_k_qs) copy_k = old_k_qs;
            if (copy_v > old_v_qs) copy_v = old_v_qs;
            if (len > ctx->max_seq) {
                copy_k_sc = (size_t)ctx->max_seq * sizeof(half);
                copy_v_sc = (size_t)ctx->max_seq * v_groups * sizeof(half);
            }
            if (copy_k > 0 || copy_v > 0) {
                if ((copy_k > 0
                     && cudaMemcpy(nkq + i * new_k_qs, ctx->d_k_qs[i], copy_k, cudaMemcpyDeviceToDevice)
                            != cudaSuccess)
                    || (copy_v > 0
                        && cudaMemcpy(nvq + i * new_v_qs, ctx->d_v_qs[i], copy_v, cudaMemcpyDeviceToDevice)
                               != cudaSuccess)) {
                    cudaFree(nkq);
                    cudaFree(nvq);
                    cudaFree(nks);
                    cudaFree(nvs);
                    return -1;
                }
            }
            if (copy_k_sc > 0 || copy_v_sc > 0) {
                if ((copy_k_sc > 0
                     && cudaMemcpy(nks + i * new_cap, ctx->d_k_scales[i], copy_k_sc,
                                   cudaMemcpyDeviceToDevice)
                            != cudaSuccess)
                    || (copy_v_sc > 0
                        && cudaMemcpy(nvs + i * new_cap * v_groups, ctx->d_v_scales[i], copy_v_sc,
                                      cudaMemcpyDeviceToDevice)
                               != cudaSuccess)) {
                    cudaFree(nkq);
                    cudaFree(nvq);
                    cudaFree(nks);
                    cudaFree(nvs);
                    return -1;
                }
            }
        }
        cudaFree(ctx->d_k_qs_arena);
        cudaFree(ctx->d_v_qs_arena);
        cudaFree(ctx->d_k_scale_arena);
        cudaFree(ctx->d_v_scale_arena);
        ctx->d_k_qs_arena = nkq;
        ctx->d_v_qs_arena = nvq;
        ctx->d_k_scale_arena = nks;
        ctx->d_v_scale_arena = nvs;
        ctx->max_seq = new_cap;
        kv_bind_layer_ptrs(ctx);
        return 0;
    }

    const size_t old_layer = (size_t)ctx->max_seq * (size_t)ctx->kv_dim;
    const size_t new_layer = (size_t)new_cap * (size_t)ctx->kv_dim;
    const size_t new_bytes = new_layer * sizeof(half) * (size_t)ctx->n_kv_layers;
    half *nk = nullptr;
    half *nv = nullptr;
    if (cudaMalloc((void **)&nk, new_bytes) != cudaSuccess) return -1;
    if (cudaMalloc((void **)&nv, new_bytes) != cudaSuccess) {
        cudaFree(nk);
        return -1;
    }
    for (uint32_t i = 0; i < ctx->n_kv_layers; ++i) {
        uint32_t len = ctx->kv_len ? ctx->kv_len[i] : 0;
        size_t copy = (size_t)len * (size_t)ctx->kv_dim * sizeof(half);
        size_t max_copy = old_layer * sizeof(half);
        if (copy > max_copy) copy = max_copy;
        if (copy > 0) {
            if (cudaMemcpy(nk + i * new_layer, ctx->d_k_cache[i], copy, cudaMemcpyDeviceToDevice)
                    != cudaSuccess
                || cudaMemcpy(nv + i * new_layer, ctx->d_v_cache[i], copy, cudaMemcpyDeviceToDevice)
                    != cudaSuccess) {
                cudaFree(nk);
                cudaFree(nv);
                return -1;
            }
        }
    }
    cudaFree(ctx->d_k_arena);
    cudaFree(ctx->d_v_arena);
    ctx->d_k_arena = nk;
    ctx->d_v_arena = nv;
    ctx->max_seq = new_cap;
    kv_bind_layer_ptrs(ctx);
    return 0;
}

static int ensure_attn_smem_optin(ge_decode_ctx *ctx, size_t smem) {
    if (!ctx || smem == 0) return 0;
    int def_smem = 48 * 1024;
    cudaDeviceGetAttribute(&def_smem, cudaDevAttrMaxSharedMemoryPerBlock, ctx->device_id);
    if ((int)smem <= def_smem) return 0;
    cudaError_t e0 = cudaFuncSetAttribute(gqa_attn_decode_kernel,
                                          cudaFuncAttributeMaxDynamicSharedMemorySize, (int)smem);
    cudaError_t e1 = cudaFuncSetAttribute(gqa_attn_decode_q8_kernel,
                                          cudaFuncAttributeMaxDynamicSharedMemorySize, (int)smem);
    cudaError_t e2 = cudaFuncSetAttribute(gqa_attn_decode_q8v4_kernel,
                                          cudaFuncAttributeMaxDynamicSharedMemorySize, (int)smem);
    return (e0 == cudaSuccess && e1 == cudaSuccess && e2 == cudaSuccess) ? 0 : -1;
}

extern "C" int ge_decode_kv_setup(ge_decode_ctx *ctx, uint32_t n_layers, uint32_t max_seq,
                                  uint32_t kv_dim, uint32_t kv_dtype) {
    if (!ctx || n_layers == 0 || max_seq == 0 || kv_dim == 0) return -1;
    if (!set_dev(ctx)) return -1;
    if (ctx->kv_hard_cap == 0) ctx->kv_hard_cap = query_kv_hard_cap(ctx->device_id);
    if (ctx->kv_grow_cap == 0 || ctx->kv_grow_cap > ctx->kv_hard_cap)
        ctx->kv_grow_cap = ctx->kv_hard_cap;
    if (kv_dtype != GE_KV_DTYPE_Q8 && kv_dtype != GE_KV_DTYPE_Q8V4) kv_dtype = GE_KV_DTYPE_F16;
    if (kv_dtype == GE_KV_DTYPE_Q8V4
        && ((kv_dim % GE_KV_Q4_V_GROUP) != 0u || (kv_dim % 2u) != 0u))
        return -1;
    if (max_seq > ctx->kv_hard_cap) max_seq = ctx->kv_hard_cap;
    free_kv_locked(ctx);
    ctx->kv_dtype = kv_dtype;
    ctx->kv_len = new uint32_t[n_layers]();
    ctx->n_kv_layers = n_layers;
    ctx->max_seq = max_seq;
    ctx->kv_dim = kv_dim;

    if (kv_dtype == GE_KV_DTYPE_Q8 || kv_dtype == GE_KV_DTYPE_Q8V4) {
        ctx->d_k_qs = new int8_t *[n_layers]();
        ctx->d_v_qs = new int8_t *[n_layers]();
        ctx->d_k_scales = new half *[n_layers]();
        ctx->d_v_scales = new half *[n_layers]();
        const size_t v_groups =
            (kv_dtype == GE_KV_DTYPE_Q8V4) ? ((size_t)kv_dim / (size_t)GE_KV_Q4_V_GROUP) : 1u;
        const size_t k_qs_bytes = (size_t)n_layers * (size_t)max_seq * (size_t)kv_dim;
        const size_t v_qs_bytes = (kv_dtype == GE_KV_DTYPE_Q8V4)
                                      ? ((size_t)n_layers * (size_t)max_seq * ((size_t)kv_dim / 2u))
                                      : k_qs_bytes;
        const size_t k_sc_bytes = (size_t)n_layers * (size_t)max_seq * sizeof(half);
        const size_t v_sc_bytes =
            (size_t)n_layers * (size_t)max_seq * v_groups * sizeof(half);
        if (cudaMalloc((void **)&ctx->d_k_qs_arena, k_qs_bytes) != cudaSuccess
            || cudaMalloc((void **)&ctx->d_v_qs_arena, v_qs_bytes) != cudaSuccess
            || cudaMalloc((void **)&ctx->d_k_scale_arena, k_sc_bytes) != cudaSuccess
            || cudaMalloc((void **)&ctx->d_v_scale_arena, v_sc_bytes) != cudaSuccess) {
            free_kv_locked(ctx);
            return -1;
        }
        kv_bind_layer_ptrs(ctx);
        return 0;
    }

    ctx->d_k_cache = new half *[n_layers]();
    ctx->d_v_cache = new half *[n_layers]();
    size_t bytes = (size_t)n_layers * (size_t)max_seq * (size_t)kv_dim * sizeof(half);
    if (cudaMalloc((void **)&ctx->d_k_arena, bytes) != cudaSuccess
        || cudaMalloc((void **)&ctx->d_v_arena, bytes) != cudaSuccess) {
        free_kv_locked(ctx);
        return -1;
    }
    kv_bind_layer_ptrs(ctx);
    return 0;
}

/* M5.4 exports: soft grow ceiling (≤ smem hard_cap) + ensure (wrap ensure_kv_cap). */
extern "C" void ge_decode_kv_set_grow_cap(ge_decode_ctx *ctx, uint32_t grow_cap) {
    if (!ctx) return;
    if (ctx->kv_hard_cap == 0) ctx->kv_hard_cap = query_kv_hard_cap(ctx->device_id);
    uint32_t hard = ctx->kv_hard_cap;
    if (grow_cap == 0 || grow_cap > hard) grow_cap = hard;
    /* StreamingLLM window clamps grow so peak MiB stays at the hot window. */
    if (ctx->kv_hot_cap > 0 && grow_cap > ctx->kv_hot_cap) grow_cap = ctx->kv_hot_cap;
    ctx->kv_grow_cap = grow_cap;
}

extern "C" void ge_decode_kv_set_window(ge_decode_ctx *ctx, uint32_t hot_cap, uint32_t sinks) {
    if (!ctx) return;
    ctx->kv_hot_cap = hot_cap;
    if (hot_cap == 0) {
        ctx->kv_sinks = sinks ? sinks : 4u;
        return;
    }
    if (sinks >= hot_cap) sinks = hot_cap > 1u ? hot_cap - 1u : 0u;
    ctx->kv_sinks = sinks;
    if (ctx->kv_hard_cap == 0) ctx->kv_hard_cap = query_kv_hard_cap(ctx->device_id);
    uint32_t hard = ctx->kv_hard_cap ? ctx->kv_hard_cap : 4096u;
    uint32_t ceiling = hot_cap;
    if (ceiling > hard) ceiling = hard;
    if (ctx->kv_grow_cap == 0 || ctx->kv_grow_cap > ceiling) ctx->kv_grow_cap = ceiling;
}

extern "C" int ge_decode_kv_ensure(ge_decode_ctx *ctx, uint32_t need_seq) {
    if (!ctx || need_seq == 0) return 0;
    if (!set_dev(ctx)) return -1;
    if (ctx->kv_hot_cap > 0 && need_seq > ctx->kv_hot_cap) need_seq = ctx->kv_hot_cap;
    return ensure_kv_cap(ctx, need_seq);
}

extern "C" size_t ge_decode_kv_nbytes(const ge_decode_ctx *ctx) {
    if (!ctx) return 0;
    return kv_nbytes_for(ctx, ctx->max_seq);
}

extern "C" uint32_t ge_decode_kv_hard_cap(const ge_decode_ctx *ctx) {
    if (!ctx) return 4096u;
    return ctx->kv_hard_cap ? ctx->kv_hard_cap : 4096u;
}

extern "C" uint32_t ge_decode_kv_dtype(const ge_decode_ctx *ctx) {
    return ctx ? ctx->kv_dtype : GE_KV_DTYPE_F16;
}

extern "C" uint32_t ge_decode_kv_seq_len(const ge_decode_ctx *ctx) {
    if (!ctx || !ctx->kv_len || ctx->n_kv_layers == 0) return 0;
    return ctx->kv_len[0];
}

extern "C" uint64_t ge_decode_kv_evictions(const ge_decode_ctx *ctx) {
    return ctx ? ctx->kv_evictions : 0;
}

extern "C" void ge_decode_kv_clear(ge_decode_ctx *ctx) {
    if (!ctx || !ctx->kv_len) return;
    for (uint32_t i = 0; i < ctx->n_kv_layers; ++i) ctx->kv_len[i] = 0;
    ctx->kv_evictions = 0;
    ctx->graphs_armed = 0;
    invalidate_layer_graphs(ctx);
}

extern "C" void ge_decode_kv_truncate(ge_decode_ctx *ctx, uint32_t new_len) {
    if (!ctx || !ctx->kv_len) return;
    for (uint32_t i = 0; i < ctx->n_kv_layers; ++i) {
        if (ctx->kv_len[i] > new_len) ctx->kv_len[i] = new_len;
    }
}

static bool kv_ready(const ge_decode_ctx *ctx, uint32_t layer) {
    if (!ctx || layer >= ctx->n_kv_layers) return false;
    if (ctx->kv_dtype == GE_KV_DTYPE_Q8 || ctx->kv_dtype == GE_KV_DTYPE_Q8V4)
        return ctx->d_k_qs && ctx->d_v_qs && ctx->d_k_scales && ctx->d_v_scales;
    return ctx->d_k_cache && ctx->d_v_cache;
}

static int launch_rope_and_store(ge_decode_ctx *ctx, uint32_t layer, uint32_t pos, uint32_t n_heads,
                                 uint32_t n_kv_heads, uint32_t head_dim, float rope_theta,
                                 float rope_freq_scale, int rope_mode, uint32_t t) {
    const int threads = 64;
    const ge_graph_tok_args *tok =
        (ctx->use_graph_tok && ctx->d_graph_tok) ? ctx->d_graph_tok : nullptr;
    if (ensure_rope_inv_freq(ctx, rope_theta, head_dim) != 0) return -1;
    if (ctx->kv_dtype == GE_KV_DTYPE_Q8 || ctx->kv_dtype == GE_KV_DTYPE_Q8V4) {
        rope_kv_append_kernel<<<n_heads + n_kv_heads, threads, 0, ctx->stream>>>(
            ctx->d_q, ctx->d_k, ctx->d_v, nullptr, nullptr, n_heads, n_kv_heads, head_dim, pos,
            ctx->d_inv_freq, rope_freq_scale, rope_mode, t, tok);
        if (cudaGetLastError() != cudaSuccess) return -1;
        const int qthreads = 256;
        size_t qsmem = (size_t)qthreads * sizeof(float);
        if (ctx->kv_dtype == GE_KV_DTYPE_Q8V4) {
            kv_append_q8v4_kernel<<<2, qthreads, qsmem, ctx->stream>>>(
                ctx->d_k, ctx->d_v, ctx->d_k_qs[layer], ctx->d_k_scales[layer],
                (uint8_t *)ctx->d_v_qs[layer], ctx->d_v_scales[layer], ctx->kv_dim, t);
        } else {
            kv_append_q8_kernel<<<2, qthreads, qsmem, ctx->stream>>>(
                ctx->d_k, ctx->d_v, ctx->d_k_qs[layer], ctx->d_k_scales[layer], ctx->d_v_qs[layer],
                ctx->d_v_scales[layer], ctx->kv_dim, t, tok);
        }
        return cudaGetLastError() == cudaSuccess ? 0 : -1;
    }
    rope_kv_append_kernel<<<n_heads + n_kv_heads, threads, 0, ctx->stream>>>(
        ctx->d_q, ctx->d_k, ctx->d_v, ctx->d_k_cache[layer], ctx->d_v_cache[layer], n_heads,
        n_kv_heads, head_dim, pos, ctx->d_inv_freq, rope_freq_scale, rope_mode, t, tok);
    return cudaGetLastError() == cudaSuccess ? 0 : -1;
}

static int launch_gqa_attn(ge_decode_ctx *ctx, uint32_t layer, uint32_t n_heads, uint32_t n_kv_heads,
                           uint32_t head_dim, uint32_t seq, uint32_t q_dim) {
    float scale = 1.0f / sqrtf((float)head_dim);
    const int threads = 256;
    size_t smem = attn_launch_smem(ctx, seq);
    if ((size_t)seq * sizeof(float) > smem) return -1;
    if (ensure_attn_smem_optin(ctx, smem) != 0) return -1;
    const float *k_cur = (q_dim >= 4096u) ? ctx->d_k : nullptr;
    const float *v_cur = (q_dim >= 4096u) ? ctx->d_v : nullptr;
    const ge_graph_tok_args *tok =
        (ctx->use_graph_tok && ctx->d_graph_tok) ? ctx->d_graph_tok : nullptr;
    if (ctx->kv_dtype == GE_KV_DTYPE_Q8V4) {
        gqa_attn_decode_q8v4_kernel<<<n_heads, threads, smem, ctx->stream>>>(
            ctx->d_q, ctx->d_k_qs[layer], ctx->d_k_scales[layer], (const uint8_t *)ctx->d_v_qs[layer],
            ctx->d_v_scales[layer], k_cur, v_cur, ctx->d_attn, n_heads, n_kv_heads, head_dim, seq,
            scale);
    } else if (ctx->kv_dtype == GE_KV_DTYPE_Q8) {
        gqa_attn_decode_q8_kernel<<<n_heads, threads, smem, ctx->stream>>>(
            ctx->d_q, ctx->d_k_qs[layer], ctx->d_k_scales[layer], ctx->d_v_qs[layer],
            ctx->d_v_scales[layer], k_cur, v_cur, ctx->d_attn, n_heads, n_kv_heads, head_dim, seq,
            scale, tok);
    } else {
        gqa_attn_decode_kernel<<<n_heads, threads, smem, ctx->stream>>>(
            ctx->d_q, ctx->d_k_cache[layer], ctx->d_v_cache[layer], k_cur, v_cur, ctx->d_attn,
            n_heads, n_kv_heads, head_dim, seq, scale, tok);
    }
    return cudaGetLastError() == cudaSuccess ? 0 : -1;
}

extern "C" int ge_decode_q4_attn_block(ge_decode_ctx *ctx,
                                       const void *d_wq, const void *d_wk, const void *d_wv,
                                       const void *d_wo, const float *x_host, float *proj_host,
                                       uint32_t layer, uint32_t pos, uint32_t hidden,
                                       uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim,
                                       float rope_theta, float rope_freq_scale, int rope_mode,
                                       uint32_t ggml_type) {
    if (!ctx || !d_wq || !d_wk || !d_wv || !d_wo || !x_host || !proj_host) return -1;
    if (!kv_ready(ctx, layer)) return -1;
    if (hidden == 0 || n_heads == 0 || n_kv_heads == 0 || head_dim == 0) return -1;
    if (n_heads % n_kv_heads != 0 || (head_dim & 1u) != 0) return -1;
    uint32_t q_dim = n_heads * head_dim;
    uint32_t kv_dim = n_kv_heads * head_dim;
    if (kv_dim != ctx->kv_dim) return -1;
    if (kv_prepare_append(ctx, layer) != 0) return -1;
    uint32_t t = ctx->kv_len[layer];
    if (!set_dev(ctx)) return -1;

    if (!stage_h2d_x(ctx, x_host, hidden)) return -1;
    if (!ensure_dev(&ctx->d_q, &ctx->cap_q, q_dim)) return -1;
    if (!ensure_dev(&ctx->d_k, &ctx->cap_k, kv_dim)) return -1;
    if (!ensure_dev(&ctx->d_v, &ctx->cap_v, kv_dim)) return -1;
    if (!ensure_dev(&ctx->d_attn, &ctx->cap_attn, q_dim)) return -1;
    if (!ensure_dev(&ctx->d_y, &ctx->cap_y, hidden)) return -1;

    if (launch_gemv3(ctx, d_wq, d_wk, d_wv, ctx->d_x, ctx->d_q, ctx->d_k, ctx->d_v, hidden, q_dim,
                     kv_dim, ggml_type)
        != 0)
        return -1;

    if (launch_rope_and_store(ctx, layer, pos, n_heads, n_kv_heads, head_dim, rope_theta,
                              rope_freq_scale, rope_mode, t)
        != 0)
        return -1;
    uint32_t seq = t + 1;
    ctx->kv_len[layer] = seq;

    if (launch_gqa_attn(ctx, layer, n_heads, n_kv_heads, head_dim, seq, q_dim) != 0) return -1;

    if (launch_gemv(ctx, d_wo, ctx->d_attn, ctx->d_y, q_dim, hidden, ggml_type) != 0) return -1;
    if (!stage_d2h(ctx, ctx->d_y, proj_host, hidden)) return -1;
    return cudaStreamSynchronize(ctx->stream) == cudaSuccess ? 0 : -1;
}

/* ---- Full-layer fuse: RMSNorm + attn + residual + RMSNorm + SwiGLU + residual --------------- */

static int attn_block_dev(ge_decode_ctx *ctx, const void *d_wq, const void *d_wk, const void *d_wv,
                          const void *d_wo, uint32_t layer, uint32_t pos, uint32_t hidden,
                          uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim,
                          float rope_theta, float rope_freq_scale, int rope_mode,
                          uint32_t ggml_type, float *res_add) {
    if (!kv_ready(ctx, layer)) return -1;
    uint32_t q_dim = n_heads * head_dim;
    uint32_t kv_dim = n_kv_heads * head_dim;
    if (kv_dim != ctx->kv_dim) return -1;
    if (kv_prepare_append(ctx, layer) != 0) return -1;
    uint32_t t = ctx->kv_len[layer];
    uint32_t seq = t + 1;
    if (!attn_seq_ok(ctx, seq)) return -1;
    if (!ensure_dev(&ctx->d_q, &ctx->cap_q, q_dim)) return -1;
    if (!ensure_dev(&ctx->d_k, &ctx->cap_k, kv_dim)) return -1;
    if (!ensure_dev(&ctx->d_v, &ctx->cap_v, kv_dim)) return -1;
    if (!ensure_dev(&ctx->d_attn, &ctx->cap_attn, q_dim)) return -1;
    if (!res_add && !ensure_dev(&ctx->d_y, &ctx->cap_y, hidden)) return -1;

    if (launch_gemv3(ctx, d_wq, d_wk, d_wv, ctx->d_x, ctx->d_q, ctx->d_k, ctx->d_v, hidden, q_dim,
                     kv_dim, ggml_type)
        != 0)
        return -1;

    if (launch_rope_and_store(ctx, layer, pos, n_heads, n_kv_heads, head_dim, rope_theta,
                              rope_freq_scale, rope_mode, t)
        != 0)
        return -1;
    ctx->kv_len[layer] = seq;
    if (launch_gqa_attn(ctx, layer, n_heads, n_kv_heads, head_dim, seq, q_dim) != 0) return -1;
    return launch_gemv_ex(ctx, d_wo, ctx->d_attn, res_add ? nullptr : ctx->d_y, q_dim, hidden,
                          ggml_type, res_add);
}

/* Assumes xn in d_x; writes FFN out into d_y or += res_add. */
static int swiglu_dev(ge_decode_ctx *ctx, const void *d_gate, const void *d_up, const void *d_down,
                      uint32_t hidden, uint32_t inter, uint32_t ggml_type, float *res_add) {
    if (!ensure_dev(&ctx->d_y2, &ctx->cap_y2, inter)) return -1;
    /* R17-gate-up-silu-micro: fused silu writes only d_y2; d_y3 unused */
    if (!res_add && !ensure_dev(&ctx->d_y, &ctx->cap_y, hidden)) return -1;
    if (launch_gemv2(ctx, d_gate, d_up, ctx->d_x, ctx->d_y2, nullptr, hidden, inter, ggml_type,
                     true)
        != 0)
        return -1;
    return launch_gemv_ex(ctx, d_down, ctx->d_y2, res_add ? nullptr : ctx->d_y, inter, hidden,
                          ggml_type, res_add);
}

extern "C" int ge_decode_residual_begin(ge_decode_ctx *ctx, const float *residual_host,
                                        uint32_t hidden) {
    if (!ctx || !residual_host || hidden == 0) return -1;
    if (!set_dev(ctx)) return -1;
    if (!ensure_dev(&ctx->d_res, &ctx->cap_res, hidden)) return -1;
    if (cudaMemcpyAsync(ctx->d_res, residual_host, (size_t)hidden * sizeof(float),
                        cudaMemcpyHostToDevice, ctx->stream) != cudaSuccess)
        return -1;
    return 0;
}

/* Dequant one Q4_0 vocab row (tied emb / lm_head) into d_res — R27.1 device GET_ROWS (no H2D). */
__global__ void q4_0_embed_row_kernel(const half *__restrict__ scales,
                                      const uint8_t *__restrict__ qs, float *__restrict__ out,
                                      uint32_t row, uint32_t n_blocks, uint32_t hidden) {
    const size_t base = (size_t)row * n_blocks;
    for (uint32_t b = blockIdx.x; b < n_blocks; b += gridDim.x) {
        const float d = __half2float(scales[base + b]);
        const uint8_t *qb = qs + (base + b) * GE_Q4_QS_BYTES;
        for (uint32_t j = threadIdx.x; j < 16; j += blockDim.x) {
            const int byte = (int)qb[j];
            const uint32_t i0 = b * 32u + j;
            const uint32_t i1 = i0 + 16u;
            if (i0 < hidden) out[i0] = (float)((byte & 0x0f) - 8) * d;
            if (i1 < hidden) out[i1] = (float)(((byte >> 4) & 0x0f) - 8) * d;
        }
    }
}

/* Device embed into residual (R27.1 / llama #25962): gather Q4_0 row from resident emb/lm_head. */
extern "C" int ge_decode_residual_from_embed(ge_decode_ctx *ctx, const void *d_emb, uint32_t token_id,
                                             uint32_t hidden, uint32_t n_vocab, uint32_t ggml_type) {
    if (!ctx || !d_emb || hidden == 0 || n_vocab == 0 || token_id >= n_vocab) return -1;
    if (ggml_type != 2) return -1; /* Q4_0 only (Green .green requant; Q6_K GET_ROWS = follow-up) */
    if (!set_dev(ctx)) return -1;
    if (!ensure_dev(&ctx->d_res, &ctx->cap_res, hidden)) return -1;
    ge_q4_gpu_view v = q4_view(d_emb, hidden, n_vocab);
    if (v.n_blocks == 0) return -1;
    const int threads = 64;
    const int blocks = (int)((v.n_blocks < 256u) ? v.n_blocks : 256u);
    q4_0_embed_row_kernel<<<blocks, threads, 0, ctx->stream>>>(v.scales, v.qs, ctx->d_res, token_id,
                                                              v.n_blocks, hidden);
    return cudaGetLastError() == cudaSuccess ? 0 : -1;
}

extern "C" int ge_decode_residual_end(ge_decode_ctx *ctx, float *residual_host, uint32_t hidden) {
    if (!ctx || !residual_host || hidden == 0 || !ctx->d_res) return -1;
    if (!set_dev(ctx)) return -1;
    if (cudaMemcpyAsync(residual_host, ctx->d_res, (size_t)hidden * sizeof(float),
                        cudaMemcpyDeviceToHost, ctx->stream) != cudaSuccess)
        return -1;
    return cudaStreamSynchronize(ctx->stream) == cudaSuccess ? 0 : -1;
}

/* Device launches for one fused layer (no ensure_dev). Used by eager + graph capture. */
static int layer_fused_launches(ge_decode_ctx *ctx, const void *d_wq, const void *d_wk,
                                const void *d_wv, const void *d_wo, const void *d_gate,
                                const void *d_up, const void *d_down, const float *d_attn_norm,
                                const float *d_ffn_norm, uint32_t layer, uint32_t pos,
                                uint32_t hidden, uint32_t inter, uint32_t n_heads,
                                uint32_t n_kv_heads, uint32_t head_dim, float rope_theta,
                                float rope_freq_scale, int rope_mode, uint32_t ggml_type,
                                float rms_eps) {
    if (!launch_rmsnorm(ctx, ctx->d_res, d_attn_norm, ctx->d_x, hidden, rms_eps)) return -1;
    if (attn_block_dev(ctx, d_wq, d_wk, d_wv, d_wo, layer, pos, hidden, n_heads, n_kv_heads,
                       head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type, ctx->d_res)
        != 0)
        return -1;
    if (!launch_rmsnorm(ctx, ctx->d_res, d_ffn_norm, ctx->d_x, hidden, rms_eps)) return -1;
    if (swiglu_dev(ctx, d_gate, d_up, d_down, hidden, inter, ggml_type, ctx->d_res) != 0)
        return -1;
    return 0;
}

static bool layer_graph_topo(const ge_layer_graph *g, const void *d_wq, const void *d_wk,
                              const void *d_wv, const void *d_wo, const void *d_gate,
                              const void *d_up, const void *d_down, const float *d_attn_norm,
                              const float *d_ffn_norm, uint32_t hidden, uint32_t inter,
                              uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim,
                              uint32_t ggml_type, float rope_theta, float rope_freq_scale,
                              int rope_mode, float rms_eps) {
    return g->d_wq == d_wq && g->d_wk == d_wk && g->d_wv == d_wv && g->d_wo == d_wo
        && g->d_gate == d_gate && g->d_up == d_up && g->d_down == d_down
        && g->d_attn_norm == d_attn_norm && g->d_ffn_norm == d_ffn_norm && g->hidden == hidden
        && g->inter == inter && g->n_heads == n_heads && g->n_kv_heads == n_kv_heads
        && g->head_dim == head_dim && g->ggml_type == ggml_type && g->rope_theta == rope_theta
        && g->rope_freq_scale == rope_freq_scale && g->rope_mode == rope_mode
        && g->rms_eps == rms_eps;
}

static void layer_graph_store_topo(ge_layer_graph *lg, const void *d_wq, const void *d_wk,
                                   const void *d_wv, const void *d_wo, const void *d_gate,
                                   const void *d_up, const void *d_down, const float *d_attn_norm,
                                   const float *d_ffn_norm, uint32_t hidden, uint32_t inter,
                                   uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim,
                                   uint32_t ggml_type, float rope_theta, float rope_freq_scale,
                                   int rope_mode, float rms_eps) {
    lg->d_wq = d_wq;
    lg->d_wk = d_wk;
    lg->d_wv = d_wv;
    lg->d_wo = d_wo;
    lg->d_gate = d_gate;
    lg->d_up = d_up;
    lg->d_down = d_down;
    lg->d_attn_norm = d_attn_norm;
    lg->d_ffn_norm = d_ffn_norm;
    lg->hidden = hidden;
    lg->inter = inter;
    lg->n_heads = n_heads;
    lg->n_kv_heads = n_kv_heads;
    lg->head_dim = head_dim;
    lg->ggml_type = ggml_type;
    lg->rope_theta = rope_theta;
    lg->rope_freq_scale = rope_freq_scale;
    lg->rope_mode = rope_mode;
    lg->rms_eps = rms_eps;
}

/* Full dense layer on device residual (call between residual_begin/end).
 * Optional CUDA graph (GE_CUDA_GRAPH=1): decode-only after ge_decode_graph_arm,
 * warmup×2 stream (llama #19754), one capture, then replay via device tok args. */
extern "C" int ge_decode_q4_layer_fused(
    ge_decode_ctx *ctx, const void *d_wq, const void *d_wk, const void *d_wv, const void *d_wo,
    const void *d_gate, const void *d_up, const void *d_down, const float *d_attn_norm,
    const float *d_ffn_norm, uint32_t layer, uint32_t pos, uint32_t hidden, uint32_t inter,
    uint32_t n_heads, uint32_t n_kv_heads, uint32_t head_dim, float rope_theta,
    float rope_freq_scale, int rope_mode, uint32_t ggml_type, float rms_eps) {
    if (!ctx || !ctx->d_res || !d_wq || !d_wk || !d_wv || !d_wo || !d_gate || !d_up || !d_down
        || !d_attn_norm || !d_ffn_norm)
        return -1;
    if (hidden == 0 || inter == 0 || n_heads == 0 || n_kv_heads == 0 || head_dim == 0) return -1;
    if (n_heads % n_kv_heads != 0 || (head_dim & 1u) != 0) return -1;
    if (!set_dev(ctx)) return -1;

    uint32_t q_dim = n_heads * head_dim;
    uint32_t kv_dim = n_kv_heads * head_dim;
    if (!ensure_dev(&ctx->d_x, &ctx->cap_x, hidden)) return -1;
    if (!ensure_dev(&ctx->d_q, &ctx->cap_q, q_dim)) return -1;
    if (!ensure_dev(&ctx->d_k, &ctx->cap_k, kv_dim)) return -1;
    if (!ensure_dev(&ctx->d_v, &ctx->cap_v, kv_dim)) return -1;
    if (!ensure_dev(&ctx->d_attn, &ctx->cap_attn, q_dim)) return -1;
    if (!ensure_dev(&ctx->d_y2, &ctx->cap_y2, inter)) return -1;
    /* W27 idle-buffer: swiglu_dev fused-silu path does not use d_y3 — skip prealloc. */

    /* Allow hot_cap when this append cannot evict (ISO default hot_cap=4096). */
    const bool window_ok =
        ctx->kv_hot_cap == 0
        || (ctx->kv_len && layer < ctx->n_kv_layers && ctx->kv_len[layer] + 1u <= ctx->kv_hot_cap);
    /* Q8-only: F16 KV (quiet GE_KV_QUANT=F16 / short GE_CTX auto) makes try_graph
     * false — Wave 28 quiet SOLO BELOW vs ISO Q8 +10.9%. Rust coerces F16→Q8 when
     * GE_CUDA_GRAPH=1 so opt-in graphs actually arm. */
    const bool try_graph = ctx->use_graphs && ctx->graphs_armed && ctx->d_graph_tok
        && layer < GE_MAX_GRAPH_LAYERS && kv_ready(ctx, layer) && ggml_type == 2
        && ctx->kv_dtype == GE_KV_DTYPE_Q8 && window_ok;
    if (!try_graph) {
        return layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                    d_ffn_norm, layer, pos, hidden, inter, n_heads, n_kv_heads,
                                    head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type,
                                    rms_eps);
    }

    ge_layer_graph *lg = &ctx->layer_graphs[layer];
    if (kv_prepare_append(ctx, layer) != 0) return -1;
    lg = &ctx->layer_graphs[layer]; /* eviction may invalidate */
    uint32_t t0 = ctx->kv_len[layer];
    if (!attn_seq_ok(ctx, t0 + 1)) return -1;

    ge_graph_tok_args tok_h{pos, t0, t0 + 1};
    if (cudaMemcpyAsync(ctx->d_graph_tok, &tok_h, sizeof(tok_h), cudaMemcpyHostToDevice, ctx->stream)
        != cudaSuccess)
        return layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                    d_ffn_norm, layer, pos, hidden, inter, n_heads, n_kv_heads,
                                    head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type,
                                    rms_eps);

    const bool topo_ok = layer_graph_topo(lg, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down,
                                          d_attn_norm, d_ffn_norm, hidden, inter, n_heads,
                                          n_kv_heads, head_dim, ggml_type, rope_theta,
                                          rope_freq_scale, rope_mode, rms_eps);

    if (topo_ok && lg->ready && lg->exec) {
        if (cudaGraphLaunch(lg->exec, ctx->stream) == cudaSuccess) {
            ctx->kv_len[layer] = t0 + 1;
            return 0;
        }
        invalidate_layer_graphs(ctx);
        lg = &ctx->layer_graphs[layer];
    }

    if (!lg->ready) {
        if (!topo_ok) lg->warmup_hits = 0;
        if (lg->warmup_hits < GE_GRAPH_WARMUP) {
            lg->warmup_hits += 1;
            layer_graph_store_topo(lg, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                   d_ffn_norm, hidden, inter, n_heads, n_kv_heads, head_dim,
                                   ggml_type, rope_theta, rope_freq_scale, rope_mode, rms_eps);
            /* Eager warmup: grid args only (use_graph_tok stays 0). */
            return layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down,
                                        d_attn_norm, d_ffn_norm, layer, pos, hidden, inter, n_heads,
                                        n_kv_heads, head_dim, rope_theta, rope_freq_scale, rope_mode,
                                        ggml_type, rms_eps);
        }
    }

    cudaGraph_t graph = nullptr;
    ctx->use_graph_tok = 1;
    if (cudaStreamBeginCapture(ctx->stream, cudaStreamCaptureModeGlobal) != cudaSuccess) {
        ctx->use_graph_tok = 0;
        return layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                    d_ffn_norm, layer, pos, hidden, inter, n_heads, n_kv_heads,
                                    head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type,
                                    rms_eps);
    }

    int rc = layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                  d_ffn_norm, layer, pos, hidden, inter, n_heads, n_kv_heads,
                                  head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type,
                                  rms_eps);
    cudaError_t end_st = cudaStreamEndCapture(ctx->stream, &graph);
    ctx->use_graph_tok = 0;
    ctx->kv_len[layer] = t0;
    if (rc != 0 || end_st != cudaSuccess || !graph) {
        if (graph) cudaGraphDestroy(graph);
        return layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                    d_ffn_norm, layer, pos, hidden, inter, n_heads, n_kv_heads,
                                    head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type,
                                    rms_eps);
    }

    if (lg->exec) {
        cudaGraphExecDestroy(lg->exec);
        lg->exec = nullptr;
    }
    if (cudaGraphInstantiate(&lg->exec, graph, nullptr, nullptr, 0) != cudaSuccess) {
        cudaGraphDestroy(graph);
        lg->ready = false;
        return layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                    d_ffn_norm, layer, pos, hidden, inter, n_heads, n_kv_heads,
                                    head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type,
                                    rms_eps);
    }
    cudaGraphDestroy(graph);
    layer_graph_store_topo(lg, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                           d_ffn_norm, hidden, inter, n_heads, n_kv_heads, head_dim, ggml_type,
                           rope_theta, rope_freq_scale, rope_mode, rms_eps);
    lg->ready = true;

    if (cudaGraphLaunch(lg->exec, ctx->stream) != cudaSuccess) {
        lg->ready = false;
        if (lg->exec) {
            cudaGraphExecDestroy(lg->exec);
            lg->exec = nullptr;
        }
        return layer_fused_launches(ctx, d_wq, d_wk, d_wv, d_wo, d_gate, d_up, d_down, d_attn_norm,
                                    d_ffn_norm, layer, pos, hidden, inter, n_heads, n_kv_heads,
                                    head_dim, rope_theta, rope_freq_scale, rope_mode, ggml_type,
                                    rms_eps);
    }
    ctx->kv_len[layer] = t0 + 1;
    return 0;
}

/* Greedy next-token from device residual: optional RMSNorm + lm_head GEMV + argmax.
 * d_output_norm may be null (skip norm). Does not download residual. */
extern "C" int ge_decode_residual_argmax(ge_decode_ctx *ctx, const void *d_lm,
                                         const float *d_output_norm, uint32_t *best_out,
                                         uint32_t hidden, uint32_t n_vocab, uint32_t ggml_type,
                                         float rms_eps) {
    if (!ctx || !ctx->d_res || !d_lm || !best_out || hidden == 0 || n_vocab == 0) return -1;
    if (!set_dev(ctx)) return -1;
    if (!ensure_dev(&ctx->d_x, &ctx->cap_x, hidden)) return -1;
    if (d_output_norm) {
        if (!launch_rmsnorm(ctx, ctx->d_res, d_output_norm, ctx->d_x, hidden, rms_eps)) return -1;
    } else {
        if (cudaMemcpyAsync(ctx->d_x, ctx->d_res, (size_t)hidden * sizeof(float),
                            cudaMemcpyDeviceToDevice, ctx->stream) != cudaSuccess)
            return -1;
    }
    if (!ensure_dev(&ctx->d_y, &ctx->cap_y, n_vocab)) return -1;
    if (launch_gemv(ctx, d_lm, ctx->d_x, ctx->d_y, hidden, n_vocab, ggml_type) != 0) return -1;
    return launch_argmax_from_y(ctx, n_vocab, best_out);
}
