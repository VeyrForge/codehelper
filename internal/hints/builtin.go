package hints

import "sync"

// builtinHints are shipped defaults — idempotently upserted into
// ~/.codehelper/learned_hints.json so every project surfaces them via
// project_context / kickoff learned_hints.
var builtinHints = []struct {
	scopeType, scope, text string
}{
	{
		ScopeGlobal, "",
		"MCP param names differ by tool — read the schema before the first call: " +
			"`context` and `impact` take `name` (or a sym: id from query), not `symbol`; " +
			"`change_kit` requires `target`; `trace` needs `from` + `to`; `query` needs `query`. " +
			"If a call errors on a missing/wrong param, fix it once — do not retry blind.",
	},
	{
		ScopeGlobal, "",
		"Token routing: use codehelper MCP tools before Read/Grep/Glob. Call `project_context` " +
			"once per session (default `verbosity=short`). To find code or a symbol, use `query` or " +
			"`scout` — not a second bootstrap. `context` already returns blast_radius; skip a " +
			"separate `impact` unless you need only impact fields. Avoid `Glob **/*` — use a " +
			"narrow path or Grep.",
	},
	{
		ScopeGlobal, "",
		"Symbol miss vs. live disk: if `query`/`context` can't find a symbol you expect, the " +
			"file may be new/uncommitted and not yet indexed (watch can lag, and `freshness.index_lag` " +
			"flags this). The tools already disk-scan and report `disk_matches` on a miss — open that " +
			"path with `read_workspace_file`, or fall back to Grep, instead of assuming the symbol " +
			"doesn't exist.",
	},
	{
		ScopeProjectType, "rust",
		"Monorepo Rust layout: the Cargo crate may live in rust/ (not repo root). " +
			"Set verify_cwd=rust or rely on sub-project auto-detect for diagnostics/verify. " +
			"For duplicate names like main/run across Python scripts and Rust, pass path= or use sym: ids from query hits.",
	},
	{
		ScopeGlobal, "",
		"Duplicate symbol names (main, run, init): use sym: ids from query hits or pass path= to context/impact. " +
			"Never assume the first context hit is correct when query warns of ambiguity.",
	},
}

var ensureBuiltinOnce sync.Once

// EnsureBuiltin upserts shipped global hints (safe to call repeatedly).
func EnsureBuiltin() {
	ensureBuiltinOnce.Do(func() {
		for _, h := range builtinHints {
			_, _ = Add(h.scopeType, h.scope, h.text, "codehelper")
		}
	})
}
