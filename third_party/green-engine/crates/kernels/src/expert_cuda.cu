// Green Engine — CUDA implementation of the expert-FFN kernel ABI.
//
// Same C ABI as expert_cpu_ref.cpp (green_engine_kernels.h), so the Rust `--features gpu`
// build links either transparently. Build with nvcc (see Makefile `cuda` target).
//
// Contract (matches the CPU reference): the pointers handed in are HOST f32. This backend
// uploads them to VRAM, computes on device, and copies the result back — so it runs correctly
// on a discrete GPU without relying on the driver faulting pageable host memory over PCIe
// (HMM), which the previous sketch did and which cost ~60x the CPU backend.
//
// What makes it fast on GPU:
//   * An N-slot device-residency cache holds a WORKING SET of hot experts' weights in VRAM
//     (fixed-address slot buffers), keyed by weight host pointer. A resident expert skips the
//     H2D entirely — the cache-hit fast path the ABI is built around. MoE routing is sparse and
//     bursty (~15-20% of experts serve ~80% of tokens), so after a short warm-up a modest cache
//     drives per-token PCIe traffic toward zero. Capacity = GE_GPU_RESIDENT (default 8); it caps
//     VRAM at n_slots * 3 * hidden*inter * 4 bytes, so it is the knob that trades VRAM for hits.
//     Eviction is LRU. This is the GPU tier of the same cache the engine-core scheduler models.
//   * gate, up and the SiLU gate are FUSED into one kernel: it reads x once (shared memory),
//     accumulates both projections, applies SiLU in registers, writes only the [inter]
//     intermediate — eliminating two [inter] global buffers and two launches.
//   * Each resident slot owns a captured CUDA graph of the per-token pipeline (x H2D -> 2 kernels
//     -> y D2H); a cache hit replays it as one cudaGraphLaunch, collapsing per-call submit
//     overhead that dominates batch-1 decode. GE_GPU_NOGRAPH=1 forces the direct 2-launch path.
//   * GE_GPU_FP16=1 stores each resident expert's weights as __half in VRAM (half the bytes) and
//     converts to float in-register inside the GEMVs. Because the resident (cache-hit) path is
//     bandwidth-bound — the kernels sit near the weights-resident VRAM-bandwidth floor — halving
//     the weight bytes read per token both HALVES the VRAM the working set costs (12 vs 24 MiB per
//     OLMoE expert) and cuts kernel time, at ~1e-3 drift (fp16 has ~3 decimal digits). The f32
//     path is the default and stays bit-for-bit; fp16 is opt-in. Conversion happens on-device on a
//     cache miss (upload f32 to a shared stage buffer, launch f2h into the slot's half buffers), so
//     PCIe upload traffic and the host are untouched — only the persistent slot footprint shrinks.

#include "green_engine_kernels.h"
#include <cuda_runtime.h>
#include <cuda_fp16.h>
#include <cstdio>
#include <cstdlib>
#include <cstring>

// One resident expert: its three weight matrices in VRAM, the host-pointer identity that keys it,
// and a captured graph specialized to those device buffers.
struct WeightSlot {
    const void *k_gate = nullptr, *k_up = nullptr, *k_down = nullptr;
    unsigned k_hidden = 0, k_inter = 0;
    float *d_gate = nullptr, *d_up = nullptr, *d_down = nullptr;      // f32 mode
    __half *h_gate = nullptr, *h_up = nullptr, *h_down = nullptr;     // fp16 mode (GE_GPU_FP16=1)
    int8_t *q_gate = nullptr, *q_up = nullptr, *q_down = nullptr;     // int8 mode (GE_GPU_INT8=1)
    uint8_t *p_gate = nullptr, *p_up = nullptr, *p_down = nullptr;    // int4 mode (GE_GPU_INT4=1, packed)
    float *s_gate = nullptr, *s_up = nullptr, *s_down = nullptr;      // per-output-channel scales (int8/int4)
    size_t cap = 0; // elements backing each matrix (hidden*inter)
    cudaGraphExec_t graph = nullptr;
    bool graph_valid = false;
    unsigned g_hidden = 0, g_inter = 0;
    unsigned long long lru = 0; // last-use clock; 0 = never used (empty)
};

struct ge_ctx {
    int device_id;
    cudaStream_t stream;
    WeightSlot *slots = nullptr;
    int n_slots = 0;
    unsigned long long clock = 0;
    // Shared per-call scratch, grown on demand (the intermediate h and the x/y vectors are the
    // same for every expert, so they live once, not per slot).
    float *d_x = nullptr, *d_h = nullptr, *d_y = nullptr;
    float *h_x = nullptr, *h_y = nullptr; // pinned host staging for true async x/y DMA
    size_t cap_h = 0;   // elements backing d_h  (inter)
    size_t cap_vec = 0; // elements backing d_x/d_y and pinned h_x/h_y (hidden)
    bool use_graph = true; // GE_GPU_NOGRAPH=1 forces the direct 2-launch path (also a fallback)
    bool use_fp16 = false; // GE_GPU_FP16=1 keeps resident weights as __half (half the VRAM)
    bool use_int8 = false; // GE_GPU_INT8=1 keeps resident weights as int8 per-channel (quarter VRAM)
    bool use_int4 = false; // GE_GPU_INT4=1 keeps resident weights as int4 group-wise (~8× smaller)
    unsigned int4_group = 128; // GE_GPU_INT4_G: int4 scale group size (finer = higher quality)
    float *d_stage = nullptr; // shared f32 upload buffer for on-device narrow/quantize
    size_t cap_stage = 0;     // elements backing d_stage (hidden*inter)
};

