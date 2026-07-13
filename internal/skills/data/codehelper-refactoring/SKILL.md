# codehelper-refactoring

When to use:
- Symbol renames, module cleanup, or behavior-preserving structure changes.

Inputs needed:
- Target symbol id/name and intended replacement.
- Constraints on public API compatibility.

Default tool sequence:
1. `impact` with deeper traversal for target symbol.
2. `rename` in dry-run mode first.
3. Apply refactor changes in small batches.
4. Run `detect_changes` to summarize affected symbols.
5. `verify` to confirm no regressions.

Failure and uncertainty behavior:
- Preserve public contracts unless explicitly approved to break.
- If fanout is large, split into smaller commits/PRs.
- Re-run `codehelper analyze` after large structural refactors.

Example prompt:
- "Refactor retrieval ranking code without changing public APIs."
