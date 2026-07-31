package helpcatalog

import (
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/mcpsvc"
)

// toolRef is a one-line catalog entry for an MCP tool.
type toolRef struct {
	summary string
	params  string
}

// toolRefs is the human-readable catalog keyed by tool name.
// Keep in sync with mcpsvc.AllMCPToolNames — TestToolRefsComplete guards drift.
var toolRefs = map[string]toolRef{
	"project_context":            {summary: "PROJECT slot — one-time bootstrap: repo identity, index freshness, stack, MCP routing (composites first), hints, next_step.", params: "verbosity=short|detailed · sections=tools"},
	"scope":                      {summary: "Turn a vague idea into concrete terms and the questions that matter.", params: "idea"},
	"kickoff":                    {summary: "WORKFLOW slot (PREFERRED starter) — one-call orient + reuse + docs + steps + verify + what_next.", params: "task · role=feature|… · sections"},
	"query":                      {summary: "SEARCH slot — lexical symbol locate (BM25 + trigrams). Prefer kickoff/investigate for broader starts.", params: "query · intent · include_context_pack"},
	"search_hybrid":              {summary: "BM25/FTS → graph expand → RRF (optional vectors); optional hub-biased API map (full profile / by-name).", params: "query · path"},
	"scout":                      {summary: "Reuse candidates + usage_of_top + impact (full profile / by-name; prefer kickoff).", params: "task"},
	"ast_query":                  {summary: "Tree-sitter structural search over live files.", params: "language · pattern"},
	"similar":                    {summary: "Similar implementations of one symbol.", params: "name"},
	"find_implementations":       {summary: "Which Go types implement an interface.", params: "interface"},
	"context":                    {summary: "UNDERSTAND slot — source + callers + callees + blast radius. Prefer investigate to locate+deep-dive.", params: "name (NOT target) · body=brief|none|full"},
	"context_bundle":             {summary: "Bounded ACI bundle: source + callers + callees + imports + tests (full profile / by-name).", params: "name · max_callers · include_tests"},
	"impact":                     {summary: "IMPACT slot — call-graph blast radius. Skip if context/investigate already returned it.", params: "target (NOT name) · direction"},
	"trace":                      {summary: "Shortest call path (from+to) or outbound call tree (from).", params: "from · to"},
	"api_surface":                {summary: "Exported API of a package or directory.", params: "path"},
	"detect_changes":             {summary: "Git diff → affected symbols.", params: "base_ref"},
	"test_impact":                {summary: "Tests that reach changed or target symbols.", params: "base_ref · target"},
	"since":                      {summary: "Post-edit: changed symbols + blast radius + tests to run.", params: "base_ref"},
	"dead_code":                  {summary: "Unreferenced symbol candidates (verify before deleting).", params: "kinds"},
	"hotspots":                   {summary: "Files ranked by git churn × call-graph centrality.", params: "top_k"},
	"diagnostics":                {summary: "Auto-detect toolchain and run build/vet/tsc checks.", params: "command"},
	"plan":                       {summary: "Architect-mode planner: reuse, blast radius, steps, checklist.", params: "task · role"},
	"change_kit":                 {summary: "CHANGE slot — pre-edit bundle: definition, call sites, tests, risk tier.", params: "target (NOT name)"},
	"rename_symbol":              {summary: "Rename a symbol (graph-first; merges LSP refs when available).", params: "name · to"},
	"insert_at_symbol":           {summary: "Insert code at a symbol anchor.", params: "target · content"},
	"lsp":                        {summary: "Optional LSP hover/def/refs/rename/implementations; graph fallback when missing.", params: "action · path · line · col"},
	"read_workspace_file":        {summary: "Read a file slice from the workspace.", params: "path · offset · limit"},
	"write_workspace_file":       {summary: "Write or create a workspace file.", params: "path · content"},
	"apply_patch_workspace_file": {summary: "Apply a unified diff to a workspace file (focused advertise).", params: "path · patch"},
	"list_workspace_directory":   {summary: "List a directory in the workspace.", params: "target"},
	"revert_workspace_edit":      {summary: "Revert a prior workspace edit by edit id.", params: "edit_id"},
	"verify":                     {summary: "CHECK slot — run lint/build/test gates (argv mode by default).", params: "lint_cmd · build_cmd · test_cmd"},
	"review_diff":                {summary: "Strict review of the current git diff.", params: "base"},
	"review":                     {summary: "Deterministic diff audit: changed symbols, risk, tests to run.", params: "base_ref"},
	"finish_check":               {summary: "CHECK gate — fail-closed release gate after verify/review (focused advertise).", params: "base_ref"},
	"docs":                       {summary: "Version-correct library docs (llms.txt-first).", params: "library · topic"},
	"docs_add":                   {summary: "Register a missing documentation source.", params: "name · doc_base"},
	"web":                        {summary: "Fast HTTP fetch + assertions (no browser).", params: "url · expect_*"},
	"browser":                    {summary: "BROWSER slot — headless Chromium screenshot + console/JS checks.", params: "url · actions · device"},
	"web_search":                 {summary: "Web search → ranked URLs to fetch with web/browser.", params: "query"},
	"hints":                      {summary: "Cross-project stack rules (local learned hints).", params: "action=add|list|…"},
	"glossary":                   {summary: "Project vocabulary and naming conventions.", params: "query"},
	"agent_memory":               {summary: "Project-scoped agent memory proposals.", params: "action"},
	"agent_plan":                 {summary: "Create persisted plan todos from expand_request (experimental).", params: "request"},
	"agent_execute_todo":         {summary: "Execute one approved todo (needs LLM; experimental).", params: "todo_id"},
	"orchestration":              {summary: "Enable/disable/status local orchestration workflows.", params: "action=enable|disable|status"},
	"orchestrate":                {summary: "WORKFLOW composite (PREFERRED when orchestration on) — classify → tool chain → agent_brief.", params: "task · detail=true|false · format=toon|json"},
	"orchestration_rerun":        {summary: "Rerun a prior orchestration with new constraints.", params: "run_id · instruction"},
	"orchestration_feedback":     {summary: "Store correction for an orchestration run.", params: "run_id · message"},
	"run_trace":                  {summary: "Full tool-call trace for an orchestration run.", params: "run_id"},
	"explain_run":                {summary: "Explain why an orchestration run chose its workflow.", params: "run_id"},
	"orchestration_memory":       {summary: "Search orchestration memory from prior runs.", params: "query"},
	"investigate":                {summary: "UNDERSTAND composite (PREFERRED) — query → context → impact in one call.", params: "query · name"},
	"edit_cycle":                 {summary: "Composite: change_kit → diagnostics → verify gate.", params: "target"},
	"preflight":                  {summary: "Composite: project_context + diagnostics before editing.", params: ""},
	"usage_report":               {summary: "Per-project tool-usage and token report.", params: "refs · verbose"},
	"remote_list":                {summary: "Read-only map of SSH hosts, DB profiles, log sources, and aliases.", params: "repo"},
	"remote_exec":                {summary: "Run a named SSH recipe (never free-form shell).", params: "host · recipe · params"},
	"log_read":                   {summary: "Tail a configured local log source.", params: "source · lines"},
	"db_query":                   {summary: "Read-only SQL against a configured sqlite/mysql profile.", params: "connection · sql · max_rows"},
	"db_schema":                  {summary: "Schema introspection for sqlite/mysql connections.", params: "connection · tables"},
	"run_alias":                  {summary: "Run a configured command alias (local or remote recipe).", params: "name · approved"},
	"env_context":                {summary: "Detect toolchain versions, scripts, make targets, aliases.", params: "repo"},
	"ci_status":                  {summary: "Read-only GitHub PR/workflow summary via gh CLI.", params: "repo"},
}

