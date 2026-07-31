# MCP response quality scorecard — 2026-07-25

**Scope:** How good MCP *responses* are for agents (not just locate-hit).  
**Binary:** `.testbeds/codehelper-eval.exe` (CGO + MinGW gcc 14.2).  
**Beds:** `.testbeds/real-oss` (fresh analyze during probe).  
**Protocol:** `query` → `context` → `impact` (depth=1, max_candidates=8); `kickoff` sections=orient,reuse on a subset.  
**Harness:** `TestMCPResponseQualityLiveBeds` → `.testbeds/reports/mcp-response-quality-2026-07-25/scorecard.json`.

## Dimensions (/10)

| Dim | What “good” means |
|---|---|
| Token / size | Caps hold; no hub dump (context callers≤12, impact nodes≤24, default top_k) |
| recovery / what_next | Actionable next step (path=, sym:, change_kit, query retry) — not empty pep talk |
| Collision / path= | Demotion + honesty when samples/fixtures collide; path= / ParentID guidance |
| Provenance / sparse | provenance / self-only / sparse / confidence when graph is thin |
| No bleed | Nested bed stays on bed symbols; no `sym:codehelper:` displacing gold |

**Overall** = human judgment across the five dims (auto harness is a floor; see notes).

## Per-bed ratings

| Bed | Stack | Overall | Token | Recover | Path= | Sparse | Bleed | Notes |
|---|---|---:|---:|---:|---:|---:|---:|---|
| express | JS OSS | **9** | 10 | 9 | 9 | 8 | 10 | Demoted 13 examples; top `lib/application.js` / `createApplication`; kickoff what_next → `app.use` + path= |
| nest | Nest stub | **9** | 10 | 9 | 9 | 9 | 10 | `src/cats` beats samples; demote note; impact sparse-aware |
| godot | GDScript | **9** | 10 | 9 | 9 | 8 | 10 | `_ready` **not** HTTP health pack; ambiguous `_ready` → path=/candidates; kickoff → `scripts/player.gd` |
| unity | C# | **9** | 10 | 9 | 8 | 8 | 10 | Combat `Health` **not** health-pack expanded; context/impact clean |
| multi-repo-a | Go | **9** | 10 | 9 | 8 | 9 | 10 | `GetStock` only; no host-repo bleed; self-only BR honest |
| gin | Go | **8** | 10 | 8 | 9 | 7 | 10 | After fix: `Context.JSON` tops with `qualified_recv` (was SecureJsonPrefix). Ambiguous bare `JSON` still asks path= |
| laravel | PHP | **8** | 10 | 9 | 8 | 9 | 10 | `User` @ Models; kickoff sparse note; dynamic graph still thin |
| wordpress | PHP | **8** | 10 | 8 | 8 | 9 | 10 | ProbePlugin/boot locate; sparse warnings present |
| spring | Java | **8** | 10 | 8 | 8 | 8 | 10 | OwnerController↔PetService; compact payloads |
| nextjs | TS | **8** | 10 | 8 | 8 | 8 | 10 | Tiny App Router bed; Page locate OK |
| svelte | Svelte | **8** | 10 | 8 | 8 | 8 | 10 | Toggle/toggle; no CSS hub dump |
| fastapi | Python OSS | **8** | 10 | 9 | 8 | 8 | 10 | Demotes docs_src/tests **and** framework Depends hubs; query/kickoff prefer `get_db` / docs_src app symbols over `fastapi/dependencies` locals when the locate query includes app tokens |

**Average overall: 8.4 / 10** (12 beds).

Automated floor from harness: **9.0** mean (same beds; coarser boolean checks — does not penalize weak what_next targets).

## Cross-cutting fixes this pass

1. **Health false friends** (`isEndpointFeatureTask`)  
   Soft `health` / `ready` now require **identifier boundaries**. Stops expanding the HTTP health pack on tooling prose like `healthQueryPack`, `isClusterHealthy`, `GetReadyState`. Godot `_ready` / Unity `Health` already guarded; still green.  
   Files: `internal/mcpsvc/findings_mode.go`, `findings_mode_test.go`.

2. **Type.Method ranking / kickoff demotion** (`Context.JSON` → SecureJsonPrefix)  
   `QueryHybridWithOptions` now ranks Type.Method from the **locate query** when MCP `intent=` is empty or a mode label (`explore|debug|test|refactor`). Kickoff passes the full `task` as Intent so dots survive.  
   Files: `internal/retrieval/hybrid.go`, `hybrid_test.go`, `internal/mcpsvc/kickoff_tools.go`.  
   Regression: `TestGinContextJSONQualifiedRank`.

3. **Parser build unblock (unrelated WIP)**  
   `erlang.go` / `fsharp.go` called missing `dartIndentLen` → use shared `indentLen`.

4. **FastAPI framework-hub crowding** (`Depends` / `depends` vars → `what_next`)  
   When the query/task also names app tokens (`list_users`, `get_db`, `UserService`, …), demote framework hubs below app/tutorial hits; hub-only queries prefer public API defs (`param_functions.Depends`) over package locals. Kickoff/plan/scout fetch a larger hybrid pool before demotion so app hits are visible.  
   Files: `internal/mcpsvc/agent_ux.go`, `kickoff_tools.go`, `plan_tools.go`, `scout_tools.go`, `register.go`, `retrieval_tools.go`.  
   Regression: `TestDemoteFrameworkHubHits_*`, `TestFastAPIHubDemotionLive`.

## Already solid (no change)

- **Caps:** context caller/callee lists capped at 12 with truncation notes; impact nodes capped at 24; default `max_candidates=8`.
- **Demotion:** Express examples / Nest samples / FastAPI docs_src demoted with retrieval_note + path= honesty.
- **Nested bed scoping:** `preferNestedIndexedWorkspace` + `TestRepoNameForRootsSkipsParentWhenChildHasOwnIndex` — no parent bleed on indexed children.
- **recovery_hint / what_next:** Present on context/impact/kickoff (query stays search-shaped).

## Reproduce

```powershell
$env:Path = "$PWD\.tools\mingw\mingw64\bin;$env:Path"
$env:CGO_ENABLED = "1"; $env:CC = "gcc"
go build -o .testbeds/codehelper-eval.exe ./cmd/codehelper

$env:CODEHELPER_TESTBEDS = "$PWD\.testbeds\real-oss"
go test ./internal/mcpsvc/ -run 'TestMCPResponseQualityLiveBeds|TestIsEndpointFeatureTask_EngineFalseFriends|TestGinContextJSONQualifiedRank|TestFastAPIHubDemotionLive|TestDemoteFrameworkHubHits' -count=1 -timeout 300s -v
go test ./internal/retrieval/ -run 'TestQueryHybrid_ContextJSONPrefersQualifiedRecv|TestSplitQualifiedQueryAndModeIntent' -count=1
```

Artifacts: `.testbeds/reports/mcp-response-quality-2026-07-25/scorecard.json`

## Residual gaps

- Query responses rarely emit `what_next` (by design); agents must follow `recommended_next_tools` or use kickoff/investigate.
- Restart IDE MCP after rebuild to pick up health / Type.Method / FastAPI hub-demotion fixes in live Cursor sessions.
- FastAPI OSS still has no `list_users` symbol (tutorials use `read_users` / `get_db`); context on bare `list_users` relies on recovery_hint / soft miss — prefer `get_db` or docs_src paths from query tops.
