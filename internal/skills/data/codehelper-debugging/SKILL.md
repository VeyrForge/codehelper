# codehelper-debugging

When to use:
- CI failures, regressions, runtime errors, or failing tests.
- UI regressions caught by `browser` (failed assert, console/uncaught, bad screenshot).

Inputs needed:
- Error text/log line and failing command/test — or browser report lines (assert, diagnostics).
- Suspected symbol/file if known.
- For UI: the URL + last `actions` / `outline` result — or configured `site=` from `setup_suggestions` / connections.

Default tool sequence:
1. If no live URL/`site=` yet: propose `setup_suggestions` (from `project_context`/`kickoff`) to the user before another browser attempt.
2. `query` with exact failure phrases and symbol names (include console/uncaught text from `browser`).
3. `context` on likely failing symbol.
4. `impact` downstream to estimate side effects.
5. Implement smallest safe fix.
6. Re-verify:
   - Code/tests: `verify` with targeted commands first, then full gate if needed.
   - UI: same `browser` assert (or CMS `recipe=` + `site=`) — **retest after every fix**.
7. `finish_check` only after the failing gate (test or browser) is green.

Failure and uncertainty behavior:
- If no matching symbols, broaden query terms and check freshness state.
- If fix touches medium/high impact areas, call it out before applying.
- Do not claim completion when verification fails or abstains.
- Browser assert flaky? Prefer `wait`/`wait_nav` + stable selectors from `outline`; do not weaken the assert to greenwash.

Example prompt:
- "Debug failing eval query missing expected path in CI."
- "Browser assert on #success failed after my patch — fix and retest."
