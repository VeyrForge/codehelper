# Changelog

## [0.4.2] - 2026-07-05

### Added
- **`green_q7` / uniform sub-8-bit codec** (`qn.rs`, `QnMatrix`): bit-packed 5/6/7-bit block quant
  (symmetric per-block f16 scale, like Q8 but fewer bits). Real-perplexity validated: **Q7 = +0.28%
  ppl at −12% RAM** (passes the ≥99% gate); Q6 = +1.00% at −24%. Codec + matmul (`matmul_qn_repaired`,
  parity-tested) + 4 unit tests (bit-pack roundtrip, monotonic quality, matmul parity, bpw).
- **`qn-bench` CLI** — `greencompress qn-bench --in W.mx --activations X.mx` reports Q5–Q8
  quality/RAM/speed. On real Llama `ffn_down`: Q7 = 15.0 MiB (−12% vs Q8's 17.0), matmul ~comparable.

## [0.4.1] - 2026-07-05

### Notes
- **Real perplexity bit-width sweep found a genuine RAM win.** `scripts/perplexity_mixed_precision.py`
  now sweeps uniform Q5–Q8: on 286 tokens, **uniform Q7 = +0.28% ppl at −12% RAM (passes the ≥99%
  gate)**; Q6 = +1.00% at −24% (on the boundary); Q5 = +2.39% (fails). And **uniform intermediate
  precision beats 4/8-bit mixed at equal RAM** (Q6 +1.00% vs ffn-Q4_K +1.88% @ ~6.3 bpw) — so the
  mixed-precision axis was wrong; *uniform sub-8-bit* is the lever. Next: a bit-packed Q7/Q6 codec in
  Rust (`green_q7`/`green_q6`). See `docs/roadmap.md` #0.

## [0.4.0] - 2026-07-05

### Added
- **Real perplexity harness** (`scripts/perplexity_mixed_precision.py`): loads the actual Llama-3.2-1B
  from GGUF via transformers, swaps each Linear's weight for its compressed reconstruction, and measures
  true perplexity on real English text — the gold-standard quality metric.

### Notes
- **Mixed-precision definitively closed with real perplexity.** On 286 tokens of varied English:
  all-Q8 = **+0.20%** ppl vs fp32; every Q4_K mixed config = **+1.88% … +3.26%** (−26…−33% RAM) — all
  below the ≥99% gate. (A 98-token first run showed a misleading +0.33% for ffn-Q4_K; the larger sample
  corrected it.) This is the authoritative measurement — supersedes the rel-L2 / generation-metric
  approximations. **Q8+repair is optimal; mixed-precision does not qualify.** Roadmap #1 closed for good.

## [0.3.11] - 2026-07-05

### Notes
- **E2E harness robustness (`--tokens N`): build the input from N real token embeddings** (from the
  GGUF's own table) instead of a single captured activation vector. The larger 128-token sample corrects
  the optimistic 32-sample read: relative to all-Q8, mixed-precision is now **measurably worse** at the
  generation level too (KL 0.032 vs 0.022, top-1 −1.6%; top-5 unchanged at 99.2%). Conclusion firmed:
  mixed-precision is *not* indistinguishable from Q8 and fails the rel-L2 ≥99% gate → **Q8+repair stays**.
  A fully definitive number still needs a real perplexity eval on coherent text (torch, unavailable here).

## [0.3.10] - 2026-07-05

### Notes
- **Generation-level metrics added to the E2E harness** (`scripts/e2e_mixed_precision.py`): real logits
  via the tied embedding → top-1/top-5 next-token agreement + KL divergence. Finding: at the generation
  level mixed-precision (Q4_K) is nearly indistinguishable from all-Q8 (top-5 = 100%, KL ≈ equal) — the
  rel-L2 hidden-state metric (97%) overstates the impact. **Verdict unchanged under the rel-L2 ≥99% gate**
  (mixed fails at 97%); the gap between metrics means a real perplexity eval (torch + corpus) could reopen
  the −26% RAM path. Caveat: 32 sample positions, forward not validated vs the reference model.

## [0.3.9] - 2026-07-05

### Notes
- **Mixed-precision decided by a real end-to-end forward.** Built a numpy 16-layer Llama-3.2-1B forward
  (`scripts/e2e_mixed_precision.py`, RMSNorm + GQA/RoPE + SwiGLU) fed by real captured activations,
  running fp32 / all-Q8 / mixed in lockstep. Result: all-Q8 = **99.49%** E2E; every Q4_K mixed config
  falls to **96.7–97.6%** (−24…−33% RAM), and Q4_K+repair adds only +0.2% (broadband 4-bit residual).
  Under the agreed **≥99% E2E gate, mixed-precision does not qualify** — Q8+repair stays the default.
  The Q4_K codec+matmul remain as primitives. This closes the last open RAM lever (roadmap #1) with a
  model-level test.

## [0.3.8] - 2026-07-05

### Notes
- **Validation release.** Tested FP8 (e4m3) vs our INT8 for weight-only quantization on real Llama
  layers: INT8 wins by ~1.9% (99.46% vs 97.60%) — FP8's 3-bit mantissa loses to INT8's uniform steps
  once a per-block scale handles the range. Confirms the INT8 choice (matches 2026 MXINT8 > MXFP8
  literature). FP8 added to `scripts/codec_compare.py`; `docs/codec_comparison.md` + scoreboard updated.

## [0.3.7] - 2026-07-05

### Changed
- **Green-branded the file formats and error type.** All 8 on-disk magics renamed from the legacy
  `LCL*` (from "llm_compress_lab") to `GRN*` (`GRNMX01`, `GRNQ401`, `GRNQ802`, `GRNRP01`, `GRNFCW01`,
  `GRNBIAS1`, `GRNSUB01`, `GRNOUT01`); `LclError` → `GreenError`. **Backward compatible** — readers
  still accept the legacy `LCL*` magics, so existing compressed models load unchanged (verified on the
  real Llama layer). New files write the green magic. Everything user-facing (CLI `greencompress`,
  methods `green_*`) was already green.

### Notes
- Web scan: T-MAC (CPU table-lookup mpGEMM) and SqueezeLLM (dense-and-sparse + non-uniform) reviewed.
  T-MAC is low-bit (1–4 bit) only — not for Q8 — but would accelerate the Q4_K path (removing the
  per-row dequant) *if* the mixed-precision route is built. SqueezeLLM's dense-and-sparse is already
  what `green_optimal` does; its non-uniform bins need codebook-lookup decode (wrong niche). See roadmap.

## [0.3.6] - 2026-07-05

### Added
- **Q4_K codec** (`q4k.rs`, `Q4KMatrix`) — step 1 of the mixed-precision policy (roadmap #1). llama.cpp
  super-block layout: 256-weight blocks × 8 sub-blocks of 32, asymmetric 4-bit with 6-bit quantized
  per-sub scales/mins and fp16 super-scales (~4.5 bpw). Offset capped at 0 so its magnitude stores
  unsigned — optimal for real (zero-centred) weights (matches the validated Python: 91.77% on real
  `ffn_down`) and robust to all-positive blocks.
- **Q4_K matmul path** (`matmul_q4k_repaired`, `dequantize_row_q4k`, `reconstruct_q4k`) — RAM-lean
  per-row dequant + saxpy (+ optional repair), parity-tested against dequantize-then-dense-matmul.

### Changed
- Removed 5 dead unused imports (`q8`, `q4`, `util`, `repair`, `subspace`) — cleaner build; cfg-gated
  imports in `benchmark.rs`/`infer.rs` kept (used in the GPU build).

### Notes
- Mixed-precision analysis (roadmap #1) reached a decision point: **Q4_K + affordable repair cannot
  meet the per-layer 99.5% floor** — the 4-bit residual is broadband, so low-rank+sparse repair tops
  out at ~96% (robust) / ~92% (sensitive). The −25–37% RAM win is only reachable with an *end-to-end*
  quality gate (llama.cpp's standard), a product decision. Q4_K codec+matmul remain as usable
  primitives. See `docs/codec_comparison.md`.

## [0.3.5] - 2026-07-05

### Fixed
- **Portability: default build no longer bakes in `target-cpu=native`.** `make` now builds a portable
  x86-64 (SSE2 baseline) binary that runs on **any** 64-bit x86 CPU and selects AVX2/FMA at runtime.
  Previously the default `-C target-cpu=native` could emit AVX2/AVX-512 even in the scalar fallback,
  crashing (illegal instruction) on older CPUs. The advertised `MARCH` flag was also a no-op — now wired.

### Added
- `make native` (and `make MARCH=native`) for a machine-specific build.
- SIMD parity tests (`saxpy_simd_matches_scalar`, `q8_block_simd_matches_scalar`) proving the scalar
  fallback (old x86 / ARM) matches the AVX2 path.
- `docs/portability.md` — verified build matrix: portable x86-64, old no-AVX2 CPUs, aarch64 (ARM),
  Windows, and optional GPU with CPU fallback. Portable vs native produce identical quality/RAM.

## [0.3.4] - 2026-07-05

### Added
- **f16 Q8 scales — quality-neutral RAM cut.** Per-block dequant scales now stored f16 (2 B/block)
  instead of f32. Measured on real Llama-3.2-1B `ffn_down`: runtime RAM **20.75 → 18.75 MiB (−9.6%)**,
  compression **3.11× → 3.45×**, accuracy **99.5649 → 99.5644%** (−0.0005%, i.e. noise). Synthetic
  `green_optimal`: 0.406 → 0.390 MiB, quality unchanged. Scales are read once per 32-weight block, so
  the f16→f32 conversion cost is negligible. (bf16 scales were tested and rejected: −0.085%.)
- New unit test `q8_save_load_roundtrip_f16_scales`.

### Changed
- Q8 on-disk format bumped to magic `LCLQ802` (f16 scales). **Old `LCLQ801` files still load**
  (f32 scales up-converted to f16) — backward compatible. `half` is now a core dependency.

## [0.3.3] - 2026-07-05

### Fixed
- **GPU backend correctness:** the CUDA GEMM ignored per-row SpinQuant signs (`row_spin`), producing
  ~0% accuracy on every spun layer (all `green_optimal`/`green_spqr_svd` tensors). Spin is now applied
  to activations, matching the CPU path — GPU f32 matches CPU to 6 digits on real Llama-3.2-1B `ffn_down`.
- **GPU performance:** the fused weight matrix was re-dequantized (and re-uploaded) on *every* infer
  call. Device buffers are now cached per layer key and built only on a miss. Real `ffn_down` per-call
  latency: **~28 ms → 3.1 ms (f32) / 3.75 ms (f16)**, ~7–9×. End-to-end via green-engine
  `green-weights-bench` (2× `ffn_down`, GPU): **12,020 tok/s / 2.66 ms per pass** for `green_optimal`
  (99.51% quality, 3.11× RAM) — 2.3× faster than CPU and 4× faster than fp32-on-GPU.
- **GPU f16 host conversion** (f32↔f16 of activations/output) parallelized with rayon: f16 per-call
  **4.7 ms → 3.75 ms** (~20%), accuracy unchanged.

### Added
- **GPU f16 weights (default):** fused weights stored in VRAM as f16 (2× less) via cuBLASLt with f32
  accumulation (`CUBLAS_COMPUTE_32F`); −0.003 pp vs f32 on real `ffn_down`. Set `GREENCOMPRESS_GPU_F32=1`
  to force full-f32 weights. New regression test `gpu_f16_prepacked_matches_cpu_when_available`.
- First real-model per-layer validation (Llama-3.2-1B `ffn_down`): `green_optimal` = 3.1× RAM at 99.56%.

### Notes
- `gpu` feature now enables cudarc `cublaslt` + `f16` and adds an optional `half` dependency.
- Evaluated and rejected an MSE-optimal per-block Q8 scale (byte-identical no-op on real weights).
- Real codec comparison (`scripts/codec_compare.py`, `docs/codec_comparison.md`): on `ffn_down`, 4-bit
  Q4_K/HQQ reach only ~92% vs green_optimal's 99.56% — a Q4_K "core" is not a drop-in RAM win for
  sensitive layers. Roadmap revised toward per-tensor mixed precision.

## [0.3.2] - 2026-07-02

### Notes
- Companion release to **Green Engine `ge` 0.2.2** MCP stack (`ge embed serve --mcp`, `ge chat serve --mcp`).
- No API changes; version bump for coordinated local tooling releases.

## [0.3.1] - prior

Green Compress Rust CLI (`greencompress`) — weight compression and CPU layer inference.
