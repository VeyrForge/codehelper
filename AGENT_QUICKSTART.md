# AGENT_QUICKSTART.md — Claude / Cursor / Codex + codehelper MCP

One-page playbook. Full routing: [AGENTS.md](./AGENTS.md). Vibe vs senior: [VIBE.md](./VIBE.md).

## Why tool descriptions matter

Coding agents pick tools from **names + descriptions + JSON schemas**, not from reading Go source. Weak schemas → wrong args → wasted round trips. Research consensus (AWS MCP design, Anthropic production MCP, SERF-style recovery):

1. **Front-load the confusing param** (`name=` vs `target=`).
2. **Errors are recovery instructions** — what went wrong, what was expected, one example fix.
3. **Prefer one composite call** (`kickoff` / `orchestrate`) over chaining primitives.
4. **Pack context tightly** — default slim payloads; opt into `detail=true` / `sections=`.

## Response format (TOON-first)

Agent-facing MCP tools default to **TOON** (Token-Oriented Object Notation): indentation + tabular rows, ~30–60% fewer tokens than JSON on uniform arrays. Tool **arguments** stay JSON (what the model emits). Tool **results** are TOON text-only by default so clients that prefer `structuredContent` do not bypass the savings.

| Want | Do |
|---|---|
| Default (agents) | omit `format` → TOON |
| Scripts / harnesses / evals | `format=json` |
| Force globally | `CODEHELPER_MCP_FORMAT=json` or `toon` |

Applies to: `kickoff`, `orchestrate`, `investigate`, `context`, `impact`, `query`, `finish_check`, `agent_memory`, orchestration side tools, and other array-heavy tools. Pass `format=json` only when you need structuredContent / machine parsing.

## Param keys (memorize these)

| Tool | Canonical arg | Do not confuse with |
|---|---|---|
| `context`, `context_bundle` | `name=` | `change_kit` / `impact` `target=` |
| `change_kit`, `impact` | `target=` | `context` `name=` |
| `kickoff`, `orchestrate` | `task=` | `query` tool’s `query=` (kickoff accepts `query=` as alias) |
| `investigate` | `query=` / `recipe=` / `target=` | aliases: `symbol`/`sym`/`name` for target |
| `trace` | `from=` (+ optional `to=`) | `to` aliases: `target`/`name`/`symbol` |
| `rename_symbol` | `name=` + `to=` | `to` aliases: `new_name`/`newName` |
| `query`, `search_hybrid` | `query=` | — |
| `agent_memory` | `action=` | gated by `.codehelper/learning.json` |

Cheat sheet also ships in every `project_context` as `mcp_param_keys`.

## Cursor

1. Keep codehelper in **minimal / focused** tool mode when possible (≤~40 tools).
2. First call: `kickoff task="…"` (or `orchestrate` if local orchestration is on).
3. Paste a `next_queries` line next — do not invent tools.
4. Before edit: `change_kit target=<exact>` → patch → `diagnostics` → `review_diff` → `verify` → `finish_check`.
5. On arg errors: read the error text once, fix the **key name**, retry once — do not switch to Grep first.

## Claude Code

1. Trust `AGENTS.md` / `CLAUDE.md` codehelper block — MCP first, built-ins after empty/error.
2. Bootstrap once: `project_context verbosity=short`.
3. Feature/fix: `kickoff` with `task=` (not `query=` as the primary key).
4. Symbol deep-dive: `context name=…` (aliases `symbol`/`sym` work, but prefer `name`).
5. Orchestration loop: `orchestrate` → if wrong scope → `orchestration_feedback` → `orchestration_rerun`.

## Codex

1. Same MCP surface; prefer `format=json` when scripting / eval harnesses.
2. Slim orchestrate payload already includes `agent_brief`, `what_next`, `next_queries`, `mcp_param_keys` — pass `detail=true` only when you need the full pack.
3. Do not treat `0 callers` on sparse graphs as isolation proof — check confidence / doctor notes in the payload.

