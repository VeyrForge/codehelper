package mcpsvc

import "sort"

// MCPParamKeys is a one-line cheat sheet for the most-misused tool parameters.
const MCPParamKeys = "context/context_bundle/impact→name · change_kit→target · trace→from+to · query/search_hybrid→query · kickoff→task · scope→idea"

// MCPMainTools are the high-frequency tools agents should reach for first.
var MCPMainTools = []string{
	"project_context", "query", "scout", "context", "kickoff", "plan", "change_kit", "verify", "orchestrate", "investigate",
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

// minimalNavTools are the graph-navigation + edit/gate specialists that minimal
// mode keeps alongside the main tools. They are codehelper's differentiators
// over a plain file/grep MCP — callers/callees, blast radius, tests-to-run,
// interface impls, signatures, workspace edits, and post-edit gates — so a
// trimmed tools/list can still run add→impact→patch→review→finish end-to-end.
var minimalNavTools = []string{
	"trace", "impact", "test_impact", "find_implementations",
	"api_surface", "diagnostics", "rename_symbol", "insert_at_symbol",
	"search_hybrid", "context_bundle",
	"read_workspace_file", "write_workspace_file",
	"apply_patch_workspace_file", "revert_workspace_edit",
	"review_diff", "review", "finish_check", "dead_code", "hotspots", "since",
}

// MinimalToolSet is the focused surface advertised in tools/list when minimal
// mode is on: the main tools plus the navigation specialists. Everything else
// stays callable by name and discoverable via project_context (see toolfilter.go).
// It is deliberately kept well under the ~40-tool count above which agent
// tool-selection accuracy measurably degrades.
var MinimalToolSet = func() []string {
	out := append([]string(nil), MCPMainTools...)
	return append(out, minimalNavTools...)
}()

// IsFocusedTool reports whether name is advertised while minimal mode is active.
func IsFocusedTool(name string) bool {
	for _, n := range MinimalToolSet {
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
		"plan", "change_kit", "rename_symbol", "insert_at_symbol",
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

// AllMCPToolNames returns every MCP-registered tool name, sorted.
func AllMCPToolNames() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 60)
	for _, names := range mcpToolsByGroup {
		for _, n := range names {
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
		out[k] = append([]string(nil), v...)
	}
	return out
}
