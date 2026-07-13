# codehelper-debugging

When to use:
- CI failures, regressions, runtime errors, or failing tests.

Inputs needed:
- Error text/log line and failing command/test.
- Suspected symbol/file if known.

Default tool sequence:
1. `query` with exact failure phrases and symbol names.
2. `context` on likely failing symbol.
3. `impact` downstream to estimate side effects.
4. Implement smallest safe fix.
5. `verify` with targeted commands first, then full gate if needed.

Failure and uncertainty behavior:
- If no matching symbols, broaden query terms and check freshness state.
- If fix touches medium/high impact areas, call it out before applying.
- Do not claim completion when verification fails or abstains.

Example prompt:
- "Debug failing eval query missing expected path in CI."