## Memory / learning

- **`agent_memory`**: project ADR / decision memory. Writes (`record`/`propose`/`approve`/`reject`) require `enabled=true` in `.codehelper/learning.json`. When disabled, every response sets `learning_enabled=false` explicitly; search/list still work.
- **`orchestration_memory`**: separate gate — requires `codehelper orchestration enable`. Workflow routing hints only, not ADRs.
- This repo’s default policy is often **disabled** — do not invent stored decisions when `learning_enabled=false`.

## Error recovery (agent self-correct)

Tool errors may be JSON/TOON with `error_category`, `is_retryable`, `recovery_hint`, `message`, and often `example` / `what_next`. Prefer those fields over guessing.

| Symptom | Fix |
|---|---|
| `name is required` from `context` | Retry with `name=Symbol` (aliases `symbol`/`sym` work) |
| `target is required` from `change_kit`/`impact` | Retry with `target=Symbol` |
| `task is required` from `kickoff` | Rename `query=` → `task=` (or keep `query=`; kickoff accepts it with a note) |
| `recovery_hint=REFRESH_INDEX` / disk_matches only / mid-session edit miss | `codehelper analyze --force` (working-tree edits) or wait for watch |
| `recovery_hint=CHECK_WORKSPACE` / `workspace_warning` | Omit `repo=` or pass the open project’s name |
| `recovery_hint=DISAMBIGUATE` | Pass `path=` or a `sym:` id from candidates |
| Empty / thin `query` hits | Rephrase with concrete identifiers from the repo, then re-query |
| Symbol not indexed | Use `disk_matches` / `read_workspace_file`, or `codehelper analyze --force` |
| `learning_enabled=false` on `agent_memory` write | Enable in `.codehelper/learning.json` or skip recording |
| Edit after `change_kit` | `apply_patch_workspace_file` with `dry_run=true` first, then apply for real |
| `repo_root is required` from `verify` | Prefer `repo=<indexed name>` (auto-fills root + cmds); or pass absolute `repo_root=` / `cwd=` |
| Verify abstain / no cmds | Pass `lint_cmd`/`build_cmd`/`test_cmd` as **plain strings** (preferred) or JSON arrays of argv tokens; aliases `lint`/`build`/`test`. Or `finish_check` with `verify_abstained=true` + `verify_reason` |
| Verify `[cmd` / executable not found | Host Sprint'd a JSON array — pass `"lint_cmd":"cmd /c echo ok"` (string) or a real array; mangled `[cmd …]` is auto-recovered with a note |
| `review_diff` noisy on dirty tree | Use `base=HEAD`, or rely on demoted path-heuristics (medium/low) + suppression when >12 files change |
| `context` with `symbol=` | Works (alias) but response includes `param_correction` — prefer `name=` next time |

## Full LIVE edit loop (claim-done path)

```text
kickoff / change_kit target=…
  → apply_patch_workspace_file dry_run=true
  → apply_patch_workspace_file (write)  # or revert_workspace_edit
  → diagnostics
  → review_diff
  → verify repo=…   # argv; omit cmds to auto-fill
  → finish_check verify_ran=true
```

Every gate tool returns `what_next` + `recommended_next_tools` (TOON by default; `format=json` for harnesses). Claim done **only** when `finish_check.can_claim_done=true`.

On sparse graphs (PHP/Ruby/C): if `call_graph_confidence` starts with `LOW`, or `blast_radius.dependents=0` with a SPARSE note, do **not** treat `risk_tier=low` / `0 callers` as isolation proof — confirm with tests + a textual search.

Perf / slow locate: prefer `investigate recipe=perf` (hotspots + N+1 query pack) or `hotspots` then `query` with pack terms (`n+1`, `prefetch`, `select_related`).

## Default response shape to consume

```text
what_next: <one sentence>
next_queries:
  - <copy-paste tool call>
  - …
```

Follow those before inventing a parallel tool chain.
