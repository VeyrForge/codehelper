# Codehelper MCP tools — reference

Reference for codehelper's MCP tool surface: what each tool does, typical workflow, and response format. Recorded benchmark results: [BENCHMARK.md](BENCHMARK.md).

**Tool count:** 60 (see `internal/mcpsvc/toolcatalog.go` for the canonical grouped list).

**Defaults:** Every tool scopes to the current workspace unless `repo` is set. Array-heavy tools return **TOON** by default; pass `format=json` for JSON text.

**Minimal listing:** `CODEHELPER_MINIMAL_TOOLS=1` or `codehelper config project --minimal on` advertises only the main tools in `tools/list`. Hidden tools remain callable by name; `project_context` still returns the full catalog.

---

## Typical workflow

1. **`project_context`** — once per session: stack, versions, index freshness, `warnings` (including inventory/contains-only graph honesty), tool param keys, `workflow_recipes`, suggested verify commands.
2. **`kickoff`** / **`query`** / **`scout`** — start a task or locate symbols (production defs rank above sample/test/fixture; see `collision_note`).
3. **`context`** — one symbol: source, callers, callees, blast radius (`path=` to disambiguate).
4. **`plan`** / **`change_kit`** — design or prepare an edit.
5. Apply edits (`apply_patch_workspace_file`, `insert_at_symbol`, `rename_symbol`).
6. **`diagnostics`** → **`review_diff`** → **`verify`** → **`finish_check`** (claim done only when `can_claim_done=true`).

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
| `query` | Ranked symbol search (BM25 + trigram + call-graph centrality + synonym expansion). Production/app defs demote sample/test/fixture/style noise. |
| `scout` | Reuse-first search: ranked candidates, `usage_of_top` call site, `impact_of_top`, optional `collision_note`. |
| `context` | One symbol: definition excerpt, callers, callees, `blast_radius`. Pass `path=` on name collisions. |
| `kickoff` | Orient + reuse + docs + verify hints in one call (`task=`; `query=` alias). Surfaces `collision_note` when fixtures demoted. |
| `plan` | Role-based checklist (`architect`, `security`, `performance`, `refactor`, `feature`) with grounded steps. |
| `ast_query` | Tree-sitter structural search (19 languages, parallel file scan). |
| `api_surface` | Exported symbols for a package or directory. |
| `similar` | Find symbols similar to a reference implementation. |
| `glossary` | Project vocabulary seed. |
| `hints` | Record or retrieve cross-project stack hints. |

**Ranking signals** (for `query` / `scout` / `kickoff`): corpus IDF-weighted BM25 over name + path + doc comment; exact/prefix name boost; inbound call centrality; programming-verb synonyms; intent-based test demotion/promotion; **MCP presentation demotes sample/test/fixture/style paths** below production (and elevates `sample/01-*` when the repo is samples-only). Optional semantic rerank when `CODEHELPER_EMBED_URL` is set (see README — Green engine).

**Workflow recipes** (in `project_context`): include `locate_symbol`, `vibe_fix`,
`vibe_ui`, `programmer_ui`, `browser_qa`, plus add/remove/review/security/dead_code/
performance/architecture_qa. Obey `verify_finish_gate` before claiming done.
UI loops: implement → `browser` outline/assert → debug → **retest** → finish_check
(see skill `codehelper-browser`).

---

## Graph analysis

| Tool | Purpose |
|---|---|
| `trace` | Shortest call path between two symbols, or outbound tree from one symbol. |
| `impact` | Blast radius; **default `direction=upstream`** (who uses this). Opt-in `downstream` for deps. Class hubs self-only on downstream auto-retry upstream. |
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
| `plan` | Role-based checklist with grounded steps. |
| `read_workspace_file` | Read file or line range from disk (always current). |
| `list_workspace_directory` | List directory entries. |
| `write_workspace_file` | Full-content write/create (rejects empty by default). |
| `apply_patch_workspace_file` | Search/replace hunks; preserves file indent style. |
| `insert_at_symbol` | Insert at a symbol boundary. |
| `rename_symbol` | Rename across references (heuristic). |
| `revert_workspace_edit` | Revert a prior workspace edit by token. |

Write tools carry `destructiveHint`. Patches are validated before apply.

---

## Verification and review

| Tool | Purpose |
|---|---|
| `diagnostics` | Auto-detect toolchain; run build/vet/typecheck; return structured problems (actionable first; generated/.next noise last). |
| `verify` | Run lint/build/test commands (argv mode default; optional shell mode). |
| `review` | Deterministic diff audit: changed symbols, risk, tests to run, checklists. |
| `review_diff` | Strict diff review for agent finish gates. |
| `finish_check` | Completion gate: index freshness, verify status, blocking findings. |
| `preflight` | Pre-edit checks for a target symbol or path. |
| `edit_cycle` | Composite: change_kit → patch → diagnostics loop. |

