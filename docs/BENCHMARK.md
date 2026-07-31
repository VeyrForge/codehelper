# Benchmark results

Recorded measurements on the indexed **codehelper** repository. Deterministic local runs — no cloud LLM tokens.

Snapshot: **July 2026** · ~689 files · ~7k symbols · 2,830 call edges

Honest language confidence: [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md). Competitor comparison methodology (no invented numbers): [BENCHMARK_COMPARISON.md](BENCHMARK_COMPARISON.md).
Testbed layout + one-command prepare/eval: [TESTBEDS.md](TESTBEDS.md).

### Caveats (cite carefully)

- **Self-repo bias** — Tables above the multi-bed section were measured on this repository. Prefer hold-out beds (`CODEHELPER_TESTBEDS`) for external claims.
- **Methodology-lite ≠ agent resolve rate** — Paired MCP ON/OFF locate probes are not SWE-bench / issue-fix success rates with a frontier model.
- **Competitor cells** stay empty until a dated local re-run fills them — see [BENCHMARK_COMPARISON.md](BENCHMARK_COMPARISON.md).

---

## Call-graph caller lookup

40-symbol sample against textual ground truth.

| Metric | Value |
|---|---|
| Informative symbols (have callers) | 31 |
| No-caller agreement (true negatives) | 9 |
| Mean precision | 0.968 |
| Mean recall | 0.968 |
| Mean F1 | 0.968 |
| Latency p50 / p95 | 2.8 ms / 4.0 ms |

---

## Symbol search (`query`, lexical)

BM25 + trigram + call-graph centrality. **No embeddings** — these numbers are the
default CI / release path. Optional local semantic rerank (Granite ~195 MB via
`ge embed serve` / [LOCAL_EMBED.md](LOCAL_EMBED.md)) is off unless auto-detected
or `CODEHELPER_EMBED_URL` is set; do not compare semantic A/B runs to this table
without labeling them.

| Metric | Value |
|---|---|
| Recall@1 | 0.988 |
| Recall@5 | 1.000 |
| Recall@10 | 1.000 |
| MRR | 0.994 |
| Query latency p50 | 2.7 ms |

---

## Optional semantic channel (not CI)

When a local embed server is healthy, MCP RRF-fuses a vector re-rank of the
lexical top-N. Setup is opt-in and never runs in CI (no multi-GB pulls). Measure
cross-lingual / slang lifts separately; do not overwrite the lexical baseline
above without an explicit dated A/B note.

---

## Natural-language retrieval

8-query core regression set: CI requires Recall@1 ≥ **0.70**.

13-query full set covers feature, bugfix, refactor, security, and architecture-style queries.

---

## Response size (TOON vs JSON)

40-item `query`-style payload:

| Format | Bytes | ~Tokens (÷4) |
|---|---|---|
| JSON (indented) | 9,090 | ~2,272 |
| TOON (default) | 5,311 | ~1,327 |
| Savings | **41.6%** | ~945 tokens / response |

---

## vs blind file reads

40 locate-understand tasks (refreshed 2026-07-22 on indexed codehelper):

| Metric | codehelper (`query`/`context`) | Read whole files |
|---|---|---|
| Median tokens (who-calls) | 37 | 5,894 |
| Median tool calls | 1 | 4 |
| Token reduction | **99.4%** | — |
| Locate/scout median tokens | 79 | 5,894 (**98.7%** fewer) |

---

## Agent workflow: orchestrate vs manual MCP vs no MCP

13 indexed projects · 5 task kinds each · July 2026

| Metric | Orchestrate | Manual MCP | No MCP |
|---|---|---|---|
| Quality (avg score) | **0.968** | 0.915 | 0.188 |
| Agent-facing tokens / case | **519** | 7,191 | 2,933 |
| Latency / case | 760 ms | 394 ms | 64 ms |

### Orchestrate quality by task kind

| Kind | Avg score |
|---|---|
| feature | 0.99 |
| refactor | 0.97 |
| explain | 0.95 |
| bugfix | 0.95 |
| dead_code | 0.93 |

