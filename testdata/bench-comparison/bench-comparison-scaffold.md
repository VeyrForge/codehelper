# Benchmark comparison scaffold report

**Generated:** 2026-07-24T18:18:29Z
**Git SHA:** 949849c
**Host:** Linux / x86_64
**Go:** go version go1.25.5 linux/amd64
**Codehelper:** 3.0.1
**Testbeds:** `.testbeds` (indexed dirs discovered: 30)
**Mode:** fixture-only

## Paired methodology-lite (arms A vs B)

| Metric | Value |
|---|---:|
| Beds run | 1 |
| Pairs | 1 |
| MCP wins | 1 |
| Baseline wins | 0 |
| Ties | 0 |
| MCP locate hit rate | 100% (1/1) |

| Bed | Kind | Winner | MCP hit | Base hit | MCP ms | Base ms | Δms | MCP bytes | Base bytes | MCP calls | Base calls |
|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| 001 | architecture_qa | **mcp** | True | True | 10 | 0 | 10 | 3405 | 13936 | 3 | 1 |

Arms: **A** = host-style file walk (no graph); **B** = MCP `query`→`context`→`impact`.

## Competitor classes (no fake numbers)

| Class ID | Class | Status | Version | Metrics | Install / run hint |
|---|---|---|---|---|---|
| **D1** | editor-builtin grep/fuzzy | host_available | — | N/A | Use the same host Read/Grep/Glob tasks as arm A; record metrics only after a dated paired run. |
| **D2a** | LSP gopls | N/A | — | N/A | go install golang.org/x/tools/gopls@latest  # offline: use a pre-fetched module cache |
| **D2b** | LSP pyright | N/A | — | N/A | npm install -g pyright  # or: pip install pyright — do not run from this harness |
| **D2c** | LSP tsserver-wrapper | N/A | — | N/A | npm install -g typescript-language-server typescript  # offline install only |
| **D3** | other local MCP / code-intel | N/A | — | N/A | CODEHELPER_COMPETITOR_BIN unset — set to a pre-installed local binary to stub arm D |
| **D4** | cloud/IDE-hosted index | N/A | — | N/A | Re-run on the same beds with the cloud product; do not paste marketing tables. |
| **D5a** | SCIP CLI | N/A | — | N/A | Install SCIP indexer + CLI offline from sourcegraph/scip releases; harness never downloads. |
| **D5b** | src-cli | N/A | — | N/A | Install Sourcegraph src-cli offline if comparing precise index; harness never downloads. |

Missing competitors stay **N/A**. Do not invent scores. Fill metrics only after a dated local protocol run.

## Caveats

- self-repo figures in BENCHMARK.md are not multi-bed hold-outs
- methodology-lite locate wins are not agent resolve-rate claims
- competitor metrics must be filled from local re-runs only — never scraped
- N/A competitor cells mean the tool was missing or not measured — not a score of zero