---

## Docs, web, and search

| Tool | Purpose |
|---|---|
| `docs` | Resolve library docs from project manifests (llms.txt-first). |
| `docs_add` | Add or override doc sources for a library. |
| `web` | HTTP fetch with status/body/JSON assertions (no browser). |
| `web_search` | Configured search provider (Tavily / Brave / DuckDuckGo). |
| `browser` | Chromium (headless default): screenshot, actions, outline/snapshot (`ref:eN`), diagnostics, optional **headed/GUI**. |

**First-time setup:** `codehelper browser install` (managed Chromium under `~/.codehelper/browser`). Smoke test: `codehelper browser test https://example.com`. `codehelper init` / `codehelper doctor` print stack-aware **setup suggestions** (local URL, `connections add-site`, headed mode, SSH tunnel patterns) plus a sample `.mcp.json`.

**Per-project browser defaults** (`codehelper config project`): `browser_base_url`, `browser_site`, `browser_recipe`, `browser_headed`, `browser_allow_private`, `test_credentials_note`. Agents should propose `setup_suggestions` from `project_context` / `kickoff` **before the first browser run**.

**Site kinds / recipes:** `wordpress`→`wp_*`, `laravel`→`laravel_login`, `django`→`django_admin`, `drupal`→`drupal_login`, `magento`→`magento_login`, `spa|generic`→`spa_hydrate`. Configure with `codehelper connections add-site --kind …`.

**Local vs remote:** loopback `http://127.0.0.1:…` is always GuardURL-safe. Prefer SSH port-forward (`ssh -N -L 8080:127.0.0.1:80 user@host`) then browse the local URL. LAN/RFC1918 needs `allow_private=true`. Public HTTPS staging URLs work without it.

**GUI / headed mode:** when a human should watch — MCP `headed=true` / `gui=true`, CLI `--headed` / `--gui`, env `CODEHELPER_BROWSER_HEADED=1` (or project `browser_headed`). Optional `slow_mo` / `--slow-mo` and `pause_on_fail` / `--pause-on-fail` (`CODEHELPER_BROWSER_PAUSE_ON_FAIL=1`). Default is headless (CI-safe). Needs a graphical display; over SSH/CI stay headless or wrap with `xvfb-run`. Missing display returns a clear error suggesting xvfb or `headed=false`.

**UI verify loop (any framework/CMS):** after a visual/JS change, call `browser`
with `outline=true` and/or `snapshot=true` once (outline emits stable `e1`… refs),
then `actions` ending in `assert`/`assert_text` (prefer `role`/`name`/`testid`/
`ref:eN`; or CMS `recipe=` + `site=`). Use `wait_hydrate` / `spa_hydrate` for SPAs.
On failure, read `failure_pack` + diagnostics + failure screenshot (`trace=true` if
flaky), fix code, and **retest the same assert** before `finish_check`. Deterministic
smoke: `scripts/browser-ui-eval.sh`
(writes `.testbeds/reports/browser-ui-eval-latest.json`). See
[`.testbeds/reports/browser-ui-eval.md`](../.testbeds/reports/browser-ui-eval.md).

Network access is policy-gated per project.

---

## Orchestration (opt-in)

| Tool | Purpose |
|---|---|
| `orchestration` | Enable / disable / status for local orchestration. |
| `orchestrate` | Run a deterministic tool workflow for a task; returns `agent_brief` + trace. |
| `orchestration_feedback` | Critique a prior orchestration run. |
| `orchestration_rerun` | Re-run with feedback applied. |
| `orchestration_memory` | Recall prior orchestration runs. |
| `run_trace` / `explain_run` | Inspect stored orchestration traces. |
| `investigate` | Guided multi-step local investigation workflow. |

Enable with `codehelper orchestration enable` (or `orchestration` action=enable) before use. Disabled tools fail clearly rather than silently no-oping. Recorded comparison (no MCP / manual MCP / orchestrate): [BENCHMARK.md](BENCHMARK.md).

---

## Ops (configured connections)

| Tool | Purpose |
|---|---|
| `remote_list` | Map of SSH hosts, DB profiles, log sources, aliases. |
| `remote_exec` | Run a named SSH recipe. |
| `log_read` | Tail a configured local log source. |
| `db_query` / `db_schema` | Read-only sqlite query / schema. |
| `run_alias` | Run a configured command alias. |
| `env_context` | Toolchain versions, scripts, make targets. |
| `ci_status` | GitHub PR/workflow summary via `gh`. |

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
| `glossary` / `hints` | Project vocabulary and cross-project stack hints. |

