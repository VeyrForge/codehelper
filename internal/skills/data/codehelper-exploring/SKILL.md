# codehelper-exploring

When to use:
- Repository discovery, onboarding, or architecture mapping.
- User asks "where/how is X implemented?" across packages.

Inputs needed:
- Search topic (feature, error text, symbol, behavior).
- Optional repo name for multi-repo registries.

Default tool sequence:
1. `project_context` for the open workspace (omit `repo` on other tools).
2. `query` with topic-focused terms.
3. `context` for top hits to inspect callers/callees/imports.
4. Optional `cypher` for direct relation questions.

Failure and uncertainty behavior:
- If indexed commit lags HEAD, call out staleness and suggest `codehelper analyze`.
- Do not broad-scan files before graph-first exploration.

Example prompt:
- "Find where eval smoke checks are defined and what calls them."
