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

40 locate-understand tasks:

| Metric | codehelper (`query`/`context`) | Read whole files |
|---|---|---|
| Median tokens | 14 | 4,488 |
| Median tool calls | 1 | — |
| Token reduction | **99.7%** | — |

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
