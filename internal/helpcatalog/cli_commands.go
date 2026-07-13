package helpcatalog

// cliRefs is the CLI command catalog. Group keys match help overview sections.
var cliRefs = map[string]struct {
	group   string
	summary string
	related string
}{
	"setup":          {group: "setup", summary: "Global install: PATH, skills, prune global Cursor MCP dupes."},
	"init":           {group: "setup", summary: "Initialize a git repo: index, watch, MCP config, AGENTS.md."},
	"projects":       {group: "setup", summary: "List or manage registered projects on this machine."},
	"config":         {group: "setup", summary: "User/project settings: search, browser, LLM, MCP tools on/off."},
	"repair":         {group: "setup", summary: "Re-apply rules, MCP config, and index schema for all projects."},
	"upgrade":        {group: "setup", summary: "Install latest release from GitHub (no Go required)."},
	"update":         {group: "setup", summary: "Rebuild from local source, refresh index, ensure watch."},
	"analyze":        {group: "index", summary: "Index repository into the symbol/call graph.", related: "diagnostics"},
	"watch":          {group: "index", summary: "Auto-index on file change (foreground or --daemon)."},
	"enrich":         {group: "index", summary: "Index-time LLM enrichment (purpose + aliases).", related: "green"},
	"status":         {group: "index", summary: "Index staleness, watch state, symbol/edge counts."},
	"doctor":         {group: "index", summary: "Environment and index health diagnostics."},
	"clean":          {group: "index", summary: "Remove .codehelper index directory."},
	"hooks":          {group: "index", summary: "Git hooks to reindex after history changes."},
	"mcp":            {group: "mcp", summary: "Start MCP server (stdio by default).", related: "project_context"},
	"version":        {group: "mcp", summary: "Print codehelper version."},
	"browser":        {group: "mcp", summary: "Manage headless browser for MCP browser tool.", related: "browser"},
	"eval":           {group: "quality", summary: "Retrieval + intake-prompt eval suite (CI gate)."},
	"model-eval":     {group: "quality", summary: "Optional local model-eval via CODEHELPER_MODEL_EVAL_CMD."},
	"bench":          {group: "quality", summary: "Benchmark vs Serena/Context7 on current index."},
	"rules":          {group: "quality", summary: "Install framework review rule packs."},
	"profile":        {group: "quality", summary: "Generate .codehelper/project_profile.json."},
	"expand-request": {group: "planning", summary: "Deterministic requirement expansion from pattern packs.", related: "expand_request (CLI-only)"},
	"plan":           {group: "planning", summary: "Structured plan JSON with editable todos.", related: "plan · kickoff"},
	"step":           {group: "planning", summary: "Execute one approved todo.", related: "agent_execute_todo"},
	"patterns":       {group: "planning", summary: "Install bundled feature pattern packs.", related: "select_pattern (CLI-only)"},
	"docs":           {group: "docs", summary: "Fetch version-correct library docs.", related: "docs"},
	"docgen":         {group: "docs", summary: "Generate per-package API docs from the index."},
	"green":          {group: "optional", summary: "Optional local LLM stack for rerank/enrichment."},
	"hints":          {group: "optional", summary: "Global cross-project learned hints.", related: "hints"},
	"usage":          {group: "optional", summary: "Per-project tool-usage + token report.", related: "usage_report"},
	"web":            {group: "optional", summary: "HTTP endpoint verification (no browser).", related: "web"},
	"connections":    {group: "connections", summary: "Encrypted DB/SSH connection profiles (never in repo)."},
	"orchestration":  {group: "orchestration", summary: "Enable/disable local guided investigation workflows.", related: "orchestration"},
	"serve":          {group: "experimental", summary: "Local Agent HTTP API (loopback, experimental)."},
	"run":            {group: "experimental", summary: "Terminal agent loop (alias for agent chat)."},
	"agent":          {group: "experimental", summary: "CLI orchestration helpers (plan/review/finish).", related: "kickoff · verify"},
	"tasks":          {group: "experimental", summary: "List persisted tasks and timelines."},
	"memory":         {group: "experimental", summary: "Approve/reject project memory proposals.", related: "agent_memory"},
	"help":           {group: "meta", summary: "Browse CLI + MCP catalog (this command)."},
}

// CLIs returns CLI catalog entries, optionally filtered.
func CLIs(opts Filter) []CLI {
	var out []CLI
	for name, ref := range cliRefs {
		if opts.Group != "" && ref.group != opts.Group {
			continue
		}
		if opts.Query != "" && !matchesQuery(opts.Query, name, ref.group, ref.summary) {
			continue
		}
		out = append(out, CLI{
			Name:    name,
			Group:   ref.group,
			Summary: ref.summary,
			Related: ref.related,
		})
	}
	sortCLIs(out)
	return out
}

// CLIByName returns one CLI entry or nil.
func CLIByName(name string) *CLI {
	ref, ok := cliRefs[name]
	if !ok {
		return nil
	}
	c := CLI{
		Name:    name,
		Group:   ref.group,
		Summary: ref.summary,
		Related: ref.related,
	}
	return &c
}

// CLIGroups returns CLI commands grouped by section.
func CLIGroups() map[string][]string {
	byGroup := map[string][]string{}
	for name, ref := range cliRefs {
		if name == "help" {
			continue
		}
		byGroup[ref.group] = append(byGroup[ref.group], name)
	}
	for g := range byGroup {
		sortStrings(byGroup[g])
	}
	return byGroup
}

func sortCLIs(out []CLI) {
	sortSlice(out, func(a, b CLI) bool {
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Name < b.Name
	})
}
