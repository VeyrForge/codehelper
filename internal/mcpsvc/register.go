package mcpsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/hints"
	"github.com/VeyrForge/codehelper/internal/hubs"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/prompts"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/VeyrForge/codehelper/internal/setupsuggest"
	"github.com/VeyrForge/codehelper/internal/verify"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// stdRetrievalNote is the constant retrieval-guidance note attached to a normal
// (has-hits) query response. It is emitted once per session (see queryHandler);
// situational notes for zero-hit / disk-fallback cases differ from it and always ship.
const stdRetrievalNote = "Hits are ranked indexed symbols (path/name/signature), not arbitrary substring matches across every file. Refresh index if stale; use read_workspace_file for full documents."

// compactHit is the token-lean representation of a search hit: it drops fields
// that are redundant (id repeats repo+path+line+name), constant across hits
// (repo_id), or low-signal in a ranked list (line_end, language), and rounds the
// score. ~7x smaller than the full Symbol while keeping everything an agent needs
// to decide what to open next. Used by default; verbosity=detailed returns the
// full Symbol records instead.
type compactHit struct {
	ID      string   `json:"id,omitempty"` // sym:… id — pass to context/impact to disambiguate
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Loc     string   `json:"loc"`            // path:line_start — a clickable reference
	Recv    string   `json:"recv,omitempty"` // receiver type for methods (from parent_id)
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons,omitempty"`
}

type queryToolResponse struct {
	Hits                 any                       `json:"hits"` // []compactHit (concise) or []retrieval.RankedSymbol (detailed)
	HitsTruncated        int                       `json:"hits_truncated,omitempty"`
	Freshness            freshness.Report          `json:"freshness"`
	Warning              string                    `json:"warning,omitempty"`
	Intent               string                    `json:"intent,omitempty"`
	ContextPack          *retrieval.ContextPack    `json:"context_pack,omitempty"`
	Budgeted             *retrieval.ContextPackV2  `json:"budgeted,omitempty"`
	RetrievalMeta        *contextPackRetrievalMeta `json:"retrieval_meta,omitempty"`
	CrossRepoCandidates  []registry.Entry          `json:"cross_repo_candidates,omitempty"`
	RetrievalNote        string                    `json:"retrieval_note,omitempty"`
	SemanticRerank       string                    `json:"semantic_rerank,omitempty"`
	FileSnippets         []retrieval.FileSnippet   `json:"file_snippets,omitempty"`
	DiskMatches          []retrieval.DiskMatch     `json:"disk_matches,omitempty"`
	RecommendedNextTools []string                  `json:"recommended_next_tools,omitempty"`
	Warnings             []string                  `json:"warnings,omitempty"`
	EvidencePaths        []string                  `json:"evidence_paths,omitempty"`
	Confidence           float64                   `json:"confidence,omitempty"`
}

// queryToolResponseSchema mirrors queryToolResponse for OUTPUT-SCHEMA GENERATION
// ONLY — it is never marshaled with data. It exists because queryToolResponse.Hits
// is typed `any` (it holds []compactHit in concise mode or []retrieval.RankedSymbol
// in detailed mode), and Go's `any` reflects to the JSON-Schema boolean `true`.
// Strict MCP clients (Cursor, Claude Code) reject a property whose schema is a bare
// boolean — "Invalid input" at outputSchema.properties.hits — and that one bad
// property poisons the ENTIRE tools/list offering, so the agent silently gets NONE
// of codehelper's tools. Typing hits here as []map[string]any yields a valid,
// permissive "array of objects" schema that both hit shapes satisfy. Every other
// field MUST stay identical to queryToolResponse — TestQueryToolResponseSchemaParity
// enforces that the JSON field sets match.
type queryToolResponseSchema struct {
	Hits                 []map[string]any          `json:"hits"`
	HitsTruncated        int                       `json:"hits_truncated,omitempty"`
	Freshness            freshness.Report          `json:"freshness"`
	Warning              string                    `json:"warning,omitempty"`
	Intent               string                    `json:"intent,omitempty"`
	ContextPack          *retrieval.ContextPack    `json:"context_pack,omitempty"`
	Budgeted             *retrieval.ContextPackV2  `json:"budgeted,omitempty"`
	RetrievalMeta        *contextPackRetrievalMeta `json:"retrieval_meta,omitempty"`
	CrossRepoCandidates  []registry.Entry          `json:"cross_repo_candidates,omitempty"`
	RetrievalNote        string                    `json:"retrieval_note,omitempty"`
	SemanticRerank       string                    `json:"semantic_rerank,omitempty"`
	FileSnippets         []retrieval.FileSnippet   `json:"file_snippets,omitempty"`
	DiskMatches          []retrieval.DiskMatch     `json:"disk_matches,omitempty"`
	RecommendedNextTools []string                  `json:"recommended_next_tools,omitempty"`
	Warnings             []string                  `json:"warnings,omitempty"`
	EvidencePaths        []string                  `json:"evidence_paths,omitempty"`
	Confidence           float64                   `json:"confidence,omitempty"`
}