// Grow a device buffer to hold at least `need` elements (no-op if already big enough).
static bool ensure(float **p, size_t *cap, size_t need, cudaStream_t s) {
    if (*cap >= need && *p) return true;
    if (*p) cudaFreeAsync(*p, s);
    if (cudaMallocAsync((void **)p, need * sizeof(float), s) != cudaSuccess) {
        *p = nullptr;
        *cap = 0;
        return false;
    }
    *cap = need;
    return true;
}

extern "C" ge_ctx *ge_ctx_create(int device_id) {
    ge_ctx *c = new ge_ctx();
    c->device_id = device_id;
    cudaSetDevice(device_id);
    cudaStreamCreate(&c->stream);
    const char *ng = getenv("GE_GPU_NOGRAPH");
    c->use_graph = !(ng && ng[0] == '1');
    const char *fp = getenv("GE_GPU_FP16");
    c->use_fp16 = (fp && fp[0] == '1');
    const char *i8 = getenv("GE_GPU_INT8");
    c->use_int8 = (i8 && i8[0] == '1');
    const char *i4 = getenv("GE_GPU_INT4");
    c->use_int4 = (i4 && i4[0] == '1');
    if (const char *g = getenv("GE_GPU_INT4_G")) { int v = atoi(g); if (v >= 1) c->int4_group = (unsigned)v; }
    // Precedence (mutually exclusive): int4 > int8 > fp16 > f32.
    if (c->use_int4) { c->use_int8 = false; c->use_fp16 = false; }
    else if (c->use_int8) c->use_fp16 = false;
    int n = 8;
    if (const char *r = getenv("GE_GPU_RESIDENT")) {
        n = atoi(r);
        if (n < 1) n = 1;
        if (n > 64) n = 64;
    }
    c->n_slots = n;
    c->slots = new WeightSlot[n];
    return c;
}

extern "C" void ge_ctx_destroy(ge_ctx *ctx) {
    if (!ctx) return;
    for (int i = 0; i < ctx->n_slots; ++i) {
        WeightSlot &sl = ctx->slots[i];
        if (sl.graph) cudaGraphExecDestroy(sl.graph);
        if (sl.d_gate) cudaFreeAsync(sl.d_gate, ctx->stream);
        if (sl.d_up) cudaFreeAsync(sl.d_up, ctx->stream);
        if (sl.d_down) cudaFreeAsync(sl.d_down, ctx->stream);
        if (sl.h_gate) cudaFreeAsync(sl.h_gate, ctx->stream);
        if (sl.h_up) cudaFreeAsync(sl.h_up, ctx->stream);
        if (sl.h_down) cudaFreeAsync(sl.h_down, ctx->stream);
        if (sl.q_gate) cudaFreeAsync(sl.q_gate, ctx->stream);
        if (sl.q_up) cudaFreeAsync(sl.q_up, ctx->stream);
        if (sl.q_down) cudaFreeAsync(sl.q_down, ctx->stream);
        if (sl.p_gate) cudaFreeAsync(sl.p_gate, ctx->stream);
        if (sl.p_up) cudaFreeAsync(sl.p_up, ctx->stream);
        if (sl.p_down) cudaFreeAsync(sl.p_down, ctx->stream);
        if (sl.s_gate) cudaFreeAsync(sl.s_gate, ctx->stream);
        if (sl.s_up) cudaFreeAsync(sl.s_up, ctx->stream);
        if (sl.s_down) cudaFreeAsync(sl.s_down, ctx->stream);
    }
    cudaFreeAsync(ctx->d_x, ctx->stream);
    cudaFreeAsync(ctx->d_h, ctx->stream);
    cudaFreeAsync(ctx->d_y, ctx->stream);
    if (ctx->d_stage) cudaFreeAsync(ctx->d_stage, ctx->stream);
    cudaStreamSynchronize(ctx->stream);
    if (ctx->h_x) cudaFreeHost(ctx->h_x);
    if (ctx->h_y) cudaFreeHost(ctx->h_y);
    cudaStreamDestroy(ctx->stream);
    delete[] ctx->slots;
    delete ctx;
}

