# Browser UI eval smoke

Deterministic local check that verifies a page with the MCP
`browser` tool (no cloud model). Entry point: `scripts/browser-ui-eval.sh`.

## What it runs

1. **Fixture harness** — `go test ./internal/web/ -tags rod -run TestUIEvalHarness_AgentCanVerifyUI` against HTML under `internal/web/testdata/ui_fixture/`.
2. **Workflow recipes** — confirms `project_context` recipes include browser verify loops (`TestFeatureLifecycleRecipes` / smoke).
3. **Skills stamp** — browser skill install path still writes a version stamp.
4. **Optional live WordPress** — soft-skip unless `CODEHELPER_WP_URL` (or `http://wp-test.local`) responds and `codehelper` is on `PATH`.

## Usage

```bash
scripts/browser-ui-eval.sh
scripts/browser-ui-eval.sh --report .testbeds/reports
CODEHELPER_SKIP_BROWSER_TEST=1 scripts/browser-ui-eval.sh   # skip Chromium-heavy paths when set by tests
CODEHELPER_WP_URL=http://wp-test.local scripts/browser-ui-eval.sh
```

With `--report <dir>`, the script sets `CODEHELPER_BROWSER_UI_REPORT` to
`<dir>/browser-ui-eval-latest.json` when the harness writes one.

## Relation to MCP `browser`

After a UI change: `outline` / `snapshot` → `actions` ending in `assert` → on
failure fix and **retest the same assert** → `finish_check`. See
[MCP_TOOLS.md](MCP_TOOLS.md) (browser section) and [PERMISSIONS.md](PERMISSIONS.md).
