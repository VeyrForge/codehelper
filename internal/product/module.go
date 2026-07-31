package product

import "sort"

// ID identifies a product module.
type ID string

const (
	Core    ID = "core"
	Edit    ID = "edit"
	Check   ID = "check"
	Browser ID = "browser"
	Ops     ID = "ops"
	Team    ID = "team"
)

// Module is one installable product surface.
type Module struct {
	ID          ID     `json:"id"`
	Name        string `json:"name"`
	Purpose     string `json:"purpose"`
	BuildTag    string `json:"build_tag,omitempty"` // empty for core (always on)
	Optional    bool   `json:"optional,omitempty"`  // true = off in default full bundle
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`
}

// Catalog lists every known product module with compile-time Enabled flags.
func Catalog() []Module {
	return []Module{
		{
			ID:          Core,
			Name:        "codehelper-core",
			Purpose:     "Index, graph, search, and MCP bootstrap",
			Enabled:     true,
			Description: "Always compiled. Indexer, retrieval, project_context, query, context, impact, kickoff.",
		},
		{
			ID:          Edit,
			Name:        "codehelper-edit",
			Purpose:     "Symbol edits and refactoring",
			BuildTag:    "ch_edit",
			Enabled:     editOn,
			Description: "rename_symbol, insert_at_symbol (workspace write/patch stay core until further split).",
		},
		{
			ID:          Check,
			Name:        "codehelper-check",
			Purpose:     "Diagnostics, reviews, and finish gates",
			BuildTag:    "ch_check",
			Enabled:     checkOn,
			Description: "review tool registration gated; verify/diagnostics/finish_check still core until extracted.",
		},
		{
			ID:          Browser,
			Name:        "codehelper-browser",
			Purpose:     "Browser automation",
			BuildTag:    "ch_browser",
			Enabled:     browserOn,
			Description: "Requires rod build tag for headless Chromium tier (added by build scripts with this module).",
		},
		{
			ID:          Ops,
			Name:        "codehelper-ops",
			Purpose:     "SSH, databases, logs, and CI",
			BuildTag:    "ch_ops",
			Enabled:     opsOn,
			Description: "remote_*, log_read, db_*, run_alias, env_context, ci_status.",
		},
		{
			ID:          Team,
			Name:        "codehelper-team",
			Purpose:     "Shared indexes, policies, and organization memory",
			BuildTag:    "ch_team",
			Optional:    true,
			Enabled:     teamOn,
			Description: "Scaffold only — no MCP tools yet. Opt-in even in the default full bundle.",
		},
	}
}

// SelectMode reports whether this binary was built with -tags ch_modules
// (selective product set). False means the default full bundle.
func SelectMode() bool { return selectMode }

// EditEnabled reports whether the edit module is compiled in.
func EditEnabled() bool { return editOn }

// CheckEnabled reports whether the check module is compiled in.
func CheckEnabled() bool { return checkOn }

// BrowserEnabled reports whether the browser product module is compiled in.
// The headless rod tier is separately gated by the rod tag (web.BrowserAvailable).
func BrowserEnabled() bool { return browserOn }

// OpsEnabled reports whether the ops module is compiled in.
func OpsEnabled() bool { return opsOn }

// TeamEnabled reports whether the team scaffold module is compiled in.
func TeamEnabled() bool { return teamOn }

// EnabledIDs returns enabled module IDs in stable order.
func EnabledIDs() []ID {
	out := make([]ID, 0, 6)
	for _, m := range Catalog() {
		if m.Enabled {
			out = append(out, m.ID)
		}
	}
	return out
}

// EnabledNames returns enabled module display names, sorted.
func EnabledNames() []string {
	out := make([]string, 0, 6)
	for _, m := range Catalog() {
		if m.Enabled {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Summary is a short human/agent-facing line for doctor / version.
func Summary() string {
	names := EnabledNames()
	if !selectMode {
		return "full bundle (" + joinComma(names) + ")"
	}
	return joinComma(names)
}

func joinComma(ss []string) string {
	switch len(ss) {
	case 0:
		return ""
	case 1:
		return ss[0]
	default:
		out := ss[0]
		for i := 1; i < len(ss); i++ {
			out += ", " + ss[i]
		}
		return out
	}
}

// ToolModule maps an MCP tool name to the product module that owns it.
// Tools not listed belong to core.
func ToolModule(name string) ID {
	if m, ok := toolModule[name]; ok {
		return m
	}
	return Core
}

// ToolEnabled reports whether an MCP tool should be registered / catalogued
// for this binary's product set.
func ToolEnabled(name string) bool {
	switch ToolModule(name) {
	case Edit:
		return editOn
	case Check:
		return checkOn
	case Browser:
		return browserOn
	case Ops:
		return opsOn
	case Team:
		return teamOn
	default:
		return true
	}
}

// toolModule is the scaffold ownership map. Keep in sync with mcpsvc registration
// gates — TestToolModuleCoverage guards known gated tools.
var toolModule = map[string]ID{
	// edit
	"rename_symbol":    Edit,
	"insert_at_symbol": Edit,
	// check (partial — review only for now)
	"review": Check,
	// browser
	"browser": Browser,
	// ops
	"remote_list": Ops,
	"remote_exec": Ops,
	"log_read":    Ops,
	"db_query":    Ops,
	"db_schema":   Ops,
	"run_alias":   Ops,
	"env_context": Ops,
	"ci_status":   Ops,
}
