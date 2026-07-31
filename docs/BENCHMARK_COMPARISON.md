# Benchmark comparison methodology

**Status:** methodology + harness scaffold — **no competitor numbers invented here.**

This document defines how Codehelper should be compared honestly to other local MCP / code-intelligence setups. Fill result cells only from reproducible local runs that name tool versions, corpus SHAs, and the date.

Related measured Codehelper figures (when published from local harness runs — see caveats): fill cells below; do not invent competitor numbers.

---

## Principles

1. **Same machine, same corpus, same task set** — record OS, CPU class, and the commit / release of every tool under test.
2. **No cloud LLM in the score** unless the task explicitly measures an agent+model loop (then record model id, temperature, max turns).
3. **Separate locate quality from agent quality** — retrieval metrics ≠ end-to-end “fixed the bug” rates.
4. **Publish failures** — sparse-graph languages and abstains count; do not drop hard cases.
5. **No scraped marketing numbers** from other vendors’ sites without re-running on the same beds.
6. **No network competitor installs in the scaffold** — competitor binaries, if any, must already be present on the host or bind-mounted; the harness never `curl | sh` / `npm i -g` / `pip install` competitor tools.

---

## Evaluation arms

Primary design: **paired arms, one variable** (same prompt, same bed snapshot, same budget). Inspired by mcpbr / SkillCI / Anthropic MCP eval patterns — adapted for local-first runs.

