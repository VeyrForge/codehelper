# VIBE.md — how agents should use codehelper (vibe vs senior)

Short contract for Cursor / Claude agents. Full tool table: [AGENTS.md](./AGENTS.md).
**Claude / Cursor / Codex playbooks + param-key recovery:** [AGENT_QUICKSTART.md](./AGENT_QUICKSTART.md).

## Default response shape (always prefer)

Every vague `kickoff` / `plan` / `scout` / `investigate` / `orchestrate` answer should be usable as:

```text
what_next: <one sentence — cite file:line OR ABSTAIN — paste next: `…`>
next_queries:
  - <copy-paste tool call 1>
  - <copy-paste tool call 2>
  - <copy-paste tool call 3>
findings[] / reuse_candidates[]   # grounded when available
abstain: …                        # when not
```

- **Labeled findings:** `FRAMEWORK FOOTGUN (not an app CVE)` / `CONFIG CHECKLIST (not a CVE)` — do not scare juniors with fake CVEs.
- **Setup tax off** for simple health/sec/perf vibe asks (`setup_suggestions` cleared).
- **Honest ABSTAIN** beats inventing `/health` on Redis/Vue/Svelte.

## For Claude / Cursor / Codex (30-second)

| Client | Start with | Param traps |
|---|---|---|
| Cursor | `kickoff task=…` (or `orchestrate` if enabled) | `context` → `name=`; `change_kit`/`impact` → `target=` |
| Claude Code | `project_context` once, then `kickoff` | Prefer `task=` over `query=` on kickoff |
| Codex | Same MCP; use `format=json` in harnesses | Slim `orchestrate` already has `what_next` + `next_queries`; `detail=true` for full pack |

On tool errors: parse `recovery_hint` / `error_category` when present (JSON error body), fix the **argument key** once, retry — do not fall back to Grep first. After `change_kit`: `apply_patch_workspace_file` with `dry_run=true` first. Full playbook: [AGENT_QUICKSTART.md](./AGENT_QUICKSTART.md).

If `call_graph_confidence` starts with `LOW` (PHP/Ruby/C sparse graphs): do **not** treat `risk_tier=low` / empty callers as isolation proof.

## Vibe / junior path (messy prompts)

| User vibe | Call first | Then |
|---|---|---|
| “add health real quick” | `kickoff` (empty `sections=` → light orient+reuse+docs+steps) | `change_kit target=health` or top `route_health*` / `placement_*` |
| “idk feels insecure” | `kickoff` role=security **or** `investigate recipe=security` | Read #1 `file:line`; treat `library_guidance` as footguns |
| “make it faster somehow” | `kickoff` role=performance **or** `hotspots` / `investigate recipe=perf` | Use `why` + `rewrite_hint`; `context` before optimizing |
| “also log request ids” | `kickoff` / `scope` after health exists | Extend existing middleware/logger — don’t invent a second stack |

Rules of thumb:

1. One starting call (`kickoff` or `orchestrate`) — do not chain `project_context`→`query`→`context` for tiny asks.
2. Paste a `next_queries` line next; don’t invent tools.
3. If `findings_mode=abstain` / `abstain:` present — stop claiming vulns or `/health`; ask the user or follow the three queries.
4. Prefer extending ranked reuse over new files.

## Senior / precise path

| Goal | Call |
|---|---|
| Orient once | `project_context` verbosity=short |
| Symbol truth | `query` → `context` (name=) → `impact` only if blast radius not already in context |
| Design trade-offs | `plan` role=architect\|security\|performance\|refactor\|feature |
| Edit | `change_kit` → patch → `diagnostics` → `review_diff` → `verify` → `finish_check` |
| Security audit | `investigate recipe=security` — prefer `authz-fail-open` / `injection-taint` / SQL sinks with high confidence over footguns |
| Perf audit | `hotspots` — read `why`, `rewrite_hint`, `suggested_next_query`; confirm with `impact` + `test_impact` |

Seniors may skip vibe lightness (`sections=` explicit, full decisions) and should still respect ABSTAIN + footgun labels.

## Security signal quality (read this)

| Kind / rule | Treat as |
|---|---|
| `sink_candidate` + `authz-fail-open` / `injection-taint` / `sql-string-concat` (high conf) | Real investigate-now TP candidates — confirm with `read`/`context` |
| `library_guidance` / FRAMEWORK FOOTGUN | Trust-boundary / API footgun — not an app CVE by itself |
| `config_hardening` / CONFIG CHECKLIST | Deploy defaults — not exploit proof |
| Empty sinks + ABSTAIN | Do **not** invent vulns |

Fail-open auth and request→sink taint use **dataflow-lite** (intra-function), not full SSA. Still confirm before patching.

## HTTP health

- HTTP frameworks/apps (Flask, Django, FastAPI, Axum, Gin, Express, Nest, Laravel, Rails, Spring, …): locate `/health` **or** `placement_*` near the router — never false “may not expose HTTP”.
- Non-HTTP cores (Redis C, Vue/Svelte compilers): clear ABSTAIN with reason — do not invent a server.

## Orchestrate

When local orchestration is on: `orchestrate` returns slim `agent_brief` plus **what_next**, **next_queries**, and **mcp_param_keys** by default. Prefer that over dumping full packs unless `detail=true`.

## Eval: HUMAN vs AUTO (never merge)

Product quality claims use the **HUMAN** lens (harsh spot / human-audit). Harness **AUTO** scores are higher and format-friendly — report them separately; do **not** ship or retag on AUTO alone. See [AGENT_QUICKSTART.md](./AGENT_QUICKSTART.md) § Eval scores and [`.testbeds/reports/DUAL-SCORE-PROTOCOL.md`](./.testbeds/reports/DUAL-SCORE-PROTOCOL.md).