---

## Paired MCP ON/OFF (methodology-lite)

Implements the practical slice of the MCP eval methodology (mcpbr / SkillCI / DeepEval MCP / Anthropic eval patterns):

- **Arm A (baseline):** host-style source walk + substring locate (no graph tools).
- **Arm B (MCP):** `query` → `context` → `impact` on the same underspecified task.
- **Verdict:** locate hit first; if both hit, SkillCI-style efficiency (response bytes).

**Caveat:** these are locate / efficiency probes on indexed beds — not claims that Codehelper raises cloud-agent issue-fix rates. Full arm list and competitor classes: [BENCHMARK_COMPARISON.md](BENCHMARK_COMPARISON.md).

### Measured (2026-07-24 local, 12 indexed beds)

| Metric | Value |
|---|---:|
| MCP wins | **11** |
| Baseline wins | 0 |
| Ties | 1 (axum — both locate; payload sizes within 2× efficiency band) |
| MCP locate hit rate | **100%** (12/12) |

### Measured (2026-07-25 local, real OSS + stubs — 21 paired beds)

Staged via `scripts/prepare-oss-testbeds.sh` → `.testbeds/real-oss` (9 OSS junctions from `.eval-projects` + probe-aligned stubs). Report: `.testbeds/reports/real-oss-2026-07-25/paired-mcp-lite.{json,md}`.

| Metric | Value |
|---|---:|
| Beds run / pairs | **21** |
| MCP locate hit rate | **100%** (21/21) |
| MCP wins | **14** |
| Baseline wins | 0 |
| Ties | **7** (nest, svelte, vue, axum, sinatra, csharp, multi-repo-a — both locate; within 2× byte band) |

**Real OSS beds in this run:** axum, fiber, gin, express, fastapi, flask, djangorest (encode/django-rest-framework), laravel, spring-petclinic — all MCP locate hit; winners mcp except axum **tie**.

**Probe-aligned stubs (not full OSS):** nest, sinatra, svelte, vue, astro, mdx, cpp, unity, godot, unreal, csharp, multi-repo-a/b. Gap notes for siblings: [REAL_BED_GAPS_2026-07-25.md](REAL_BED_GAPS_2026-07-25.md).

### Measured (2026-07-25 mega — 35 paired beds)

Broadest local run: `prepare-oss-testbeds.sh` (9 OSS + stubs incl. wordpress/rails/angular/next/nuxt/kotlin/shaders/terraform/…) → `.testbeds/real-oss` (35 indexed junctions). Binary: `bin/codehelper.exe` 3.0.3 CGO+mingw with `ResolveWalkRoot`. Report: `.testbeds/reports/mega-2026-07-25/paired-mcp-lite.{json,md}`.

| Metric | Value |
|---|---:|
| Beds run / pairs | **35** |
| MCP locate hit rate | **100%** (35/35) |
| MCP wins | **28** |
| Baseline wins | **0** |
| Ties | **7** (nest, sinatra, rails, spring, unreal, swift, terraform) |

**Soft-skipped (probe wired, bed not staged in that mega):** echo, chi, beego, flutter, react-native, zig, solidity, clojure, erlang, fsharp, r, perl, ocaml, haskell, devops, prisma, typeorm. Daily prepare now stages these via \scripts/testbeds-all.sh\ (see [TESTBEDS.md](TESTBEDS.md)).

### Nest + Express real-tree focus (2026-07-25)

| Metric | Value |
|---|---:|
| nest (stub src+samples) | **tie** (MCP+baseline locate; MCP 10KB vs base 15KB) |
| express (OSS) | **mcp** win (8KB vs 172KB baseline walk) |
| Agent usability | Express OSS **8**/10 · Nest stub **8**/10 · typescript-starter **9**/10 · Nest framework monorepo **n/a** (ceiling: `path=sample/01-…`) |

Details: `.testbeds/reports/nest-express-real-2026-07-25/QUALITY.md`. Language ceilings: [LANGUAGE_MATRIX.md](public/LANGUAGE_MATRIX.md).

