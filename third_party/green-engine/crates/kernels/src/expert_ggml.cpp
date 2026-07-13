// Green Engine — GGML bridge kernel (same C ABI as the others).
//
// Implements one expert's SwiGLU FFN with ggml ops, so the engine computes through ggml's
// optimized, *portable* kernels — the same library llama.cpp uses (CPU here; CUDA/HIP/Metal/
// Vulkan when ggml is built with those backends). This is the "runs on all hardware" path.
//
// Build (CPU ggml):
//   c++ -O3 -fPIC -shared -Iinclude -I<ggml>/include src/expert_ggml.cpp \
//       -L<ggml>/build/src -lggml -lggml-cpu -lggml-base -Wl,-rpath,<ggml>/build/src \
//       -o libgreen_engine_kernels.so
//
// NOTE: weights here are uploaded per call for clarity; a production bridge stores them in
// ggml tensors once when the expert becomes resident (no per-token copy on a cache hit).

#include "green_engine_kernels.h"
#include "ggml.h"
#include "ggml-cpu.h"
#include <cstdlib>
#include <cstring>
#include <vector>

// Persistent reused scratch buffer: ggml_init() with a provided buffer does NOT malloc, so we pay
// the (large) allocation ONCE, not per expert — the fix for the per-call overhead we measured.
struct ge_ctx {
    int device_id;
    int n_threads;
    void *buf;
    size_t bufsize;
};

extern "C" ge_ctx *ge_ctx_create(int d) {
    ge_ctx *c = new ge_ctx();
    c->device_id = d;
    c->n_threads = 1;
    c->buf = nullptr;
    c->bufsize = 0;
    return c;
}
extern "C" void ge_ctx_destroy(ge_ctx *c) {
    if (c) { free(c->buf); delete c; }
}

// Our weights are row-major [in][out] (w[i*out + o]); ggml_mul_mat wants [K=in, N=out] with K
// contiguous, i.e. memory [out][in]. Transpose on the way in.
static void fill_T(struct ggml_tensor *t, const float *w, uint32_t in_dim, uint32_t out_dim) {
    float *d = (float *)t->data; // layout: d[o*in_dim + i]
    for (uint32_t i = 0; i < in_dim; ++i)
        for (uint32_t o = 0; o < out_dim; ++o)
            d[(size_t)o * in_dim + i] = w[(size_t)i * out_dim + o];
}

extern "C" int ge_gpu_compute_expert(ge_ctx *ctx,
                                     const float *gate, const float *up, const float *down,
                                     const float *x, float *y,
                                     uint32_t hidden, uint32_t inter) {
    size_t mem = (size_t)(3 * hidden * inter + 8 * (hidden + inter) + 4096) * sizeof(float)
                 + 4 * 1024 * 1024;
    if (mem > ctx->bufsize) {            // grow the reusable buffer once, then reuse every call
        free(ctx->buf);
        ctx->buf = malloc(mem);
        ctx->bufsize = mem;
    }
    struct ggml_init_params ip = { ctx->bufsize, ctx->buf, false };
    struct ggml_context *c = ggml_init(ip);

    struct ggml_tensor *xt = ggml_new_tensor_1d(c, GGML_TYPE_F32, hidden);
    memcpy(xt->data, x, (size_t)hidden * sizeof(float));
    struct ggml_tensor *gw = ggml_new_tensor_2d(c, GGML_TYPE_F32, hidden, inter); // [K=hidden,N=inter]
    struct ggml_tensor *uw = ggml_new_tensor_2d(c, GGML_TYPE_F32, hidden, inter);
    struct ggml_tensor *dw = ggml_new_tensor_2d(c, GGML_TYPE_F32, inter, hidden);  // [K=inter,N=hidden]
    fill_T(gw, gate, hidden, inter);
    fill_T(uw, up, hidden, inter);
    fill_T(dw, down, inter, hidden);

    struct ggml_tensor *g = ggml_mul_mat(c, gw, xt);                 // [inter]
    struct ggml_tensor *u = ggml_mul_mat(c, uw, xt);                 // [inter]
    struct ggml_tensor *h = ggml_mul(c, ggml_silu(c, g), u);         // [inter]
    struct ggml_tensor *out = ggml_mul_mat(c, dw, h);                // [hidden]

    struct ggml_cgraph *graph = ggml_new_graph(c);
    ggml_build_forward_expand(graph, out);
    ggml_graph_compute_with_ctx(c, graph, ctx->n_threads);

    memcpy(y, out->data, (size_t)hidden * sizeof(float));
    ggml_free(c);
    return 0;
}
