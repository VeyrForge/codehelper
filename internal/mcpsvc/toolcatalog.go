package mcpsvc

import (
	"sort"

	"github.com/VeyrForge/codehelper/internal/product"
)

// MCPParamKeys is a one-line cheat sheet for the most-misused tool parameters.
// Agents routinely confuse context.name with change_kit.target (and impact.target).
const MCPParamKeys = "context/context_bundle→name · impact/change_kit→target · trace→from+to · query/search_hybrid→query (optional group=) · kickoff/orchestrate→task · investigate→query|recipe|target · rename_symbol→name+to · scope→idea · agent_memory→action"

// ConceptualToolSlot is one of the eight agent-facing jobs. Docs, AGENTS.md
// routing, and help catalog aliases map to these slots; the Tool field is the
// primary MCP entry point for that job.
type ConceptualToolSlot struct {
	Slot string // project | search | understand | impact | change | check | browser | workflow
	Tool string // registered MCP tool name
}

// ConceptualToolSlots is the stable conceptual map for routing and docs.
// Prefer the Tool (or a composite that covers the same job) over chaining
// lower-level specialists advertised only in the full profile.
var ConceptualToolSlots = []ConceptualToolSlot{
	{Slot: "project", Tool: "project_context"},
	{Slot: "search", Tool: "query"},
	{Slot: "understand", Tool: "context"},
	{Slot: "impact", Tool: "impact"},
	{Slot: "change", Tool: "change_kit"},
	{Slot: "check", Tool: "verify"},
	{Slot: "browser", Tool: "browser"},
	{Slot: "workflow", Tool: "kickoff"},
}

// MCPMainTools are the high-frequency tools agents should reach for first.
// Composites (kickoff / investigate / orchestrate) are listed ahead of thin
// primitives so project_context "reach for these first" steers agents correctly.
var MCPMainTools = []string{
	"project_context", "kickoff", "query", "investigate",
	"change_kit", "verify", "orchestrate", "browser",
}

// IsMainTool reports whether name is one of the high-frequency main tools —
// the "reach for these first" routing hint surfaced in project_context.
func IsMainTool(name string) bool {
	for _, n := range MCPMainTools {
		if n == name {
			return true
		}
	}
	return false
}

// CoreToolSet is the smallest advertise set: one entry per conceptual slot (~8).
var CoreToolSet = []string{
	"project_context", // project
	"query",           // search
	"context",         // understand
	"impact",          // impact
	"change_kit",      // change
	"verify",          // check
	"browser",         // browser
	"kickoff",         // workflow
}

// FocusedToolSet is the default day-to-day advertise surface (~12): the eight
// conceptual slots plus composites and the edit/finish pair needed end-to-end.
// Everything else stays registered and callable by name (see toolfilter.go).
var FocusedToolSet = []string{
	"project_context",            // project
	"query",                      // search
	"context",                    // understand
	"impact",                     // impact
	"change_kit",                 // change
	"verify",                     // check
	"browser",                    // browser
	"kickoff",                    // workflow
	"orchestrate",                // workflow composite
	"investigate",                // understand composite (query+context+impact)
	"apply_patch_workspace_file", // change apply
	"finish_check",               // check gate
}

// MinimalToolSet is a legacy alias of FocusedToolSet (CODEHELPER_MINIMAL_TOOLS /
// --minimal on → focused profile).
var MinimalToolSet = FocusedToolSet

// IsFocusedTool reports whether name is advertised under the focused profile.
func IsFocusedTool(name string) bool {
	for _, n := range FocusedToolSet {
		if n == name {
			return true
		}
	}
	return false
}

// IsCoreTool reports whether name is advertised under the core profile.
func IsCoreTool(name string) bool {
	for _, n := range CoreToolSet {
		if n == name {
			return true
		}
	}
	return false
}

// MCPToolContractPath is where the full per-repo tool routing contract lives after analyze.
const MCPToolContractPath = "AGENTS.md"

