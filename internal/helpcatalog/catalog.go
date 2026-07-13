package helpcatalog

import "github.com/VeyrForge/codehelper/internal/mcpsvc"

// Tool is one MCP tool catalog entry.
type Tool struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Summary string `json:"summary"`
	Params  string `json:"params,omitempty"`
	Main    bool   `json:"main,omitempty"`
}

// CLI is one codehelper CLI command catalog entry.
type CLI struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Summary string `json:"summary"`
	Related string `json:"related,omitempty"`
}

// Filter selects catalog slices.
type Filter struct {
	Query    string
	Group    string
	MainOnly bool
}

// Overview is the top-level help payload.
type Overview struct {
	ParamKeys     string              `json:"param_keys"`
	MainTools     []string            `json:"main_tools"`
	ToolCount     int                 `json:"tool_count"`
	CLIGroups     map[string][]string `json:"cli_groups,omitempty"`
	ToolsByGroup  map[string][]string `json:"tools_by_group,omitempty"`
	CLIOnlyTools  []string            `json:"cli_only_tools,omitempty"`
	ReferencePath string              `json:"reference_path,omitempty"`
}

// OverviewData builds the default help overview.
func OverviewData() Overview {
	meta := mcpsvc.MCPToolCatalogFull()
	return Overview{
		ParamKeys:     meta.ParamKeys,
		MainTools:     meta.Main,
		ToolCount:     meta.Count,
		ToolsByGroup:  meta.ByGroup,
		CLIGroups:     CLIGroups(),
		CLIOnlyTools:  meta.CLIONly,
		ReferencePath: meta.ReferencePath,
	}
}

// ResolveTopic finds a tool, CLI command, or group by name.
// kind is "tool", "cli", or "group"; empty means not found.
func ResolveTopic(name string) (kind string, tool *Tool, cli *CLI, group []string) {
	if t := ToolByName(name); t != nil {
		return "tool", t, nil, nil
	}
	if c := CLIByName(name); c != nil {
		return "cli", nil, c, nil
	}
	meta := mcpsvc.MCPToolCatalogFull()
	if names, ok := meta.ByGroup[name]; ok {
		return "group", nil, nil, names
	}
	for g, names := range CLIGroups() {
		if g == name {
			return "cli_group", nil, nil, names
		}
	}
	return "", nil, nil, nil
}