// Fused gate+up+SiLU: h[j] = silu(x·gate[:,j]) * (x·up[:,j]).
// One thread per output column j; x is cached in shared memory and read once from global.
//   gate,up : [hidden*inter] row-major (element [i*inter + j])
//   x       : [hidden]   ->  h : [inter]
__global__ void fused_gate_up_silu(const float *__restrict gate, const float *__restrict up,
                                   const float *__restrict x, float *__restrict h,
                                   unsigned hidden, unsigned inter) {
    extern __shared__ float sx[]; // [hidden]
    for (unsigned i = threadIdx.x; i < hidden; i += blockDim.x) sx[i] = x[i];
    __syncthreads();

    unsigned j = blockIdx.x * blockDim.x + threadIdx.x;
    if (j >= inter) return;
    float g = 0.0f, u = 0.0f;
    for (unsigned i = 0; i < hidden; ++i) {
        const float xi = sx[i];
        const size_t off = (size_t)i * inter + j;
        g += xi * gate[off];
        u += xi * up[off];
    }
    h[j] = (g / (1.0f + __expf(-g))) * u; // SiLU in registers, single global write
}

// Down projection: y[o] = h·down[:,o], with h cached in shared memory.
//   down : [inter*hidden] row-major (element [j*hidden + o])
//   h    : [inter]   ->  y : [hidden]
__global__ void down_gemv(const float *__restrict h, const float *__restrict down,
                          float *__restrict y, unsigned inter, unsigned hidden) {
    extern __shared__ float sh[]; // [inter]
    for (unsigned j = threadIdx.x; j < inter; j += blockDim.x) sh[j] = h[j];
    __syncthreads();

    unsigned o = blockIdx.x * blockDim.x + threadIdx.x;
    if (o >= hidden) return;
    float acc = 0.0f;
    for (unsigned j = 0; j < inter; ++j) acc += sh[j] * down[(size_t)j * hidden + o];
    y[o] = acc;
}

// --- FP16 resident path (GE_GPU_FP16=1) -------------------------------------------------------
// Same math as above, but weights live in VRAM as __half and are widened to float per-element in
// registers during accumulation. Halves the resident VRAM and the weight bytes read per token.

// f32 -> f16 narrowing, used once per matrix on a cache miss to fill a slot's half buffers.
__global__ void f2h(const float *__restrict src, __half *__restrict dst, size_t n) {
    size_t i = (size_t)blockIdx.x * blockDim.x + threadIdx.x;
    if (i < n) dst[i] = __float2half(src[i]);
}

// Fused gate+up+SiLU reading __half weights (see fused_gate_up_silu for the f32 twin).
__global__ void fused_gate_up_silu_h(const __half *__restrict gate, const __half *__restrict up,
                                     const float *__restrict x, float *__restrict h,
                                     unsigned hidden, unsigned inter) {
    extern __shared__ float sx[]; // [hidden]
    for (unsigned i = threadIdx.x; i < hidden; i += blockDim.x) sx[i] = x[i];
    __syncthreads();

    unsigned j = blockIdx.x * blockDim.x + threadIdx.x;
    if (j >= inter) return;
    float g = 0.0f, u = 0.0f;
    for (unsigned i = 0; i < hidden; ++i) {
        const float xi = sx[i];
        const size_t off = (size_t)i * inter + j;
        g += xi * __half2float(gate[off]);
        u += xi * __half2float(up[off]);
    }
    h[j] = (g / (1.0f + __expf(-g))) * u;
}

// Down projection reading __half weights (see down_gemv for the f32 twin).
__global__ void down_gemv_h(const float *__restrict h, const __half *__restrict down,
                            float *__restrict y, unsigned inter, unsigned hidden) {
    extern __shared__ float sh[]; // [inter]
    for (unsigned j = threadIdx.x; j < inter; j += blockDim.x) sh[j] = h[j];
    __syncthreads();

    unsigned o = blockIdx.x * blockDim.x + threadIdx.x;
    if (o >= hidden) return;
    float acc = 0.0f;
    for (unsigned j = 0; j < inter; ++j) acc += sh[j] * __half2float(down[(size_t)j * hidden + o]);
    y[o] = acc;
}

// --- INT8 per-channel resident path (GE_GPU_INT8=1) -------------------------------------------
// Weights live in VRAM as int8 with one f32 scale per OUTPUT channel (column) — the same symmetric
// per-column scheme as the CPU Q8Ch tier (weights.rs), so fidelity matches. Quarter the resident
// VRAM (6 vs 24 MiB per OLMoE expert). Layout is [inn, out] row-major (element [i*out + col]); the
// scale is shared down a column, so each GEMV thread owns one output column, sums the int8 products
// in float, then applies its single column scale once at the end.

// Per-output-column absmax -> symmetric scale (amax/127). One thread per column `col` in [0,out).
__global__ void col_absmax(const float *__restrict src, float *__restrict scale,
                           unsigned inn, unsigned out) {
    unsigned col = blockIdx.x * blockDim.x + threadIdx.x;
    if (col >= out) return;
    float amax = 0.0f;
    for (unsigned i = 0; i < inn; ++i) amax = fmaxf(amax, fabsf(src[(size_t)i * out + col]));
    scale[col] = (amax > 0.0f) ? (amax / 127.0f) : 1.0f;
}

