# codehelper-guide

When to use:
- First pass on any task in this repository.
- Need a safe default sequence before editing.

Inputs needed:
- Repository root path.
- Problem statement and expected output.

Default tool sequence:
1. `kickoff` with `task=…` for feature/fix/vibe starts (orient + reuse + docs + verify in ONE call). Prefer it over chaining `project_context` → `query` → `context`. Cheaper payload: `sections=orient,reuse`.
2. If only orienting: `project_context` verbosity=short once per session — read `warnings` (inventory/sparse graph), `setup_suggestions` (propose incomplete browser/CMS setup to the user before first `browser` call), and `workflow_recipes` (`vibe_fix`, `locate_symbol`, …).
3. `query` / `scout` to locate symbols when kickoff reuse is thin. Prefer production hits; if `collision_note` appears, pass `path=` on `context`/`impact` for intentional samples.
4. `context` on the top symbol before opening files (not raw `read_workspace_file`).
5. Before editing a known symbol: `change_kit` with `target=…`, then `apply_patch_workspace_file`.
6. If local orchestration is enabled: `orchestrate` for guided multi-step investigation.
7. After edits: `diagnostics` → `review_diff` → `verify` → `finish_check`. Claim done ONLY when `finish_check.can_claim_done=true` (or explicit `verify_abstained` + reason).
8. UI / page changes: follow `codehelper-browser` — **propose `setup_suggestions` first** if no `site=`/URL yet, then `browser` outline → assert → fix → **retest** before `finish_check`. Use recipes `vibe_ui` / `programmer_ui` / `browser_qa` from `workflow_recipes`.

Failure and uncertainty behavior:
- If freshness is stale, run `codehelper analyze` before relying on retrieval.
- If kickoff/orchestrate errors on wrong params: `kickoff` wants `task` (not `query`); `query=` is accepted as an alias with a correction note.
- If risk tier is medium/high, or project_context warns inventory/contains-only, surface risk and confirm before broad edits — do not treat 0 callers as isolation proof.
- Mark missing facts as `[UNCERTAIN]` and avoid guessing.
- Never claim a UI fix done without a passing `browser` (or `web` for HTTP-only) assert.

Example prompt:
- "Use codehelper kickoff then implement the smallest safe fix."
- "Vibe-fix the checkout CTA and prove it with browser assert before finish_check."