| Arm ID | Name | Tools available | What it isolates |
|---|---|---|---|
| **A** | Host baseline | Editor/host built-ins only (Read / Grep / Glob / shell / edit) — **no** Codehelper MCP | Programmer agent without repo graph |
| **B** | Codehelper MCP (manual) | Codehelper tools; agent chooses (`query` → `context` → `impact`, etc.) | Tool surface + descriptions |
| **C** | Codehelper guided | Same as B + `kickoff` / `orchestrate` / recipes forced or strongly hinted | Workflow packaging |
| **D** | Competitor class (optional) | One competing setup at a time (see [Competitor classes](#competitor-classes-not-endorsements)) | Cross-product honesty — **only when a local binary exists** |
| **E** | Architect → execute (stretch) | Planner issues plan; executor edits only via gated write tools + must pass `finish_check` | Product-mode receipts |

**Methodology-lite (shipped today):** arms **A vs B** on locate-oriented probes — host-style source walk vs MCP `query`→`context`→`impact`. See `scripts/mcp-paired-eval.sh` and [Harness scaffold](#harness-scaffold).

**Full agent study (not claimed by default tables):** arms A/B/C with a real coding agent, fail-to-pass or rubric verifiers, and per-bed deltas. Do not equate methodology-lite win rates with “improves agents on SWE-bench.”

---

## Metrics (record per task × arm)

Fill only after a run. Empty / `_TBD_` cells are honest.

### Locate / retrieval (deterministic, no frontier LLM required)

| Metric | Definition | Notes |
|---|---|---|
| Locate hit | Gold symbol/path appears in top-*k* (or first correct file) | Primary for methodology-lite |
| Recall@1 / @5 / @10 | Fraction of queries with gold in top-*k* | Self-repo figures: fill from local harness |
| MRR | Mean reciprocal rank of gold | |
| Precision / recall / F1 (callers) | Against textual or AST ground truth | Caller-lookup suite |
| Latency p50 / p95 | Wall ms for the probe | Cold vs warm index called out |
| Response bytes / token proxy | Payload size (TOON vs JSON if relevant) | SkillCI-style efficiency when both arms hit |

### Agent / trajectory (when an LLM loop is in scope)

| Metric | Definition |
|---|---|
| Success / fail | Verifier pass (tests, behavioral contract, or rubric ≥ threshold) |
| Wrong-file rate | Incorrect files edited or heavily read ÷ files touched |
| Turns / tool calls | Until stop or budget |
| Time-to-first-correct-file | Turns or wall seconds |
| Token / cost proxy | Host API tokens **or** local usage estimate — same method for all arms |
| Diff churn | Unrelated lines / files vs gold |

### Aggregation

- Prefer **paired Δ** (B−A, C−A, D−A) per bed, not a single global average that hides stack flips.
- Efficiency wins without success wins must **not** be marketed as “improves agents.”

---

## Bed categories

Hold-out stacks so “MCP helps” is not measured only where the call graph is already dense. Canonical suite: `internal/bench.DefaultMultiBedCoverage()`. CI prepares `CIMinimalBedNames()` only. **One recipe:** [TESTBEDS.md](TESTBEDS.md) (`scripts/testbeds-all.sh` → `.testbeds/active`). Stub inventory: `testdata/minimal-testbeds/LAYOUT.md`.

| Tier | Expectation | Example beds | Typical probe kinds |
|---|---|---|---|
| **Strong** | Dense call graph (Go / Rust-ish services) | axum, gin, fiber | `architecture_qa`, `feature_orient` |
| **Medium** | Framework apps / multi-root / mixed graph / engine-lite | fastapi, flask, djangorest, nest, laravel, sinatra, spring-petclinic, spring, svelte, csharp, cpp, unity, godot, unreal, kotlin, multi-repo-a/b | `architecture_qa`, `feature_orient` |
| **Weak** | Library / sparse inbound | express, swift, elixir, dart, lua, scala | `fix_bug_orient`, `feature_orient`, `architecture_qa` |

**Corpus documentation required for any published comparison row:** bed path or public repo URL, frozen git SHA, language mix, approximate symbol/edge counts, whether fixtures/samples are included, and whether `.codehelper` index was cold or warm.

`scripts/prepare-oss-testbeds.sh` pins each OSS hold-out to `name|url|commit_sha` and writes `oss-testbed-pins.json` under the prepare OUT (and `.eval-projects/`). `scripts/bench-comparison-scaffold.sh` copies those rows into report field **`oss_testbeds`** (plus **`oss_testbeds_source`**). Cite `oss_testbeds[].commit_sha` / `pinned_sha` in any external claim. Optional: `CODEHELPER_OSS_INDEX_TIMING=1` records `cold_index_ms` / `warm_index_ms` on one bed into the same manifest.

Env root for local beds: `CODEHELPER_TESTBEDS` (default `.testbeds/active` after `scripts/testbeds-all.sh prepare`; else `$REPO/.testbeds` when present).

---

## Competitor classes (not endorsements)

Names are **classes** until a dated local run fills numbers. Add a row only after recording version + command line.

| Class ID | Class | Fair compare notes | Result column |
|---|---|---|---|
| **D1** | Editor-builtin grep / fuzzy file search | Same tasks; no index warm-up hidden | _TBD — measure_ |
| **D2** | Language-server-only (gopls, pyright, tsserver, …) | Symbol/rename tasks; not multi-hop “who calls” unless LSP supports it | _TBD_ |
| **D3** | Other local MCP / code-intelligence servers | Same MCP client, same model budget if agentic | _TBD_ |
| **D4** | Cloud / IDE-hosted index (Cody-class, Cursor index, …) | Call out network dependency + privacy; still re-run — do not paste marketing tables | _TBD_ |
| **D5** | SCIP / LSIF / Sourcegraph-style precise index (local) | Only if indexer + query CLI already installed offline | _TBD_ |

**Scaffold policy:** competitor arms are **opt-in stubs**. The harness prints `skip: competitor binary not found` rather than downloading anything.

---

## Dimensions summary (fill from runs)

| Dimension | Metric | Codehelper source | Competitor |
|---|---|---|---|
| Symbol locate | Recall@1 / MRR | local harness | _TBD — measure_ |
| Caller lookup | Precision / recall / F1 | local harness | _TBD_ |
| Token / byte cost | Median tokens or response bytes vs whole-file reads | local harness | _TBD_ |
| Tool-catalog load | Advertised tools × schema tokens | `core` / `focused` / `full` profiles | _TBD_ |
| Language coverage | Honest bands | [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md) | _TBD_ |
| Offline / privacy | Network required? | Local-first | _TBD_ |
| Paired locate (methodology-lite) | MCP vs baseline win / hit rate | local harness § Paired MCP | _TBD_ (D-class) |

---

## Caveats (read before citing)

1. **Self-repo bias** — Many self-repo tables (when measured) were captured on the indexed **codehelper** repository. That favors tools tuned to this tree. Prefer multi-bed hold-outs for external claims.
2. **Methodology-lite ≠ agent resolve rate** — Paired A/B locate probes prove graph tools can beat naïve file walks on those tasks. They do **not** prove higher SWE-bench / issue-fix rates with a frontier model.
3. **Orchestrate scores** are harness quality scores on fixed task kinds, not human product QA.
4. **Missing competitor cells are intentional** — empty is better than invented.

---

## Harness scaffold

Offline-friendly entry points (no competitor network installs):

| Entry | Purpose |
|---|---|
| `scripts/mcp-paired-eval.sh` | Methodology-lite A vs B (fixture always; multi-bed when indexed beds exist) |
| `scripts/bench-comparison-scaffold.sh` | Bake-off wrapper: fixture → optional multi-bed → competitor-class probes → JSON/MD reports with metrics columns |
| `scripts/container/Dockerfile.methodology-lite` | Container **outline**: build Codehelper tests, bind-mount `CODEHELPER_TESTBEDS`, run scaffold; never `apt`/`npm` competitor tools |
| `scripts/update-mcpb-hashes.sh` | Post-release: refresh `server.json` `fileSha256` from local `*.mcpb` (no publish) |

### Report artifacts

When `--report DIR` is set (default `.testbeds/reports` if local beds exist):

| File | Contents |
|---|---|
| `paired-mcp-lite.json` / `.md` | Per-bed A/B rows (locate hit, ms, bytes, tool calls, winner) |
| `bench-comparison-scaffold.json` / `.md` | Host metadata + paired summary + **metrics columns** + competitor N/A table with install hints + **`oss_testbeds`** corpus pins |
| `oss-testbed-pins.json` (under prepare OUT) | Per-OSS-bed `pinned_sha` / `commit_sha` / `pin_match` (+ optional cold/warm index ms) |

Committed golden fixture (small, always reproducible): `testdata/bench-comparison/` from `--fixture-only --report testdata/bench-comparison`.

### How to run

```bash
# Always-safe fixture (no beds required); optional golden report dir
scripts/bench-comparison-scaffold.sh --fixture-only
scripts/bench-comparison-scaffold.sh --fixture-only --report testdata/bench-comparison

# When local indexed beds exist (e.g. .testbeds/*/ with .codehelper/)
CODEHELPER_TESTBEDS=.testbeds scripts/bench-comparison-scaffold.sh --report .testbeds/reports
# (report dir defaults to .testbeds/reports when .testbeds is present)

# Container outline (beds bind-mounted; no network competitor installs)
docker build -f scripts/container/Dockerfile.methodology-lite -t codehelper-bench-lite .
docker run --rm \
  -v "$PWD/.testbeds:/beds:ro" \
  -e CODEHELPER_TESTBEDS=/beds \
  codehelper-bench-lite
```

Competitor stubs (optional, local paths only):

```bash
CODEHELPER_COMPETITOR_BIN=/usr/local/bin/some-local-tool \
  scripts/bench-comparison-scaffold.sh --report .testbeds/reports
```

If a competitor class binary is missing, the scaffold records **N/A** plus an install/run hint and continues — never invents scores.

---

## Related

- This file — methodology + harness; fill measured cells from reproducible local runs
- [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md) — qualitative per-language confidence
- [ROADMAP.md](ROADMAP.md) — remaining honesty / release work
- `.testbeds/reports/mcp-eval-methodology.md` — fuller literature-backed protocol (gitignored local notes; not required to run the scaffold)
