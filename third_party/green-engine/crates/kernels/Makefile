# Green Engine native kernels. Produces libgreen_engine_kernels.so for the engine-core
# `--features gpu` build to link against.
#
#   make cpu     # portable C++ reference (no GPU toolkit needed)
#   make cuda    # CUDA build (needs nvcc)
#
# Then:
#   GREEN_ENGINE_KERNELS_DIR=$(pwd) LD_LIBRARY_PATH=$(pwd) \
#     cargo test -p engine-core --features gpu

LIB := libgreen_engine_kernels.so
INC := -Iinclude
# Point GGML_DIR at a built ggml checkout (has include/ and build/src/libggml*.so).
GGML_DIR ?= ../../../ggml

.PHONY: cpu opt cuda ggml clean
cpu:                                  # portable C++ reference (no toolkit)
	c++ -O3 -shared -fPIC $(INC) src/expert_cpu_ref.cpp -o $(LIB)

opt:                                  # optimized C++ (AVX-512/AVX2/NEON + OpenMP)
	c++ -O3 -march=native -fopenmp -shared -fPIC $(INC) src/expert_opt.cpp -o $(LIB)

ggml:                                 # ggml bridge — inherits all ggml backends (portability path)
	c++ -O3 -shared -fPIC $(INC) -I$(GGML_DIR)/include src/expert_ggml.cpp \
	    -L$(GGML_DIR)/build/src -lggml -lggml-cpu -lggml-base \
	    -Wl,-rpath,$(GGML_DIR)/build/src -o $(LIB)

cuda:                                 # NVIDIA (needs nvcc). -arch=native targets the local GPU.
	nvcc -O3 -arch=native -Xcompiler -fPIC -shared $(INC) src/expert_cuda.cu -lcudart -o $(LIB)

clean:
	rm -f $(LIB)
