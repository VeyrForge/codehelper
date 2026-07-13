# Changelog

All notable changes to Green Engine. Format follows [Keep a Changelog](https://keepachangelog.com);
versioning is [SemVer](https://semver.org).

## [0.2.2] - 2026-07-02

### Added
- **MCP profile** (`ge embed serve --mcp`, `ge chat serve --mcp`): ONNX embed (onnx-st),
  LRU cache, request batching; 1B Q4_K_M chat with 2k ctx for codehelper enrich/routing.
- **`ge bench mcp`** and **`runner/bench_mcp_stack.py`**: local embed/chat latency benchmark.
- **`runner/start_mcp_stack.sh`**: start embed + chat in background for codehelper.
- **`docs/BENCHMARKS.md`**, **`docs/MCP_STACK.md`**: performance index and MCP tuning guide.
- **`ge stack config`**: writes `~/.codehelper/green.json` (MCP server profile).

### Fixed
- **green-embed**: use SentenceTransformer `backend="onnx"` (1.0 quality vs torch); removed broken
  custom optimum pooling that degraded cross-lingual rerank.
- **green-chat**: drop unsupported `--cache_type_k/v` on `llama_cpp.server` (fixes startup).

## [0.2.0] - 2026-06-28

### Added
- **`green-weights-bench` (0.2.0)**: manifest-driven whole-model benchmark — load Green Compress
  weights via `model_manifest.json`, report tok/s, RAM, compression ratio, and quality vs fp32
  (`crates/green-weights-bench`, `docs/green-weights-bench.md`).
- **`ge translate`**: routed translation server (Green Engine + Green Compress) on port **8768**.
  - Hy-MT2-7B for 33 languages; **GaMS-9B-SFT-Translator** for Slovenian (auto-routed).
  - One model loaded at a time; swap on language / `X-Green-Route` / JSON `route`.
  - OpenAI `/v1/chat/completions`, Ollama `/api/chat`, Green `/v1/translate` + batch.
  - Session usage metering, `/v1/pricing`, `/v1/pricing/estimate`, `/v1/routes`.
  - `ge translate pull|compress|serve|install`; config at `~/.green/translate-router.json`.
- **`runner/green_translate.py`**: HTTP server implementing the translate API.

## [0.1.0] - 2026-06-24

First public release — the validated scheduling engine (research-complete, integration-pending).

### Added
- **Expert scheduling**: residency cache (LRU/LFU/reuse), transition + hidden-state predictors,
  layer-ahead & speculative-salvage prefetch (`engine`, `cache`, `predictor`, `hidden`).
- **KV tier**: eviction (StreamingLLM/H2O/SnapKV/Quest), adaptive per-layer budget, 2-bit model,
  context-extension benchmark (`kv`, `kv_bench`).
- **Persistence**: cross-turn / shared-prefix KV reuse (`prefix`).
- **Serving**: continuous batching, chunked prefill, multi-token prediction, disaggregation
  (`batching`, `serving`).
- **Execution**: CPU MoE runtime, tiered weight store with Q8, `ExpertBackend` trait + ggml/CUDA/CPU
  bridge (`runtime`, `weights`, `backend`, `tensor`, `crates/kernels`).
- **Heterogeneous** CPU+GPU split (`hetero`) and **energy / tokens-per-watt** model (`energy`).
- **Green compression seam**: per-expert manifest consumer (`manifest`).
- Hardware detection + backend registry (`sys`); 11 benchmark binaries; 22 tests.
- CI + release automation (binaries published to the `release` branch; GitHub Releases on tags).

### Known limitations
- Numbers for offload/serving are projected from measured parts; the on-device wall-clock run needs
  the integration milestone (real weights + batched ggml kernels in `decode_loop`).
- The ggml bridge is correctness-verified but the bundled ggml build is unoptimized; batched + Release
  ggml is the perf path.
- Dense-model benefit is modest and lossy; the engine's strong, lossless wins are MoE + long context.

[0.2.0]: https://github.com/VeyrForge/GreenEngine/releases/tag/v0.2.0
[0.1.0]: https://github.com/VeyrForge/GreenEngine/releases/tag/v0.1.0