// Quantize f32 -> int8 with the per-column scale (element `idx` is in column `idx % out`).
__global__ void f2i8(const float *__restrict src, int8_t *__restrict dst,
                     const float *__restrict scale, size_t n, unsigned out) {
    size_t idx = (size_t)blockIdx.x * blockDim.x + threadIdx.x;
    if (idx >= n) return;
    float v = src[idx] / scale[idx % out];
    v = rintf(v);
    v = fminf(127.0f, fmaxf(-127.0f, v));
    dst[idx] = (int8_t)v;
}

// Fused gate+up+SiLU reading int8 weights + per-column scales (see fused_gate_up_silu for the twin).
__global__ void fused_gate_up_silu_i8(const int8_t *__restrict gate, const float *__restrict sgate,
                                      const int8_t *__restrict up, const float *__restrict sup,
                                      const float *__restrict x, float *__restrict h,
                                      unsigned hidden, unsigned inter) {
    extern __shared__ float sx[]; // [hidden]
    for (unsigned i = threadIdx.x; i < hidden; i += blockDim.x) sx[i] = x[i];
    __syncthreads();

    unsigned j = blockIdx.x * blockDim.x + threadIdx.x;
    if (j >= inter) return;
    float g = 0.0f, u = 0.0f;
    for (unsigned i = 0; i < hidden; ++i) {
        const float xi = sx[i];
        const size_t off = (size_t)i * inter + j;
        g += xi * (float)gate[off];
        u += xi * (float)up[off];
    }
    g *= sgate[j];
    u *= sup[j]; // apply each column's scale once, after the integer-weighted sum
    h[j] = (g / (1.0f + __expf(-g))) * u;
}

// Down projection reading int8 weights + per-column scales (see down_gemv for the twin).
__global__ void down_gemv_i8(const float *__restrict h, const int8_t *__restrict down,
                             const float *__restrict sdown, float *__restrict y,
                             unsigned inter, unsigned hidden) {
    extern __shared__ float sh[]; // [inter]
    for (unsigned j = threadIdx.x; j < inter; j += blockDim.x) sh[j] = h[j];
    __syncthreads();

    unsigned o = blockIdx.x * blockDim.x + threadIdx.x;
    if (o >= hidden) return;
    float acc = 0.0f;
    for (unsigned j = 0; j < inter; ++j) acc += sh[j] * (float)down[(size_t)j * hidden + o];
    y[o] = acc * sdown[o];
}

// --- INT4 GROUP-WISE resident path (GE_GPU_INT4=1, GE_GPU_INT4_G=group) ------------------------
// Two signed 4-bit weights ([-8,7]) packed per byte. Scales are GROUP-WISE: for each output column,
// its `inn` input elements are split into groups of `group`, one scale (amax/7) per group. A finer
// scale tracks the local magnitude far better than one per whole column, so int4 quality roughly
// doubles vs per-channel (the 2026 best-practice; matches the CPU Q4G tier). ~8× smaller than f32.
// Scale index for element (i, col): col * n_groups + i/group,  n_groups = ceil(inn/group).

// Per-(column, group) absmax -> int4 scale. One thread per (col, grp) pair (t = col*n_groups + grp).
__global__ void group_scale4(const float *__restrict src, float *__restrict scale,
                             unsigned inn, unsigned out, unsigned group, unsigned n_groups) {
    unsigned t = blockIdx.x * blockDim.x + threadIdx.x;
    if (t >= out * n_groups) return;
    unsigned col = t / n_groups, grp = t % n_groups;
    unsigned i0 = grp * group, i1 = min(i0 + group, inn);
    float amax = 0.0f;
    for (unsigned i = i0; i < i1; ++i) amax = fmaxf(amax, fabsf(src[(size_t)i * out + col]));
    scale[t] = (amax > 0.0f) ? (amax / 7.0f) : 1.0f;
}

// Group scale index for flat element `idx` in an [inn,out] matrix.
__device__ __forceinline__ size_t sidx4(size_t idx, unsigned out, unsigned group, unsigned n_groups) {
    unsigned col = (unsigned)(idx % out);
    unsigned grp = (unsigned)((idx / out) / group);
    return (size_t)col * n_groups + grp;
}

// Quantize + pack: one thread per output byte (two consecutive elements, each with its group scale).
__global__ void f2i4(const float *__restrict src, uint8_t *__restrict dst,
                     const float *__restrict scale, size_t n, unsigned out, unsigned group, unsigned n_groups) {
    size_t b = (size_t)blockIdx.x * blockDim.x + threadIdx.x;
    size_t i0 = 2 * b;
    if (i0 >= n) return;
    float v0 = rintf(src[i0] / scale[sidx4(i0, out, group, n_groups)]);
    int q0 = (int)fminf(7.0f, fmaxf(-8.0f, v0)) & 0xF;
    int q1 = 0;
    if (i0 + 1 < n) {
        float v1 = rintf(src[i0 + 1] / scale[sidx4(i0 + 1, out, group, n_groups)]);
        q1 = (int)fminf(7.0f, fmaxf(-8.0f, v1)) & 0xF;
    }
    dst[b] = (uint8_t)(q0 | (q1 << 4));
}