// Tools returns catalog entries, optionally filtered.
func Tools(opts Filter) []Tool {
	main := map[string]struct{}{}
	for _, n := range mcpsvc.MCPMainTools {
		main[n] = struct{}{}
	}
	groupMap := invertGroups(mcpsvc.MCPToolCatalogFull().ByGroup)
	var out []Tool
	for name, ref := range toolRefs {
		group := groupMap[name]
		if opts.Group != "" && group != opts.Group {
			continue
		}
		if opts.MainOnly {
			if _, ok := main[name]; !ok {
				continue
			}
		}
		if opts.Query != "" && !matchesQuery(opts.Query, name, group, ref.summary) {
			continue
		}
		_, isMain := main[name]
		out = append(out, Tool{
			Name:    name,
			Group:   group,
			Summary: ref.summary,
			Params:  ref.params,
			Main:    isMain,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ToolByName returns one MCP tool entry or nil.
func ToolByName(name string) *Tool {
	ref, ok := toolRefs[name]
	if !ok {
		return nil
	}
	groupMap := invertGroups(mcpsvc.MCPToolCatalogFull().ByGroup)
	main := false
	for _, m := range mcpsvc.MCPMainTools {
		if m == name {
			main = true
			break
		}
	}
	t := Tool{
		Name:    name,
		Group:   groupMap[name],
		Summary: ref.summary,
		Params:  ref.params,
		Main:    main,
	}
	return &t
}

// Groups returns sorted MCP tool group names.
func Groups() []string {
	full := mcpsvc.MCPToolCatalogFull()
	names := make([]string, 0, len(full.ByGroup))
	for g := range full.ByGroup {
		names = append(names, g)
	}
	sort.Strings(names)
	return names
}

// CatalogMeta returns bootstrap-style MCP metadata.
func CatalogMeta() mcpsvc.MCPToolCatalog {
	return mcpsvc.MCPToolCatalogFull()
}

func invertGroups(byGroup map[string][]string) map[string]string {
	out := make(map[string]string)
	for g, names := range byGroup {
		for _, n := range names {
			out[n] = g
		}
	}
	return out
}

func matchesQuery(q, name, group, summary string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	for _, s := range []string{name, group, summary} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}
