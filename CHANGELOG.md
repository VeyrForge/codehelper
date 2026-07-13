# Changelog

## Unreleased

## Version index

Releases from **3.0.0** through **1.1.0** (newest first):

**3.0.0** · **2.59.0** · **2.58.0** · **2.57.0** · **2.56.0** · **2.55.0**
**2.54.0** · **2.53.0** · **2.52.0** · **2.51.0** · **2.50.0** · **2.49.0**
**2.48.0** · **2.47.0** · **2.46.0** · **2.45.0** · **2.44.0** · **2.43.0**
**2.42.18** · **2.42.17** · **2.42.16** · **2.42.15** · **2.42.14** · **2.42.13**
**2.42.12** · **2.42.11** · **2.42.10** · **2.42.9** · **2.42.8** · **2.42.7**
**2.42.6** · **2.42.5** · **2.42.4** · **2.42.3** · **2.42.2** · **2.42.1**
**2.42.0** · **2.41.0** · **2.40.0** · **2.39.0** · **2.38.3** · **2.38.2**
**2.38.1** · **2.38.0** · **2.37.1** · **2.37.0** · **2.36.0** · **2.35.0**
**2.34.0** · **2.33.0** · **2.32.0** · **2.31.0** · **2.30.0** · **2.29.0**
**2.28.0** · **2.27.0** · **2.26.0** · **2.25.0** · **2.24.0** · **2.23.0**
**2.22.1** · **2.22.0** · **2.21.1** · **2.21.0** · **2.20.0** · **2.19.0**
**2.18.1** · **2.18.0** · **2.17.0** · **2.16.0** · **2.15.1** · **2.15.0**
**2.14.0** · **2.13.0** · **2.12.0** · **2.11.0** · **2.10.0** · **2.9.0**
**2.8.0** · **2.7.0** · **2.6.1** · **2.6.0** · **2.5.0** · **2.4.11**
**2.4.0–2.4.10** · **2.3.0** · **2.2.0** · **2.1.1** · **2.1.0** · **2.0.0**
**1.9.0** · **1.8.0** · **1.7.0** · **1.6.0** · **1.5.0** · **1.4.0**
**1.3.0** · **1.2.0** · **1.1.0**

## 3.0.0

### Added

- **Public release 3.0.0** — prebuilt universal bundles for Linux, macOS, and Windows; one
  archive includes `codehelper`, `codehelper-mcp`, `ge` **1.0.0**, and `greencompress`
  **1.0.0**.
- **`docs/SETUP.md`** — one-time browser and `ge ui` setup; `install.sh` runs
  `codehelper browser install` and `ge ui install` when building from source.
- **Docs** refreshed as benchmark tables, CI gates, test environments, and tool-usage
  reference.

## 2.59.0

### Added

- **Bundled `ge` 0.10.0** — Green UI + local dashboard; `ge ui install` wired into install flow.
- **Bundled `greencompress` 0.9.0** — Perplexity-validated Q7 codec production-ready for bundle.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.58.0

### Added

- **Bundled `ge` 0.10.0** — NDJSON multi-file codegen in Green UI.
- **Bundled `greencompress` 0.8.0** — Real-perplexity harness + mixed-precision policy finalized.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.57.0

### Added

- **Bundled `ge` 0.10.0** — Green UI model catalog + project workspace.
- **Bundled `greencompress` 0.7.0** — Green-branded on-disk formats + E2E quality gates consolidated.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.56.0

### Added

- **Bundled `ge` 0.10.0** — Local dashboard with NDJSON multi-file codegen (ge 0.10.0).
- **Bundled `greencompress` 0.6.0** — Q4_K matmul path + repair measurement baseline.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.55.0

### Added

- **Bundled `ge` 0.9.1** — Register-blocked matvec (~1.5× faster CPU decode).
- **Bundled `greencompress` 0.5.0** — GPU inference cache + portability fixes for release matrix.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.54.0

### Added

- **Bundled `ge` 0.9.0** — Mixed-precision allocation at fixed RAM budget.
- **Bundled `greencompress` 0.4.2** — Subtree refresh — GPU spin/cache fixes land in bundle.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.53.0

### Added

- **Bundled green tools (subtree refresh):** `ge` **0.9.1** and `greencompress` **0.4.2** — register-blocked matvec, VRAM tiers, paged KV, Green UI, QN codecs, GPU inference.
- Refresh vendored trees from GreenEngine and green-compress `main` (HTTPS subtree pull).

## 2.52.0

### Added

- **Adaptive orchestration tiers** (`fast` / `standard` / `deep`) — simple tasks use 1–2 MCP tools; deep tasks get full chains. Skip `scout` when kickoff already has ≥3 reuse hits; skip local-LLM classify when confidence ≥ 0.80.
- **Release script** — `npm run release -- <version>` updates VERSION, README, CHANGELOG, VS Code extension version, orchestration doc, builds, commits, and tags.
- `agent_brief` adds `Locations: path:line`, explicit reuse line on feature tasks, and tier label.
- Classify cache + CPU-safe MCP defaults (`GE_MCP_THREADS=2`); benchmark harness excludes `ch-init-test`.

## 2.51.0

### Changed

- **Bulk symref resolution (large-repo indexing).** `ResolveSymrefs` now resolves
  every symref edge in memory, then applies inserts and deletes in one transaction
  via temp-table bulk writes (`INSERT … SELECT` + `DELETE … IN (SELECT …)`)
  instead of two `ExecContext` calls per resolved edge. On a synthetic 200k-symref
  graph this is ~40k edges/s; the per-edge path was the dominant indexing cost on
  real repos (e.g. ~35s symref phase on Django). Batched prepared statements were
  tried in v2.43 and regressed on `modernc.org/sqlite` — this is a set-oriented
  write path instead. Resolution strategies, confidence values, and stats are
  unchanged; `dedupeSymrefInserts` preserves the old `ON CONFLICT MAX(confidence)`
  semantics in memory.

## 2.50.0

### Changed

- **Bounded context caller loading.** Hub symbols no longer materialize thousands of
  callers for a short view — validated on real Django (1M edges): `assertEqual`
  (9,622 callers) **50ms → 11ms**. Detailed `verbosity=detailed` remains unbounded.

## 2.49.0

### Fixed

- **Impact blast-radius BFS N+1.** New `graph.Store.Neighbors` returns one-hop neighbor
  symbols in a single indexed JOIN instead of a `SymbolByID` per edge — **4.4× faster**
  on hub nodes, same result.

## 2.48.0

### Fixed

- **Context callers N+1.** Single indexed JOIN replaces per-caller lookups — **5.1×
  faster** on mega-hubs.

### Added

- **Large-graph scalability harness** (gated) proving the v2.46.0 ANALYZE
  structural-query fix stays sub-ms at 300k edges.

## 2.47.0

### Changed

- **Read-heavy SQLite tuning** — 64MB cache, 256MB mmap, in-memory temp store for
  large-repo query speed.
- **Query-plan guard** — regression test locks in the v2.46.0 sub-ms structural-query
  path.

## 2.46.0

### Changed

- **ANALYZE at index time** — structural queries (`context` callers/callees, `impact`,
  `trace`) use the selective edge index instead of scanning every call edge — **~19×
  faster**, sub-ms, scaling to large monorepos. Backfilled for existing indexes on next
  analyze/repair.

## 2.45.0

### Added

- **Precomputed call-graph hubs** (symbol + package) as an index artifact
  (`internal/hubs`), read instantly by `project_context`.
- **Short-bootstrap architecture teaser** — top-3 load-bearing modules on every
  session's default bootstrap (detailed mode still gets full hub lists from 2.44.0).

## 2.44.0

### Added

- **`project_context` surfaces call-graph hubs (detailed).** The detailed bootstrap
  now lists the most-referenced symbols — "what's linked" — as compact
  `name path:line ×callers` entries (top 8 by inbound calls, vendored/generated
  paths filtered). An agent grasps the load-bearing code in the one bootstrap call
  it already makes, instead of discovering it through a chain of trace/context
  probes. Detailed-only, so the short bootstrap every session makes is unchanged.
  Backed by a new `graph.Store.TopHubs` (one grouped query with a LIMIT).
- **`project_context` surfaces package hubs (detailed).** Alongside symbol hubs,
  the detailed bootstrap lists the top packages by **cross-package inbound calls** —
  the architectural "which modules the rest of the code depends on" — as
  `dir ×callers ←N pkgs`. Language-agnostic (packages are directories in Go, PHP,
  JS, Rust, …) and internal by construction (the call graph only holds resolved
  in-repo edges); build/vendored dirs filtered. Backed by `graph.Store.TopPackages`
  (~22ms on a 52k-edge repo).

### Chore

- gofmt-clean 9 files with pre-existing const-block alignment drift (no code change).

## 2.43.0

### Added

- **Connections security policy (CLI-only).** Per-project `policy set` with
  `verify_allowlist` (caps what the LLM can pass to `verify`), `allow_git` (opt-in
  per verify policy), and `agent_trust` (`none` | `allowlist_edits`). MCP reads policy
  caps via `project_context` but cannot mutate them.
- **Log source profiles.** `connections add-log`, `detect-logs`, and `rm-log` store
  nginx/Apache/WordPress/app log paths as metadata (no reads at config time).
- **SSH allowlist hardening.** `ValidateSSHAllowlist` rejects shells, secret readers,
  DB clients, and other never-allowed basenames when adding SSH hosts.
- **Ops MCP tools (8).** `remote_list`, `remote_exec`, `log_read`, `db_query`,
  `db_schema`, `run_alias`, `env_context`, and `ci_status` — security-gated
  external operations driven by CLI-configured profiles (60 MCP tools total).
- **SSH recipes and command aliases.** `connections add-recipe`, `add-alias`, and
  `rm-alias` for `remote_exec` / `run_alias`; recipe argv uses `{param}` placeholders
  with safe substitution (no shell metacharacters).
- **GitHub CI integration.** `connections policy set --github-repo` and
  `--github-token-ref env:VAR` for read-only `ci_status` via `gh`.

### Changed

- **Leaner tool responses (fewer tokens/call).** Concise `query` hits cap the
  per-hit ranking-signal list to the 3 strongest (`reasons`) instead of shipping
  all ~8 retrieval internals on every hit; `evidence_paths` (in
  `query`/`context_pack`/`impact`) is deduplicated to a distinct-file list; the
  kept `reasons` prefer discriminating signals (name/graph matches) over the
  ubiquitous `bm25`/`trigram`; and the `freshness` block attached to **every** tool
  response drops `watch_pid` (a raw PID), drops `head_commit` when it equals
  `indexed_commit` (reported only on drift), and truncates `indexed_at` to seconds.
  A full top_k=10 `query` response is ~31% smaller with no change in retrieval
  coverage (orchestration benchmark: orch 0.960→0.959 across 14 projects). Detailed
  mode (`verbosity=detailed`) and the Go structs are unchanged.
- **Once-per-session query guidance.** The constant `retrieval_note` (the same
  ~30-token "hits are ranked symbols…" reminder) is now emitted only on the first
  `query` of a session and dropped on later calls (progressive disclosure), saving
  those tokens on every subsequent query. Situational notes (zero hits, disk
  fallback) always ship; a sessionless transport keeps the note.
- **Diff-boost robustness.** The "recently changed" ranking boost now decays
  (IDF-style) when a large fraction of a query's candidates are changed, so a big
  branch diff (via `base_ref`) can't let edited-but-irrelevant symbols bury a
  strongly-relevant unchanged target. No-op for a clean tree or a small diff — the
  boost stays full below a 25%-changed threshold.
- **Actionable ops-tool errors.** `remote_exec` and `db_query` fail fast with a
  message that names the missing args, gives an example, and points at
  `remote_list` — instead of a confusing downstream error like
  `ssh host "" not configured`.
- **Minimal-tools mode keeps the differentiators.** The trimmed `tools/list`
  surface (`CODEHELPER_MINIMAL_TOOLS` / per-project `--minimal`) now advertises a
  focused ~18-tool set — the main tools **plus** the graph-navigation specialists
  (`trace`, `impact`, `test_impact`, `find_implementations`, `api_surface`,
  `diagnostics`, `rename_symbol`, `read_workspace_file`) — instead of only the 10
  main tools. That's a ~70% cut from the full 60-tool catalog (below the ~40-tool
  count where agent tool-selection accuracy degrades) without hiding the
  callers/impact/tests navigation that is codehelper's reason to exist. The
  `MCPMainTools` "reach-for-these-first" hint in `project_context` is unchanged.
- **`project_context` connections brief** now lists recipes, aliases, and GitHub
  policy metadata (never secrets).
- **Orchestration eval** no longer panics when anchor discovery returns zero
  symbols (`pickSecondAnchor`); heavy eval tests skip under `-short`.
