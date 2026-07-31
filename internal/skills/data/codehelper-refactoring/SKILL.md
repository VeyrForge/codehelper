# codehelper-refactoring

When to use:
- Renames, extractions, or structural edits to indexed symbols.

Sequence:
1. `change_kit` `target=<symbol>` — definition, call sites, covering tests, risk.
2. If symbol missing: follow the error (use `query`, then retry with exact name / `sym:` id).
3. Prefer `apply_patch_workspace_file` (indent-preserving). Use `write_workspace_file` only for new files or wholesale rewrites; empty content is refused.
4. `rename_symbol` / `insert_at_symbol` when those fit better than a patch.
5. `diagnostics` (actionable errors first) → `verify` → `finish_check`.

Notes:
- Hub utilities (`log`, `error`, `cn`, …) often rank high on centrality — confirm with `context` before treating them as the edit target.
- Do not claim done without the verify loop.
