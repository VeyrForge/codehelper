// Green Engine — portable C++ reference implementation of the kernel ABI.
//
// Mirrors the Rust CpuBackend so the FFI boundary can be exercised end-to-end WITHOUT a GPU
// toolkit. Swap this translation unit for expert_cuda.cu to run on device; the ABI is identical.
//
// Build (standalone shared lib):
//   c++ -O3 -shared -fPIC -Iinclude src/expert_cpu_ref.cpp -o libgreen_engine_kernels.so
// Then build engine-core with `--features gpu` and link against it.

#include "green_engine_kernels.h"
#include <cmath>
#include <cstdlib>
#include <vector>

struct ge_ctx {
    int device_id;
};

extern "C" ge_ctx *ge_ctx_create(int device_id) {
    ge_ctx *c = new ge_ctx();
    c->device_id = device_id;
    return c;
}

extern "C" void ge_ctx_destroy(ge_ctx *ctx) { delete ctx; }

static inline float silu(float v) { return v / (1.0f + std::exp(-v)); }

// y[o] = sum_i x[i] * w[i*out + o]
static void matvec(const float *x, const float *w, uint32_t in_dim, uint32_t out_dim, float *y) {
    for (uint32_t o = 0; o < out_dim; ++o) y[o] = 0.0f;
    for (uint32_t i = 0; i < in_dim; ++i) {
        const float xi = x[i];
        if (xi == 0.0f) continue;
        const float *row = w + (size_t)i * out_dim;
        for (uint32_t o = 0; o < out_dim; ++o) y[o] += xi * row[o];
    }
}

extern "C" int ge_gpu_compute_expert(ge_ctx * /*ctx*/,
                                     const float *gate, const float *up, const float *down,
                                     const float *x, float *y,
                                     uint32_t hidden, uint32_t inter) {
    std::vector<float> g(inter), u(inter);
    matvec(x, gate, hidden, inter, g.data());
    matvec(x, up, hidden, inter, u.data());
    for (uint32_t j = 0; j < inter; ++j) g[j] = silu(g[j]) * u[j];
    matvec(g.data(), down, inter, hidden, y);
    return 0;
}