- **Responsive background indexing.** Tree-sitter parse-worker threads are now
  niced per-thread (Linux), so on-demand/auto reindexing inside the MCP server
  yields CPU to the editor and interactive tool calls instead of spiking and
  lagging the machine — including when several codehelper daemons reindex at once.
  Tune with `CODEHELPER_NICE` (default 10, `0` disables, max 19); worker count is
  still bounded by `CODEHELPER_MAX_WORKERS` (default cores/4, clamped 1–8).
- **Faster cold-build I/O.** Parse workers now hash the file buffer they already
  read (`filehash.OfBytes`) and take size from it, instead of an extra `os.Stat`
  plus a second full read per file. ~46% less time and ~260× fewer allocations on
  the per-file hash step even warm-cached; larger on cold/network filesystems.
- **`log_read` tail is O(n).** The tail loop counted lines with `bytes.Count`
  instead of re-splitting the whole buffer into a `[]string` every iteration
  (previously O(n²) in the tail size), with no per-iteration allocation.

### Fixed

- **`TestOrchestrationBenchmark`** slice panic on empty/small repos during task
  generation (`internal/orchestrator/eval/tasks.go`).

### Testing

- **Rank-aware retrieval eval.** `eval.Run` now reports Recall@1/5/10 and MRR over
  the golden suite (it only checked whether a target appeared *anywhere* in top-K
  before, so a target sliding from #1 to #100 went unnoticed). A gated
  `TestGoldenRankQualityFloor` guards a live floor; pure `TestRankMetrics` /
  `TestFirstRelevantRank` guard the metric math on every run (golden suite on a
  clean index: MRR≈0.83, R@5=1.0). Complements the existing `internal/bench` qrels
  gate (R@1≈0.88, nDCG@10≈0.95).
- **All-tools schema guard.** `TestAllToolSchemasValid` registers every MCP tool
  and asserts each advertised input/output schema is object-typed with no bare
  boolean property schemas — the shape (an `any`-typed field reflecting to `true`)
  that once made strict clients reject the entire `tools/list` and disable every
  tool. Previously only `query` was guarded.

## 2.42.18

### Added

- **Bundled `ge` 0.9.0** — Mixed-precision allocation — best quality at a fixed RAM budget.
- **Bundled `greencompress` 0.4.2** (unchanged).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.17

### Added

- **Bundled `ge` 0.8.2** — Green-int6 balance tier (nearly-lossless Pareto win).
- **Bundled `greencompress` 0.4.2** (unchanged).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.16

### Added

- **Bundled `ge` 0.8.1** — Usable RVQ tier replaces unusable int2; branded ladder is usable-only.
- **Bundled `greencompress` 0.4.2** (unchanged).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.15

### Added

- **Bundled `ge` 0.8.0** — Int2 frontier (QuIP#-lite) + branded GreenTier ladder.
- **Bundled `greencompress` 0.4.2** (unchanged).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.14

### Added

- **Bundled `ge` 0.7.0** — Hadamard rotation + full engine-vs-greencompress benchmark.
- **Bundled `greencompress` 0.4.2** (unchanged).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.13

### Added

- **Bundled `ge` 0.6.1** — NF4 (NormalFloat-4) — best 4-bit quality tier.
- **Bundled `greencompress` 0.4.1** — Perplexity bit-sweep finds uniform Q7 RAM win (−12%).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.12

### Added

- **Bundled `ge` 0.6.0** — AWQ + outlier isolation + live predictive prefetch + real-units report.
- **Bundled `greencompress` 0.4.0** — Definitive real-perplexity test closes mixed-precision path.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.11

### Added

- **Bundled `ge` 0.5.0** — Q4G/Q3G pageable from disk + MSE-optimal clipping (better scales).
- **Bundled `greencompress` 0.3.11** — Robust E2E test with real token embeddings.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.10

### Added

- **Bundled `ge` 0.4.4** — Group-wise int3 (Q3G) — smallest quality-frontier tier.
- **Bundled `greencompress` 0.3.10** — E2E harness reports generation metrics (top-1/top-5/KL).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.9

### Added

- **Bundled `ge` 0.4.3** — GPU int4 residency is now group-wise (higher quality).
- **Bundled `greencompress` 0.3.9** — End-to-end forward test settles mixed-precision (fails 99% gate).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.8

### Added

- **Bundled `ge` 0.4.2** — Group-wise int4 (best quality-per-byte) + quality sweep.
- **Bundled `greencompress` 0.3.8** — FP8 vs INT8 validated — INT8 optimal for weight-only.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.7

### Added

- **Bundled `ge` 0.4.1** — KV-cache quantization + consolidated with/without-green benchmark.
- **Bundled `greencompress` 0.3.7** — Green-branded GRN* file formats (backward compatible).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.6

### Added

- **Bundled `ge` 0.4.0** — Int4 VRAM tier, GPU multi-session, hidden-state predictor.
- **Bundled `greencompress` 0.3.6** — Q4_K codec (mixed-precision build, step 1).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.5

### Added

- **Bundled `ge` 0.3.2** — Predictive prefetch — LayerAheadPredictor + accuracy on real trace.
- **Bundled `greencompress` 0.3.5** — Portable-by-default build + SIMD parity tests.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.4

### Added

- **Bundled `ge` 0.3.1** — Multi-session sharing, async expert prefetch, green-on-disk.
- **Bundled `greencompress` 0.3.4** — Q8 f16 scales — quality-neutral RAM cut (−9.6% on real ffn_down).
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.3

### Added

- **Bundled `ge` 0.3.0** — VRAM residency tiers + Green Compress integration.
- **Bundled `greencompress` 0.3.3** — GPU SpinQuant fix, cached fused weights, f16 VRAM weights.
- Refresh vendored trees via `git subtree pull` (see [third_party/README.md](third_party/README.md)).

## 2.42.2

### Added

- **Per-OS universal bundles on GitHub Releases** — one download each for Linux, macOS, and
  Windows (amd64 + arm64 inside; `install.sh` / `install.ps1` auto-detect CPU).

### Fixed

- **Windows ARM64 CI:** use MSVC (`ilammy/msvc-dev-cmd` + `CC=cl`), strip mingw from PATH.
- **Release checksums:** hash only `.tar.gz`/`.zip` files (skip universal bundle directories).
- **Release publish:** ship successful platform artifacts even when an optional matrix leg fails.
- **CI stability:** mark `windows/arm64` as experimental/non-blocking so releases publish reliably.
- **Windows ARM64 gating:** temporarily disable `windows/arm64` matrix legs to keep releases green while
  Go CGO + runner toolchain mismatch is investigated upstream.

## 2.42.1

### Added

- **Per-OS universal release bundles** (`*_linux_universal.tar.gz`, `*_darwin_universal.tar.gz`,
  `*_windows_universal.zip`) with `install.sh` / `install.ps1` that auto-detect CPU arch.
- **Windows ARM64 release builds** (`codehelper_*_windows_arm64.zip`) on `windows-11-arm` CI runners.
- **`package-share` CI workflow** for manual multi-platform packaging; macOS + Windows arm64 fetch from Linux.

### Changed

- **Release workflow:** `macos-15-intel` for darwin/amd64, `-tags rod`, `INSTALL.txt` in archives.
- **`install.sh` / `install.ps1`:** prefer universal bundles from GitHub releases, fallback to per-arch.
- **`package-share.sh`:** `--all-platforms`, `--macos`, CI-backed fetches; emits universal bundles when possible.

## 2.42.0

### Added

- **Multi-variant orchestration benchmark.** `codehelper orchestration eval --variants all`
  runs fresh/skip/force index and TOON vs JSON manual-chain permutations with cross-variant
  analysis. (`internal/orchestrator/eval/config.go`, `variants.go`)
- **`context body=brief`.** Eight-line source excerpt for explain/dead-code workflows; surfaced
  in `agent_brief` as a source block.
- **Dead-code workflow:** adds `scout` step; manual eval chain aligned with orchestration.
- **`AgentFacingTokensFormat`.** TOON vs JSON token estimate for slim orchestrate payloads.

### Changed

- **Orchestration quality (benchmark):** avg score **0.899 → 0.959** on 11 projects; explain
  **0.82 → 0.95**, dead_code **0.87 → 0.93** with brief body + scout.
- **Default eval harness:** manual MCP chain uses TOON; scoring recognizes TOON/brief signals.
- **Help catalog** synced for `investigate`, `edit_cycle`, `preflight` (52 tools).
- **Bundled green tools (subtree refresh):** `ge` **0.2.2** (MCP profile: `--mcp` embed/chat,
  ONNX embed fix, `start_mcp_stack.sh`, `ge bench mcp`) and `greencompress` **0.3.2**.

### Benchmark notes

- Fresh vs skip index: negligible when registry is warm; force analyze **+0.004** score.
- Orchestrate agent payload: TOON **~9% leaner** than JSON encoding.
- Prefer **`orchestrate` one-call** over long manual TOON chains for token budget.

## 2.41.0

### Added

- **Slim orchestrate responses for cloud agents.** Default `orchestrate` / `orchestration_rerun`
  return `agent_brief` + compact trace instead of full `answer_markdown` / `context_pack`.
  Pass `detail=true` for the full pack. (`internal/orchestrator/agent_brief.go`)
- **Split usage metering.** `usage.mcp` (internal tool chain), `usage.local_llm` (classify +
  compress on your machine), and `usage.agent_facing_tokens` (cloud bill estimate).
- **`context body=none`.** Symbol graph without source body — used during orchestration to
  avoid duplicating large definitions; call `context` directly when you need source.
- **Composite MCP tools:** `investigate`, `edit_cycle`, `preflight`.
- **Symbol disambiguation:** optional `path` / `line` on `context` and `impact`; query hits
  include `sym:` ids.
- **Monorepo verify:** `verify_cwd`, `verify_build`, `verify_test`, `verify_lint` in project
  config for sub-project gates.
- **Orchestration benchmark doc:** [docs/ORCHESTRATION_BENCHMARK.md](docs/ORCHESTRATION_BENCHMARK.md).

### Changed

- **Orchestration token reduction (~30% internal MCP; ~70–85% slimmer cloud payload).**
  Workflow `context` steps use `body=none`; local LLM compresses long briefs when configured.
- **`orchestrate` and `investigate` promoted** to `MCPMainTools` in `project_context`.
- **MCP tool count 52** (was 49 in 2.40.0 catalog smoke tests).

### Fixed

- Review-consolidation eval harness: explain workflow runs `context` after `query`; kickoff
  enrichment; dead-code workflow; green-compress verify cwd.

## 2.40.0

### Added

- **Local orchestration (opt-in guided investigator).** New per-project feature:
  `codehelper orchestration enable|disable|status` and MCP tools `orchestration`,
  `orchestrate`, `orchestration_rerun`, `orchestration_feedback`, `run_trace`,
  `explain_run`, `orchestration_memory`. Runs deterministic workflows
  (bugfix_triage, feature_scope, refactor_impact, explain_code, review_gate) with
  compact tool traces, SQLite run memory, and feedback/rerun loops. Local LLM
  (green engine `CODEHELPER_ENRICH_URL` or `~/.codehelper/llm.json`) optionally
  improves routing/summary; Go still validates and chooses tools.
  (`internal/orchestrator`, `internal/mcpsvc/orchestration_tools.go`)
- **Orchestration eval harness.** `TestOrchestrationEval` scores orchestrate vs
  manual MCP chains on feature/debug/explain/refactor/dead-code style tasks.

### Changed

- **Green setup defaults.** `codehelper setup` now writes embed + llm server entries
  in `~/.codehelper/green.json` when `ge` is on PATH (feeds `CODEHELPER_ENRICH_URL`
  for orchestration and index enrichment).
- **MCP tool count 49** (seven orchestration tools added to catalog).

## 2.39.0

### Added

- **`since` tool — what changed + blast radius + tests, in one call.** Fuses
  detect_changes + impact (downstream) + test_impact (reverse closure): the symbols
  changed vs `base_ref` (incl. uncommitted edits), distinct dependents, worst risk tier,
  must-update call sites, the test files to re-run, and new untracked files the index can't
  see yet — the post-edit companion to `scout`. (`internal/mcpsvc/since_tools.go`)
- **ADR / decision memory.** `agent_memory action=record` persists a decision *with its
  rationale* (`memory.Decision` gained `Rationale` + `Tags`); `search` renders the "why",
  and `plan` surfaces matching `prior_decisions` so later sessions recall a considered
  choice instead of reversing it.
- **Minimal-tools mode.** `CODEHELPER_MINIMAL_TOOLS=1` (global) or
  `codehelper config project --minimal on` (per-project) trims `tools/list` to the main
  tools to cut tool-definition token cost; hidden specialists stay callable by name and
  `project_context` keeps emitting the full grouped catalog. (`internal/mcpsvc/toolfilter.go`)
- **Connection profiles (databases + SSH) with encrypted secrets.** New `codehelper
  connections` CLI stores multiple per-project database (MySQL/Postgres/SQLite/MSSQL/Oracle/
  CockroachDB/ClickHouse/MongoDB/Redis) and SSH host profiles beside the index. Passwords are
  either referenced (`password_ref: env:VAR`) or stored **AES-256-GCM encrypted, out of the
  repo** via `connections set-secret` (key + ciphertext live 0600 under `~/.codehelper`, never
  in the project tree — `internal/secrets`). Profiles carry `enabled`, `read_only`, and an
  SSH `allowed_commands` allowlist. `project_context` reports only the non-secret view
  (name/driver/host/enabled/read_only/allowed_commands + `has_secret` bool) — the plaintext is
  never exposed to the agent. Config/discovery + secret storage only; query/command execution
  is a separate, security-gated phase. (`internal/connections`, `internal/secrets`)

### Changed

- **Process-cached graph read store.** MCP tools previously opened a fresh SQLite
  connection per call; `graph.OpenCached` keeps one read-configured store per DB file alive
  for the process (WAL multi-reader, safe re-index invalidation). Open path ~223µs → ~1µs
  (~130×) in `internal/graph/store_cache_test.go`; latency removed from every tool call.

## 2.38.3

### Added (browser — action step previews)

- **Opt-in screenshots after each browser interaction step.** When
  `ch config browser set --action-previews on` (disabled by default) and a
  `browser` call passes `preview_actions=true` with `actions`, the tool returns a
  viewport capture after each click/fill/type/etc. before the final screenshot —
  so agents can see automation as it runs without multiplying tokens unless
  explicitly enabled. Configurable via `~/.codehelper/browser.json` or
  `CODEHELPER_BROWSER_ACTION_PREVIEWS=1`.

## 2.38.2

### Changed (license — source-available)

- **Switched all three components to the VeyrForge Source-Available License.**
  codehelper, `ge`, and `greencompress` move from all-rights-reserved proprietary to
  source-available: you may **run/use** them, **view** the published source, and **submit
  suggested changes** (Contributions, licensed to VeyrForge) — but you may **not** copy,
  fork, build derivative/competing versions, or redistribute. Re-vendored the green
  licenses via `git subtree`. Not open source; not legal advice — have counsel review.

## 2.38.1

### Changed (licensing)

- **Aligned bundled green tool licenses with codehelper.** `ge` (Green Engine) and
  `greencompress` (Green Compress) now ship under the VeyrForge Source-Available License
  alongside codehelper. Re-vendored via subtree refresh.
- **Ship the notices.** Release archives include `LICENSE-ge` and `LICENSE-greencompress`
  alongside bundled binaries.

## 2.38.0

### Added (one download → the whole stack works on any OS)

- **Bundle `ge` + `greencompress` into the codehelper release.** The green-engine and
  green-compress sources are vendored under `third_party/` via `git subtree`, and each
  native build-matrix job builds `ge` + `greencompress` from that same checkout for its
  OS/arch and includes them in the codehelper archive — so a single download has
  codehelper, codehelper-mcp, and the green binaries for Linux (x86_64/arm64), macOS
  (Intel/Apple Silicon), and Windows. No cross-repo credentials or secrets: the bundle
  builds from vendored source. Refresh the copies with `git subtree pull` (see
  [third_party/README.md](third_party/README.md)). The green projects remain independent
  with their own standalone releases.
- **Installers place the bundle on `PATH`.** `install.sh` / `install.ps1` now read the
  binaries from the archive's versioned subdirectory (fixing release installs after the
  packaging change) and additionally install `codehelper-mcp`, `ge`, and `greencompress`
  when present — best-effort, skipped if absent.
- **Docs.** README documents the green engine end to end: the two opt-in features
  (semantic rerank + enrichment), what's bundled, `green.json` setup, `codehelper green`
  commands, how to disable, and exactly what you lose by doing so (nothing in core).

## 2.37.1

### Fixed (Windows build — release matrix now green on all 5 targets)

- **`internal/green` didn't compile on Windows.** `manage.go` used `syscall.SysProcAttr{Setsid}`
  and `syscall.Kill(-pid, SIGTERM)` directly — neither exists on Windows — so the new
  windows/amd64 release leg failed to build (the other four OS/arch targets passed). Split the
  detach + terminate primitives into build-constrained `manage_unix.go` (Setsid + process-group
  SIGTERM) and `manage_windows.go` (CREATE_NEW_PROCESS_GROUP + `Process.Kill`). Verified
  `GOOS=windows/linux/darwin go build ./internal/green/` all pass.

## 2.37.0

### Fixed (PHP call graph → callers/callees finally resolve)

- **PHP emitted zero `calls` edges.** `ParsePHP` indexed symbols + `imports`/`reads`
  but never called `extractCalls`, and the language was registered with `Calls: false` —
  so `impact`, `context` (callers/callees), `trace`, and `change_kit` returned **empty**
  for every PHP symbol. Across the three indexed PHP repos, caller-lookup F1 was
  0.0–0.10 (vs 0.8–1.0 for Go/Rust/C#). Root cause: `extractCallsScoped` recognized the
  call-node names of every other grammar but not PHP's (`function_call_expression`,
  `member_call_expression`, `scoped_call_expression`, `nullsafe_member_call_expression`),
  and `calleeName` didn't resolve PHP's `name`/`qualified_name` leaves.
- **Fix.** Taught `extractCallsScoped`/`calleeName` the PHP call nodes, wired
  `extractCalls` into `ParsePHP` for functions and methods, and flipped `Calls: true`.
  Measured on real indexes: PHP-origin call edges rose from 0 to thousands per repo;
  caller-lookup F1 improved from ~0.07–0.10 to 0.66–0.92 on sample PHP projects.
  Added `TestParsePHP_CallEdges`.

### Changed (release builds now cover every OS, not just linux/amd64)

- **Native per-OS release matrix.** CGO (tree-sitter) can't cross-compile to
  darwin/windows from one Linux runner, so every release since v2.2.0 shipped
  linux/amd64 only. `release.yml` now builds each target **natively** on its own runner
  — linux amd64+arm64, macOS Intel+Apple Silicon, windows amd64 — gated behind a
  `go test ./...` job, then merges the artifacts into one GitHub release with checksums.
  `.goreleaser.yaml` is retained only for local linux snapshots.

## 2.36.0

### Changed (read_workspace_file pagination → kills the biggest token sink)

- **Default 500-line read window.** `read_workspace_file` returned the WHOLE file when
  the caller passed no limit — and agents rarely pass one, so whole-file reads were ~46%
  of all injected tokens (the single biggest context cost). It now returns up to a default
  500-line window: files within it return in full (no change for the common case), while
  larger files return the first window plus a note with the exact next `offset` to page
  from (and a nudge toward query/context/ast_query for code). Explicit `offset`/`limit`
  still override. Windowing extracted into a pure `windowLines` helper with tests
  (small-file-whole, default-window-pages, explicit-slice).

## 2.35.0

### Fixed (apply_patch whitespace-drift errors → near zero)

- **Whitespace-tolerant patch re-anchoring.** `apply_patch_workspace_file` previously
  required a byte-exact `old_string` and failed outright on whitespace/indentation drift
  (tabs vs spaces, trailing space) — the single biggest source of apply_patch errors in
  the usage data (5–12% on actively-edited repos). Now, when an exact match fails, it
  retries with line-anchored whitespace tolerance: it matches `old_string` against the
  file with each line's whitespace normalized, and **only if exactly one span matches**
  (no guessing) it splices `new_string` in — **reindented to the file's actual base
  indentation** so the file's tab/space style is preserved, never the agent's wrong one.
  The response reports `whitespace_adjusted: N` + a note so the change is visible; verify
  the diff. Ambiguous (0 or >1 normalized spans) still returns the precise self-correct
  error. New tests cover tab/space reindent, trailing space, and ambiguous-refusal.

## 2.34.0

### Changed (shorter AGENTS.md → higher tool adoption)

- **Rewrote the AGENTS.md rules header from ~180 lines to ~58.** It described the same
  tool sequence three times (a routing section, a duplicate numbered "use in this order"
  list with buggy numbering, and a "strict review workflow"). Replaced with a single
  situation→tool **routing table** ("when you are X → call Y"), a compact specialized-tools
  line, tight defaults, and freshness. Every tool still appears exactly once, with a clear
  trigger. Research (and our own A/B) shows overlong/repetitive tool docs bias models
  toward built-ins; a punchy situation-based table raises the odds the right tool is used.
  Total generated AGENTS.md: 279 → 172 lines (shared guardrail/planning/intake prompt
  contracts unchanged). `codehelper repair` propagates it to every project.

## 2.33.0

### Changed (trim apply_patch/write context bloat)

- **Tiered diff-echo caps.** `apply_patch_workspace_file` and `write_workspace_file` used
  a flat 64KB diff cap, so every applied edit could dump up to 64KB of unified diff back
  into context — pure waste, since the agent already knows what it sent. Now an APPLIED
  edit echoes at most **2KB** (just enough to confirm + the `revert_token`), while a
  `dry_run` PREVIEW gets **12KB**. Large edits elide the rest (`diff_elided: true`).

### Reviewed (no code change)

- **The 5 "rarely used" tools all work and are useful** (`dead_code`, `api_surface`,
  `find_implementations`, `ast_query`, `review_diff`) — live-tested; underused for
  discovery/barrier reasons, not quality. See `docs/COMPARISON.md`.
- **Full 40-tool uniqueness audit** added to `docs/COMPARISON.md`: ~21 commodity, ~10
  differentiated, ~8 genuinely unique (the knowledge layer — gotchas/hints/glossary/
  docs_add — plus built-in self-measurement is the actual moat, not retrieval).
- Confirmed `review_diff` already emits TOON in current source (a stale in-session MCP
  server can still show JSON until the client reloads).

## 2.32.0

### Added (keep the registry to YOUR projects)

- **`codehelper projects prune`** — drops registry entries that are missing, live under
  a `.testbeds/` fixtures dir, or sit in `/tmp` scratch space, leaving only real
  projects. `--dry-run` previews. Repos on disk are never touched.
- **`codehelper projects forget <name|path> …`** — deregister specific projects;
  `--purge-index` also deletes their `.codehelper/` index dir. Accepts a name or an
  absolute path (matched to a registered root).
- Cleaned the live registry from 37 → 10 entries (removed 26 test-fixture/scratch
  clones + 1 Downloads stray), so MCP/agent tooling and `projects list` only ever
  show the user's real repos.

### Note (token efficiency / TOON)

- Confirmed every hot-path tool response (`query`, `context`, `scout`, `project_context`,
  `impact`, `trace`, `plan`, `review`, `kickoff`, workspace/ast/diagnostics/hotspots/
  dead_code, …) is already encoded as **TOON text-only** by default (`toolResultFormatted`
  + `resolveFormat`), which is what actually delivers the token savings to the model.
  The dominant remaining context cost is `read_workspace_file` (raw source, not
  TOON-compressible) — see docs/EVAL.md.

## 2.31.0

### Added (prove it — measure whether codehelper actually helps agents)

- **`scripts/codehelper-eval.mjs`** — a standalone evaluation harness (NOT wired into
  the MCP server, so it costs an agent zero context) that measures codehelper's real
  value across **any** MCP client. Two modes:
  - `analyze` — observational, zero model cost. Mines data you already have: Claude
    Code transcripts (`~/.claude/projects`), Codex rollouts (`~/.codex/sessions`) and
    codehelper's own usage events (`<repo>/.codehelper/usage/events.jsonl`). Reports
    per-tool call volume, token cost, error/empty-result rate, a +/- sentiment read of
    the agent's next message, a heuristic USEFUL/RARELY-USED/NEVER-CALLED verdict, the
    most context-heavy tools, and real before→after snippets (why a tool was called and
    what happened next). Call counts (broad, from transcripts) and token cost (from
    events.jsonl) are kept as separate lenses, never merged into a fake number.
  - `ab` — controlled experiment. Runs the same read-only task twice through headless
    Claude Code (WITH codehelper vs WITHOUT, built-ins only) and compares cumulative
    tokens, tool calls and turns. Auto-finds the `claude` CLI from the Cursor/VS Code
    extension; bound nested cost with `CH_AB_MAX_TURNS`.
- **`docs/EVAL.md`** — how to run both modes plus the first real findings: codehelper
  cuts tokens ~65% on understand/explain tasks but can over-spend on trivial one-shot
  lookups; `read_workspace_file` is the dominant token sink (~46% of injected tokens).
- `eval-results/` is gitignored (derived from local transcripts).

## 2.30.0

### Added (Codex real-token usage — measure "with codehelper" cost for a 2nd client)

- **Codex model-token tracking.** `codehelper usage` (and the `usage_report` tool) already parsed real billed tokens from Claude Code transcripts; they now also parse **Codex** rollouts (`~/.codex/sessions/**/*.jsonl`), reporting real per-session cumulative tokens (input/output/reasoning/cached) for the rollouts whose `cwd` is within the project. So actual model-token usage is now measurable for both Claude and Codex (Cursor still doesn't expose per-session counts locally — use a model-proxy for that client). This is the data needed to compare token cost with vs. without codehelper per session.

## 2.29.0

### Added / Improved

- **Gotchas + learned hints in `kickoff`.** The one-call task starter now returns the stack's curated gotchas plus any matching learned hints, so the agent sees the known pitfalls right where it's about to implement — not just at `project_context` bootstrap.
- **Patch tool diagnoses whitespace drift.** From reviewing the MCP usage logs, the only recurring error across all projects was `apply_patch_workspace_file` "old_string not found" — almost always indentation/whitespace mismatch. When the text matches after normalizing per-line whitespace, the error now says so precisely ("a match exists differing only in whitespace/indentation") instead of a generic miss, turning a wasted round-trip into a one-shot fix.

## 2.28.0

### Added (global learned-hints memory + WordPress version/sub-type)

- **`hints` MCP tool + `codehelper hints` CLI — global, cross-project agent memory.** When the agent discovers a non-obvious rule for a stack, it records a hint keyed by framework/language/dependency/project_type. Hints are stored ONCE in `~/.codehelper/learned_hints.json` (local-first) and applied to ANY matching project — `project_context` surfaces them live as `learned_hints` with no reindex needed. The store is plain JSON; `codehelper hints export/import` (or just copying the file) syncs it across machines. This is the "remember this next time, for every project on this stack" memory.
- **WordPress version + sub-type detail.** `project_context` already distinguished `wordpress_plugin` / `wordpress_theme` / `wordpress_child_theme` / `wordpress_site` in `project_type`; now it also reports the WordPress version where determinable (a site's running core from `wp-includes/version.php`, or a plugin/theme's `Requires at least` / `Tested up to` / `Requires PHP` headers) and leaves it empty otherwise. Plugins and themes get their own gotchas (ABSPATH guard / activation hooks; template hierarchy / enqueue in functions.php) on top of the WordPress base rules.

## 2.27.0

### Added (more framework gotchas + dependency-driven library gotchas)

- **Filament, Livewire, Inertia** are detected as the specific framework on Laravel (type stays `laravel`, `framework` becomes the stacked one) and carry their own gotchas — e.g. Filament: `->default()` only applies on Create pages (use `->formatStateUsing()` for Edit), never mix `->relationship()` with `->options()`, custom Tailwind classes must be in a scanned file, and v3-in-resource vs v4-split-schema files. Plus **Svelte 5 runes**, **Astro islands**, and **TypeScript** (strict/avoid-any) gotchas.
- **Dependency-driven library gotchas.** Beyond the defining framework, the profile now adds pitfalls for declared libraries that commonly trip LLMs: Tailwind (only emits classes found in scanned files; v4 import/PostCSS change), Prisma / Drizzle (regenerate the client / generate migrations after schema edits), tRPC (end-to-end inferred types — no codegen), Inertia clients, TanStack Query (owns server state), and Electron (main/preload vs renderer). These layer on top of the framework rules, deduped.

## 2.26.0

### Added (framework gotchas, child themes, monorepo split)

- **Framework/language "gotchas" surfaced up front.** The profile now carries `gotchas` — the pitfalls an LLM commonly gets wrong for the detected stack — so the agent knows the sharp edges immediately instead of discovering them by trial and extra tool calls. Curated per framework/language and shown in `project_context`. Examples: **WordPress** — enqueue CSS/JS with `filemtime()` as the `$ver` so an edited asset isn't served stale from cache (the "my CSS change does nothing" trap); **Next.js** — App Router vs Pages Router and Server-vs-Client components; **Laravel** — Eloquent over raw SQL, `migrate` / `config:clear`; **Django** — `makemigrations`/`migrate`; plus Go/Rust/Python/Godot/Unity/Unreal baselines.
- **WordPress child themes** are detected (a `Template:` header in `style.css`) and classified as `wordpress_child_theme`, with child-specific rules (enqueue the parent stylesheet first; templates override but `functions.php` is additive; `get_stylesheet_directory()` vs `get_template_directory()`).
- **Monorepo sub-project detection.** A repo with multiple stacks (e.g. `frontend/` Nuxt + `backend/` Laravel + tool config at the root) now reports each as a `sub_projects` entry with its own type/framework/version/primary language, and the root's `gotchas` aggregate every sub-stack's pitfalls. Scans immediate children plus conventional container dirs (`apps/`, `packages/`, `services/`, `crates/`, …), honoring `.gitignore`.

## 2.25.0

### Added (framework detection + dependency extraction)

- **Concrete framework detection.** Beyond the language/engine, the profile now reports a `framework` and its version, detected from file headers and declared dependencies (never the repo path): WordPress (**site / plugin / theme**, classified by `wp-config.php`/`Plugin Name:`/`Theme Name:` headers — WooCommerce only when actually present), Laravel, Symfony, Next.js, Nuxt, SvelteKit, Angular, NestJS, Astro, Gatsby, Remix, Vue, Svelte, React, Django, Flask, FastAPI, Rails, and Spring Boot. `project_type` becomes the framework when one is found (e.g. `nextjs`, `django`, `wordpress_plugin`).
- **Fixes the WordPress mislabel.** A translation plugin under a `wordpress/` directory was previously reported as `wordpress_woocommerce` purely because the path contained "wordpress". It is now correctly `wordpress_plugin` with the plugin's own `Version:` header, and WooCommerce is reported only when its markers exist.
- **Dependency list with versions.** The profile now extracts declared direct dependencies and their version constraints across ecosystems — go.mod, package.json, composer.json, Cargo.toml, requirements.txt, pyproject.toml (PEP 621 + Poetry), Gemfile, and pom.xml — surfaced as `dependencies` in `project_context` (capped at 200, deduped, sorted). Headline `version` now resolves engine → framework → language runtime in order of specificity.

## 2.24.0

### Added (engine/runtime/framework type + version detection)

- **`project_type` now recognizes game engines and reports a concrete version.** `profile.Generate` detects Godot (`project.godot` → `config/features`), Unity (`ProjectSettings/ProjectVersion.txt` → `m_EditorVersion`), and Unreal (`*.uproject` → `EngineAssociation`), and these override the runtime-derived type when a language manifest is also present (e.g. a Godot game with a Go client is `godot`, not `go`). The profile gained `version` (the headline version of the defining stack) and `versions` (every detected tech: go, php, node, react, vue, angular, svelte, laravel, rust, python, godot, unity, unreal). `project_context` surfaces both as `project_version` and `versions`.
- **Versions extracted from real manifests:** Go (`go.mod`), PHP + Laravel (`composer.json` require), Rust edition/rust-version (`Cargo.toml`), Python (`requires-python`), and JS frameworks (`package.json` deps). C#/C++/GDScript files now count toward the languages list, and engine build/cache trees (`Library`, `Temp`, `.godot`, `PackageCache`, `Binaries`, …) are pruned from the language scan.
- **GitHub-style language breakdown + primary language.** The profile now reports `primary_language` and `language_stats` (percentage of the codebase per language, by bytes, vendored/generated trees excluded), surfaced in `project_context`. The scan honors the repo's root `.gitignore` for top-level directory excludes, so gitignored vendored/fixture trees (e.g. codehelper's 14GB `.testbeds/`) don't dominate the breakdown — matching how GitHub excludes ignored code. And when no manifest or engine matches, `project_type` falls back to the dominant language — so a repo is `unknown` only when it contains no recognizable source at all (e.g. green-compress, previously `unknown`, is now detected by language).

## 2.23.0

### Added (project vocabulary seed + reviewable glossary)

- **Index-time vocabulary (`internal/vocab`).** Every index now extracts the most frequent identifiers and their sub-words across all supported languages into `.codehelper/vocab.json`. A single language-agnostic `SplitIdentifier` handles snake_case, camelCase/PascalCase, acronym runs (`HTTPServer→http,server`) and letter↔digit boundaries; terms are drawn from symbol names and signatures (so parameters are captured), filtered against a stoplist, and ranked. It is a derived artifact, regenerated on each `indexer.Run` (including watch-triggered reindex).
- **`glossary` MCP tool (review/promote/list).** `review` lists candidate terms cross-referenced against the symbol graph — each shows the symbols it `connects_to` — so a reviewer can judge real project vocabulary. `promote` writes a term + definition into the committable `project_memory.json` glossary, auto-attaching the connected symbols. `list` shows the approved glossary. This turns raw frequency into reviewed, shareable project knowledge.

### Fixed (project_type was almost always "unknown")

- **Profile written at index time.** `project_profile.json` (project_type, languages, verify commands) was produced only by the agent loop, so any project that was merely indexed reported `project_type: "unknown"`. `indexer.Run` now writes the profile alongside the other artifacts, and `profile.ReadOrGenerate` provides a live fallback so already-indexed projects resolve correctly without a reindex. As a side effect `project_context` now also surfaces `languages` and `suggested_verify_commands`.

### Fixed (binary upgrades didn't reach up-to-date indexes)

- **Backfill derived artifacts on the up-to-date path.** When an index already matched HEAD, `indexer.Run` returned before writing artifacts — so a binary upgrade that added a new artifact (vocab.json, project_profile.json) never reached projects that hadn't changed. `backfillDerivedArtifacts` now regenerates missing derived artifacts from the existing symbol graph (no reparse) on that path, so an upgrade self-applies to every project on its next watch/tool access. This is what makes "updates work for all".

### Fixed (command log hid which file each edit touched)

- **Identifier-first usage args preview.** The reviewable `args` field was a plain `json.Marshal` of the argument map, which sorts keys alphabetically — burying `path`/`symbol` behind bulky `hunks`/`content` and past the 200-char cap. `requestArgString` now emits high-signal keys first and elides large values to a size, so the file/symbol/action always survive in the log.

### Fixed (AGENTS.md tool list drift)

- **`glossary` documented** in the generated AGENTS.md tool contract; regenerating with the current binary also restores `usage_report`, which an older generator had dropped.

## 2.22.1

### Fixed (indexing hung on repos with large gitignored fixture trees)

- **`analyze`/`init` no longer hang on gitignored vendored trees.** On a repo with big ignored checkouts (e.g. `.testbeds/` holding vendored linux/kubernetes/django), the layered `.gitignore` loader descended into them and pulled their thousands of nested `.gitignore` patterns into one matcher — which then caught catastrophic regex backtracking on deep paths. `collectNestedGitignorePaths` now prunes `defaultSkipDirs` and root-ignored directories while collecting, so those trees never contribute rules. On this repo: a >2-minute hang → ~1.4s.
- **Walk prunes ignored subtrees instead of stat-then-discard.** `WalkSourceFiles` gained a dedicated exclusion-only `skipDir` parameter (kept separate from the per-file `skip`, so inclusion filters like `ast_query`'s `path_glob` are never misapplied to directories). `analyze` and `ast_query` pass the gitignore matcher as `skipDir`, so a gitignored fixture tree is never descended (no ~100k wasted stats).
- **Watch daemon no longer watches gitignored trees.** `autoindex` now passes a gitignore-based `Ignore`, and the watcher prunes ignored directories from recursive fsnotify registration — previously it registered an inotify watch per directory across vendored trees, which is slow and can exhaust the per-user inotify watch limit (starving every other watcher on the machine).

## 2.22.0

### Added (codehelper-managed local "green engine")

- **`internal/green` + `codehelper green` CLI.** codehelper can now own the lifecycle of the optional local LLM stack that powers its two opt-in features — semantic rerank (`CODEHELPER_EMBED_URL`) and index-time enrichment (`CODEHELPER_ENRICH_URL`). One config file (`~/.codehelper/green.json`) describes each server (launch command, port, health path, the env var its URL feeds); user-specific paths live there, never in the binary.
- **Always-on while MCP runs.** The MCP server exports the engine's URLs for itself and supervises the processes: it spawns any that are down on startup and a watchdog respawns any that get killed (`green.Watch`). Servers are spawned **detached**, so a single instance survives MCP restarts — no 7B model reload on every reconnect. Every codehelper entrypoint (`analyze`, `watch`, `query`, `mcp`) calls `green.ExportEnv`, so index-time enrichment (which runs outside the MCP server) is wired too.
- **Clean disable + deterministic fallback.** `codehelper green disable` (or `enabled:false`) stops the engine and exports nothing, so codehelper drops the LLM-dependent ranking signal and runs its pure deterministic path (BM25 + trigram) — nothing breaks. `enable`, `start`, `stop`, `restart`, and `status` (per-server health + pid) round out the CLI. An env var the user set by hand is never overridden.

## 2.21.1

### Added (reviewable call log)

- **Input + output preview per tool call.** Each recorded event now keeps a capped, single-line preview of the request (`args`) and the response (`snippet`), so you can review HOW each tool was used and whether its answer was any good — not just how big it was. Surfaced with `codehelper usage --verbose` (`-v`) or the `usage_report` tool's `verbose:true`: the recent-call trail expands to show, per call, the input, an output preview, status, tokens, and latency. Previews are whitespace-collapsed and length-capped (args 200 / snippet 280 chars) to keep the log scannable. Backward compatible — existing event logs load unchanged.

## 2.21.0

### Added (per-project token + tool-usage reporting)

- **`usage_report` MCP tool + `codehelper usage` CLI.** A real per-project report of where context and tokens go. Two layers: (1) **codehelper output** — how much context each MCP tool injected, broken down by tool, session, and client (claude-code / cursor / codex); measurable for **every** client. (2) **Claude model tokens** — real billed input/output/cache tokens per session, parsed from Claude Code's local transcripts (`~/.claude/projects/`). Cursor/Codex do not expose per-session token counts locally, so for those clients the report shows codehelper output only and says so. Also surfaces the last `verify`/`diagnostics` outcome and a recent-call trail (`--refs`). `--json` for machine consumption.
- **`internal/usage` package.** Records every MCP tool call (response token-estimate, latency, client, error) to an append-only, size-rotated per-project log under `<index>/usage/events.jsonl` (respects `CODEHELPER_INDEX_HOME`; gitignored). Best-effort: a recording failure never affects the tool result. Wired via server `AfterInitialize` + `AfterCallTool` hooks, so it captures all tools with zero per-handler churn.

### Why

- codehelper cannot see the agent's real LLM token bill (it lives in the client), but it **can** measure the size of every tool response it injects — and since the project rule is that agents route reads/searches through codehelper instead of raw Read/Grep/Bash, those response sizes **are** the context volume. This makes "which tool is heavy, per project" a deterministic, reviewable number.

## 2.20.0

### Added (cross-language ranking + deterministic agent guidance)

- **Rust `///` doc + signature capture.** The Rust parser now extracts leading doc comments and parameter/return signatures into `Signature`, and indexes `struct`/`enum`/`trait`/`type`/`union` items (was: only `fn` + `impl`). Natural-language and cross-lingual queries now match what a symbol *does*, not just its identifier.
- **Git co-change coupling in `change_kit`.** Surfaces files that historically change together with the target's file (evolutionary coupling mined from `git log`) — architectural "also edit Y" dependencies the call graph can't see. New `gitutil.LogNameOnly` + pure, unit-tested `internal/cochange`.
- **Ambiguity guard.** `query` warns when the top two hits are near-tied (don't trust the first blindly) — fires only when genuinely ambiguous, so zero token cost on a clear winner.
- **Adaptive `recommended_next_tools`.** Reflects the result state (nothing found → `ast_query`; one clear winner → `context`; near-ties → `context`+`scout`) instead of a static list, steering the agent to the cheapest correct next move.
- **Semantic-rerank status.** The opt-in multilingual rerank (`CODEHELPER_EMBED_URL`) now reports whether it engaged, with a reminder that semantic hits are conceptual matches to verify.

### Fixed

- **Single semantic rerank.** Removed a redundant binary-quantized rerank pass over bare identifiers that made a second embed round-trip and undid the full-precision, doc-enriched ranking. One rerank, in one place.

## 2.19.0

### Added

- **Opt-in multilingual semantic rerank** (`internal/semantic`) — point
  `CODEHELPER_EMBED_URL` at a local multilingual embedder (e.g. Ollama `bge-m3`, 100+
  languages); `query` reranks top lexical hits by semantic similarity. OFF by default;
  lexical path unchanged when no model is configured.
- Rerank-only over ≤40 lexical candidates; binary-quantized vectors; best-effort
  fallback to lexical on endpoint errors. Slovenian→English concept match unit-tested.
  Setup: `docs/SEMANTIC.md`.

## 2.18.1

### Changed

- **Deterministic vague-query expansion** — abbreviation/plural/pay-synonym enrichment
  lifts phrasing-level hit rate **65% → 84%** without embeddings.
- **AGENTS.md** teaches rephrasing vague natural language into concrete codebase terms
  before falling back to grep.

## 2.18.0

### Added

- **`kickoff` and `scope` MCP tools** — one-call task starter and idea→concrete-terms
  scoping.
- **CSS and HTML parsers** for stylesheet/markup symbol indexing.
- **FTS5 + scoped centrality** — retrieval scales to **3.2M symbols**.
- **Concept aliases** and expanded synonym/stopword tables.

### Changed

- **TypeScript indexing ~21× faster**; parse workers CPU-capped and niced in watch
  daemon.
- **`trace` ambiguous-name fix** — resolves symbols more reliably on tied names.
- **AGENTS.md v2.0** with explicit fallback-to-built-ins rule.

## 2.17.0

### Changed (plan: honest reuse verdict — no false "Likely YES")

- **`plan`'s `already_exists` no longer over-asserts the top match.** Lexical ranking can put a spurious word-match on top (e.g. a perf helper `itoa` matching "hot" for a caching task), and the old verdict said "Likely YES — `itoa` closely matches" — misleading. It now lists the top candidates as a guess to verify: "Closest existing code (ranked, may not be relevant): `X`, `Y`, `Z`. Confirm with `context` which — if any — actually fits." Consistent with plan being scaffolding, not a decision; the LLM/user judges from the candidate names/signatures.

### Tested (vibe-coding scenarios, live, web-researched workflows)

- FIX (locate `SymbolsByName`) and EXTEND (`scout` for a license field) resolve to exactly the right code + callers + pattern in one call; RESEARCH ("how ranking works") surfaces the ranking files; typo/vague inputs resolve via trigram. The weak spot — `plan`'s over-confident reuse verdict on a spurious match — is now fixed.

## 2.16.0

### Changed (QA pass: TOON consistency + smarter plan/scout ranking)

- **`detect_changes` now returns TOON** (was the last JSON-only tool) and gained a `format` param + a `count`.
- **`plan` and `scout` rank by the task's SUBJECT, not the imperative verb.** `taskSubjectTokens` strips action/filler words (add/create/fix/the/to/…) before ranking, so "add caching to docs resolution" surfaces the docs-resolution code (`Resolve`/`Lookup`/`resolve`) instead of a generic `Add` method the verb "add" exact-matched. Verified live.
- **Robustness:** typo-heavy and vague phrasings resolve correctly via trigram + BM25 — e.g. "fucntion that parses teh git diff" still returns `ChangedSymbols` #1.

## 2.15.1

### Fixed (bare-name resolution picked a test over the real symbol)

- **`SymbolsByName` now ranks exact, non-test matches first**, so a bare name resolves to the symbol a human means. The lookup had no `ORDER BY` and matched substrings, so e.g. `projectBrief` could resolve to `TestProjectBrief` — which made `context` show the test instead of the function and `test_impact` falsely report "no tests reach" (it had audited the test). Fixes `context`, `impact`, `test_impact`, `trace`, `change_kit` receiver lookup, and `bench`, all of which take `SymbolsByName(...)[0]`. Verified live; regression test `TestSymbolsByName_PrefersExactNonTest`.

## 2.15.0

### Added (`review` — deterministic diff audit; main tools sorted on top)

- **New `review` tool: a one-call diff AUDIT (no LLM)** — the write-side complement to `plan`. It lists the symbols changed vs `base_ref`, each with its blast radius + risk tier + covering-test count, then flags **`public_api_changes`** (potential breaking), **`untested_changes`** (test gaps), and **`high_risk`**, gives the **`tests_to_run`**, and a security/perf/reuse/contracts checklist. Use after editing, before finishing. Verified on this repo's last 3 commits: 91 changed symbols → 2 public-API, 15 untested, 2 high-risk (`RegisterAll`, `queryHandler`), 17 test files to run.
- **Main tools sort to the top.** `plan` and `review` now register before the workspace/edit/agent tools, so the 6 main (`project_context`, `query`/`scout`, `context`, `plan`, `change_kit`+edit, `diagnostics`/`verify`/`review`) appear first in `tools/list`. AGENTS.md (v1.8) and docs are sorted main-first with each tool's purpose, so the LLM sees the most-frequent tools up front.

## 2.14.0

### Added (project_context is now a full "what am I working with" snapshot)

- **project_context gained `os`, `git`, `scripts`, and `surfaces`** so one bootstrap call describes any kind of project:
  - `os` — the host OS (linux/darwin/windows).
  - `git` — branch + remote (embedded credentials stripped), or absent when not a git repo.
  - `scripts` — runnable commands from `package.json` scripts, `composer.json` scripts, and Makefile targets.
  - `surfaces` — what KIND of project it is: frontend / backend / api / native (C/C++/Rust) / kernel / game (godot/unity/unreal) / mobile-desktop / database / infra-ci.

  With 2.9's frameworks + dependency versions + README summary, project_context now answers "what is this, what stack/versions, what OS, is it git-connected, what can I run, which surfaces does it touch" in a single call — verified on this repo (os=linux, surfaces=[backend, infra/ci], git branch+remote, npm scripts, dep versions, summary).

### Changed

- **Rules (v1.7): proactively guide the user.** When a request is ambiguous or a change needs adjacent work (tests, docs, a migration, a flag, a security/perf implication, backward-compat), the agent should ask "do you also want X?" and offer the concrete options — using project_context's `surfaces` and plan's `decision_points` to raise what the user didn't know to ask for.

## 2.13.0

### Changed (consolidate to 6 one-shot main tools)

- **`context` now folds in the blast radius** (`blast_radius`: risk tier + dependent count + top dependents), so understanding a symbol AND assessing what a change would affect is ONE call instead of `context` + `impact`. Combined with the 2.10 source snippet, `context` returns code + signature + callers + callees + imports + blast radius in a single shot.
- **AGENTS.md (v1.6) presents the 6 main tools** the LLM should reach for — `project_context` (orient) → `query`/`scout` (find) → `context` (understand, one-shot) → `plan` (design) → `change_kit`+edit (change) → `diagnostics`/`verify` (check) — each a one-shot gatherer covering ~90% of needs, with the rest (`trace`, `ast_query`, `find_implementations`, `dead_code`, `api_surface`, `docs`/`web`, `read`/`list`) framed as specialized. Fewer tools reached for = fewer calls and less tool-selection overhead.

## 2.12.0

### Added (architect-mode planning — expert guidance, fewer calls)

- **New `plan` tool: turns a task into a grounded, step-by-step plan in ONE call** — the pre-work a senior engineer does before writing code. It (1) checks whether it **already exists** (ranked reuse candidates with caller counts), (2) shows the **blast radius + risk tier** of the closest match, (3) frames the real **decisions** as `decision_points` (extend vs add, backward-compat, trust boundary, hot path…) to resolve with the user, (4) gives a role-specific **`considerations`** checklist with **security & performance always included**, and (5) lays out implementation **`steps`** + the project's verification commands. Deliberately ONE role-parameterized tool (`role=architect|security|performance|refactor|feature`) instead of five, to avoid tool overload. Composes the existing centrality-ranked retrieval + impact + profile, so it's deterministic and grounded — the reasoning stays with the LLM.

### Eval (real repos, `CODEHELPER_PLAN_EVAL=1`, no LLM)

- On this codebase, `plan` surfaced the **correct existing symbol to extend** across roles: security "rate-limit the docs/web fetch" → `NewHTTPFetcher`; performance "speed up query ranking" → `QueryHybridWithOptions` (correctly flagged **risk=high, 50 dependents**); refactor "extract manifest parsing" → `projectBrief`. Each call returns reuse + blast radius + decisions + role checklist + steps in one shot, collapsing the project_context → query → scout → impact → read sequence an agent would otherwise run.

## 2.11.0

### Fixed (the real reason "update didn't change anything")

- **`repair` (and therefore `update`/`upgrade`) now retires stale MCP servers.** Editors keep a long-lived stdio `codehelper mcp` server alive for the whole session, so after the binary was rebuilt the editor kept serving **old code forever** — e.g. responses stayed JSON instead of TOON, `project_context` showed `0.0.0-dev`, and none of the new tools/fields appeared, no matter how many times you updated. `repair` now SIGTERMs any `codehelper mcp` / `codehelper-mcp` process that started before the current binary; the editor respawns a fresh server (the new binary) on its next tool call. Linux-only (reads `/proc`); a safe no-op elsewhere, and it never kills the current process or a server newer than the binary.

  Net effect: **one `codehelper update` now actually updates every editor's server** — no manual restart/toggle needed.

## 2.10.0

### Added (context is now a one-call feature report)

- **`context` returns the symbol's definition SOURCE (truncated to 40 lines) + signature/doc**, alongside callers/callees/imports. So "understand feature X — the code, what it does, and who references it" is ONE `context` call instead of `context` + `read_workspace_file`. Long methods include a `source_note` pointing `read_workspace_file` at the remaining lines (via `offset`). Reuses the same definition reader as `change_kit`.

### Benchmarks (`codehelper bench`, this repo, fully local, no LLM)

- **vs grep+read baseline:** median **66.7% fewer tool calls** (1 vs 3) and **99.7% fewer tokens** (16 vs 5,875 per answer) for "who calls X" / "locate X", at equal answer quality — grep precision is only 0.49, so half the files a grep agent reads are noise.
- **TOON vs JSON:** **41.6% fewer tokens** on a 40-hit `query` payload (2,272 → 1,327 est tokens; 9,090 → 5,311 bytes).
- **Retrieval quality:** recall@1 = 1.00, MRR = 1.00, p50 3.9 ms over 40 queries. Centrality lifts ambiguous-query **MRR@10 0.886 → 1.000, P@1 0.86 → 1.00**, no regressions on specific queries.
- **Caller lookup:** precision/recall/F1 = 1.00 on informative symbols (33/40); 3,489 concrete call edges; internal-unresolved just 246 vs 5,792 external (deps/stdlib).

  Reproduce: `codehelper bench` and `go test ./internal/retrieval/ -run RealIndex -v`.

## 2.9.0

### Added (make the bootstrap call actually worth it)

- **`project_context` now returns a real project brief**, so the agent learns the stack in one call instead of reading manifests itself:
  - **`summary`** — what the project is, from its README (title + first prose paragraphs; badges/images/HTML stripped, length-capped).
  - **`frameworks`** — detected from dependencies and marker files, including game engines (`react`, `nextjs`, `vue`, `svelte`/`sveltekit`, `angular`, `laravel`, `django`, `fastapi`, `flask`, `rails`, `gin`, `fiber`, `capacitor`, `electron`, plus `godot`/`unity`/`unreal`).
  - **`key_dependencies`** — direct dependencies WITH versions, parsed from `go.mod`/`package.json`/`composer.json`/`requirements.txt`/`Cargo.toml` (`name@version`, indirect deps excluded, bounded).

  This follows the research that structure-aware bootstrap context (stack, dependencies, README) is what an agent needs first to reason like a human would.

### Note

- `codehelper update` already rebuilds **both** `codehelper` and `codehelper-mcp` with the embedded version and fans them out to every install dir, then repairs all projects — so one `codehelper update` is all that's needed to get every editor's MCP server onto the latest (no more `0.0.0-dev`).

## 2.8.0

### Changed (stop wasting context; TOON everywhere)

- **`read_workspace_file` and `list_workspace_directory` now default to TOON** (text-only, like the rest) so the model actually reads the token-efficient form in Cursor/Claude Code instead of JSON. `format=json` still available.
- **`read_workspace_file` gained line-range reads** (`offset` + `limit`) plus `total_lines`/`line_start`/`line_end`, so an agent can pull just the slice it needs instead of a whole large file. Large whole-file reads now return a `note` steering to `query`/`context`/`ast_query`, and the description says to prefer those for code.
- **`list_workspace_directory` description and `recommended_next_tools` now steer to `query`/`scout`** (search the whole indexed graph) instead of walking the tree directory-by-directory and reading files. Targets the observed failure where an agent burned many calls listing dirs and reading 40 KB+ files instead of one `query`.

### Fixed

- **Release/dev builds embed the version** via `-X internal/version.linkVersion`, so `project_context` reports the real version (e.g. `2.8.0`) instead of `0.0.0-dev` when the MCP server runs with a different project as CWD. This makes a stale MCP server (the usual reason TOON "isn't working" in an editor) visible at a glance. goreleaser ldflags corrected to the same path.

## 2.7.0

### Changed (drive tool usage — agents were stopping at project_context)

- **`project_context` now steers the agent onward instead of being a dead end.** It returns a `next_step` directive ("this is a bootstrap, not an answer — now call query/scout, then context/trace…"), its `recommended_next_tools` point at the high-value tools (`query`, `scout`, `context`) instead of `list`/`read`, and its description states it does NOT search code. This targets the most common failure mode: the agent calls `project_context` once and stops (or falls back to reading files) rather than chaining into the retrieval tools.
- **`project_context` now defaults to TOON** like the other tools (text-only; `format=json` for the JSON object).
- **Rules reinforce the loop.** AGENTS.md (v1.5) and the CLAUDE.md/`.cursor` rules now spell out the standard loop — `project_context` (once) -> `query`/`scout` -> `context`/`trace` -> `change_kit` -> `diagnostics` — and that a purpose-built tool (trace/impact/find_implementations/ast_query/docs/dead_code) beats re-reading files. Design follows Anthropic's "writing effective tools for agents" guidance (descriptions and next-step context drive tool selection).

## 2.6.1

### Fixed

- **`repair` now prunes deleted projects from the registry** instead of skipping them every run. When a registered project's root directory no longer exists, its entry is removed from `~/.codehelper/registry.json` (only on a definitive "does not exist" — a permission/transient stat error is still skipped, not deleted). `update`/`upgrade` inherit this since they call `repair`. Adds `Registry.Remove`.

## 2.6.0

### Added

- **Shader / material language indexing for every engine.** `.hlsl/.hlsli/.fx/.fxh/.cginc/.compute/.usf/.ush` (Unity & Unreal HLSL), `.shader` (Unity ShaderLab), `.gdshader/.gdshaderinc` (Godot), `.glsl/.vert/.frag/.geom/.comp/.tesc/.tese` + ray-tracing stages (GLSL), `.metal` (Metal), and `.wgsl` (WebGPU) are now indexed via one C-family line-based lite extractor (functions, structs, cbuffers, `#define`, uniforms/varyings, WGSL `fn`/`var`, and the ShaderLab `Shader "name"`). Combined with existing C# (Unity), C++ (Unreal), and GDScript (Godot) support, codehelper now covers the scripting **and** shader surface of Unity, Unreal, and Godot projects. Parser version bumped to 5 (one-time full reindex). Verified: 555 shader symbols on a real Godot project.

## 2.5.0

### Added

- **GDScript (Godot) indexing.** `.gd` files are now indexed via a line-based lite extractor (`func`, `class`, `class_name`, `signal`, `enum`, `const`, and `@export`/`@onready` vars). Previously `.gd` fell through to generic text and Godot codebases were invisible to `query`/`scout` — on a real Godot project this lights up 20k+ symbols (the level editor, gameplay, autopilot, etc.). Parser version bumped to 4, so the next index is a one-time full reindex.

### Changed

- **TOON responses now actually reach the model.** The array-heavy tools (`query`, `scout`, `context`, `impact`, `trace`, `dead_code`, `ast_query`, `api_surface`, `change_kit`, `find_implementations`, `docs`, `web`, `diagnostics`, devkit) return their default TOON encoding as the text block with **no** `structuredContent`. Previously the JSON payload was also attached as `structuredContent`, which structured-output clients (Claude Code, Cursor) always prefer — so the model read the verbose JSON and the token-efficient TOON was wasted. Pass `format=json` for JSON text. Small fixed-shape tools (`project_context`, workspace reads, edit previews) keep `structuredContent`.
- **Output schemas removed** from the MCP tools. A tool with no `outputSchema` can't emit an invalid one, which retires the entire class of "invalid outputSchema poisons tools/list → strict clients silently get no tools" failures while letting every client read the text result.

## 2.4.11

### Breaking (retrieval is lexical-only)

- **Removed embeddings/vectors entirely:** deleted `internal/retrieval/vector`, `internal/retrieval/embed` (ONNX, Ollama, OpenAI/Voyage/Cohere providers, resolver), `internal/paths/vectors.go`, the `--embeddings`/`--embeddings-force` analyze flags, the `watch --embed` policy, and the `CODEHELPER_EMBED_*`/`OLLAMA_HOST` env vars. Retrieval is now BM25 + trigram fused with RRF — no vector index, no model dependency, no network at query time. Fused via RRF, vectors did not beat the lexical baseline on code-symbol queries.

### Added (retrieval quality)

- **Call-graph centrality ranking:** `query` and `scout` now boost hits by `0.15·log1p(inbound calls)` (`graph.InDegrees` bulk lookup + `retrieval.DefaultCentralityWeight`), so the most-depended-on definition wins lexically ambiguous queries; the context pack inherits the boosted scores. A/B over the codehelper index (query + ambiguous + scout-task golden sets): **MRR@10 0.823→0.921, P@1 0.74→0.89, zero regressions** (e.g. scout "acquire a single instance lock" 7→2, query "save"/"load" 2→1) — reproducible via `go test ./internal/retrieval/ -run RealIndex -v`.
- **Corpus IDF in BM25:** lexical scoring now weights each query token by its smoothed inverse document frequency over the candidate pool (`idfForTokens`), so common words ("file", "data") no longer drown the rare, discriminating token ("debounce", "centrality") by sheer match count. Raw scores are then normalized to `[0,1]` before reranking so the fixed additive signals (`exact_name`, `centrality`, …) mean the same thing for every query. Lifts the A/B *baseline* (centrality-off) MRR@10 0.823→0.842 and recovers previously-missed targets (e.g. `rerankWithSignals` for "rerank search results by centrality" was absent from the top-10, now surfaces).
- **Intent-gated test ranking:** for the default reuse/explain intent, test symbols whose names happen to contain the query term are demoted (×0.5, applied last) so they can't outrank the implementation they cover; for `test`/`debug` intent they're boosted (`nearest_test`) as before. Mechanism guarded by `TestTestDemotion_RanksImplementationOverItsTest` and `TestIDF_DownweightsCommonTokens`.

### Fixed (MCP "connected but tool calls hang")

- **Root cause: the per-call `ListRoots` round-trip blocked indefinitely.** Nearly every tool resolves the workspace by asking the client for its roots (`scopeRoots`→`mcpWorkspaceRoots`→`SessionWithRoots.ListRoots`). mcp-go's stdio session *always* satisfies the `SessionWithRoots` Go interface, so the server issued a server→client `ListRoots` even to clients that never advertised the roots capability — and when a client advertised roots but didn't answer promptly, the call waited **forever** (the tool-call context only cancels on disconnect). Result: the client shows "✓ connected" (initialize + tools/list never touch roots) yet every `repo`-less tool call hangs. Reproduced via the new `mcp.log`: `project_context` took 24,994 ms against a roots-advertising client that never replied.
- **Three-part fix in `internal/mcpsvc`:**
  1. **Capability gating** — skip `ListRoots` entirely unless the client advertised the roots capability at initialize (`sessionRootsCap`, recorded by always-on `registerCapabilityHooks`). Clients without roots now resolve by CWD instantly (10 ms vs 2 s).
  2. **Bounded round-trip** — `ListRoots` runs under a 2 s timeout (`CODEHELPER_ROOTS_TIMEOUT_MS` to override); on timeout it falls back to CWD scoping, which reliably resolves the project since clients spawn the server with CWD = project root. Worst case 25 s+ hang → ~2 s, still resolving the right repo.
  3. **Per-session roots cache** (`rootsCache`, 3 s TTL, evicted on session close) collapses the up-to-two `ListRoots` calls per tool invocation — a pattern the mcp-go code itself flags as "unreliable across MCP clients" — down to one.
- **`server.WithRecovery()` enabled** so a panic in any tool handler returns an error instead of tearing down the stdio loop (which would leave the client "connected" but every later call dead).
- **Diagnostic trace at `~/.codehelper/logs/mcp.log`** (JSONL, 8 MiB rotation; `CODEHELPER_MCP_LOG=off` to disable): records server start (version/CWD/exe), each session's advertised client + capabilities + negotiated protocol, every tool call with duration + repo arg + error, and each `ListRoots` outcome with timing. This is how the hang was pinpointed and how future "connected but…" reports can be diagnosed in one look.
- **Dropped the spurious `project_context` warning** "MCP workspace did not resolve to an indexed repo; run codehelper analyze". It fired whenever the client hadn't advertised MCP roots (`!selOK`) — now the common case under capability gating — even though `resolveRepo` had already matched the repo by CWD and passed the scope assertion. The bogus warning falsely told the agent the project wasn't indexed.
- Covered by `TestListRootsTimesOutInsteadOfHanging` and `TestListRootsSkippedWhenCapabilityNotAdvertised`.

### Fixed (`codehelper update` left stale binaries)

- **`update` now refreshes ALL install locations and `codehelper-mcp`.** Previously it replaced only the running binary's path (`os.Executable()`), so the second install copy (the `~/.local/bin` vs `~/go/bin` drift) and the thin `codehelper-mcp` binary stayed stale — a client pointed at the other path kept launching the old build (the classic "I updated but nothing changed"). `update` now builds `codehelper-mcp` and fans **both** binaries out to every existing install dir (`fanOutBinaries` → `~/go/bin`, `~/.local/bin`, plus the executable's own dir) via staging-file + rename (which replaces even a running binary on Linux/macOS). Verified: after `update`, all four binaries (codehelper + codehelper-mcp × both dirs) are fresh and each `codehelper-mcp` lists 30 tools.

### Added (self-healing updates — `codehelper repair`)

Updating the binary no longer silently leaves other projects on stale rules, config, or index schema:

- **`codehelper repair`** sweeps **every** registered project and makes it consistent with the current binary: rewrites the per-client tool-first rules (CLAUDE.md + `.cursor/rules`), refreshes MCP config + Claude Code approval, **re-indexes any project whose schema/parser version changed** (a no-op for up-to-date ones), and **restarts running watch daemons** so they run the new binary. Never starts a daemon for a project that didn't have one.
- **`codehelper update` runs the repair automatically** after rebuilding (`--skip-projects` to opt out), so one update fixes all projects.
- **`parser.Version` bumped to 3** (doc-comment indexing) — so the update sweep reindexes existing projects to pick up parser changes. The pattern: bump the version whenever parser/extraction output changes, and every project self-heals on the next `update`/`repair`.
- **MCP-server safety net:** `project_context` (the once-per-session bootstrap) writes the client rules on first use (per-process dedup), so even an update path that doesn't run `repair` (e.g. `npm run update:go`) still applies the rules the moment a restarted client connects.

Verified across 5 real projects (Go, Godot, Laravel): rules + config written, v2→v3 reindex, daemons restarted.

### Fixed (clients connect but don't use the tools; cross-project isolation)

- **Agents now actually call the connected MCP.** A connected server is invisible to the model unless the project's instruction file tells it to use the tools — and each client reads a *different* file: Codex→`AGENTS.md` (already written), but **Claude Code→`CLAUDE.md`** and **Cursor→`.cursor/rules/`** got nothing. New `setup.WriteClientRules` writes a tool-first directive to both: a dedicated `.cursor/rules/codehelper.mdc` (`alwaysApply: true`) and a *managed block* in `CLAUDE.md` (markers preserve the user's own content). Wired into `init` **and** `analyze` (written before the up-to-date early-return, so a running watch daemon refreshes it without a re-init). Guarded by `clientrules_test.go` (idempotent + preserves user content).
- **Workspace scoping now falls back to the server's working directory.** `mcpWorkspaceRoots` previously only used the MCP roots protocol; a client that doesn't advertise roots (some Cursor/Codex setups) made every tool error "workspace not initialized" (so the agent gave up) *and* disabled the cross-project isolation guard. It now falls back to the spawn CWD (the open project), so tools scope correctly and the isolation guard stays active even without the roots protocol. This is what keeps 10+ indexed projects strictly separate — a tool call in project A still errors if it resolves to project B.
- **AGENTS.md refreshed** — lists the new tools (`trace`, `change_kit`, `api_surface`, `find_implementations`, `diagnostics`, `ast_query`) and drops the stale "vector retrieval" mention.

### Added (deterministic, token-saving tools — `change_kit`, `api_surface`, `find_implementations`)

Three tools in the `scout` spirit — they do deterministic work the agent would otherwise spend tokens and tool-calls on (32 tools total):

- **`change_kit`** — everything to change one symbol safely, in a single call: its definition source, every call site (with the exact calling line), the tests that cover it, the risk tier, and a consistency checklist. Replaces the read/grep round-trips before an edit and stops you missing a caller. Verified real: `change_kit Acquire` → risk=medium, 7 callers (each with `daemon.Acquire(...)` line), 3 covering tests.
- **`api_surface`** — the public API of a package/directory: its exported symbols + signatures (+ doc-comment summaries) in one query, so the agent learns what a package exposes without reading every file. Proper export semantics (Go uppercase-initial; leading-underscore private elsewhere); `include_unexported=true` for internals.
- **`find_implementations`** — heuristic interface→implementation map for Go without go/types: reads the interface's method set from source, reports types whose indexed methods cover it (structural typing); partial matches list the missing methods (often = embedding). Verified real: `find_implementations Extractor` → `fnExtractor` implements `[Extract, Capabilities]`.

All deterministic, structured output + TOON, and missing-symbol errors steer to `query`. Guarded by `devkit_tools_test.go`.

### Added (deterministic navigation — `trace`)

- **`trace` MCP tool — call-graph navigation in one deterministic step (29th tool).** Agents otherwise answer "how does A reach B?" by hopping `context`→`context` (a tool call, and tokens, per hop). `trace` does a single BFS over the resolved call graph: with `from` + `to` it returns the **exact shortest call path** between two symbols (and detects when the dependency actually runs the other way); with only `from` it returns the outbound call-flow tree. This is the exact multihop navigation ranked search can't give — directly targeting the "hidden/transitive dependency" failure mode that the 2026 *Navigation Paradox* work (CodeCompass, arXiv:2602.20048) shows graph navigation fixes (+23pp). Exact, no LLM, no wasted tokens. Resolves a symbol by exact name (preferring a non-test definition) so a path never starts at a test caller. Structured output + TOON; missing symbols steer to `query`. Verified on the real index (`QueryHybridWithOptions`→`bm25Score`, `scoutHandler`→`callerCountOf`).

### Changed (LLM guidance — errors are feedback)

- **Tool errors and empty results now steer the next action** (following Anthropic's *Writing effective tools for agents* — description=UI, schema=form, errors=feedback). `context` on a missing symbol points the agent at `query`/`ast_query` and the stale-index fallback instead of a bare "not found"; empty `query` results name concrete next moves (rephrase, single distinctive term, `ast_query`, `read_workspace_file`, re-index); `context`/`query` empty-name errors explain the valid input form. Fixed a real **stale-guidance bug**: the `context` note pointed at `cypher path_between`, a tool that was removed — now points at `impact`. Guarded by `guidance_test.go`, including a regression test that no removed tool (cypher/scip/list_repos) is referenced in any live guidance string.
- **New [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md)** — the single reference for how retrieval/structural-search/indexing work, their measured performance and quality numbers, what each tool responds, and the errors-as-feedback guidance design. Linked from the README (tool count corrected to 28).

### Added (diagnostics, scout usage, big-repo indexing)

- **`diagnostics` MCP tool — an LSP-free compiler self-check loop.** Auto-detects the repo toolchain and runs its canonical static checks (Go: `go build ./...` + `go vet ./...`; Rust: `cargo check`; TypeScript: `tsc --noEmit`) through the sandboxed argv-mode verify runner, then parses the compiler/vet output into structured `file:line:col` problems. `command` overrides the auto-detected check. This is the one capability LSP-backed competitors have that a pure tree-sitter index lacked — without taking on an LSP. Structured output + TOON. (28th MCP tool.)
- **`scout` now returns `usage_of_top` — a real call site of the top reuse candidate.** Instead of only telling the agent a symbol exists, scout reads a non-test caller's body, finds the line that invokes the candidate, and returns `{caller, loc, code}` so the agent can copy the calling convention without another round-trip.
- **Parallel, batched cold-build indexing (`analyze`).** Two changes to the index build for 100k+ file repos: (1) **batched-transaction writes** — `graph.IngestFiles` persists a batch of files' symbols/edges in one transaction with prepared statements instead of a WAL transaction+fsync per row; measured **56.8× faster** on the write path (16.8s→295ms for 20k syms + 30k edges). (2) **parallel parsing** — tree-sitter parsing now runs across `GOMAXPROCS` workers feeding a single batched writer (the Codebase-Memory cold-build pattern, arXiv:2603.27277), 1.48× on the parse phase. The combined effect turns the parse+ingest phase for a 20k-file repo from an extrapolated ~70s into <5s, producing a byte-for-byte identical graph (verified: same symbol/edge/file counts serial vs parallel). Race-tested. Phase timings now logged (`parse+ingest done`, `post-processing done`) to diagnose slow indexes.

### Added (retrieval quality — doc-comment recall)

- **Leading doc comments are now indexed for natural-language search.** The Go parser captures a symbol's preceding `//` doc-comment block into its `Signature` (lightweight — no schema change; a blank line ends the block, truncated to 160 chars), so a query matches what a symbol *does*, not only its identifier. Fixes the acronym/renamed-symbol gap that pure name matching can't: "reciprocal rank fusion" now finds `func RRF` (**rank 0 → 1**). On the real-index A/B this makes the *specific-concept* and *ambiguous-name* benchmark groups **perfect (MRR@10 1.000, P@1 1.00)** while overall MRR@10 holds at 0.913. Guarded by `TestGoDocComment_IndexedForNLSearch`. This is the research-backed approach (CodeSearchNet; "appending docstrings/comments improves NL searchability") — still no embedding model.

### Evaluated — Rust offload (declined, with data)

- Measured the indexing hot path on the real repo: file read is ~5µs/file (negligible); parse+extraction is ~1.8ms/file and dominant — but that cost is the **Go symbol/edge extraction logic**, not the tree-sitter parse (already native C via cgo). Since extraction is **already parallelized across cores** (and the prior real bottleneck — per-row DB writes — was fixed at 56.8×), a Rust rewrite of the 19 language extractors would buy a constant-factor gain on an already-parallel path at the cost of a second toolchain + FFI boundary. **Decision: stay pure-Go** (the lightweight, single-toolchain design); revisit only if a >500k-file repo proves extraction-bound.

### Added (retrieval quality — synonym gap)

- **Programming-verb synonym expansion (`query` + `scout`):** the query is enriched with verb/noun synonyms (get/fetch/load, close/shutdown/release, acquire/obtain/grab, save/store/persist, …) so a task phrased differently from the symbol still finds it — the no-model form of SIRA-style query-vocabulary enrichment (arXiv:2605.06647), which closes the lexical synonym gap without an embedding index or reranker model. Expansions are searched and scored at a discount (`synonymWeight` 0.4) and a synonym absent from the corpus contributes nothing, so precision is preserved (zero regressions on the A/B controls). Real-index effect: "shut down the store"→`Close` **8→2**, "obtain a single instance lock"→`Acquire` **4→1**, "store a task to disk"→`Save` **3→1**. New benchmark Group D (synonym-gap) guards it; mechanism guarded by `TestExpandSynonyms` / `TestSynonymExpansion_FindsCrossVerbSymbol`. This is codehelper's answer to the long-standing "synonym/renamed-symbol recall gap" open question — addressed lexically, no model dependency.

### Added (structural search)

- **`ast_query` MCP tool:** precise structural code search via a tree-sitter S-expression pattern — find every AST node of a given *shape* (e.g. `(function_declaration name: (identifier) @name)`, or `.Lock()` call sites via `(call_expression … (#eq? @m "Lock"))`) where lexical `query` can only match text. Reads files live from disk so results are **never stale**; 19 languages (Go, Python, TS/JS, Rust, Java, C#, C/C++, PHP, Ruby, Kotlin, Swift, Scala, Lua, Elixir, Bash, HCL, protobuf) via `internal/parser.ASTQuery`. Returns per-match `path:line`, capture name, node kind, and snippet, with structured output (`WithOutputSchema`) + TOON text. **Scales to 100k+ file repos:** files are scanned in parallel across all cores (`parser.ScanFiles`, one scanner per worker — the ast-grep technique), with early-exit once `max_results` is reached so a matching query is near-instant regardless of repo size. Benchmarked on a synthetic 100k-Go-file repo: a matching query returns in **~5ms scanning only ~70 files** (early-exit), and a worst-case no-match full scan of all 100k files in **~280ms** (~9× faster than serial, was capped/partial before). Respects `.gitignore` (reuses the same layered matcher `analyze` uses) so vendored/generated trees aren't scanned. Bounded and safe by construction: one compiled query + reused parser/cursor per worker (no per-file recompile), results capped at 200 and sorted by `path:line`, prompt context-cancellation, `path_glob` that can't escape the repo, RE2 predicates (no ReDoS), and a recover() that turns a malformed `#match?` regex into a clean error instead of crashing the server. Race-tested. (27th MCP tool; well under the ~40 client soft-cap.)

### Added (multi-client setup)

- **`codehelper init` wires three clients in one step:** per-project `.mcp.json` (Cursor/VS Code), Claude Code approval recorded in `~/.claude.json` (`projects[<root>].enabledMcpjsonServers`, since `.mcp.json` alone is gated behind approval), and a global `[mcp_servers.codehelper]` block in `~/.codex/config.toml` for Codex. All idempotent and non-fatal to indexing.
- **Tool annotations:** read-only graph/docs tools carry `readOnlyHint`/`idempotentHint`/`openWorldHint`; write/verify tools carry `destructiveHint`/`openWorldHint` so clients can auto-approve safe tools.

### Breaking (goal alignment cleanup)

- **Removed CLI:** `codehelper review` (use `codehelper agent review`).
- **Removed dead code:** unregistered MCP handlers (`rename`, `cypher`, `scip_export`/`scip_import`, `project_profile`, `question_gate`, `patch_critic`, review sub-tools), packages `internal/scip`, `internal/cypherdsl`.
- **Todo execute:** `agent_execute_todo` and HTTP execute require **Approved** todos (not `Planned`).
- **Write gate:** `POST /v1/tools/call` for `write_workspace_file` / `apply_patch_workspace_file` requires `task_id` with approved todos (or `force_write`).

### Added (goal alignment)

- **LLM-enriched plans:** `plan.BuildEnriched` — deterministic skeleton + optional Plan-mode LLM turn; wired to `POST /v1/tasks` (`enrich_llm`), regenerate, `agent plan --llm`, MCP `agent_plan enrich_llm`, VS Code Plan tab.
- **Skills wiring:** matched skills inject implementation notes and verify hints into plans.
- **Research gate:** plans that need research get a `research-first` decision point until Research First runs or user skips.
- **Docs:** [docs/AGENT_API.md](docs/AGENT_API.md) — HTTP API contract for IDE adapters.
- **Branding:** "senior workflow" renamed to **strict review workflow** (not the removed Senior Loop).

### Breaking (MCP trim — agent workspace)

- **Public MCP surface (16 tools):** `project_context`, `query`, `context`, `impact`, `detect_changes`, `review_diff`, `verify`, `finish_check`, `agent_plan`, `agent_execute_todo`, `agent_memory`, `read_workspace_file`, `write_workspace_file`, `apply_patch_workspace_file`, `revert_workspace_edit`, `list_workspace_directory`.
- **Removed MCP tools:** `context_pack` (use `query` with `include_context_pack`, `limit`, `budget_tokens`), `contract_guard`, `test_gap`, `risk_score`, `perf_guard`, `migration_guard`, `architecture_lint`, `security_context`, `release_readiness`, `explain_plan_risk`, `summarize_change`, `expand_request`, `select_pattern`, `project_profile`, `question_gate`, `patch_critic`, `get_verify_plan`, `agent_research`, `rename`, `cypher`, `scip_export`, `scip_import`, plus prior removals (`context_pack_v2`, `memory_search`, `agent_review`, `context_plan`, Senior Loop tools).
- **`agent_memory`** `action=search` replaces `memory_search`.
- **CLI:** `codehelper review` deprecated (no `--mode`); use `codehelper agent review`.
- **Pattern expansion** is CLI-only: `codehelper expand-request` (not MCP).

### Added (agent core alignment)

- **VS Code Plan tab:** Implementation options, decision-point buttons, blocks approve/execute until decisions resolved; History tab shows structured `final_summary`.
- **Explore then plan:** Plan tab runs read-only Ask exploration, then `POST /v1/tasks` with `prior_context`.
- **Agent core:** Repo-aware `plan.Build` (query/context/impact), execute preflight, bounded debug loop, command blocklist, chat→task `RecordMessage`, `SetFinalSummary` (structured final summary sections), `ProposeMemory`.
- **CLI:** `codehelper run`, `memory list|approve|reject`, `tasks list|show|timeline`.
- **HTTP:** `POST /v1/memory/proposals` for Save Memory.
- **VS Code:** Diff and History tabs, Save/Reject memory, rules editor, panel Review/Verify/Finish buttons, Create vs Quick plan.

### Cleanup (agent core alignment — dead code + plan workflow)

- **VS Code extension:** Removed unreachable panel chat modes (`review`/`verify`/`finish`), unused `attachFiles`/`attachFolder` handlers, unused `updateTaskPlan` API wrapper, legacy `codehelper.model` setting, and stale `workflow.mode` / `#secrets-line` UI refs.
- **Plan workflow:** Panel **Plan** mode and `/plan` now call `createTask` only (no preceding LLM plan chat). Use **Ask** for read-only exploration.
- **Plan tab:** Todo execution feedback shown in `#plan-exec-result` (gate output and errors).
- **Go core:** `project_context` entrypoint list uses `mainPanelProvider.ts` (not deleted `mainPanelProviderV2.ts`).

### Added (VS Code Plan tab)

- **VS Code extension:** **Plan** panel tab wired to `codehelper serve` `/v1/tasks` — create plan, task picker, editable todo notes, approve all, execute next/selected todo, timeline.
- **`coreClient` task API:** `listTasks`, `createTask`, `getTask`, `patchTodo`, `executeTodo`, `getTaskTimeline`.
- **Unified plan flow:** Plan mode and `/plan` create persisted tasks via core API (removed hybrid CLI `agent plan` JSON appendix in chat).
- **Commands:** `codehelper.openWorkspacePanel`, `codehelper.openSettingsTab` registered in `package.json`.
- **Docs/scripts:** Removed stale `grounded_answer` / `review_current_diff` references; `verify-codehelper.sh` smoke tests `agent_plan` + `agentapi`.

### Added (editable todos)

- **MCP tools:** `agent_plan` (persist editable plan + todos under `.codehelper/tasks/`), `agent_execute_todo` (run one todo through the agent loop with optional verify gate).
- **HTTP API** on `codehelper serve`: `GET/POST /v1/tasks`, `GET /v1/tasks/{id}`, `PUT /v1/tasks/{id}/plan`, `PATCH /v1/tasks/{id}/todos/{todoId}`, `POST /v1/tasks/{id}/todos/{todoId}/execute`, `GET /v1/tasks/{id}/timeline`.
- **CLI:** `codehelper agent plan --save`, `codehelper agent step --task-id --todo-id`.
- **Packages:** `internal/taskstore` (goal-aligned task JSON), `internal/plan` (deterministic plan builder), `internal/agent.ExecuteTodo`.

### Breaking (Senior Loop removal — goal-aligned cleanup)

- **Removed CLI:** `codehelper task`, `codehelper agent-run`, `codehelper senior`.
- **Removed MCP tools:** Senior Loop conductor and aliases (`task_start`, `start_task`, `task_status`, `task_next_step`, `task_next`, `task_add_decision`, `task_add_question`, `get_next_action`, `execute_task`, `approve_edit_intent`, `task_finish_check`, `infer_task`, `review_current_diff`, `grounded_answer`).
- **Removed packages:** `internal/seniorloop`, old phase-based `internal/taskstore`.
- **Removed HTTP API (replaced above):** legacy Senior Loop task endpoints on `codehelper serve`.
- **Simplified:** `finish_check` no longer requires `task_id`; `agent plan` drops agent contract preview; VS Code extension removes `codehelper.workflow.mode`.
- **Relocated:** `BuildFinishCheck` → `internal/review`; question gate → `internal/questiongate`.
- Use **`agent_plan`** → edit todos → **`agent_execute_todo`** (or HTTP `/v1/tasks`) for stepwise orchestration; **`codehelper agent chat`** remains for free-form turns.

### Other

- **Indexer:** Respects layered `.gitignore` (repo root + `.git/info/exclude`, plus nested `.gitignore` with path-prefixed heuristics). `codehelper analyze --progress-json` emits JSON progress lines (`_ch_progress`) on stderr for IDE UIs.
- **VS Code extension:** Thin adapter over `codehelper serve`; panel provider renamed from `mainPanelProviderV2` to `mainPanelProvider`; removed dead `prefetchBroadAskEvidence` setting.

## 2.4.0–2.4.10

- No separate tagged releases in this range; cumulative changes shipped as **2.4.11** (above).

## 2.3.0

- **Senior Loop (historical):** Task files under `.codehelper/tasks/`, CLI `task`, `profile`, `agent-run`, `model-eval`; MCP tools include `get_next_action`, `execute_task`, `finish_check`, `context_pack_v2`, `patch_critic`, `project_profile`, `memory_search`, and related gates.
- **expand_request:** Deterministic requirement expansion from bundled/repo pattern packs (`internal/patterns`, `.codehelper/patterns/`); CLI `expand-request`, `patterns install`, `agent plan|review|verify|finish`; MCP `expand_request`, `select_pattern`, `infer_task`, `context_plan`.
- **VS Code extension:** New [`vscode-extension/`](vscode-extension/) — thin UI over the `codehelper` binary (commands for plan/review/verify/finish/expand).
- **Performance posture:** Documented architecture: VS Code → Codehelper CLI/MCP → graph tools → optional local LLM; small context packs and bounded tool orchestration.

## 2.2.0

- Added Senior Review MCP tools: `review_diff`, `risk_score`, `contract_guard`, `test_gap`, `architecture_lint`, `migration_guard`, `perf_guard`, `security_context`, `release_readiness`, `explain_plan_risk`, and `summarize_change`.
- Added `codehelper review`, `codehelper senior`, and `codehelper rules install` CLI commands.
- Added framework rule packs for Laravel, WordPress, Node, Go, and Svelte.
- Added release gating output contract (`completion_state`, `can_claim_done`, `missing_before_done`) and AGENTS senior workflow defaults.
- Added SARIF security bridge primitives for integrating Semgrep/CodeQL findings into review context.

## 2.1.1

- Added `doctor` command for repository health diagnostics (freshness, meta, registry, watch state).
- Added `impact` controls for `include_tests` and `max_candidates` to reduce noisy blast-radius output.
- Added strict `rename` preflight checks with conflict/public-contract guards.
- Improved Windows install/update flow and refined repository ignore rules.

## 2.1.0

- Improved context-pack quality with dependency-neighbor expansion and richer ranking reasons.
- Improved reranking with trigram attribution, path proximity, and stronger intent-aware signals.
- Added additive cross-repo candidate hints in `query` and `context_pack` outputs.
- Upgraded SCIP-like export metadata (`symbol_count`, `edge_count`) for better precision diagnostics.
- Expanded eval smoke coverage for context-pack and SCIP retrieval paths.
- Moved release notes to `docs/releases/` to keep repository root clean.

## 2.0.0

- Consolidated progressive v1.x work into the final major release line.
- Published complete release chain and normalized version lineage metadata.

## 1.9.0

- Backfilled progressive release history and stabilized eval defaults for CI.

## 1.8.0

- Hardened eval retrieval smoke checks for deterministic CI behavior.

## 1.7.0

- Shipped release packaging for MCP context-pack and SCIP upgrades.

## 1.6.0

- Improved retrieval candidate ranking determinism.

## 1.5.0

- Added cross-repo registry groundwork for import-owner resolution.
- Added dependency-distance graph traversal helper for context expansion.
- Added architecture summary artifact generation after index runs.

## 1.4.0

- Added context-pack primitives and intent-aware retrieval packaging.
- Added trigram-assisted retrieval and additive reranking signals.
- Added diff-aware and nearest-test relevance boosts in retrieval ranking.

## 1.3.0

- Added local learning policy bootstrap in `analyze` via `.codehelper/learning.json`.
- Learning scope is explicitly project-only (`project_scoped_only=true`) to keep memory boundaries clean.
- Added configurable learning mode:
  - `auto`: apply safe local improvements automatically after verify gates pass.
  - `approval`: require explicit approval before applying improvements.
- `AGENTS.md` generation now includes a "Local learning loop" policy section that mirrors project config.
- Added tests for learning policy config creation/normalization and AGENTS rendering behavior.

## 1.2.0

- Runtime hardening for watch mode:
  - overflow recovery path in watcher loop;
  - explicit watch tuning profiles (`auto|small|large`) and status visibility.
- Prompt contract upgrades:
  - stronger uncertainty/failure language (`[UNCERTAIN]`, fail-closed behavior).
- Eval coverage expanded for prompt contract checks.
- Docs expanded with MCP refresh troubleshooting and watch profile guidance.
- Release readiness baseline:
  - added explicit CLI version command and `VERSION` file.

## 1.1.0

- Parser plugin registry and expanded language coverage (C/C++, PHP, Ruby, Kotlin, Swift, Scala, Bash, Lua, Elixir, HCL, protobuf, symbol-lite SQL/HTML/CSS/Dart).
- Hybrid retrieval: BM25 + optional chromem-go vectors with RRF fusion and hit `reasons`.
- Incremental indexing: content hash cache (Badger), transitive invalidation (eager/lazy), parser/schema version triggers.
- Sharded workspaces: `analyze --shard` / `--path` for subdirectory roots.
- Verify v1.1: weighted gates, abstain without commands, optional `Judge`, `changed_paths` hints.
- Cypher tool graph DSL helpers on top of SQL-safe mode.
- Telemetry p50/p95; `codehelper status --json`; optional `--metrics-addr` / `CODEHELPER_METRICS_ADDR` Prometheus text.
- Embeddings: `analyze --embeddings` with Ollama-first resolver and OpenAI-compat providers via env.
