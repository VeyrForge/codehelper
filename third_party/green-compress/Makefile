CXX ?= g++
# Portable by default: the binary runs on ANY x86-64 CPU (SSE2 baseline). AVX2/FMA
# are detected at runtime and used when present, so one binary is both portable and
# fast. Opt into a machine-specific build with `make native` or `make MARCH=native`.
MARCH ?=

CARGO ?= cargo
ifeq ($(MARCH),native)
RUSTFLAGS ?= -C target-cpu=native
else
RUSTFLAGS ?=
endif
BIN := bin/greencompress

.PHONY: all rust native rust-gpu rust-check rust-test clean

all: rust

# Machine-specific build (target-cpu=native): fastest here, NOT portable to older CPUs.
native:
	$(MAKE) MARCH=native rust

rust:
	cd rust && RUSTFLAGS="$(RUSTFLAGS)" CARGO_TARGET_DIR=target $(CARGO) build --release
	mkdir -p bin
	cp rust/target/release/greencompress $(BIN)

rust-gpu:
	cd rust && RUSTFLAGS="$(RUSTFLAGS)" CARGO_TARGET_DIR=target CUDA_PATH="$${CUDA_PATH:-/usr/local/cuda}" $(CARGO) build --release --features gpu
	mkdir -p bin
	cp rust/target/release/greencompress $(BIN)

rust-check:
	cd rust && CARGO_TARGET_DIR=target $(CARGO) check

rust-test:
	cd rust && CARGO_TARGET_DIR=target $(CARGO) test

clean:
	rm -rf bin rust/target
