// Green Engine — OPTIMIZED native expert kernel (same C ABI as expert_cpu_ref.cpp).
//
// Represents an "optimized backend" (the class ggml ships): blocked, OpenMP-parallel,
// auto-vectorized to AVX-512/AVX2/NEON via -O3 -march=native. Build:
//   c++ -O3 -march=native -fopenmp -shared -fPIC -Iinclude src/expert_opt.cpp \
//       -o libgreen_engine_kernels.so
// Drop-in: point GREEN_ENGINE_KERNELS_DIR at it and build engine-core --features gpu.

#include "green_engine_kernels.h"
#include <cmath>
#include <vector>
#ifdef _OPENMP
#include <omp.h>
#endif

struct ge_ctx { int device_id; };
extern "C" ge_ctx *ge_ctx_create(int d) { auto *c = new ge_ctx(); c->device_id = d; return c; }
extern "C" void ge_ctx_destroy(ge_ctx *c) { delete c; }

static inline float silu(float v) { return v / (1.0f + std::exp(-v)); }

// y[o] = sum_i x[i] * w[i*out + o]; parallel over output blocks, vectorized inner loop.
static void matvec(const float *__restrict x, const float *__restrict w,
                   uint32_t in_dim, uint32_t out_dim, float *__restrict y) {
#pragma omp parallel for schedule(static)
    for (long o = 0; o < (long)out_dim; ++o) {
        float acc = 0.0f;
        for (uint32_t i = 0; i < in_dim; ++i) acc += x[i] * w[(size_t)i * out_dim + o];
        y[o] = acc;
    }
}

extern "C" int ge_gpu_compute_expert(ge_ctx *, const float *gate, const float *up,
                                     const float *down, const float *x, float *y,
                                     uint32_t hidden, uint32_t inter) {
    std::vector<float> g(inter), u(inter);
    matvec(x, gate, hidden, inter, g.data());
    matvec(x, up, hidden, inter, u.data());
#pragma omp parallel for schedule(static)
    for (long j = 0; j < (long)inter; ++j) g[j] = silu(g[j]) * u[j];
    matvec(g.data(), down, inter, hidden, y);
    return 0;
}