// RegisterAll wires MCP tools, resources, and prompts.
func RegisterAll(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("project_context",
		mcp.WithDescription("One-time BOOTSTRAP for the current MCP workspace: repo identity, index freshness + stats, stack summary, MCP tool count/routing, matching cross-project hints, and next_step. Default verbosity=short (essentials); verbosity=detailed adds layout/deps/git/scripts; sections=tools adds grouped MCP tool names. It does NOT search code — after calling once, use query/scout. Omit repo on other tools to reuse this workspace."),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithString("verbosity", mcp.Description("short (default: essentials + MCP catalog header) | detailed (full layout, deps, scripts, git, language stats, grouped tool names)")),
		mcp.WithString("sections", mcp.Description("Optional comma list for cheaper payloads: tools (grouped MCP tool names only, skips layout)")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default, token-efficient) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("project_context", projectContextHandler(regRef)))

	s.AddTool(mcp.NewTool("query",
		mcp.WithDescription("Locate symbols in the indexed graph (BM25/FTS + 1–2 hop graph expand + RRF; optional vector channel) — not web search. Prefer search_hybrid when you also want a package public_api_map. Production/app defs rank above sample/test/fixture/style noise; pass path= on follow-up context/context_bundle/impact when ambiguous. For broad/architecture questions set include_context_pack=true and limit 24-32. Pair hits with context_bundle before claiming behavior. Empty hits → rephrase, ast_query, or analyze."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Symbol name, concept, or natural-language locate task")),
		mcp.WithString("repo", mcp.Description("Repository name (optional if only one indexed)")),
		mcp.WithString("intent", mcp.Description("Optional task intent: explore|debug|test|refactor")),
		mcp.WithNumber("top_k", mcp.Description("Max ranked hits to return (default 10). Lower = fewer tokens, sharper focus."), mcp.DefaultNumber(0)),
		mcp.WithString("verbosity", mcp.Description("concise (default: name/kind/loc/score) | detailed (full symbol records)")),
		mcp.WithBoolean("include_context_pack", mcp.Description("Include ranked context_pack in response"), mcp.DefaultBool(false)),
		mcp.WithNumber("limit", mcp.Description("Max items in context_pack when include_context_pack or limit>0 (default 24)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("budget_tokens", mcp.Description("When set (>0), also return token-budgeted buckets"), mcp.DefaultNumber(0)),
		mcp.WithString("base_ref", mcp.Description("Diff base for changed-symbol boosting"), mcp.DefaultString("HEAD~1")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default, token-efficient) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("query", queryHandler(regRef)))

	s.AddTool(mcp.NewTool("context",
		mcp.WithDescription("Full report for ONE symbol: definition SOURCE, signature/doc, callers, callees, imports, AND blast_radius + risk. Use after query/scout INSTEAD of raw read_workspace_file or a separate impact call. Pass name or sym: id; when names collide (Nest samples, FastAPI docs_src), pass path= to pin the production (or intentional sample) definition."),
		mcp.WithString("name", mcp.Required(), mcp.Description("REQUIRED — symbol name or sym: id from query (NOT 'symbol'; aliases: symbol, sym, target)")),
		mcp.WithString("path", mcp.Description("Definition file (relative to repo root) to disambiguate when several symbols share the name")),
		mcp.WithNumber("line", mcp.Description("Definition line to disambiguate (optional)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("body", mcp.Description("full = complete definition source; none = symbol graph only (no source — use for orchestration); default caps at 40 lines")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default, token-efficient) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("context", contextHandler(regRef)))

	s.AddTool(mcp.NewTool("impact",
		mcp.WithDescription("Blast radius over the call graph before change/delete/rename. Default direction=upstream (who uses this?). Pass direction=downstream for deps. Bare names prefer non-fixture defs; pass path= on sample collisions. Class/type hubs that are self-only on downstream auto-retry upstream. Sparse graphs: do not treat 0 callers as proof of isolation — check confidence / doctor warnings."),
		mcp.WithString("target", mcp.Required(), mcp.Description("Symbol name or id")),
		mcp.WithString("path", mcp.Description("Definition file (relative to repo root) to disambiguate when several symbols share the name")),
		mcp.WithNumber("line", mcp.Description("Definition line to disambiguate (optional)")),
		mcp.WithString("direction", mcp.Description("upstream (default: who uses this) | downstream (what this depends on)"), mcp.DefaultString("upstream")),
		mcp.WithNumber("depth", mcp.Description("Max depth"), mcp.DefaultNumber(2)),
		mcp.WithBoolean("include_tests", mcp.Description("Include test files in impact nodes"), mcp.DefaultBool(true)),
		mcp.WithNumber("max_candidates", mcp.Description("Max must-update candidates returned"), mcp.DefaultNumber(8)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default, token-efficient) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("impact", impactHandler(regRef)))

	s.AddTool(mcp.NewTool("detect_changes",
		mcp.WithDescription("Map git-diff changed files to affected symbol ids (vs base_ref). Pair with `review` (audit the diff) or `test_impact` (tests to run)."),
		mcp.WithString("base_ref", mcp.Description("Git ref to diff against"), mcp.DefaultString("HEAD~1")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("detect_changes", detectChangesHandler(regRef)))

	s.AddTool(mcp.NewTool("scout",
		mcp.WithDescription("Before adding/fixing: ranked reuse candidates (caller counts) + usage_of_top call site + impact_of_top. Production defs beat sample/test/fixture (collision_note when demoted). Use when locating 'what already does X?' — reuse beats reinventing. Then context/change_kit before editing."),
		mcp.WithString("task", mcp.Required(), mcp.Description("What you want to add or fix, in natural language (e.g. 'parse a git diff into changed symbols')")),
		mcp.WithNumber("top_k", mcp.Description("Max reuse candidates (default 8)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("scout", scoutHandler(regRef)))

	s.AddTool(mcp.NewTool("test_impact",
		mcp.WithDescription("Which tests to run for a change. Walks the reverse call graph from changed (or a named target) symbols to the test functions that reach them — a SAFE over-approximation (never silently skips an affected test). Use after editing, before running the suite, to run only what matters."),
		mcp.WithString("base_ref", mcp.Description("Git ref to diff against for changed symbols (default HEAD~1)"), mcp.DefaultString("HEAD~1")),
		mcp.WithString("target", mcp.Description("Analyze tests reaching this specific symbol instead of a diff")),
		mcp.WithNumber("depth", mcp.Description("Max reverse-closure depth (default 6)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("test_impact", testImpactHandler(regRef)))

	s.AddTool(mcp.NewTool("since",
		mcp.WithDescription("What changed since a git ref, and what to do about it — in ONE call. Fuses detect_changes + impact + test_impact: the symbols changed vs base_ref (including uncommitted edits), the downstream blast radius (distinct dependents, worst risk tier, must-update call sites), and the test files to re-run (reverse call-graph closure, a SAFE over-approximation). Use right after editing, before running the suite — the post-edit companion to `scout`. Also lists new untracked source files the index can't see yet."),
		mcp.WithString("base_ref", mcp.Description("Git ref to diff against (default HEAD~1)"), mcp.DefaultString("HEAD~1")),
		mcp.WithNumber("impact_depth", mcp.Description("Downstream blast-radius depth (default 2)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("test_depth", mcp.Description("Reverse-closure depth for test selection (default 6)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("since", sinceHandler(regRef)))

	s.AddTool(mcp.NewTool("dead_code",
		mcp.WithDescription("Find symbols that nothing in the indexed graph references — candidate dead code. Lists functions/methods (optionally types/vars) with no inbound call or read edge, after excluding entrypoints, tests, and HTTP handlers that a runtime invokes. Returns CANDIDATES to verify, not a delete list: the call graph misses dynamic dispatch, reflection, and cross-repo callers. Use before a cleanup pass; confirm each with impact(upstream) + a name search first."),
		mcp.WithString("kinds", mcp.Description("Comma list of symbol kinds to scan: function,method,class,variable,type_alias,enum (default function,method)")),
		mcp.WithBoolean("include_exported", mcp.Description("Also report exported/public symbols (higher false-positive rate — they may have external callers). Default false."), mcp.DefaultBool(false)),
		mcp.WithBoolean("include_tests", mcp.Description("Include symbols defined in test files. Default false."), mcp.DefaultBool(false)),
		mcp.WithNumber("top_k", mcp.Description("Max candidates to return (default 50)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("dead_code", deadCodeHandler(regRef)))

	s.AddTool(mcp.NewTool("hotspots",
		mcp.WithDescription("Rank files by architectural RISK = git churn × call-graph centrality. A file changed often (git history) AND depended on heavily (inbound call edges) is where defects are most likely and refactoring most valuable — high churn alone is just active code, high centrality alone is stable infrastructure, their product isolates the risky core. Deterministic, no model. Use to pick refactor targets, focus review, or find where a change is most dangerous; inspect the top rows with `context`/`change_kit` and `impact`."),
		mcp.WithNumber("commits", mcp.Description("How many recent commits to scan for churn (default 1500)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("top_k", mcp.Description("Max hotspot files to return (default 20)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("hotspots", hotspotsHandler(regRef)))

	s.AddTool(mcp.NewTool("ast_query",
		mcp.WithDescription("Structural code search via a tree-sitter pattern — find every AST node of a given SHAPE, precisely, where lexical `query` can only match text. Reads files live from disk (never stale). `pattern` is tree-sitter's S-expression query syntax with @captures, e.g. `(function_declaration name: (identifier) @name)` to list Go funcs, or `(call_expression function: (selector_expression field: (field_identifier) @m) (#eq? @m \"Lock\"))` to find every `.Lock()` call site. Use for refactors and audits: 'all functions returning error', 'every struct embedding X', 'all callers shaped like Y'."),
		mcp.WithString("language", mcp.Required(), mcp.Description("Source language: go, python, typescript, javascript, rust, java, csharp, c, cpp, php, ruby, kotlin, swift, scala, lua, elixir, bash, hcl, protobuf (aliases like py/ts/js/rs accepted)")),
		mcp.WithString("pattern", mcp.Required(), mcp.Description("Tree-sitter S-expression query with @captures (NOT a regex). Node types are grammar-specific.")),
		mcp.WithString("path_glob", mcp.Description("Restrict to files whose relative path contains this substring (e.g. 'internal/parser')")),
		mcp.WithNumber("max_results", mcp.Description("Max matches to return (default 50, cap 200)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("ast_query", astQueryHandler(regRef)))

	s.AddTool(mcp.NewTool("api_surface",
		mcp.WithDescription("The PUBLIC API of a package/directory: its exported symbols with signatures (and doc-comment summaries), in one query — so you learn what a package exposes without reading every file. Pass a repo-relative path prefix (e.g. \"internal/retrieval\"). include_unexported=true also lists internals."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Repo-relative package/directory or file prefix (e.g. internal/retrieval)")),
		mcp.WithBoolean("include_unexported", mcp.Description("Also include unexported/internal symbols (default false)"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("api_surface", apiSurfaceHandler(regRef)))

	s.AddTool(mcp.NewTool("change_kit",
		mcp.WithDescription("Everything needed to change one symbol SAFELY, in a single call: its definition source, every call site (with the calling line), the tests that cover it, the risk tier, and a consistency checklist. Use right before editing a symbol — it replaces the read/grep round-trips you'd otherwise make and stops you from missing a caller. The edit-time companion to `scout`."),
		mcp.WithString("target", mcp.Required(), mcp.Description("REQUIRED — symbol to change: name or sym: id from query (aliases: name, symbol, sym)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("change_kit", changeKitHandler(regRef)))

	s.AddTool(mcp.NewTool("find_implementations",
		mcp.WithDescription("Which concrete types implement a Go interface — a heuristic interface→implementation map without go/types. Reads the interface's method set and reports every type whose methods cover it (structural typing); partial matches list the missing methods (often means embedding). Use to answer 'what satisfies this interface?' that ranked search can't. Heuristic: verify pointer-receiver/signatures before relying on it."),
		mcp.WithString("interface", mcp.Required(), mcp.Description("The Go interface type name (e.g. Reader)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("find_implementations", findImplementationsHandler(regRef)))

	s.AddTool(mcp.NewTool("similar",
		mcp.WithDescription("Similar-implementation search for ONE symbol: ranks other symbols whose name, signature, and package resemble the target. Use when you found one function and want peers to extend/copy — distinct from `scout` (task-oriented reuse) and `find_implementations` (Go interface satisfaction)."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Symbol name to find similar implementations for")),
		mcp.WithNumber("top_k", mcp.Description("Max similar symbols (default 8)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("similar", similarHandler(regRef)))

	s.AddTool(mcp.NewTool("trace",
		mcp.WithDescription("Call-graph navigation in ONE deterministic step instead of hopping context→context (a tool call per hop). With `from` and `to`: the exact SHORTEST call path between two symbols — \"how does the HTTP handler reach the DB write?\" — including detecting when the dependency actually runs the other way. With only `from`: the outbound call-flow tree. Use this for hidden/transitive dependencies that ranked search can't surface; pair with `impact` (blast radius) and `context` (1-hop neighbors)."),
		mcp.WithString("from", mcp.Required(), mcp.Description("Entrypoint symbol name or sym: id to trace outward from")),
		mcp.WithString("to", mcp.Description("Optional target symbol; when set, returns the shortest call path from→to")),
		mcp.WithNumber("depth", mcp.Description("Max call-graph hops to traverse (default 12)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("trace", traceHandler(regRef)))

	s.AddTool(mcp.NewTool("diagnostics",
		mcp.WithDescription("Compiler/static-check self-loop without an LSP: auto-detects the repo toolchain and runs its canonical checks (Go: go build + go vet; Rust: cargo check; TS: tsc --noEmit) in the sandboxed argv runner, then returns structured file:line:col problems. Use after editing to confirm the change compiles and vets clean before you claim it works — the fast feedback loop an LSP would give you. Pass `command` to run a custom check (e.g. \"npm run typecheck\")."),
		mcp.WithString("command", mcp.Description("Override the auto-detected check command (argv mode, no shell)")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Per-command timeout (default 120)"), mcp.DefaultNumber(0)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("diagnostics", diagnosticsHandler(regRef)))

	s.AddTool(mcp.NewTool("verify",
		mcp.WithDescription("Run lint/build/test gates with argv-mode default (no shell), per-command timeout, optional allowlist. REQUIRED before finish_check: after a green run set finish_check verify_ran=true; if cmds are missing/ephemeral use verify_abstained=true + verify_reason — never invent a green gate."),
		mcp.WithString("repo_root", mcp.Required()),
		mcp.WithString("lint_cmd", mcp.Description("e.g. npm run lint")),
		mcp.WithString("build_cmd", mcp.Description("e.g. go build ./...")),
		mcp.WithString("test_cmd", mcp.Description("e.g. go test ./...")),
		mcp.WithString("patch_unified", mcp.Description("Optional unified diff for heuristics")),
		mcp.WithString("exec_mode", mcp.Description("argv (default, secure, no shell) | shell (opt-in)"), mcp.DefaultString("argv")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Per-command timeout cap (default 300)"), mcp.DefaultNumber(300)),
		mcp.WithString("allowed_commands", mcp.Description("Comma-separated allowlist of executable basenames in argv mode (e.g. \"go,npm,make\")")),
		annotVerify(),
	), timedTool("verify", verifyHandler(regRef)))

	s.AddTool(mcp.NewTool("review_diff",
		mcp.WithDescription("Strict review of actual code changes"),
		mcp.WithString("base", mcp.Description("Diff base"), mcp.DefaultString("HEAD~1")),
		mcp.WithString("severity_floor", mcp.Description("low|medium|high|critical"), mcp.DefaultString("medium")),
		mcp.WithBoolean("include_tests", mcp.DefaultBool(true)),
		mcp.WithBoolean("include_security", mcp.DefaultBool(true)),
		mcp.WithBoolean("include_performance", mcp.DefaultBool(true)),
		mcp.WithBoolean("include_contracts", mcp.DefaultBool(true)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotReadOnlyClosedWorld(),
	), timedTool("review_diff", reviewDiffHandler(regRef)))

	s.AddTool(mcp.NewTool("docs",
		mcp.WithDescription("Up-to-date official documentation for a library/framework/API (codehelper's local-first answer to Context7). Resolves the version this project pins from its manifests, then fetches version-correct docs preferring the llms.txt/llms-full.txt standard before HTML. `library` may also be a direct https URL (docs page, API reference, or OpenAPI page) to fetch it as-is. Unknown libraries resolve via npm/PyPI/crates metadata; if one is still missing, register it with docs_add. Network fetch is privacy-gated."),
		mcp.WithString("library", mcp.Required(), mcp.Description("Library/framework name (next, react, laravel, cobra, django) OR a direct https docs/API URL")),
		mcp.WithString("topic", mcp.Description("Optional focus, e.g. 'app router', 'middleware', 'migrations'")),
		mcp.WithString("version", mcp.Description("Override version (default: detected from this project's manifest)")),
		mcp.WithNumber("max_tokens", mcp.Description("Approx token budget for returned docs (default 5000)"), mcp.DefaultNumber(5000)),
		mcp.WithBoolean("approve_network", mcp.Description("Allow network fetch for this call even if research is disabled in learning.json"), mcp.DefaultBool(false)),
		mcp.WithBoolean("no_cache", mcp.Description("Bypass the on-disk docs cache"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default, token-efficient) | json")),
		annotReadOnlyOpenWorld(),
	), timedTool("docs", docsHandler(regRef)))

	s.AddTool(mcp.NewTool("docs_add",
		mcp.WithDescription("Register a documentation source the curated index is missing, so a later `docs` call resolves to it. Use when `docs <name>` can't find a framework, internal library, or API reference. Writes the global catalog by default (scope=project for this repo only). Idempotent: re-adding a name replaces it."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Primary name to resolve, e.g. 'acme-sdk' or 'mycompany-api'")),
		mcp.WithString("doc_base", mcp.Required(), mcp.Description("Docs site root (https), e.g. https://docs.acme.dev — llms.txt is probed automatically")),
		mcp.WithArray("aliases", mcp.Description("Extra names that should resolve to this source (package name, import path, short name)")),
		mcp.WithString("llms_txt", mcp.Description("Explicit llms.txt URL if not at <doc_base>/llms.txt")),
		mcp.WithString("llms_full", mcp.Description("Explicit llms-full.txt URL")),
		mcp.WithNumber("trust", mcp.Description("Curation confidence 0-10 (default 7)"), mcp.DefaultNumber(0)),
		mcp.WithString("ecosystem", mcp.Description("go|npm|pip|composer|cargo (optional hint)")),
		mcp.WithString("scope", mcp.Description("global (default) | project (this repo only)")),
		mcp.WithString("repo", mcp.Description("Repository name when scope=project")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotProjectProfile(),
	), timedTool("docs_add", docsAddHandler(regRef)))

	s.AddTool(mcp.NewTool("web",
		mcp.WithDescription("Fast HTTP-only web verification (codehelper's optimized Playwright alternative): fetch a URL and assert on status, content, JSON paths, regex, and latency. No browser, no JS rendering — ideal for verifying APIs, SSR pages, and health checks in milliseconds. Use for finish/verify gates on web changes."),
		mcp.WithString("url", mcp.Required(), mcp.Description("URL to check (e.g. http://localhost:3000/health)")),
		mcp.WithString("method", mcp.Description("HTTP method (default GET)")),
		mcp.WithString("body", mcp.Description("Request body for POST/PUT")),
		mcp.WithNumber("timeout_sec", mcp.Description("Request timeout seconds (default 15)"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("follow_redirect", mcp.Description("Follow redirects (default false)"), mcp.DefaultBool(false)),
		mcp.WithBoolean("insecure", mcp.Description("Skip TLS verification (local dev only)"), mcp.DefaultBool(false)),
		mcp.WithBoolean("allow_private", mcp.Description("Permit private/LAN (RFC1918) targets; loopback is always allowed, cloud-metadata/link-local always blocked (default false)"), mcp.DefaultBool(false)),
		mcp.WithBoolean("extract_text", mcp.Description("Return readable text extracted from HTML"), mcp.DefaultBool(false)),
		mcp.WithNumber("expect_status", mcp.Description("Assert HTTP status code"), mcp.DefaultNumber(0)),
		mcp.WithArray("expect_contains", mcp.Description("Assert the body contains each of these strings")),
		mcp.WithArray("expect_absent", mcp.Description("Assert the body contains none of these strings")),
		mcp.WithString("expect_regex", mcp.Description("Assert the body matches this regex")),
		mcp.WithString("expect_json_path", mcp.Description("Dotted JSON path to assert, e.g. data.items.0.id")),
		mcp.WithString("expect_json_value", mcp.Description("Expected value at expect_json_path")),
		mcp.WithNumber("max_latency_ms", mcp.Description("Assert response latency is at or below this (ms)"), mcp.DefaultNumber(0)),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotVerify(),
	), timedTool("web", webHandler()))

	s.AddTool(mcp.NewTool("browser",
		mcp.WithDescription("Render a URL in headless Chromium and SEE it: returns a WebP screenshot the model can view, plus console output, uncaught JS errors, failed requests, optional performance metrics, and page metadata. Use this (not `web`, which is HTTP-only) for the VISUAL result or client-side JS behavior — verifying a local dev UI (http://localhost:3000) after a change. Write & run a UI test: outline=true lists the interactive elements with ready-to-use selectors, then `actions` clicks/fills/asserts through the flow (pass/fail reported). WordPress admin: recipe=wp_login|wp_admin|wp_plugins|wp_posts|wp_new_post + site=<connections website> fills login from encrypted/env secrets (never logged) and waits for #wpadminbar (plugins/posts navigate after). Session reuse: session=<name> keeps cookies across browser calls in this MCP process. Responsive check: device=mobile|tablet|desktop, or devices=[\"all\"] to capture every viewport in one call. Performance check: metrics=true (FCP, load, request count, page weight). Watch it happen: headed=true opens a visible browser that highlights each click/input. Lean by default — the opt-in outline is bounded, not a full-DOM dump. Loopback always allowed; set allow_private for LAN. Needs the managed browser: if missing, run `ch browser install` once. Binary must be built with -tags rod (default `codehelper update` / install.sh)."),
		mcp.WithString("url", mcp.Description("URL to open (e.g. http://localhost:3000). Optional when site= is set — then the site login/admin URL is used.")),
		mcp.WithString("recipe", mcp.Description("Named interaction recipe prepended before actions: wp_login | wp_admin | wp_plugins | wp_posts | wp_new_post | laravel_login | django_admin | drupal_login | magento_login | spa_hydrate. Requires site=. When omitted with site=, uses site kind / project browser_recipe default.")),
		mcp.WithString("site", mcp.Description("Connections website profile name (codehelper connections add-site). Supplies base URL + user; password from env:/secret store only — never pass passwords in MCP args.")),
		mcp.WithString("repo", mcp.Description("Repository name for site/secret resolution (optional; defaults to current MCP workspace)")),
		mcp.WithString("session", mcp.Description("Named in-process cookie jar. Captures sharing the same session reuse auth cookies (e.g. wp_login then open plugins without re-login). Lives for the MCP server process lifetime.")),
		mcp.WithBoolean("session_clear", mcp.Description("Clear the named session cookie jar before this capture"), mcp.DefaultBool(false)),
		mcp.WithString("device", mcp.Description("Viewport preset: desktop (1280x800, default) | tablet (768x1024) | mobile (390x844). Sets size, pixel ratio, mobile emulation, and UA.")),
		mcp.WithArray("devices", mcp.Description("Capture several viewports in one call, e.g. [\"mobile\",\"desktop\"] or [\"all\"]. Overrides `device`. Returns one image per device.")),
		mcp.WithString("format", mcp.Description("Screenshot format: webp (default, smallest) | png | jpeg")),
		mcp.WithNumber("quality", mcp.Description("Compression quality 1-100 for webp/jpeg (default 80)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("width", mcp.Description("Override viewport width px (else from device)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("height", mcp.Description("Override viewport height px (else from device)"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("full_page", mcp.Description("Capture the full scrollable page, not just the viewport"), mcp.DefaultBool(false)),
		mcp.WithBoolean("split", mcp.Description("Capture the full page split into vertical pieces (~2000px each), returned as multiple images at full resolution — read a long page without the downscaling a single tall screenshot suffers"), mcp.DefaultBool(false)),
		mcp.WithNumber("segment_height", mcp.Description("Max height (CSS px) per piece when splitting a full-page capture (implies split+full_page)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("clip_y", mcp.Description("Capture only a region starting at this Y offset (CSS px); pair with clip_height"), mcp.DefaultNumber(0)),
		mcp.WithNumber("clip_height", mcp.Description("Height (CSS px) of the clipped region to capture at full width"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("metrics", mcp.Description("Collect performance metrics: FCP, DOMContentLoaded, load, request count, transfer KB, JS heap"), mcp.DefaultBool(false)),
		mcp.WithString("audit", mcp.Description("Accessibility + Core Web Vitals audit. 'lite' = fast built-in checks (missing alt/labels/accessible-names, page lang/title); 'full' = the axe-core engine (comprehensive, with impact levels — needs `ch browser install`). Both also report LCP/CLS/FCP/TTFB with good/poor verdicts.")),
		mcp.WithBoolean("outline", mcp.Description("Return a compact map of the page's INTERACTIVE elements (inputs, buttons, links, form controls) — each with a stable ref (e1,e2,…), ready-to-use CSS selector, role, accessible name, input type, placeholder and value. Use this FIRST to discover targets; drive them with selector=ref:e3 or ref=\"e3\". Bounded (≤100 elements), not a full-DOM dump."), mcp.DefaultBool(false)),
		mcp.WithBoolean("snapshot", mcp.Description("Return a bounded ARIA/role snapshot (Playwright-MCP style: role \"name\" lines, ≤80 nodes). Prefer over dumping HTML. Use with role/name/testid locators in actions."), mcp.DefaultBool(false)),
		mcp.WithBoolean("trace", mcp.Description("Include a compact timing trail (navigate/action/wait/heal/fail) for debugging flaky flows — not a CDP file."), mcp.DefaultBool(false)),
		mcp.WithBoolean("wait_hydrate", mcp.Description("After load, wait for network idle + DOM stable (SPA/React/Vue/Next and WP admin hydration). Pair with wait_selector for a ready landmark (#root, #wpadminbar, …)."), mcp.DefaultBool(false)),
		mcp.WithArray("actions", mcp.Description(`Interaction + test steps before the screenshot. Locators: selector CSS, testid:/role:button:Name/text:/name:/ref:e3 prefixes, or fields role/name/testid/ref. Actions: click|type|fill|select|hover|press|scroll|wait|wait_idle|wait_hydrate|navigate|wait_nav|assert|assert_text|upload|snapshot|storage_set|storage_get|storage_clear|clear_cookies. Example: [{"action":"click","selector":"ref:e3"},{"action":"assert_text","selector":".ok","text":"Thanks"}]. Stops at first failure; failure_pack + screenshot always attached. Tip: outline/snapshot first; session= for login cookies.`)),
		mcp.WithBoolean("headed", mcp.Description("Run a VISIBLE browser (default headless) so a human can WATCH the agent drive the page: each action flashes a labelled box on its target element and SlowMotion paces the clicks/inputs. Needs a graphical display (skip over SSH/CI — or use xvfb-run). Alias: gui=true. Env CODEHELPER_BROWSER_HEADED=1 or project browser_headed sets the default."), mcp.DefaultBool(false)),
		mcp.WithBoolean("gui", mcp.Description("Alias for headed=true (visible Chromium)."), mcp.DefaultBool(false)),
		mcp.WithNumber("slow_mo", mcp.Description("Headed only: delay in ms before each action so clicks/inputs are perceptible (default ~650ms). Ignored in headless."), mcp.DefaultNumber(0)),
		mcp.WithBoolean("pause_on_fail", mcp.Description("Headed only: keep the window open ~3s after a failed step so a human can see the failure. Env CODEHELPER_BROWSER_PAUSE_ON_FAIL=1."), mcp.DefaultBool(false)),
		mcp.WithNumber("pause_on_fail_ms", mcp.Description("Headed + pause_on_fail: override pause duration in ms (default 3000)."), mcp.DefaultNumber(0)),
		mcp.WithBoolean("preview_actions", mcp.Description("Return a viewport screenshot after each interaction step (before the final capture). Requires `ch config browser set --action-previews on` (disabled by default). Failed steps always attach a shot even when this is off."), mcp.DefaultBool(false)),
		mcp.WithString("baseline", mcp.Description("Visual regression: name a baseline. First call saves the screenshot; later calls return a diff image (changed pixels in red) + % changed. Per-device baselines.")),
		mcp.WithBoolean("update_baseline", mcp.Description("Overwrite the named baseline with the current screenshot instead of diffing"), mcp.DefaultBool(false)),
		mcp.WithString("selector", mcp.Description("Screenshot only this CSS-selected element")),
		mcp.WithString("wait_selector", mcp.Description("Wait for this CSS selector to appear before capturing (also used as hydrate landmark when wait_hydrate=true)")),
		mcp.WithNumber("wait_ms", mcp.Description("Extra fixed wait after load, in milliseconds (with wait_hydrate: overall hydrate timeout)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("timeout_sec", mcp.Description("Overall timeout seconds (default 30)"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("allow_private", mcp.Description("Permit private/LAN (RFC1918) targets; loopback always allowed, cloud-metadata/link-local always blocked (default false)"), mcp.DefaultBool(false)),
		mcp.WithString("debug_pack_dir", mcp.Description("On action/assert failure, write a debug pack (failure screenshot + report.json with console errors, failed network, outline/snapshot, URL, action log) to this directory. Default: ~/.codehelper/browser/debug-packs/<timestamp>/.")),
		mcp.WithString("upload_allow", mcp.Description("Extra upload sandbox roots (os path-list separator). Upload paths must live under the workspace repo root and/or these dirs (also CODEHELPER_BROWSER_UPLOAD_ALLOW). Multi-file: text= path1||path2 or newlines.")),
		annotVerify(),
	), timedTool("browser", browserHandler(regRef)))

	s.AddTool(mcp.NewTool("web_search",
		mcp.WithDescription("Search the web for current information and return a compact ranked list of {title, url, snippet} to then fetch/verify with the `web` or `browser` tools. Use for finding docs, error messages, library/version facts, or current events — anything outside the indexed repo. Provider is configured via `ch config search` (Tavily/Brave free keys, or keyless DuckDuckGo fallback). Does not crawl; it finds the URLs to look at."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
		mcp.WithNumber("count", mcp.Description("Max results (default 5)"), mcp.DefaultNumber(0)),
		mcp.WithString("provider", mcp.Description("Override provider for this call: tavily | brave | duckduckgo")),
		annotReadOnlyOpenWorld(),
	), timedTool("web_search", webSearchHandler()))

	s.AddTool(mcp.NewTool("usage_report",
		mcp.WithDescription("Per-project tool-usage + token report. Layers: (1) codehelper OUTPUT — how much context each tool injected, by tool/session/client (claude-code/cursor/codex) — measurable for EVERY client; (2) Claude + Codex MODEL TOKENS — real billed tokens per session, parsed from Claude Code transcripts (~/.claude) and Codex rollouts (~/.codex); Cursor doesn't expose these locally. Also surfaces the last change-verification (verify/diagnostics) outcome and a recent-call trail. Read-only; never indexes."),
		mcp.WithNumber("refs", mcp.Description("How many recent tool calls to include in the trail (default 20; 0 disables)"), mcp.DefaultNumber(20)),
		mcp.WithBoolean("verbose", mcp.Description("Expand the recent-call trail to show each call's input + output preview (to review tool quality)"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithString("format", mcp.Description("Response text encoding: text (default, human-readable) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("usage_report", usageReportHandler(regRef)))

	s.AddPrompt(mcp.NewPrompt("detect_impact",
		mcp.WithPromptDescription("Pre-change impact checklist"),
	), detectImpactPrompt())

	s.AddPrompt(mcp.NewPrompt("generate_map",
		mcp.WithPromptDescription("Architecture map from graph"),
	), generateMapPrompt())

	s.AddPrompt(mcp.NewPrompt("intake_project_brief",
		mcp.WithPromptDescription("Structured intake: ask only architecture-shifting questions, document assumptions"),
	), staticPrompt("Intake", prompts.IntakeProjectBrief))

	s.AddPrompt(mcp.NewPrompt("planning_contract",
		mcp.WithPromptDescription("Output contract before edits: files, tests, verify commands, rollback"),
	), staticPrompt("Planning", prompts.PlanningContract))

	s.AddPrompt(mcp.NewPrompt("agent_guardrails",
		mcp.WithPromptDescription("Always-on rails: graph tools first, argv-mode verify, no debug prints"),
	), staticPrompt("Guardrails", prompts.AgentGuardrails))

	// Main / high-frequency tools register first so they sort to the top of tools/list.
	RegisterKickoffTools(s, regRef)
	RegisterScopeTools(s, regRef)
	RegisterPlanTools(s, regRef)
	RegisterReviewTools(s, regRef)
	RegisterWorkspaceTools(s, regRef)
	RegisterSymbolEditTools(s, regRef)
	RegisterAgentSupportTools(s, regRef)
	RegisterAgentPlanTools(s, regRef)
	RegisterGlossaryTools(s, regRef)
	RegisterHintsTools(s, regRef)
	RegisterCompositeTools(s, regRef)
	RegisterRetrievalFacadeTools(s, regRef)
	RegisterOpsTools(s, regRef)
	RegisterOrchestrationTools(s, regRef)
}

// projectContextWantsTools is true when sections includes "tools".
func projectContextWantsTools(args map[string]any) bool {
	if sel := parseSections(argString(args, "sections")); sel != nil {
		return sel["tools"]
	}
	return false
}

// projectContextToolsOnly returns only the MCP catalog slice (sections=tools).
func projectContextToolsOnly(args map[string]any) bool {
	if sel := parseSections(argString(args, "sections")); sel != nil {
		return sel["tools"] && len(sel) == 1
	}
	return false
}

func toolsOnlyProjectContext(out projectContextMCPResponse) projectContextMCPResponse {
	return projectContextMCPResponse{
		Repo:               out.Repo,
		MCPToolCount:       out.MCPToolCount,
		MCPMainTools:       out.MCPMainTools,
		MCPParamKeys:       out.MCPParamKeys,
		ToolContractPath:   out.ToolContractPath,
		ToolsReferencePath: out.ToolsReferencePath,
		MCPToolsByGroup:    out.MCPToolsByGroup,
		CLIONlyTools:       out.CLIONlyTools,
		WorkflowRecipes:    out.WorkflowRecipes,
		NextStep:           out.NextStep,
	}
}

func existingAgentInstructionFiles(root string) []string {
	candidates := []string{
		MCPToolContractPath,
		".cursor/rules/codehelper.mdc",
		"CLAUDE.md",
		MCPToolsReferencePath,
	}
	out := make([]string, 0, len(candidates))
	for _, rel := range candidates {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

func keyEntrypointsUnderRoot(root string) []string {
	candidates := []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "composer.json", "pom.xml", "build.gradle", "README.md", "AGENTS.md"}
	out := make([]string, 0, 8)
	for _, rel := range candidates {
		if len(out) >= 8 {
			break
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

func topLevelLayoutUnderRoot(root string) ([]string, []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil
	}
	dirs := make([]string, 0, 16)
	files := make([]string, 0, 16)
	for _, ent := range entries {
		name := strings.TrimSpace(ent.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if ent.IsDir() {
			dirs = append(dirs, filepath.ToSlash(name))
			continue
		}
		files = append(files, filepath.ToSlash(name))
	}
	sort.Strings(dirs)
	sort.Strings(files)
	if len(dirs) > 20 {
		dirs = dirs[:20]
	}
	if len(files) > 20 {
		files = files[:20]
	}
	return dirs, files
}

func likelyEntrypointFilesUnderRoot(root string) []string {
	candidates := []string{
		"cmd/codehelper/main.go",
		"cmd/codehelper-mcp/main.go",
		"cmd/main.go",
		"main.go",
		"vscode-extension/src/extension.ts",
		"vscode-extension/src/mainPanelProvider.ts",
		"internal/mcpsvc/register.go",
	}
	out := make([]string, 0, len(candidates))
	for _, rel := range candidates {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

func uniqueTrimmedStrings(in ...[]string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 16)
	for _, arr := range in {
		for _, s := range arr {
			t := strings.TrimSpace(s)
			if t == "" {
				continue
			}
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// clientRulesWritten dedupes the bootstrap rule-write to once per project per
// server process (project_context can be called more than once per session).
var clientRulesWritten sync.Map

func ensureClientRulesOnce(root string) {
	if root == "" {
		return
	}
	if _, loaded := clientRulesWritten.LoadOrStore(root, true); loaded {
		return
	}
	if err := setup.WriteClientRules(root); err != nil {
		slog.Debug("client rules write", "root", root, "err", err)
	}
	hints.EnsureBuiltin()
}

func projectContextHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepo(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// Self-heal on bootstrap: ensure this project's per-client tool-first rules
		// exist (idempotent, cheap). project_context is the once-per-session entry
		// point, so this guarantees a freshly-restarted client always has the rules
		// — even if codehelper was updated via a path that didn't run `repair`.
		ensureClientRulesOnce(repo.RootPath)
		initOK, _ := registry.InitStatus(repo.RootPath)
		fresh := freshness.Inspect(repo.RootPath)
		currentName, reason, selOK := currentWorkspaceRepoName(ctx, reg)
		selectionReason := ""
		if selOK && currentName == repo.Name {
			selectionReason = reason
		}
		idx := "unknown"
		if _, merr := meta.Read(repo.RootPath); merr == nil {
			idx = "indexed"
			if fresh.Stale {
				idx = "stale"
			}
		} else {
			idx = "missing"
		}
		pt := pickProjectType(repo.RootPath, "")
		if pt == "" {
			pt = "unknown"
		}
		topDirs, topFiles := topLevelLayoutUnderRoot(repo.RootPath)
		likelyEntrypoints := likelyEntrypointFilesUnderRoot(repo.RootPath)
		langs := []string{}
		verifyCmds := []string{}
		projVersion := ""
		var versions map[string]string
		primaryLang := ""
		framework := ""
		var langStats []profile.LanguageStat
		var deps []profile.Dependency
		var subProjects []profile.SubProject
		var gotchas []string
		var learnedHints []string
		if pr, perr := profile.ReadOrGenerate(repo.RootPath); perr == nil && pr != nil {
			langs = uniqueTrimmedStrings(pr.Languages)
			verifyCmds = uniqueTrimmedStrings(pr.LintCommands, pr.TestCommands)
			likelyEntrypoints = uniqueTrimmedStrings(likelyEntrypoints, pr.Entrypoints)
			projVersion = pr.Version
			versions = pr.Versions
			primaryLang = pr.PrimaryLanguage
			framework = pr.Framework
			langStats = pr.LanguageStats
			deps = pr.Dependencies
			subProjects = pr.SubProjects
			gotchas = pr.Gotchas
			// Learned, cross-project hints matching this stack — applied LIVE (no
			// reindex needed) so a hint added via the `hints` tool shows up
			// immediately on every matching project.
			depNames := make([]string, 0, len(pr.Dependencies))
			for _, d := range pr.Dependencies {
				depNames = append(depNames, d.Name)
			}
			learnedHints, _ = hints.MatchingFor(pr.Framework, pr.ProjectType, pr.Languages, depNames)
		}
		frameworks, keyDeps, summary := projectBrief(repo.RootPath)
		out := projectContextMCPResponse{
			CodehelperVersion:       version.Current(),
			Repo:                    repo.Name,
			RepoRoot:                filepath.ToSlash(repo.RootPath),
			Initialized:             initOK,
			SelectionReason:         selectionReason,
			ProjectType:             pt,
			Framework:               framework,
			ProjectVersion:          projVersion,
			Versions:                versions,
			Dependencies:            deps,
			SubProjects:             subProjects,
			Gotchas:                 gotchas,
			LearnedHints:            learnedHints,
			Freshness:               fresh,
			IndexStatus:             idx,
			KeyEntrypoints:          keyEntrypointsUnderRoot(repo.RootPath),
			TopLevelDirectories:     topDirs,
			TopLevelFiles:           topFiles,
			LikelyEntrypointFiles:   likelyEntrypoints,
			PrimaryLanguage:         primaryLang,
			Languages:               langs,
			LanguageStats:           langStats,
			Frameworks:              frameworks,
			KeyDependencies:         keyDeps,
			Summary:                 summary,
			OS:                      hostOS(),
			Git:                     gitInfo(repo.RootPath),
			Surfaces:                projectSurfaces(repo.RootPath, langs, frameworks),
			Scripts:                 projectScripts(repo.RootPath),
			SuggestedVerifyCommands: verifyCmds,
			RecommendedNextTools:    []string{"kickoff", "investigate", "query", "scout", "context", "hotspots", "dead_code", "review"},
			WorkflowRecipes:         FeatureLifecycleRecipes(),
			VerifyFinishGate:        VerifyFinishGateText,
			NextStep:                "project_context is a one-time BOOTSTRAP — it does not search the code. Read setup_suggestions and PROPOSE incomplete steps to the user before the first browser run. Pick a workflow_recipes entry (add_feature / remove_feature / locate_symbol / vibe_fix / vibe_ui / programmer_ui / browser_qa / review_changes / security_review / dead_code / performance / architecture_qa), or call `kickoff` (role=architect for design Q&A; default feature/fix) / `investigate` (recipe=architecture|dead_code|security|perf) / `query`→`context`/`change_kit`. UI work: propose setup → implement→browser assert→debug→retest. Obey verify_finish_gate before claiming done. Do NOT stop here or fall back to blindly reading files.",
			Confidence:              0.95,
		}
		if fresh.Stale && fresh.StaleReason != "" {
			out.Warnings = append(out.Warnings, "index stale: "+fresh.StaleReason)
		}
		if idx == "missing" || !initOK {
			out.Warnings = append(out.Warnings, "project is not initialized; run `codehelper init` in the project root")
		}
		if m, merr := meta.Read(repo.RootPath); merr == nil && m != nil && (m.FileCount > 0 || m.SymbolCount > 0) {
			out.IndexStats = &indexStatsBrief{
				Files:   m.FileCount,
				Symbols: m.SymbolCount,
				Edges:   m.EdgeCount,
			}
			out.Warnings = append(out.Warnings, indexGraphQualityWarnings(m.SymbolCount, m.EdgeCount)...)
		}
		out.Connections = connectionsBriefFor(repo.RootPath)
		out.SetupSuggestions = buildSetupSuggestions(repo.RootPath, pt, framework, frameworks)
		out.AgentInstructionFiles = existingAgentInstructionFiles(repo.RootPath)
		if cfg, cerr := projcfg.Load(repo.RootPath); cerr == nil {
			enabled := cfg.ToolsEnabled
			out.MCPToolsEnabled = &enabled
		}
		// In minimal-tools mode tools/list only advertises the main tools, so the
		// bootstrap must carry the full grouped catalog (regardless of verbosity)
		// to keep the hidden-but-callable specialists discoverable.
		minimal := minimalModeActive(ctx, reg)
		includeToolGroups := projectContextDetailed(args) || projectContextWantsTools(args)
		applyMCPCatalogFields(&out, includeToolGroups)
		// "What's linked": every session's short bootstrap gets a top-3 package
		// architecture teaser so the agent orients without a follow-up call;
		// detailed adds the full symbol + package hubs.
		if idx == "indexed" {
			detailed := projectContextDetailed(args)
			if hd, herr := hubs.Read(repo.RootPath); herr == nil {
				// Precomputed at index time — instant, no per-request graph scan.
				out.Architecture = topPackageDirs(filterNoisePackageHubs(hd.PackageHubs), 3)
				if detailed {
					out.Hubs = formatHubs(hd.SymbolHubs)
					out.PackageHubs = formatPackageHubs(hd.PackageHubs)
				}
			} else if detailed {
				// Fallback for indexes built before hubs.json existed.
				if st, gerr := openGraph(repo.RootPath); gerr == nil {
					if h, e := st.TopHubs(ctx, repo.Name, 8); e == nil {
						out.Hubs = formatHubs(h)
					}
					if pkgs, e := st.TopPackages(ctx, repo.Name, 6); e == nil {
						out.PackageHubs = formatPackageHubs(pkgs)
						out.Architecture = topPackageDirs(filterNoisePackageHubs(pkgs), 3)
					}
					st.Close()
				}
			}
		}
		if projectContextToolsOnly(args) {
			out = toolsOnlyProjectContext(out)
		} else if !projectContextDetailed(args) {
			out = compactProjectContext(out)
		}
		if minimal {
			out.MCPToolsMode = "minimal"
			full := MCPToolCatalogFull()
			out.MCPToolsByGroup = full.ByGroup
			out.CLIONlyTools = full.CLIONly
			out.Warnings = append(out.Warnings, "minimal-tools mode: tools/list advertises the focused lifecycle set ("+strings.Join(MinimalToolSet, ", ")+"); every tool in mcp_tools_by_group is still callable by name. Disable with `codehelper config project --minimal off` or unset CODEHELPER_MINIMAL_TOOLS.")
		}
		// Note: reaching here means resolveRepo already matched this repo to the
		// workspace (by client roots OR spawn CWD) and passed the scope assertion,
		// so we do NOT warn merely because the client didn't advertise MCP roots —
		// that fired spuriously for every roots-less client and falsely told the
		// agent the project wasn't indexed.
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// buildSetupSuggestions returns LLM-facing setup steps for the detected stack.
func buildSetupSuggestions(repoRoot, projectType, framework string, frameworks []string) *setupsuggest.Report {
	conn, _ := connections.Load(repoRoot)
	pcfg, _ := projcfg.Load(repoRoot)
	rep := setupsuggest.Build(setupsuggest.Input{
		RepoRoot:    repoRoot,
		ProjectType: projectType,
		Framework:   framework,
		Frameworks:  frameworks,
		Connections: conn,
		Projcfg:     pcfg,
	})
	return &rep
}

// formatHubs renders call-graph hubs as compact "name loc ×callers" strings — a
// clickable, token-lean "what's linked" list for the detailed bootstrap.
func formatHubs(hubs []graph.Hub) []string {
	out := make([]string, 0, len(hubs))
	for _, h := range filterNoiseHubs(hubs) {
		out = append(out, fmt.Sprintf("%s %s:%d ×%d", h.Name, h.Path, h.Line, h.Callers))
	}
	return out
}

// topPackageDirs returns the bare directory names of the top n package hubs — the
// compact architecture teaser carried in the short bootstrap (no counts).
func topPackageDirs(pkgs []graph.PackageHub, n int) []string {
	out := make([]string, 0, n)
	for _, p := range pkgs {
		if p.Dir == "" {
			continue
		}
		out = append(out, p.Dir)
		if len(out) >= n {
			break
		}
	}
	return out
}

// formatPackageHubs renders package-level hubs as "dir ×callers ←N pkgs" — the
// architectural "which modules the rest of the code depends on" view.
func formatPackageHubs(pkgs []graph.PackageHub) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range filterNoisePackageHubs(pkgs) {
		out = append(out, fmt.Sprintf("%s ×%d ←%d pkgs", p.Dir, p.Callers, p.FromPkgs))
	}
	return out
}

func queryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		q := argQuery(args)
		if q == "" {
			return mcp.NewToolResultError("query must not be empty — pass a symbol name, a concept, or a natural-language task (e.g. \"parse a git diff\", \"reciprocal rank fusion\"). Synonyms are expanded automatically, so describe the behavior you want."), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()
		intent := argString(args, "intent")
		baseRef := argString(args, "base_ref")
		if strings.TrimSpace(baseRef) == "" {
			baseRef = "HEAD~1"
		}
		packLimit := int(mcp.ParseInt64(req, "limit", 0))
		includePack := argBool(args, "include_context_pack", false) || packLimit > 0
		if includePack && packLimit <= 0 {
			packLimit = 24
		}
		budgetTokens := int(mcp.ParseInt64(req, "budget_tokens", 0))

		hitLimit := 40
		candidateLimit := 80
		if includePack {
			hitLimit = candidateLimit
			if packLimit >= 40 {
				candidateLimit = 120
				hitLimit = candidateLimit
			}
		}
		fresh := freshness.Inspect(repo.RootPath)
		// Hot cache: a repeated identical query within the TTL window skips the
		// expensive hybrid retrieval. Keyed on index identity so any reindex
		// invalidates it; freshness above is recomputed every call regardless.
		cacheKey := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%d", repo.Name, q, intent, hitLimit, fresh.IndexedCommit, fresh.IndexedAt.Unix())
		hits, cached := globalQueryHitsCache.get(cacheKey)
		if !cached {
			diffSet, _ := detect.ChangedSymbolSet(ctx, repo.RootPath, repo.Name, baseRef, st)
			retrieval.EnsureEmbedder()
			h, err := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, q, hitLimit, retrieval.MCPQueryOptionsWithProfile(
				repo.RootPath, intent, strings.Fields(strings.ToLower(q)), diffSet,
			))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			hits = h
			globalQueryHitsCache.put(cacheKey, hits)
		}
		// The OPT-IN multilingual/semantic rerank already ran inside
		// QueryHybridWithOptions (full-precision cosine over doc-enriched symbol text,
		// min-max normalized). It is NOT re-applied here: a second pass over the same
		// CODEHELPER_EMBED_URL would make a redundant embed round-trip AND re-rank with
		// lower-precision binary-quantized vectors over bare identifiers, undoing the
		// enriched ranking. One rerank, in one place, is the contract.
		if hits == nil {
			hits = []retrieval.RankedSymbol{}
		}
		var demoted int
		hits, demoted = demoteFixtureHits(hits)
		// Surface only the top_k hits (default 10), compact by default. Retrieval
		// kept a larger candidate pool above for context-pack ranking, but dumping
		// all of it wastes tokens and dilutes the agent's focus (context rot).
		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = 10
		}
		detailed := strings.EqualFold(argString(args, "verbosity"), "detailed")
		surfaced, truncated := capHits(hits, topK)
		retrievalNote := stdRetrievalNote
		if note := fixtureCollisionNote(demoted); note != "" {
			retrievalNote = note + " " + retrievalNote
		}
		var fileSnippets []retrieval.FileSnippet
		if len(surfaced) < 3 {
			snips, serr := retrieval.SearchFileSnippets(ctx, st, repo.RootPath, repo.Name, strings.Fields(strings.ToLower(q)), 6)
			if serr == nil && len(snips) > 0 {
				fileSnippets = snips
				retrievalNote += " Few symbol hits — file_snippets lists matching config/doc lines from indexed paths."
			}
		}
		var diskMatches []retrieval.DiskMatch
		if len(surfaced) == 0 {
			// Zero hits is the agent's cue to adapt, not stop. Errors/empties are
			// feedback: name the concrete next moves.
			retrievalNote = "No indexed symbols matched. Next: rephrase with the behavior you want (synonyms are auto-expanded), drop to a single distinctive term, try `ast_query` for a structural match, or `read_workspace_file` if you already know the path. If you expect a match, the index may be stale or this code may be unindexed — run `codehelper analyze`."
			// Disk fallback: if the query's most distinctive token is a bare
			// identifier, scan disk directly — it catches symbols in new/uncommitted
			// files the index can't see yet (the gap a live grep would win on).
			if term := distinctiveIdentifier(q); term != "" {
				if dm := retrieval.DiskGrepIdentifier(repo.RootPath, term, 5); len(dm) > 0 {
					diskMatches = dm
					retrievalNote += fmt.Sprintf(" But %q WAS found on disk (see disk_matches — likely unindexed/new files): open with read_workspace_file or run `codehelper analyze`.", term)
				}
			}
		}
		// The standard retrieval-guidance note is identical on every successful
		// query, so emit it once per session (progressive disclosure) and drop the
		// repeat on later calls — situational notes (zero hits, disk match) differ
		// from the constant and are always kept.
		if retrievalNote == stdRetrievalNote &&
			!usageRecorder.MarkOnce(sessionIDFromContext(ctx), "retrieval_note") {
			retrievalNote = ""
		}
		out := queryToolResponse{
			Hits:                hitsView(surfaced, detailed),
			HitsTruncated:       truncated,
			Freshness:           fresh,
			Intent:              intent,
			CrossRepoCandidates: resolveCrossRepoCandidates(reg, q),
			RetrievalNote:       retrievalNote,
			SemanticRerank:      semanticRerankStatus(surfaced),
			FileSnippets:        fileSnippets,
			DiskMatches:         diskMatches,
		}
		if includePack {
			pack, perr := retrieval.BuildContextPack(ctx, st, repo.Name, q, intent, hits, packLimit)
			if perr == nil {
				out.ContextPack = pack
			}
			meta := contextPackRetrievalMeta{
				RankedSymbolHits: len(hits),
				ContextPackLimit: packLimit,
				CandidatePool:    candidateLimit,
				Note:             "Pack items come from the symbol index (BM25 + trigrams), not the open web. For prose manifests use read_workspace_file.",
			}
			out.RetrievalMeta = &meta
			if budgetTokens > 0 {
				budgeted, berr := retrieval.BuildContextPackV2(ctx, st, repo.Name, repo.RootPath, q, intent, hits, budgetTokens)
				if berr == nil {
					out.Budgeted = budgeted
				}
			}
		}
		if fresh.Stale {
			out.Warning = "index may be stale: " + fresh.StaleReason
		}
		enrichQueryToolResponse(&out, surfaced)
		if includePack && out.ContextPack != nil && len(out.ContextPack.ContextPack) > 0 {
			out.Confidence = 0.88
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func marshalQueryToolResponse(out queryToolResponse) ([]byte, error) {
	if out.Hits == nil {
		out.Hits = []retrieval.RankedSymbol{}
	}
	return json.MarshalIndent(out, "", "  ")
}

// compactSym / calleeRef are token-lean projections of a context bundle.
type compactSym struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	Loc  string `json:"loc"`
	Recv string `json:"recv,omitempty"`
}

type calleeRef struct {
	Name     string  `json:"name"`
	Loc      string  `json:"loc,omitempty"`      // path:line when resolved to a project symbol
	External bool    `json:"external,omitempty"` // unresolved symref → stdlib/3rd-party call
	Conf     float64 `json:"conf,omitempty"`
}

// blastRadius is the compact "what depends on this" summary folded into context
// so a feature can be understood AND its impact assessed in a single call.
type blastRadius struct {
	RiskTier   string   `json:"risk_tier"`
	Dependents int      `json:"dependents"`
	Top        []string `json:"top,omitempty"`
}

type compactContext struct {
	Symbol     compactSym   `json:"symbol"`
	Signature  string       `json:"signature,omitempty"`
	Source     string       `json:"source,omitempty"`
	SourceNote string       `json:"source_note,omitempty"`
	Callers    []compactSym `json:"callers"`
	Callees    []calleeRef  `json:"callees"`
	Imports    []string     `json:"imports,omitempty"`
	Truncated  string       `json:"truncated,omitempty"`
}

// contextView renders a context bundle compactly by default — dropping the full
// edge IDs (which redundantly concatenate the source+target symbol IDs), repo_id,
// and the constant source_id — or returns the raw bundle when detailed.
func contextView(bun *retrieval.ContextBundle, detailed bool, root string, bodyMode string) any {
	if bun == nil || detailed {
		return bun
	}
	bodyMode = strings.ToLower(strings.TrimSpace(bodyMode))
	skipSource := bodyMode == "none"
	fullBody := bodyMode == "full"
	briefBody := bodyMode == "brief"
	cv := compactContext{
		Symbol: compactSym{
			Name: bun.Symbol.Name, Kind: string(bun.Symbol.Kind),
			Loc:  fmt.Sprintf("%s:%d", bun.Symbol.Path, bun.Symbol.LineStart),
			Recv: bun.Symbol.ParentID,
		},
		Signature: strings.TrimSpace(bun.Symbol.Signature),
	}
	// Attach the definition's own source (truncated) so `context` is a one-call
	// feature report — the code, what it does, and who references it — instead of
	// forcing a follow-up read_workspace_file.
	if root != "" && !skipSource {
		total := bun.Symbol.LineEnd - bun.Symbol.LineStart + 1
		// Default caps the body at 40 lines to stay token-lean, BUT auto-includes
		// the full body for small symbols (≤80 lines) so a typical handler returns
		// in ONE call instead of forcing a follow-up read. body=full lifts the cap
		// entirely (bounded at 400 lines for safety on huge functions).
		const (
			defaultMaxDefLines = 40
			briefMaxDefLines   = 8
			autoFullThreshold  = 80
			fullBodyHardCap    = 400
		)
		maxDefLines := defaultMaxDefLines
		switch {
		case briefBody:
			maxDefLines = briefMaxDefLines
		case fullBody:
			maxDefLines = total
			if maxDefLines > fullBodyHardCap {
				maxDefLines = fullBodyHardCap
			}
		case total <= autoFullThreshold:
			maxDefLines = total
		}
		if src := readSymbolDefinition(root, *bun.Symbol, maxDefLines); src != "" {
			cv.Source = src
			shown := strings.Count(src, "\n") + 1
			if total > shown {
				cv.SourceNote = fmt.Sprintf("showing first %d of %d lines — pass body=full, or read_workspace_file %s offset=%d to see the rest", shown, total, bun.Symbol.Path, bun.Symbol.LineStart+shown)
			}
		}
	}
	// Token budget: a hub symbol can have hundreds of callers/callees, blowing the
	// response to tens of thousands of tokens. Cap each list in the default
	// (compact) view and report what was withheld; verbosity=detailed returns all.
	const maxList = 12
	for i, c := range bun.Callers {
		if i >= maxList {
			break
		}
		cv.Callers = append(cv.Callers, compactSym{
			Name: c.Name, Kind: string(c.Kind),
			Loc: fmt.Sprintf("%s:%d", c.Path, c.LineStart), Recv: c.ParentID,
		})
	}
	for i, e := range bun.Callees {
		if i >= maxList {
			break
		}
		cv.Callees = append(cv.Callees, calleeRef{}.fill(e.TargetID, e.Confidence))
	}
	for _, im := range bun.Imports {
		if len(cv.Imports) >= maxList {
			break
		}
		if p := modIDPath(im.TargetID); p != "" {
			cv.Imports = append(cv.Imports, p)
		}
	}
	var notes []string
	// CallersTotal is the exact count even when Callers was capped at load time.
	callerTotal := bun.CallersTotal
	if callerTotal < len(bun.Callers) {
		callerTotal = len(bun.Callers)
	}
	if callerTotal > maxList {
		notes = append(notes, fmt.Sprintf("%d callers (showing %d)", callerTotal, maxList))
	}
	if len(bun.Callees) > maxList {
		notes = append(notes, fmt.Sprintf("%d callees (showing %d)", len(bun.Callees), maxList))
	}
	if len(notes) > 0 {
		cv.Truncated = strings.Join(notes, ", ") + " — pass verbosity=detailed for the full graph, or use impact/trace."
	}
	return cv
}

// fill resolves a callee edge's target into a compact callee reference: a project
// symbol becomes name+loc; an unresolved symref becomes name+external.
func (calleeRef) fill(targetID string, conf float64) calleeRef {
	cr := calleeRef{Conf: round3(conf)}
	switch {
	case strings.HasPrefix(targetID, "sym:"):
		cr.Name = symIDName(targetID)
		cr.Loc = symIDLoc(targetID)
	case strings.HasPrefix(targetID, "symref:"):
		cr.Name = lastColonSeg(targetID)
		cr.External = true
	default:
		cr.Name = targetID
	}
	return cr
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// symIDName/symIDLoc parse a `sym:repo:path:line:name` id.
func symIDName(id string) string { return lastColonSeg(id) }

func symIDLoc(id string) string {
	parts := strings.Split(id, ":")
	if len(parts) < 5 {
		return ""
	}
	path := strings.Join(parts[2:len(parts)-2], ":")
	return path + ":" + parts[len(parts)-2]
}

func lastColonSeg(s string) string {
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// modIDPath extracts the module path from a `mod:repo:modulepath` id.
func modIDPath(id string) string {
	if !strings.HasPrefix(id, "mod:") {
		return ""
	}
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// capHits returns the first k hits and the count dropped (for the truncation
// marker). k<=0 or fewer hits than k returns everything.
func capHits(hits []retrieval.RankedSymbol, k int) ([]retrieval.RankedSymbol, int) {
	if k <= 0 || len(hits) <= k {
		return hits, 0
	}
	return hits[:k], len(hits) - k
}

// hitsView renders hits compactly (default) or as full Symbol records (detailed).
// maxConciseReasons caps the per-hit ranking-signal list in concise mode: the
// long tail is retrieval-internal noise that ships on every hit and scales with
// top_k. Detailed mode keeps all of them.
const maxConciseReasons = 3

// ubiquitousReasons fire on nearly every lexical hit, so they carry almost no
// signal for choosing between hits in a ranked list. They are dropped first when
// trimming, so the kept slots go to discriminating signals (exact_name,
// name_prefix, name_field, centrality, semantic, diff, …) instead.
var ubiquitousReasons = map[string]bool{"bm25": true, "trigram": true, "primary_lang": true}

func capReasons(r []string) []string {
	if len(r) <= maxConciseReasons {
		return r
	}
	// Keep discriminating signals (in their original strongest-first order), then
	// backfill remaining slots with the ubiquitous ones only if room is left.
	out := make([]string, 0, maxConciseReasons)
	for _, x := range r {
		if !ubiquitousReasons[x] && len(out) < maxConciseReasons {
			out = append(out, x)
		}
	}
	for _, x := range r {
		if len(out) >= maxConciseReasons {
			break
		}
		if ubiquitousReasons[x] {
			out = append(out, x)
		}
	}
	return out
}

func hitsView(hits []retrieval.RankedSymbol, detailed bool) any {
	if detailed {
		return hits
	}
	out := make([]compactHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, compactHit{
			ID:      h.Symbol.ID,
			Name:    h.Symbol.Name,
			Kind:    string(h.Symbol.Kind),
			Loc:     fmt.Sprintf("%s:%d", h.Symbol.Path, h.Symbol.LineStart),
			Recv:    h.Symbol.ParentID,
			Score:   math.Round(h.Score*1000) / 1000,
			Reasons: capReasons(h.Reasons),
		})
	}
	return out
}

func contextHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		name := argFirst(args, "name", "symbol", "sym", "target")
		if name == "" {
			return mcp.NewToolResultError("name is required — pass `name` (not `symbol`) with a symbol name or sym: id from query/scout"), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()
		wantPath := strings.TrimSpace(argString(args, "path"))
		wantLine := int(argFloat(args, "line", 0))
		sym, cands, err := resolveSymbolByName(ctx, st, repo.Name, name, wantPath, wantLine)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				if dm := retrieval.DiskGrepIdentifier(repo.RootPath, name, 5); len(dm) > 0 {
					return mustToolResultFormatted(map[string]any{
						"found":        false,
						"name":         name,
						"disk_matches": dm,
						"freshness":    freshness.Inspect(repo.RootPath),
						"note":         fmt.Sprintf("%q is not in the symbol index but EXISTS on disk (likely a new/uncommitted file the index hasn't caught up with). Open it with `read_workspace_file path=%s`, or run `codehelper analyze` to index it. Disk matches are listed in disk_matches (definitions first).", name, dm[0].Path),
					}, resolveFormat(args))
				}
				return mcp.NewToolResultError(fmt.Sprintf("no indexed symbol named %q (and no on-disk match found). Next: call `query` with %q to find the correct name/sym: id, or `ast_query` for a structural match. If you expect it to exist, the index may be stale — run `codehelper analyze`.", name, name)), nil
			}
			return mcp.NewToolResultError(err.Error()), nil
		}
		if sym == nil {
			return mustToolResultFormatted(map[string]any{
				"ambiguous":  true,
				"name":       name,
				"candidates": cands,
				"note":       "multiple symbols share this name — pass path= (and optional line=) or use the sym: id from query hits",
			}, resolveFormat(args))
		}
		detailed := strings.EqualFold(argString(args, "verbosity"), "detailed")
		// Compact view shows at most maxList callers; cap the load a bit above that
		// so a hub symbol (Django's assertEqual has ~9.6k callers) doesn't materialize
		// them all — CallersTotal still reports the true count. Detailed = unbounded.
		callerCap := 0
		if !detailed {
			callerCap = 24
		}
		bun, err := retrieval.BuildContextLimited(ctx, st, repo.Name, sym.ID, callerCap)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fresh := freshness.Inspect(repo.RootPath)
		bodyMode := strings.ToLower(strings.TrimSpace(argString(args, "body")))
		out := map[string]any{
			"bundle":    contextView(bun, detailed, repo.RootPath, bodyMode),
			"freshness": fresh,
		}
		// Fold in the blast radius so `context` also answers "what does changing this
		// affect?" — no separate `impact` call for the common case.
		if bun != nil && bun.Symbol != nil {
			if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, bun.Symbol.ID, 3, "upstream"); aerr == nil && res != nil && len(res.Nodes) > 1 {
				br := blastRadius{RiskTier: res.RiskTier, Dependents: len(res.Nodes) - 1}
				for _, n := range res.Nodes {
					if n.Depth == 0 || len(br.Top) >= 6 {
						continue
					}
					br.Top = append(br.Top, fmt.Sprintf("%s %s", n.Name, locOf(n.Path, n.SymbolID)))
				}
				out["blast_radius"] = br
			}
		}
		if fresh.Stale {
			out["warning"] = "index may be stale: " + fresh.StaleReason
		}
		// Empty caller/callee/import edges are common when the indexer hasn't
		// resolved cross-file references yet. Tell the agent so it doesn't
		// silently think the symbol is leaf-like.
		if bun != nil && len(bun.Callers) == 0 && len(bun.Callees) == 0 && len(bun.Imports) == 0 {
			out["note"] = "no graph edges resolved for this symbol — either it is truly leaf-like, or the index lacks call-graph edges (run `codehelper analyze --force`). Use `impact` (direction=upstream/downstream) to traverse cross-file dependencies."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func impactHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		target := argFirst(args, "target", "name", "symbol", "sym")
		dirExplicit := strings.TrimSpace(argString(args, "direction")) != ""
		dir := argString(args, "direction")
		if dir == "" {
			// Upstream answers "who uses this?" — the default agents need before edits.
			// Downstream remains available for "what does this depend on?".
			dir = "upstream"
		}
		depth := int(mcp.ParseInt64(req, "depth", 2))
		includeTests := true
		if v, ok := args["include_tests"].(bool); ok {
			includeTests = v
		}
		maxCandidates := int(mcp.ParseInt64(req, "max_candidates", 8))
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()
		wantPath := strings.TrimSpace(argString(args, "path"))
		wantLine := int(argFloat(args, "line", 0))
		sym, cands, err := resolveSymbolByName(ctx, st, repo.Name, target, wantPath, wantLine)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if sym == nil {
			return mustToolResultFormatted(map[string]any{
				"ambiguous":  true,
				"target":     target,
				"candidates": cands,
				"note":       "multiple symbols share this name — pass path= or a sym: id from query",
			}, resolveFormat(args))
		}
		res, err := mcpimpact.Analyze(ctx, st, repo.Name, sym.ID, depth, dir)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		autoRetried := false
		// Class/type hubs often have no outbound edges; when the caller asked for
		// (or defaulted into) downstream and got a self-only graph, flip once to
		// upstream so Nest/Axum-style "blast radius" questions stay useful.
		if res != nil && len(res.Nodes) <= 1 &&
			(dir == "downstream" || dir == "callees") &&
			(sym.Kind == types.SymbolKindClass || sym.Kind == types.SymbolKindInterface || sym.Kind == types.SymbolKindNamespace) {
			if up, uerr := mcpimpact.Analyze(ctx, st, repo.Name, sym.ID, depth, "upstream"); uerr == nil && up != nil && len(up.Nodes) > 1 {
				res = up
				dir = "upstream"
				autoRetried = true
			}
		}
		if !includeTests {
			res.Nodes = filterImpactNodesExcludeTests(res.Nodes)
			res.MustUpdateCandidates = filterImpactNodesExcludeTests(res.MustUpdateCandidates)
		}
		if maxCandidates > 0 && len(res.MustUpdateCandidates) > maxCandidates {
			res.MustUpdateCandidates = res.MustUpdateCandidates[:maxCandidates]
		}
		fresh := freshness.Inspect(repo.RootPath)
		structured := impactMCPResponse{
			Impact:    res,
			Freshness: fresh,
		}
		if fresh.Stale {
			structured.Warning = "index may be stale: " + fresh.StaleReason
		}
		if autoRetried {
			if dirExplicit {
				structured.Note = "downstream was self-only for this class/type hub; auto-retried direction=upstream (who uses it). Pass direction=downstream explicitly only when you need callees/deps."
			} else {
				structured.Note = "auto-retried direction=upstream for class/type hub (downstream was self-only)"
			}
		} else if res != nil && len(res.Nodes) <= 1 && len(res.MustUpdateCandidates) == 0 {
			structured.Note = "no impacted nodes beyond the target itself; verify `target` matches a real symbol id from query/context and that the index has cross-file edges"
			if sym.Kind == types.SymbolKindClass || sym.Kind == types.SymbolKindInterface || sym.Kind == types.SymbolKindNamespace {
				structured.Note += "; class/type hubs are often self-only while methods carry the edges — try impact on a method, or the opposite direction"
			}
		}
		enrichImpactResponse(&structured, res)
		return mustToolResultFormatted(structured, resolveFormat(args))
	}
}

func detectChangesHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		base := argString(args, "base_ref")
		if base == "" {
			base = "HEAD~1"
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()
		ids, err := detect.ChangedSymbols(ctx, repo.RootPath, repo.Name, base, st)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// changed_symbol_ids already reflects UNCOMMITTED edits to tracked files
		// (git diff <base> compares against the working tree). Brand-new untracked
		// source files are invisible to that diff AND to the symbol index, so list
		// them separately — otherwise an agent assumes a new file's symbols don't
		// exist. Mirrors the disk-fallback that query/context use on a miss.
		out := map[string]any{
			"base_ref":           base,
			"count":              len(ids),
			"changed_symbol_ids": ids,
			"note":               "changed_symbol_ids includes uncommitted edits to tracked files (diff is vs the working tree, not just commits).",
		}
		if untracked, uerr := gitutil.UntrackedFiles(repo.RootPath); uerr == nil {
			if src := filterSourceFiles(untracked); len(src) > 0 {
				out["untracked_source_files"] = src
				out["untracked_note"] = "new files not yet tracked by git or indexed — their symbols won't appear in changed_symbol_ids or query/context until indexed. Read them with read_workspace_file or run `codehelper analyze`."
			}
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// detectSourceExt is the set of extensions whose untracked files are worth
// flagging in detect_changes — the languages the indexer parses into symbols.
var detectSourceExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".py": true, ".rs": true, ".java": true,
	".cs": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".cxx": true, ".hpp": true, ".hh": true, ".hxx": true, ".php": true, ".rb": true,
	".kt": true, ".swift": true, ".scala": true, ".lua": true,
}

// filterSourceFiles keeps only parseable source files, capped to avoid a noisy
// dump when a fresh checkout has many untracked files.
func filterSourceFiles(paths []string) []string {
	const cap = 25
	var out []string
	for _, p := range paths {
		if detectSourceExt[strings.ToLower(filepath.Ext(p))] {
			out = append(out, p)
			if len(out) >= cap {
				break
			}
		}
	}
	return out
}

func filterImpactNodesExcludeTests(in []types.ImpactNode) []types.ImpactNode {
	out := make([]types.ImpactNode, 0, len(in))
	for _, n := range in {
		p := strings.ToLower(n.Path)
		if strings.Contains(p, "test") || strings.Contains(p, "_spec") {
			continue
		}
		out = append(out, n)
	}
	return out
}

func startsWithUpper(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(s))
	return unicode.IsUpper(r)
}

func likelyPublicSymbol(sym types.Symbol) bool {
	name := strings.TrimSpace(sym.Name)
	if name == "" || strings.HasPrefix(name, "_") {
		return false
	}
	lang := strings.ToLower(sym.Language)
	p := strings.ToLower(filepath.ToSlash(sym.Path))
	// Go: exported iff initial uppercase.
	if lang == "go" || strings.HasSuffix(p, ".go") {
		return startsWithUpper(name)
	}
	// Python / PHP / Ruby: magic methods are public; leading `_` already filtered;
	// remaining names default private so dead_code can surface unused helpers.
	if lang == "python" || lang == "php" || lang == "ruby" ||
		strings.HasSuffix(p, ".py") || strings.HasSuffix(p, ".php") || strings.HasSuffix(p, ".rb") {
		if strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__") {
			return true
		}
		return false
	}
	// JS/TS: PascalCase components/classes and api/public paths are public.
	if lang == "typescript" || lang == "javascript" ||
		strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx") ||
		strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".jsx") {
		if startsWithUpper(name) {
			return true
		}
		if strings.Contains(p, "/api/") || strings.Contains(p, "/public/") ||
			strings.HasPrefix(p, "api/") || strings.HasPrefix(p, "public/") {
			return true
		}
		return false
	}
	if startsWithUpper(name) {
		return true
	}
	if strings.Contains(p, "/pkg/") || strings.HasPrefix(p, "pkg/") || strings.HasPrefix(p, "cmd/") {
		return true
	}
	return false
}

func verifyHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		mode := verify.ExecMode(strings.ToLower(strings.TrimSpace(argString(args, "exec_mode"))))
		if mode == "" {
			mode = verify.ExecArgv
		}
		var allow []string
		if raw := argString(args, "allowed_commands"); raw != "" {
			for _, p := range strings.Split(raw, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					allow = append(allow, p)
				}
			}
		}
		timeoutSec := int(mcp.ParseInt64(req, "timeout_seconds", 0))
		repoRoot := strings.TrimSpace(argString(args, "repo_root"))
		if repoRoot == "" {
			if repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo")); err == nil {
				repoRoot = repo.RootPath
			}
		}
		lintCmd := argString(args, "lint_cmd")
		buildCmd := argString(args, "build_cmd")
		testCmd := argString(args, "test_cmd")
		if repoRoot != "" && lintCmd == "" && buildCmd == "" && testCmd == "" {
			cfg, _ := projcfg.Load(repoRoot)
			lintCmd = cfg.VerifyLint
			buildCmd = cfg.VerifyBuild
			testCmd = cfg.VerifyTest
			if lintCmd == "" && buildCmd == "" && testCmd == "" {
				if pr, err := profile.ReadOrGenerate(repoRoot); err == nil && pr != nil {
					if len(pr.LintCommands) > 0 {
						lintCmd = pr.LintCommands[0]
					}
					if len(pr.TestCommands) > 0 {
						testCmd = pr.TestCommands[0]
					}
				}
			}
		}
		cwd := repoRoot
		if repoRoot != "" {
			ws := resolveVerifyWorkspace(repoRoot)
			if ws.Cwd != "" {
				cwd = ws.Cwd
			}
		}
		vr := verify.Request{
			RepoRoot:        cwd,
			LintCmd:         lintCmd,
			BuildCmd:        buildCmd,
			TestCmd:         testCmd,
			PatchUnified:    argString(args, "patch_unified"),
			ExecMode:        mode,
			AllowedCommands: connections.ResolveVerifyAllowlist(repoRoot, allow),
			BlockPolicy:     connections.VerifyBlockPolicy(repoRoot),
			TimeoutSeconds:  timeoutSec,
		}
		res, err := verify.Run(ctx, vr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, _ := verify.ResultJSON(res)
		return mcp.NewToolResultText(string(b)), nil
	}
}

func reviewDiffHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()
		out, err := review.ReviewDiff(ctx, st, review.DiffRequest{
			RepoRoot:           repo.RootPath,
			RepoName:           repo.Name,
			Base:               argString(args, "base"),
			SeverityFloor:      review.Severity(strings.ToLower(argString(args, "severity_floor"))),
			IncludeTests:       argBool(args, "include_tests", true),
			IncludeSecurity:    argBool(args, "include_security", true),
			IncludePerformance: argBool(args, "include_performance", true),
			IncludeContracts:   argBool(args, "include_contracts", true),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	}
}

func detectImpactPrompt() server.PromptHandlerFunc {
	return staticPrompt("Impact", prompts.DetectImpact)
}

func generateMapPrompt() server.PromptHandlerFunc {
	return staticPrompt("Map", prompts.GenerateMap)
}

func staticPrompt(title, body string) server.PromptHandlerFunc {
	return func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		_ = ctx
		_ = req
		return mcp.NewGetPromptResult(title, []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(body)),
		}), nil
	}
}

func resolveRepo(ctx context.Context, reg *registry.Registry, name string) (registry.Entry, error) {
	name = normalizeRepoArg(name)
	if name == "" {
		// Fetch the workspace roots once and reuse them for both registry matching
		// and (if unregistered) auto-registration — issuing ListRoots twice in one
		// tool call is unreliable across MCP clients.
		// Match an already-registered project by the workspace roots OR the spawn
		// CWD — so a registered project resolves even when the client doesn't send
		// roots (some Cursor/Codex setups).
		scoped, _ := scopeRoots(ctx)
		if n, _, ok := repoNameForRoots(reg, scoped); ok {
			name = n
		} else if explicit, ok := mcpWorkspaceRoots(ctx); ok {
			// Auto-register only on EXPLICIT client roots, never on the CWD fallback:
			// a stray CWD (a subdirectory, or a test process) must not index the tree.
			if n, ok := autoRegisterRoots(ctx, reg, explicit); ok {
				name = n
			} else {
				return registry.Entry{}, fmt.Errorf("current workspace is not initialized; run `codehelper init` in the open project (see `codehelper projects list` for registered projects)")
			}
		} else {
			return registry.Entry{}, fmt.Errorf("current workspace is not initialized; run `codehelper init` in the open project (see `codehelper projects list` for registered projects)")
		}
	}
	n, err := reg.ResolveName(name)
	if err != nil {
		if errors.Is(err, registry.ErrAmbiguousRepo) {
			return registry.Entry{}, fmt.Errorf("ambiguous repo %q: omit repo to use the current workspace, or call project_context", name)
		}
		return registry.Entry{}, fmt.Errorf("repo %q is not registered; run `codehelper init` in the project root", name)
	}
	e, ok := reg.Get(n)
	if !ok {
		return registry.Entry{}, fmt.Errorf("repo not found: %s", n)
	}
	if err := assertRepoInWorkspaceScope(ctx, e); err != nil {
		return registry.Entry{}, err
	}
	return e, nil
}

func resolveRepoInitialized(ctx context.Context, reg *registry.Registry, name string) (registry.Entry, error) {
	e, err := resolveRepo(ctx, reg, name)
	if err != nil {
		return e, err
	}
	if err := registry.RequireInitialized(e.RootPath); err != nil {
		// Auto-index instead of bouncing the LLM back with a "not indexed" error
		// it must notice, act on, and retry — that burns a tool round-trip and
		// tokens on something code can just do. Build the index once, validate,
		// then answer. A short cooldown stops an unindexable repo from looping.
		if !autoIndexAllowed(e.RootPath) {
			return registry.Entry{}, fmt.Errorf("project %q is not indexed and a recent auto-index did not populate it; run `codehelper analyze` in %s", e.Name, e.RootPath)
		}
		slog.Info("auto-indexing repo on first use", "repo", e.Name, "root", e.RootPath)
		if ixErr := indexer.Run(ctx, e.RootPath, indexer.Options{RepoName: e.Name}); ixErr != nil {
			return registry.Entry{}, fmt.Errorf("project %q is not indexed and auto-index failed: %w", e.Name, ixErr)
		}
		if err := registry.RequireInitialized(e.RootPath); err != nil {
			return registry.Entry{}, fmt.Errorf("project %q could not be indexed (no symbols found): %w", e.Name, err)
		}
	}
	return e, nil
}

// autoRegisterRoots indexes and registers the open MCP workspace when it isn't in
// the registry yet, so a brand-new project needs zero `init`. roots are the
// already-fetched workspace roots. Returns the registered repo name.
func autoRegisterRoots(ctx context.Context, reg *registry.Registry, roots []string) (string, bool) {
	if len(roots) == 0 {
		return "", false
	}
	return registerAndIndexRoot(ctx, reg, roots[0])
}

// registerAndIndexRoot builds the index for root and registers it. Rate-limited
// by the auto-index cooldown so a non-project directory can't trigger indexing on
// every tool call. Extracted from the MCP plumbing so it is directly testable.
func registerAndIndexRoot(ctx context.Context, reg *registry.Registry, root string) (string, bool) {
	if strings.TrimSpace(root) == "" || !autoIndexAllowed(root) {
		return "", false
	}
	gitRoot, indexRoot, err := indexer.ResolveIndexPaths(root, "")
	if err != nil {
		// Non-git workspace: index in place, no commit pinning.
		gitRoot, indexRoot = root, root
	}
	slog.Info("auto-registering + indexing open workspace", "root", indexRoot)
	if err := indexer.Run(ctx, root, indexer.Options{}); err != nil {
		slog.Warn("auto-register index failed", "root", root, "err", err)
		return "", false
	}
	name := filepath.Base(indexRoot)
	commit, _ := gitutil.HeadCommit(gitRoot)
	if err := reg.Upsert(name, indexRoot, commit, meta.SchemaVersion); err != nil {
		slog.Warn("auto-register upsert failed", "name", name, "err", err)
		return "", false
	}
	if err := reg.Save(); err != nil {
		slog.Warn("auto-register save failed", "err", err)
		return "", false
	}
	return name, true
}

// autoIndexCooldowns rate-limits auto-indexing per repo root so a genuinely
// unindexable repo (empty / no supported sources) can't trigger a re-index on
// every tool call. Returns true if an auto-index attempt is allowed now.
var autoIndexCooldowns sync.Map // root -> time.Time of last attempt

const autoIndexCooldown = 20 * time.Second

func autoIndexAllowed(root string) bool {
	now := time.Now()
	if last, ok := autoIndexCooldowns.Load(root); ok {
		if now.Sub(last.(time.Time)) < autoIndexCooldown {
			return false
		}
	}
	autoIndexCooldowns.Store(root, now)
	return true
}

// rootsCache memoizes the client's advertised workspace roots per session.
// Resolving a repo touches ListRoots up to twice per tool call (once to match,
// once to assert scope); the mcp-go code itself warns that issuing ListRoots
// twice in one call is "unreliable across MCP clients". A short-TTL cache
// collapses those server→client round-trips to one, which removes both the
// latency and the intermittent hang that made a "connected" client unusable.
// Entries are evicted on session close (see the OnUnregisterSession hook).
var rootsCache sync.Map // sessionID -> rootsCacheEntry

type rootsCacheEntry struct {
	roots []string
	ok    bool
	at    time.Time
}

const rootsCacheTTL = 3 * time.Second

// sessionRootsCap records, per session, whether the client advertised the MCP
// roots capability at initialize. mcp-go's stdio session always satisfies the
// SessionWithRoots Go interface, so without this we'd issue a server→client
// ListRoots even to clients that never offered roots — and wait out the timeout
// for a response that can't come. Populated by registerCapabilityHooks.
var sessionRootsCap sync.Map // sessionID -> bool

// registerCapabilityHooks records client capabilities and evicts per-session
// caches on disconnect. Always registered (independent of debug logging) so
// roots gating is correct in every build.
func registerCapabilityHooks(h *server.Hooks) {
	h.AddAfterInitialize(func(ctx context.Context, id any, req *mcp.InitializeRequest, res *mcp.InitializeResult) {
		if sid := sessionIDFromContext(ctx); sid != "" {
			sessionRootsCap.Store(sid, req.Params.Capabilities.Roots != nil)
		}
	})
	h.AddOnUnregisterSession(func(ctx context.Context, session server.ClientSession) {
		sid := session.SessionID()
		rootsCache.Delete(sid)
		sessionRootsCap.Delete(sid)
	})
}

// clientAdvertisedRoots reports whether the client offered the roots capability.
// Returns true when unknown (no initialize record yet) so behavior degrades to
// the timeout-bounded attempt rather than silently skipping a capable client.
func clientAdvertisedRoots(sid string) bool {
	if sid == "" {
		return true
	}
	if v, ok := sessionRootsCap.Load(sid); ok {
		return v.(bool)
	}
	return true
}

func mcpWorkspaceRoots(ctx context.Context) ([]string, bool) {
	sid := sessionIDFromContext(ctx)
	if sid != "" {
		if v, ok := rootsCache.Load(sid); ok {
			e := v.(rootsCacheEntry)
			if time.Since(e.at) < rootsCacheTTL {
				return e.roots, e.ok
			}
		}
	}
	roots, ok := listRootsUncached(ctx)
	if sid != "" {
		rootsCache.Store(sid, rootsCacheEntry{roots: roots, ok: ok, at: time.Now()})
	}
	return roots, ok
}

// rootsListTimeout bounds the server→client ListRoots round-trip. A client that
// advertises the roots capability but never answers the request (observed with
// real Claude Code / Cursor sessions) would otherwise block the tool call
// indefinitely — the connection shows "connected" yet every call that omits
// `repo` hangs. On timeout we fall back to CWD-based scoping, which clients make
// reliable by spawning the server with CWD set to the open project. Override with
// CODEHELPER_ROOTS_TIMEOUT_MS.
func rootsListTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CODEHELPER_ROOTS_TIMEOUT_MS")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 2 * time.Second
}

func listRootsUncached(ctx context.Context) ([]string, bool) {
	sid := sessionIDFromContext(ctx)
	// Don't issue a server→client ListRoots to a client that never advertised
	// the roots capability — the response can't come and we'd just burn the
	// timeout. Scope by CWD instead.
	if !clientAdvertisedRoots(sid) {
		mcpLog.event("roots", map[string]any{"sid": shortID(sid), "supported": false, "reason": "not advertised"})
		return nil, false
	}
	session := server.ClientSessionFromContext(ctx)
	rootSession, ok := session.(server.SessionWithRoots)
	if !ok {
		mcpLog.event("roots", map[string]any{"sid": shortID(sid), "supported": false})
		return nil, false
	}
	rootsCtx, cancel := context.WithTimeout(ctx, rootsListTimeout())
	defer cancel()
	start := time.Now()
	res, err := rootSession.ListRoots(rootsCtx, mcp.ListRootsRequest{})
	ms := time.Since(start).Milliseconds()
	if err != nil || res == nil || len(res.Roots) == 0 {
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		mcpLog.event("roots", map[string]any{"sid": shortID(sessionIDFromContext(ctx)), "supported": true, "ms": ms, "count": 0, "err": errStr})
		return nil, false
	}
	roots := make([]string, 0, len(res.Roots))
	for _, r := range res.Roots {
		rootPath, ok := fileURIToPath(r.URI)
		if !ok {
			continue
		}
		roots = append(roots, normalizeComparablePath(rootPath))
	}
	mcpLog.event("roots", map[string]any{"sid": shortID(sessionIDFromContext(ctx)), "supported": true, "ms": ms, "count": len(roots), "roots": roots})
	if len(roots) == 0 {
		return nil, false
	}
	return roots, true
}

// workingDirRoot returns the server's working directory as a workspace root.
// Clients spawn `codehelper mcp` with CWD set to the open project, so this is a
// reliable fallback for SCOPING when a client doesn't implement the MCP roots
// protocol. Used only to (a) match an already-registered project and (b) keep the
// cross-project isolation guard active — NOT to auto-register/index a new path
// (that stays gated on explicit client roots so an unexpected CWD, e.g. a test or
// a subdirectory, never indexes the source tree).
func workingDirRoot() ([]string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, false
	}
	return []string{normalizeComparablePath(wd)}, true
}

// scopeRoots returns the client's advertised workspace roots, or the server's CWD
// as a fallback. This is the set used for project matching and isolation scoping.
func scopeRoots(ctx context.Context) ([]string, bool) {
	if roots, ok := mcpWorkspaceRoots(ctx); ok {
		return roots, true
	}
	return workingDirRoot()
}

func entryMatchesRoots(e registry.Entry, roots []string) bool {
	repoRoot := normalizeComparablePath(e.RootPath)
	for _, root := range roots {
		if repoRoot == root || pathContains(repoRoot, root) || pathContains(root, repoRoot) {
			return true
		}
	}
	return false
}

func assertRepoInWorkspaceScope(ctx context.Context, e registry.Entry) error {
	roots, ok := scopeRoots(ctx)
	if !ok {
		return nil
	}
	if entryMatchesRoots(e, roots) {
		return nil
	}
	return fmt.Errorf("repo %q is outside the current workspace; per-project tools only work in the open project (see `codehelper projects list`)", e.Name)
}

func currentWorkspaceRepoName(ctx context.Context, reg *registry.Registry) (string, string, bool) {
	roots, ok := mcpWorkspaceRoots(ctx)
	if !ok {
		return "", "", false
	}
	return repoNameForRoots(reg, roots)
}

// repoNameForRoots matches already-fetched workspace roots against the registry.
// Split out so the caller can fetch roots once and reuse them (the MCP ListRoots
// round-trip should not be issued twice within a single tool call).
//
// When several registered projects contain the workspace (nested testbeds under
// an indexed parent), the deepest RootPath wins so agents vibe-code the open
// project instead of silently answering from the parent graph.
func repoNameForRoots(reg *registry.Registry, roots []string) (string, string, bool) {
	if len(roots) == 0 {
		return "", "", false
	}
	var exact, containing, parent []registry.Entry
	seenExact, seenContain, seenParent := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, e := range reg.List() {
		repoRoot := normalizeComparablePath(e.RootPath)
		for _, root := range roots {
			switch {
			case repoRoot == root:
				if _, ok := seenExact[e.Name]; !ok {
					seenExact[e.Name] = struct{}{}
					exact = append(exact, e)
				}
			case pathContains(repoRoot, root):
				if _, ok := seenContain[e.Name]; !ok {
					seenContain[e.Name] = struct{}{}
					containing = append(containing, e)
				}
			case pathContains(root, repoRoot):
				if _, ok := seenParent[e.Name]; !ok {
					seenParent[e.Name] = struct{}{}
					parent = append(parent, e)
				}
			}
		}
	}
	if len(exact) > 0 {
		return pickDeepestEntry(exact).Name, "matched_mcp_roots", true
	}
	if len(containing) > 0 {
		return pickDeepestEntry(containing).Name, "matched_mcp_roots", true
	}
	if len(parent) == 1 {
		return parent[0].Name, "matched_mcp_roots", true
	}
	return "", "", false
}

// pickDeepestEntry returns the registry entry with the longest RootPath (most
// specific nested project). Ties keep the first entry.
func pickDeepestEntry(entries []registry.Entry) registry.Entry {
	best := entries[0]
	bestLen := len(normalizeComparablePath(best.RootPath))
	for _, e := range entries[1:] {
		n := len(normalizeComparablePath(e.RootPath))
		if n > bestLen {
			best = e
			bestLen = n
		}
	}
	return best
}

func fileURIToPath(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	p, err := url.PathUnescape(u.Path)
	if err != nil {
		return "", false
	}
	if u.Host != "" {
		p = string(filepath.Separator) + string(filepath.Separator) + u.Host + p
	}
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	if strings.TrimSpace(p) == "" {
		return "", false
	}
	return filepath.Clean(filepath.FromSlash(p)), true
}

func normalizeComparablePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(filepath.Clean(filepath.FromSlash(p)))
	if err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func pathContains(parent, child string) bool {
	parent = normalizeComparablePath(parent)
	child = normalizeComparablePath(child)
	if parent == "" || child == "" || parent == child {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// openGraph returns the process-cached read store for a repo. It's opened once
// per DB file and shared across tool calls, so the many `defer st.Close()` sites
// are no-ops on the shared handle (graph.OpenCached / Store.Close). MCP tools
// only read the graph; the indexer writes it from its own connection.
func openGraph(root string) (*graph.Store, error) {
	return graph.OpenCached(paths.DBPath(root))
}

func argString(args map[string]any, k string) string {
	if args == nil {
		return ""
	}
	v, ok := args[k]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// argFirst returns the first non-empty (trimmed) value among the given keys.
// It lets a tool accept common LLM aliases (e.g. `symbol` for `name`) instead
// of erroring on a wrong-but-obvious param name — a frequent agent friction.
func argFirst(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(argString(args, k)); v != "" {
			return v
		}
	}
	return ""
}

// argQuery accepts "query" or the common LLM alias "q".
func argQuery(args map[string]any) string {
	q := strings.TrimSpace(argString(args, "query"))
	if q != "" {
		return q
	}
	return strings.TrimSpace(argString(args, "q"))
}

// distinctiveIdentifier picks the most distinctive bare-identifier token from a
// query string — the one worth a disk grep when the index returns nothing. It
// prefers longer, identifier-shaped, non-common tokens; returns "" when the query
// is all prose (no single token a precise disk grep could match).
func distinctiveIdentifier(q string) string {
	best := ""
	for _, tok := range strings.FieldsFunc(q, func(r rune) bool {
		return r != '_' && r != '$' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9')
	}) {
		if len(tok) < 4 || retrieval.IsCommonWord(strings.ToLower(tok)) {
			continue
		}
		// A camelCase / snake_case / PascalCase token is a strong identifier signal.
		mixed := strings.ToLower(tok) != tok || strings.Contains(tok, "_")
		if mixed && len(tok) >= len(best) {
			best = tok
			continue
		}
		if best == "" && len(tok) > 4 {
			best = tok
		}
	}
	return best
}

// normalizeRepoArg strips instructional placeholders so single-repo default applies.
func normalizeRepoArg(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 {
		f, l := s[0], s[len(s)-1]
		if (f == '"' && l == '"') || (f == '\'' && l == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	switch lower {
	case "<repository_name>", "repository_name",
		"repo_name", "your_repo", "your-repo", "example_repo", "the_repository_name",
		"my-repo-name", "my_repo_name":
		return ""
	}
	if len(lower) >= 2 && lower[0] == '<' && lower[len(lower)-1] == '>' {
		return ""
	}
	return s
}

func argBool(args map[string]any, k string, def bool) bool {
	if args == nil {
		return def
	}
	v, ok := args[k]
	if !ok || v == nil {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func resolveCrossRepoCandidates(reg *registry.Registry, q string) []registry.Entry {
	parts := strings.Fields(strings.TrimSpace(q))
	if len(parts) == 0 {
		return []registry.Entry{}
	}
	seen := map[string]registry.Entry{}
	for _, p := range parts {
		if !strings.Contains(p, "/") && !strings.Contains(p, ".") {
			continue
		}
		owners := reg.ResolveImportOwners(p)
		for _, o := range owners {
			seen[o.Name] = o
		}
	}
	if len(seen) == 0 {
		return []registry.Entry{}
	}
	out := make([]registry.Entry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