// Read one signed int4 weight at flat index `idx` from the packed buffer.
__device__ __forceinline__ float deq4(const uint8_t *__restrict p, size_t idx) {
    uint8_t byte = p[idx >> 1];
    int nib = (idx & 1) ? (byte >> 4) : (byte & 0xF);
    nib = (nib ^ 0x8) - 0x8; // sign-extend 4-bit two's complement to [-8,7]
    return (float)nib;
}

// Fused gate+up+SiLU reading packed int4 + group scales (scale applied per element, as it varies
// within a column). `ng` = n_groups for gate/up (both [hidden,inter]).
__global__ void fused_gate_up_silu_i4(const uint8_t *__restrict gate, const float *__restrict sgate,
                                      const uint8_t *__restrict up, const float *__restrict sup,
                                      const float *__restrict x, float *__restrict h,
                                      unsigned hidden, unsigned inter, unsigned group, unsigned ng) {
    extern __shared__ float sx[]; // [hidden]
    for (unsigned i = threadIdx.x; i < hidden; i += blockDim.x) sx[i] = x[i];
    __syncthreads();

    unsigned j = blockIdx.x * blockDim.x + threadIdx.x;
    if (j >= inter) return;
    float g = 0.0f, u = 0.0f;
    for (unsigned i = 0; i < hidden; ++i) {
        const float xi = sx[i];
        const size_t off = (size_t)i * inter + j;
        const size_t si = (size_t)j * ng + (i / group); // scale index for column j, group i/group
        g += xi * deq4(gate, off) * sgate[si];
        u += xi * deq4(up, off) * sup[si];
    }
    h[j] = (g / (1.0f + __expf(-g))) * u;
}

// Down projection reading packed int4 + group scales. `ng` = n_groups for down ([inter,hidden]).
__global__ void down_gemv_i4(const float *__restrict h, const uint8_t *__restrict down,
                             const float *__restrict sdown, float *__restrict y,
                             unsigned inter, unsigned hidden, unsigned group, unsigned ng) {
    extern __shared__ float sh[]; // [inter]
    for (unsigned j = threadIdx.x; j < inter; j += blockDim.x) sh[j] = h[j];
    __syncthreads();

    unsigned o = blockIdx.x * blockDim.x + threadIdx.x;
    if (o >= hidden) return;
    float acc = 0.0f;
    for (unsigned j = 0; j < inter; ++j) {
        const size_t si = (size_t)o * ng + (j / group); // scale index for column o, group j/group
        acc += sh[j] * deq4(down, (size_t)j * hidden + o) * sdown[si];
    }
    y[o] = acc;
}

// Capture the per-token pipeline for one resident slot: x H2D -> fused gate+up+SiLU -> down ->
// y D2H, reading that slot's weight buffers. Replayed as a single cudaGraphLaunch on a cache hit.
static bool build_graph(ge_ctx *c, WeightSlot *sl, unsigned hidden, unsigned inter) {
    cudaStream_t s = c->stream;
    if (sl->graph) {
        cudaGraphExecDestroy(sl->graph);
        sl->graph = nullptr;
    }
    const unsigned T = 256;
    cudaGraph_t g = nullptr;
    if (cudaStreamBeginCapture(s, cudaStreamCaptureModeThreadLocal) != cudaSuccess) return false;
    cudaMemcpyAsync(c->d_x, c->h_x, hidden * sizeof(float), cudaMemcpyHostToDevice, s);
    if (c->use_int4) {
        const unsigned G = c->int4_group;
        const unsigned ng_gu = (hidden + G - 1) / G, ng_down = (inter + G - 1) / G;
        fused_gate_up_silu_i4<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
            sl->p_gate, sl->s_gate, sl->p_up, sl->s_up, c->d_x, c->d_h, hidden, inter, G, ng_gu);
        down_gemv_i4<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
            c->d_h, sl->p_down, sl->s_down, c->d_y, inter, hidden, G, ng_down);
    } else if (c->use_int8) {
        fused_gate_up_silu_i8<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
            sl->q_gate, sl->s_gate, sl->q_up, sl->s_up, c->d_x, c->d_h, hidden, inter);
        down_gemv_i8<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
            c->d_h, sl->q_down, sl->s_down, c->d_y, inter, hidden);
    } else if (c->use_fp16) {
        fused_gate_up_silu_h<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
            sl->h_gate, sl->h_up, c->d_x, c->d_h, hidden, inter);
        down_gemv_h<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
            c->d_h, sl->h_down, c->d_y, inter, hidden);
    } else {
        fused_gate_up_silu<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
            sl->d_gate, sl->d_up, c->d_x, c->d_h, hidden, inter);
        down_gemv<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
            c->d_h, sl->d_down, c->d_y, inter, hidden);
    }
    cudaMemcpyAsync(c->h_y, c->d_y, hidden * sizeof(float), cudaMemcpyDeviceToHost, s);
    if (cudaStreamEndCapture(s, &g) != cudaSuccess || !g) return false;
    cudaError_t err = cudaGraphInstantiate(&sl->graph, g, 0);
    cudaGraphDestroy(g);
    if (err != cudaSuccess) {
        sl->graph = nullptr;
        return false;
    }
    sl->graph_valid = true;
    sl->g_hidden = hidden;
    sl->g_inter = inter;
    return true;
}

