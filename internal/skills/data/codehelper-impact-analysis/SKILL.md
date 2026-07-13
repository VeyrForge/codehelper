# codehelper-impact-analysis

When to use:
- Before non-trivial edits, especially shared helpers or public interfaces.
- During review to assess blast radius.

Inputs needed:
- Target symbol id/name.
- Direction (`upstream` or `downstream`) and depth.
- Base ref for `detect_changes` comparison.

Default tool sequence:
1. `impact` on the target symbol.
2. If medium/high risk tier, pause and summarize risk before editing.
3. After edits, run `detect_changes` to map touched symbols.
4. Re-run `impact` when edits expand to new symbols.

Failure and uncertainty behavior:
- If symbol resolution is ambiguous, run `query` then `context` first.
- Prefer small scoped edits when impact fanout is large.

Example prompt:
- "Assess blast radius for changing runCommand timeout behavior."
