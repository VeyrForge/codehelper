# codehelper-guide

When to use:
- First pass on any task in this repository.
- Need a safe default sequence before editing.

Inputs needed:
- Repository root path.
- Problem statement and expected output.

Default tool sequence:
1. `project_context` to confirm the current workspace is indexed.
2. `query` to locate symbols/files for the task.
3. `context` on top symbol before opening files.
4. `impact` on edited symbols before changes.
5. `detect_changes` after edits to summarize touched symbols.
6. `verify` in argv mode for lint/build/test gates.

Failure and uncertainty behavior:
- If freshness is stale, run `codehelper analyze` before relying on retrieval.
- If risk tier is medium/high, surface risk and ask before broad edits.
- Mark missing facts as `[UNCERTAIN]` and avoid guessing.

Example prompt:
- "Use codehelper MCP-first flow, then implement the smallest safe fix."
