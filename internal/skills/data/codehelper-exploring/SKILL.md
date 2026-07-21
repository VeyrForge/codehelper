# codehelper-exploring

When to use:
- Orienting in an unfamiliar area of the repo.
- Finding where behavior lives before reading files.

Preferred calls:
1. `kickoff` `task=<what you're trying to understand>` — one-shot orient + reuse.
2. `scout` `task=…` when you want reuse candidates + usage_of_top before building.
3. `query` `query=…` for name/concept search (Locate recipe: query → context → impact).
4. `context` `name=…` for one symbol (source + callers + callees + blast radius). Pass `path=` when Nest/FastAPI/Express sample collisions appear.
5. `trace` when you need how A reaches B.
6. `orchestrate` only when orchestration is enabled (`codehelper orchestration enable`).

Avoid:
- Starting with Read/Grep/Glob when kickoff/query would answer cheaper.
- Passing `query=` to kickoff as the only param without knowing it is an alias for `task=`.
- Stopping after `project_context` — it does not search code.
- Trusting sample/test/fixture or CSS hubs as the "main" definition when a production hit exists (watch `collision_note` / demoted hits).
