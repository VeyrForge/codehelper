# Benchmark results

Recorded measurements on the indexed **codehelper** repository. Deterministic local runs — no cloud LLM tokens.

Snapshot: **July 2026** · ~689 files · ~7k symbols · 2,830 call edges

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

BM25 + trigram + call-graph centrality. No embeddings.

| Metric | Value |
|---|---|
| Recall@1 | 0.988 |
| Recall@5 | 1.000 |
| Recall@10 | 1.000 |
| MRR | 0.994 |
| Query latency p50 | 2.7 ms |

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

Implements the practical slice of `.testbeds/reports/mcp-eval-methodology.md` §1.1 (mcpbr / SkillCI / DeepEval MCP / Anthropic eval patterns):

- **Arm A (baseline):** host-style source walk + substring locate (no graph tools).
- **Arm B (MCP):** `query` → `context` → `impact` on the same underspecified task.
- **Verdict:** locate hit first; if both hit, SkillCI-style efficiency (response bytes).

### Measured (2026-07-22 local, 12 indexed beds)

| Metric | Value |
|---|---:|
| MCP wins | **11** |
| Baseline wins | 0 |
| Ties | 1 (axum — both locate; large impact payload) |
| MCP locate hit rate | **100%** (12/12) |

| Harness | Command |
|---|---|
| Fixture (always) | `go test ./internal/mcpsvc/ -run TestPairedMCPLiteFixture` |
| Multi-bed | `CODEHELPER_TESTBEDS=… scripts/mcp-paired-eval.sh` |
| Verify gate | `scripts/verify-codehelper.sh` (fixture + beds when present) |
| Optional CI | job `testbeds-paired` when repo var `CODEHELPER_TESTBEDS` is set |

Local refresh (gitignored reports OK):

```bash
scripts/mcp-paired-eval.sh --report .testbeds/reports
```


---

## Multi-bed coverage (12-bed lite suite)

Hold-out stacks per methodology §1.1 — strong / medium / weak graph tiers.
Canonical list: `internal/bench.DefaultMultiBedCoverage()`.

| Tier | Beds | Probe kinds |
|---|---|---|
| Strong | axum, gin, fiber | architecture_qa, feature_orient |
| Medium | fastapi, flask, djangorest, nest, laravel, sinatra, spring-petclinic, svelte | architecture_qa, feature_orient |
| Weak | express | fix_bug_orient |

Workflow smoke (cwd bind + edit loop) additionally covers the same beds via `TestWorkflowSmokeMultiTestbed`.