// y = down( silu(x @ gate) * (x @ up) ).  gate/up:[hidden*inter], down:[inter*hidden], x,y:[hidden].
extern "C" int ge_gpu_compute_expert(ge_ctx *ctx,
                                     const float *gate, const float *up, const float *down,
                                     const float *x, float *y,
                                     unsigned hidden, unsigned inter) {
    if (!ctx) return 1;
    const size_t mat = (size_t)hidden * inter;
    cudaStream_t s = ctx->stream;

    // Shared x/y + pinned staging (grown together under cap_vec) and the h intermediate. If any
    // shared buffer moves, every slot's captured graph (which baked these pointers) is void.
    bool shared_grew = false;
    if (ctx->cap_vec < hidden) {
        if (ctx->d_x) cudaFreeAsync(ctx->d_x, s);
        if (ctx->d_y) cudaFreeAsync(ctx->d_y, s);
        if (ctx->h_x) cudaFreeHost(ctx->h_x);
        if (ctx->h_y) cudaFreeHost(ctx->h_y);
        if (cudaMallocAsync((void **)&ctx->d_x, hidden * sizeof(float), s) != cudaSuccess ||
            cudaMallocAsync((void **)&ctx->d_y, hidden * sizeof(float), s) != cudaSuccess ||
            cudaMallocHost((void **)&ctx->h_x, hidden * sizeof(float)) != cudaSuccess ||
            cudaMallocHost((void **)&ctx->h_y, hidden * sizeof(float)) != cudaSuccess) {
            return 2;
        }
        ctx->cap_vec = hidden;
        shared_grew = true;
    }
    size_t prev_h = ctx->cap_h;
    if (!ensure(&ctx->d_h, &ctx->cap_h, inter, s)) return 2;
    if (ctx->cap_h != prev_h) shared_grew = true;
    if (shared_grew) {
        for (int i = 0; i < ctx->n_slots; ++i) ctx->slots[i].graph_valid = false;
    }

    // Look up the expert in the residency cache (hit = weights already in VRAM).
    WeightSlot *active = nullptr;
    for (int i = 0; i < ctx->n_slots; ++i) {
        WeightSlot &sl = ctx->slots[i];
        if (sl.k_gate == gate && sl.k_up == up && sl.k_down == down &&
            sl.k_hidden == hidden && sl.k_inter == inter) {
            active = &sl;
            break;
        }
    }
    if (!active) {
        // Miss: evict the least-recently-used slot and upload this expert into it.
        WeightSlot *victim = &ctx->slots[0];
        for (int i = 1; i < ctx->n_slots; ++i) {
            if (ctx->slots[i].lru < victim->lru) victim = &ctx->slots[i];
        }
        if (ctx->use_int4) {
            // Persistent slot holds packed int4 weights + GROUP-WISE scales (~8× smaller, higher
            // quality than per-column). Upload f32 to the stage, compute group scales, quantize+pack.
            size_t packed = (mat + 1) / 2;
            const unsigned G = ctx->int4_group;
            const unsigned ng_gu = (hidden + G - 1) / G, ng_down = (inter + G - 1) / G;
            // scales: gate/up have `inter` columns × ng_gu groups; down has `hidden` × ng_down.
            const size_t sc_gu = (size_t)inter * ng_gu, sc_dn = (size_t)hidden * ng_down;
            if (victim->cap < mat) {
                if (victim->p_gate) cudaFreeAsync(victim->p_gate, s);
                if (victim->p_up) cudaFreeAsync(victim->p_up, s);
                if (victim->p_down) cudaFreeAsync(victim->p_down, s);
                if (victim->s_gate) cudaFreeAsync(victim->s_gate, s);
                if (victim->s_up) cudaFreeAsync(victim->s_up, s);
                if (victim->s_down) cudaFreeAsync(victim->s_down, s);
                if (cudaMallocAsync((void **)&victim->p_gate, packed, s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->p_up, packed, s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->p_down, packed, s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->s_gate, sc_gu * sizeof(float), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->s_up, sc_gu * sizeof(float), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->s_down, sc_dn * sizeof(float), s) != cudaSuccess) {
                    return 2;
                }
                victim->cap = mat;
            }
            if (!ensure(&ctx->d_stage, &ctx->cap_stage, mat, s)) return 2;
            const unsigned CT = 256;
            const unsigned nb_pack = (unsigned)((packed + CT - 1) / CT); // one thread per output byte
            const float *srcs[3] = {gate, up, down};
            uint8_t *dsts[3] = {victim->p_gate, victim->p_up, victim->p_down};
            float *scs[3] = {victim->s_gate, victim->s_up, victim->s_down};
            unsigned inns[3] = {hidden, hidden, inter};
            unsigned outs[3] = {inter, inter, hidden};
            unsigned ngs[3] = {ng_gu, ng_gu, ng_down};
            for (int m = 0; m < 3; ++m) {
                cudaMemcpyAsync(ctx->d_stage, srcs[m], mat * sizeof(float), cudaMemcpyHostToDevice, s);
                unsigned nscale = outs[m] * ngs[m];
                group_scale4<<<(nscale + CT - 1) / CT, CT, 0, s>>>(ctx->d_stage, scs[m], inns[m], outs[m], G, ngs[m]);
                f2i4<<<nb_pack, CT, 0, s>>>(ctx->d_stage, dsts[m], scs[m], mat, outs[m], G, ngs[m]); // stream serializes
            }
            victim->k_gate = gate;
            victim->k_up = up;
            victim->k_down = down;
            victim->k_hidden = hidden;
            victim->k_inter = inter;
            victim->graph_valid = false;
            active = victim;
        } else if (ctx->use_int8) {
            // Persistent slot holds int8 weights + per-output-column scales (quarter the VRAM).
            // Upload each f32 matrix to the shared stage buffer, reduce per-column absmax into the
            // scale, then quantize into the slot's int8 buffer — all on-device.
            if (victim->cap < mat) {
                if (victim->q_gate) cudaFreeAsync(victim->q_gate, s);
                if (victim->q_up) cudaFreeAsync(victim->q_up, s);
                if (victim->q_down) cudaFreeAsync(victim->q_down, s);
                if (victim->s_gate) cudaFreeAsync(victim->s_gate, s);
                if (victim->s_up) cudaFreeAsync(victim->s_up, s);
                if (victim->s_down) cudaFreeAsync(victim->s_down, s);
                if (cudaMallocAsync((void **)&victim->q_gate, mat, s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->q_up, mat, s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->q_down, mat, s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->s_gate, inter * sizeof(float), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->s_up, inter * sizeof(float), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->s_down, hidden * sizeof(float), s) != cudaSuccess) {
                    return 2;
                }
                victim->cap = mat;
            }
            if (!ensure(&ctx->d_stage, &ctx->cap_stage, mat, s)) return 2;
            const unsigned CT = 256;
            const unsigned nb = (unsigned)((mat + CT - 1) / CT);
            // gate/up: [hidden,inter] -> inter output columns; down: [inter,hidden] -> hidden columns.
            const float *srcs[3] = {gate, up, down};
            int8_t *qs[3] = {victim->q_gate, victim->q_up, victim->q_down};
            float *scs[3] = {victim->s_gate, victim->s_up, victim->s_down};
            unsigned inns[3] = {hidden, hidden, inter};
            unsigned outs[3] = {inter, inter, hidden};
            for (int m = 0; m < 3; ++m) {
                cudaMemcpyAsync(ctx->d_stage, srcs[m], mat * sizeof(float), cudaMemcpyHostToDevice, s);
                col_absmax<<<(outs[m] + CT - 1) / CT, CT, 0, s>>>(ctx->d_stage, scs[m], inns[m], outs[m]);
                f2i8<<<nb, CT, 0, s>>>(ctx->d_stage, qs[m], scs[m], mat, outs[m]); // stream serializes
            }
            victim->k_gate = gate;
            victim->k_up = up;
            victim->k_down = down;
            victim->k_hidden = hidden;
            victim->k_inter = inter;
            victim->graph_valid = false;
            active = victim;
        } else if (ctx->use_fp16) {
            // Persistent slot holds __half weights (half the VRAM). Upload the f32 matrix into a
            // shared stage buffer, then narrow it in place into the slot's half buffers on-device.
            if (victim->cap < mat) {
                if (victim->h_gate) cudaFreeAsync(victim->h_gate, s);
                if (victim->h_up) cudaFreeAsync(victim->h_up, s);
                if (victim->h_down) cudaFreeAsync(victim->h_down, s);
                if (cudaMallocAsync((void **)&victim->h_gate, mat * sizeof(__half), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->h_up, mat * sizeof(__half), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->h_down, mat * sizeof(__half), s) != cudaSuccess) {
                    return 2;
                }
                victim->cap = mat;
            }
            if (!ensure(&ctx->d_stage, &ctx->cap_stage, mat, s)) return 2;
            const unsigned CT = 256;
            const unsigned nb = (unsigned)((mat + CT - 1) / CT);
            const float *srcs[3] = {gate, up, down};
            __half *dsts[3] = {victim->h_gate, victim->h_up, victim->h_down};
            for (int m = 0; m < 3; ++m) {
                cudaMemcpyAsync(ctx->d_stage, srcs[m], mat * sizeof(float), cudaMemcpyHostToDevice, s);
                f2h<<<nb, CT, 0, s>>>(ctx->d_stage, dsts[m], mat); // same stream serializes stage reuse
            }
            victim->k_gate = gate;
            victim->k_up = up;
            victim->k_down = down;
            victim->k_hidden = hidden;
            victim->k_inter = inter;
            victim->graph_valid = false;
            active = victim;
        } else {
            if (victim->cap < mat) {
                if (victim->d_gate) cudaFreeAsync(victim->d_gate, s);
                if (victim->d_up) cudaFreeAsync(victim->d_up, s);
                if (victim->d_down) cudaFreeAsync(victim->d_down, s);
                if (cudaMallocAsync((void **)&victim->d_gate, mat * sizeof(float), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->d_up, mat * sizeof(float), s) != cudaSuccess ||
                    cudaMallocAsync((void **)&victim->d_down, mat * sizeof(float), s) != cudaSuccess) {
                    return 2;
                }
                victim->cap = mat;
            }
            cudaMemcpyAsync(victim->d_gate, gate, mat * sizeof(float), cudaMemcpyHostToDevice, s);
            cudaMemcpyAsync(victim->d_up, up, mat * sizeof(float), cudaMemcpyHostToDevice, s);
            cudaMemcpyAsync(victim->d_down, down, mat * sizeof(float), cudaMemcpyHostToDevice, s);
            victim->k_gate = gate;
            victim->k_up = up;
            victim->k_down = down;
            victim->k_hidden = hidden;
            victim->k_inter = inter;
            victim->graph_valid = false; // weights changed -> the slot's graph args are unchanged, but
            active = victim;             // rebuild is cheap and keeps invariants simple after resize.
        }
    }
    active->lru = ++ctx->clock;

    // Stage x into pinned memory, then run the pipeline for the active slot: one replayed graph
    // launch, or the direct 2-launch path (GE_GPU_NOGRAPH / graph-capture fallback).
    memcpy(ctx->h_x, x, hidden * sizeof(float));
    bool launched = false;
    if (ctx->use_graph) {
        if (!active->graph_valid || active->g_hidden != hidden || active->g_inter != inter) {
            build_graph(ctx, active, hidden, inter);
        }
        if (active->graph_valid && cudaGraphLaunch(active->graph, s) == cudaSuccess) {
            launched = true;
        }
    }
    if (!launched) {
        const unsigned T = 256;
        cudaMemcpyAsync(ctx->d_x, ctx->h_x, hidden * sizeof(float), cudaMemcpyHostToDevice, s);
        if (ctx->use_int4) {
            const unsigned G = ctx->int4_group;
            const unsigned ng_gu = (hidden + G - 1) / G, ng_down = (inter + G - 1) / G;
            fused_gate_up_silu_i4<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
                active->p_gate, active->s_gate, active->p_up, active->s_up, ctx->d_x, ctx->d_h, hidden, inter, G, ng_gu);
            down_gemv_i4<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
                ctx->d_h, active->p_down, active->s_down, ctx->d_y, inter, hidden, G, ng_down);
        } else if (ctx->use_int8) {
            fused_gate_up_silu_i8<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
                active->q_gate, active->s_gate, active->q_up, active->s_up, ctx->d_x, ctx->d_h, hidden, inter);
            down_gemv_i8<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
                ctx->d_h, active->q_down, active->s_down, ctx->d_y, inter, hidden);
        } else if (ctx->use_fp16) {
            fused_gate_up_silu_h<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
                active->h_gate, active->h_up, ctx->d_x, ctx->d_h, hidden, inter);
            down_gemv_h<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
                ctx->d_h, active->h_down, ctx->d_y, inter, hidden);
        } else {
            fused_gate_up_silu<<<(inter + T - 1) / T, T, (size_t)hidden * sizeof(float), s>>>(
                active->d_gate, active->d_up, ctx->d_x, ctx->d_h, hidden, inter);
            down_gemv<<<(hidden + T - 1) / T, T, (size_t)inter * sizeof(float), s>>>(
                ctx->d_h, active->d_down, ctx->d_y, inter, hidden);
        }
        cudaMemcpyAsync(ctx->h_y, ctx->d_y, hidden * sizeof(float), cudaMemcpyDeviceToHost, s);
    }
    cudaStreamSynchronize(s);
    memcpy(y, ctx->h_y, hidden * sizeof(float));

    cudaError_t err = cudaGetLastError();
    if (err != cudaSuccess) {
        fprintf(stderr, "ge_gpu_compute_expert: %s\n", cudaGetErrorString(err));
        return 3;
    }
    return 0;
}
