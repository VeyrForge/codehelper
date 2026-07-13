# Codehelper MCP tools — reference

Reference for codehelper's MCP tool surface: what each tool does, typical workflow, and response format. Recorded benchmark results: [BENCHMARK.md](BENCHMARK.md).

**Tool count:** 42 (see `internal/mcpsvc/toolcatalog.go` for the canonical grouped list).

**Defaults:** Every tool scopes to the current workspace unless `repo` is set. Array-heavy tools return **TOON** by default; pass `format=json` for JSON text.

**Minimal listing:** `CODEHELPER_MINIMAL_TOOLS=1` or `codehelper config project --minimal on` advertises only the main tools in `tools/list`. Hidden tools remain callable by name; `project_context` still returns the full catalog.

---

## Typical workflow

1. **`project_context`** — once per session: stack, versions, index freshness, tool param keys, suggested verify commands.
2. **`query`** or **`scout`** — find symbols or reuse candidates before adding code.
3. **`context`** — one symbol: source, callers, callees, blast radius.
4. **`plan`** / **`change_kit`** — design or prepare an edit.
5. Apply edits (`apply_patch_workspace_file`, `insert_at_symbol`, `rename_symbol`).
6. **`diagnostics`** → **`verify`** → **`review_diff`** → **`finish_check`**.

For multi-step investigation with a single call, use **`orchestrate`** (requires orchestration enabled in project config).

---

## Response format

- **TOON** (default): tabular/array payloads use a compact text encoding (~40% smaller than indented JSON on uniform rows). The model reads the text block; there is no `structuredContent` on TOON responses.
- **`format=json`**: same data as indented JSON text.
- **Caps:** Results are ranked and truncated to stay within client output limits.
- **Errors / empty results** include suggested next tools (e.g. stale index → `codehelper analyze`).

---

## Bootstrap and search

| Tool | Purpose |
|---|---|
| `project_context` | Project brief: type, languages, entrypoints, freshness, tool contract. Does not search code. |
| `query` | Ranked symbol search (BM25 + trigram + call-graph centrality + synonym expansion). |
| `scout` | Reuse-first search: ranked candidates, `usage_of_top` call site, `impact_of_top`. |
| `context` | One symbol: definition excerpt, callers, callees, `blast_radius`. |
| `kickoff` | Orient + reuse + docs + verify hints in one call for starting a task. |
| `plan` | Role-based checklist (`architect`, `security`, `performance`, `refactor`, `feature`) with grounded steps. |
| `ast_query` | Tree-sitter structural search (19 languages, parallel file scan). |
| `api_surface` | Exported symbols for a package or directory. |
| `similar` | Find symbols similar to a reference implementation. |
| `glossary` | Project vocabulary seed. |
| `hints` | Record or retrieve cross-project stack hints. |

**Ranking signals** (for `query` / `scout`): corpus IDF-weighted BM25 over name + path + doc comment; exact/prefix name boost; inbound call centrality; programming-verb synonyms; intent-based test demotion/promotion. Optional semantic rerank when `CODEHELPER_EMBED_URL` is set (see README — Green engine).

---

## Graph analysis

| Tool | Purpose |
|---|---|
| `trace` | Shortest call path between two symbols, or outbound tree from one symbol. |
| `impact` | Upstream/downstream blast radius and risk tier. |
| `test_impact` | Tests that may cover a symbol (safe over-approximation). |
| `dead_code` | Unreferenced symbols (candidates to verify manually). |
| `detect_changes` | Git diff → affected symbols. |
| `since` | Changed symbols + blast radius + tests to run since a ref. |
| `find_implementations` | Go types implementing an interface (heuristic method-set match). |
| `hotspots` | High-churn files from git history. |

---

## Edit assist

| Tool | Purpose |
|---|---|
| `change_kit` | Definition + call sites + covering tests + risk for one symbol. |
| `read_workspace_file` | Read file or line range from disk (always current). |
| `list_workspace_directory` | List directory entries. |
| `apply_patch_workspace_file` | Apply a unified diff patch. |
| `insert_at_symbol` | Insert at a symbol boundary. |
| `rename_symbol` | Rename across references (heuristic). |
| `revert_workspace_edit` | Revert a prior workspace edit by token. |

Write tools carry `destructiveHint`. Patches are validated before apply.

---

## Verification and review

| Tool | Purpose |
|---|---|
| `diagnostics` | Auto-detect toolchain; run build/vet/typecheck; return structured problems. |
| `verify` | Run lint/build/test commands (argv mode default; optional shell mode). |
| `review` | Deterministic diff audit: changed symbols, risk, tests to run, checklists. |
| `review_diff` | Strict diff review for agent finish gates. |
| `finish_check` | Completion gate: index freshness, verify status, blocking findings. |
| `preflight` | Pre-edit checks for a target symbol or path. |

---

## Docs, web, and search

| Tool | Purpose |
|---|---|
| `docs` | Resolve library docs from project manifests (llms.txt-first). |
| `docs_add` | Add or override doc sources for a library. |
| `web` | HTTP fetch with status/body/JSON assertions (no browser). |
| `web_search` | Configured search provider (Tavily / Brave / DuckDuckGo). |
| `browser` | Headless Chromium: screenshot, actions, optional a11y/perf audit. |

**First-time setup:** `codehelper browser install` (managed Chromium under `~/.codehelper/browser`). Smoke test: `codehelper browser test https://example.com`.

Network access is policy-gated per project.

---

## Orchestration (opt-in)

| Tool | Purpose |
|---|---|
| `orchestrate` | Run a deterministic tool workflow for a task; returns `agent_brief` + trace. |
| `orchestration_feedback` | Critique a prior orchestration run. |
| `orchestration_rerun` | Re-run with feedback applied. |
| `orchestration_memory` | Recall prior orchestration runs. |
| `run_trace` / `explain_run` | Inspect stored orchestration traces. |
| `investigate` | Guided multi-step local investigation workflow. |

Enable in project config before use. Recorded comparison (no MCP / manual MCP / orchestrate): [BENCHMARK.md](BENCHMARK.md).

---

## Agent planning (HTTP / extension)

| Tool | Purpose |
|---|---|
| `agent_plan` | Create a task with structured todos. |
| `agent_execute_todo` | Execute one approved todo. |
| `agent_memory` | Record or search project memory. |

The VS Code extension (`vscode-extension` branch) calls `codehelper serve` over HTTP instead of duplicating this logic.

---

## Utility

| Tool | Purpose |
|---|---|
| `scope` | Turn a vague idea into concrete terms and questions. |
| `usage_report` | Per-project tool and token usage from local logs. |
| `detect_changes` | Git working tree → symbols. |