| Harness | Command |
|---|---|
| **One recipe** | `scripts/testbeds-all.sh` (-> `.testbeds/active` + dated report); see [TESTBEDS.md](TESTBEDS.md) |
| Fixture (always) | `scripts/testbeds-all.sh fixture` / `go test ./internal/mcpsvc/ -run TestPairedMCPLiteFixture` |
| Multi-bed | `CODEHELPER_TESTBEDS=... scripts/mcp-paired-eval.sh` |
| OSS stage + stubs | `scripts/prepare-oss-testbeds.sh` (default `.testbeds/active`) |
| Comparison scaffold | `scripts/bench-comparison-scaffold.sh` (fixture + beds + competitor N/A rows + metrics columns) |
| Verify gate | `scripts/verify-codehelper.sh` (fixture + beds when present) |
| Optional CI | job `testbeds-paired` prepares minimal beds in-job (or `vars.CODEHELPER_TESTBEDS`); skip only via `CODEHELPER_SKIP_TESTBEDS` |

Local refresh (gitignored reports OK; golden fixture under `testdata/bench-comparison/`):

```bash
scripts/testbeds-all.sh                          # preferred daily / mega
# or building blocks:
scripts/bench-comparison-scaffold.sh --fixture-only --report testdata/bench-comparison
scripts/prepare-oss-testbeds.sh              # default OUT=.testbeds/active; cache under .eval-projects/
scripts/mcp-paired-eval.sh --report .testbeds/reports   # defaults to .testbeds/active
```

---

## Multi-bed coverage (lite + extended stubs)

Hold-out stacks per methodology §1.1 — strong / medium / weak graph tiers.
Canonical list: `internal/bench.DefaultMultiBedCoverage()` (original 12 + stub extensions).
CI smoke prepares only `CIMinimalBedNames()` (`gin` / `nest` / `express`) via
`scripts/ci-prepare-minimal-testbeds.sh` (`SUITE=ci`). Local extended stubs:
`SUITE=extended` (see `testdata/minimal-testbeds/LAYOUT.md`).

| Tier | Beds | Probe kinds |
|---|---|---|
| Strong | axum, gin, fiber | architecture_qa, feature_orient |
| Medium | fastapi, flask, djangorest, nest, laravel, sinatra, spring-petclinic, spring, svelte, vue, astro, mdx, csharp, cpp, unity, godot, unreal, kotlin, multi-repo-a, multi-repo-b | architecture_qa, feature_orient |
| Weak | express, swift, elixir, dart, zig, solidity, clojure, erlang, fsharp, r, perl, ocaml, haskell, lua, scala, shaders, terraform, protobuf | fix_bug_orient, feature_orient, architecture_qa |

**Stub vs OSS:** beds marked `Source=stub` ship under `testdata/minimal-testbeds/` and are prepared without downloads. OSS beds (`axum`, `fiber`, `spring-petclinic`, …) are cached under `.eval-projects/<name>` and staged with `scripts/prepare-oss-testbeds.sh` (junction/symlink into `CODEHELPER_TESTBEDS`; optional stub merge for probe-aligned names).

Workflow smoke (cwd bind + edit loop) additionally covers beds via `TestWorkflowSmokeMultiTestbed`.

### Cursor agent recipe - live testbeds (MCP ON vs OFF)

**Canonical steps:** [TESTBEDS.md](TESTBEDS.md#cursor-agent-recipe) (`scripts/testbeds-all.sh`).

Short path:

1. `scripts/testbeds-all.sh fixture`
2. `scripts/testbeds-all.sh prepare` (offline: `CODEHELPER_OSS_SKIP_CLONE=1`)
3. `scripts/testbeds-all.sh eval` - arms **A** host walk vs **B** MCP `query`->`context`->`impact`
4. Read `.testbeds/reports/<stamp>/paired-mcp-lite.md`; soft-skips OK; do not claim agent fix-rate from locate probes alone
5. Never download competitors; never delete `.eval-projects/`

In Cursor: ask the agent to run `scripts/testbeds-all.sh` and paste the scorecard summary.