// MCPToolsReferencePath points to the shipped tool reference (when present in the repo).
const MCPToolsReferencePath = "docs/MCP_TOOLS.md"

// mcpToolsByGroup is the canonical grouped catalog for bootstrap responses.
// Keep in sync with RegisterAll — TestMCPToolCatalogComplete guards drift.
var mcpToolsByGroup = map[string][]string{
	"bootstrap": {
		"project_context", "scope", "kickoff",
	},
	"search": {
		"query", "search_hybrid", "scout", "ast_query", "similar", "find_implementations",
	},
	"graph": {
		"context", "context_bundle", "impact", "trace", "api_surface", "detect_changes", "test_impact", "since",
	},
	"analysis": {
		"dead_code", "hotspots", "diagnostics",
	},
	"plan_edit": {
		"plan", "change_kit", "rename_symbol", "insert_at_symbol", "lsp",
	},
	"workspace": {
		"read_workspace_file", "list_workspace_directory", "write_workspace_file",
		"apply_patch_workspace_file", "revert_workspace_edit",
	},
	"gates": {
		"verify", "review_diff", "review", "finish_check",
	},
	"docs_web": {
		"docs", "docs_add", "web", "browser", "web_search",
	},
	"memory": {
		"hints", "glossary", "agent_memory",
	},
	"experimental": {
		"agent_plan", "agent_execute_todo",
	},
	"orchestration": {
		"orchestration", "orchestrate", "orchestration_rerun", "orchestration_feedback",
		"run_trace", "explain_run", "orchestration_memory",
	},
	"composite": {
		"investigate", "edit_cycle", "preflight",
	},
	"ops": {
		"remote_list", "remote_exec", "log_read", "db_query", "db_schema",
		"run_alias", "env_context", "ci_status",
	},
	"meta": {
		"usage_report",
	},
}

// CLIOnlyTools are available via the codehelper CLI but not registered on the MCP server.
var CLIOnlyTools = []string{"expand_request", "select_pattern"}

// MCPToolCatalog is bootstrap metadata about the MCP surface (no LLM, any project).
type MCPToolCatalog struct {
	Count         int                 `json:"mcp_tool_count"`
	Main          []string            `json:"mcp_main_tools"`
	ParamKeys     string              `json:"mcp_param_keys"`
	ContractPath  string              `json:"tool_contract_path,omitempty"`
	ReferencePath string              `json:"tools_reference_path,omitempty"`
	ByGroup       map[string][]string `json:"mcp_tools_by_group,omitempty"`
	CLIONly       []string            `json:"cli_only_tools,omitempty"`
}

// MCPToolCatalogBrief returns the cheap fields every project_context should carry.
func MCPToolCatalogBrief() MCPToolCatalog {
	return MCPToolCatalog{
		Count:         len(AllMCPToolNames()),
		Main:          append([]string(nil), MCPMainTools...),
		ParamKeys:     MCPParamKeys,
		ContractPath:  MCPToolContractPath,
		ReferencePath: MCPToolsReferencePath,
	}
}

// MCPToolCatalogFull adds grouped names for documentation / onboarding tasks.
func MCPToolCatalogFull() MCPToolCatalog {
	out := MCPToolCatalogBrief()
	out.ByGroup = copyToolGroups(mcpToolsByGroup)
	out.CLIONly = append([]string(nil), CLIOnlyTools...)
	return out
}

// AllMCPToolNames returns every MCP-registered tool name for this binary's
// product modules, sorted. Default full builds match the static catalog;
// selective -tags ch_modules builds omit gated module tools.
func AllMCPToolNames() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 60)
	for _, names := range mcpToolsByGroup {
		for _, n := range names {
			if !product.ToolEnabled(n) {
				continue
			}
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func copyToolGroups(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		filtered := make([]string, 0, len(v))
		for _, n := range v {
			if product.ToolEnabled(n) {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		out[k] = filtered
	}
	return out
}
