package mcpsvc

import (
	"context"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Tool profiles trim the advertised tools/list surface. A large catalog costs
// tokens before the task starts — every tool's name, description, and JSON
// schema ship in the model's context on connect, and Cursor's ~40-tool soft
// cap / VS Code's per-request budget make that pressure real.
//
// Profiles are a *listing* filter, not a kill switch: hidden tools remain fully
// callable by name. project_context still emits the full grouped catalog while
// a trimmed profile is active so agents can call specialists on demand.
//
// Resolution order (first match wins):
//  1. CODEHELPER_TOOL_PROFILE=core|focused|full (global)
//  2. CODEHELPER_MINIMAL_TOOLS truthy → focused; explicit off/0/false/full → full
//  3. projcfg.MinimalTools → focused
//  4. default → focused (~12 conceptual + composite entry points)
//
// Override to the full ~60-tool advertise list with CODEHELPER_TOOL_PROFILE=full
// (or CODEHELPER_MINIMAL_TOOLS=off). Recommended for coding agents: focused
// (or core for locate/plan-only sessions).

// ToolProfile names the advertised surface.
type ToolProfile string

const (
	ToolProfileCore    ToolProfile = "core"
	ToolProfileFocused ToolProfile = "focused"
	ToolProfileFull    ToolProfile = "full"
)

// DefaultToolProfile is the advertise profile when no env/config override applies.
const DefaultToolProfile = ToolProfileFocused

func normalizeToolProfile(s string) ToolProfile {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "core", "main":
		return ToolProfileCore
	case "focused", "minimal", "min":
		return ToolProfileFocused
	case "full", "all", "off", "0", "false":
		return ToolProfileFull
	default:
		return ""
	}
}

// toolProfileEnv returns a profile forced by CODEHELPER_TOOL_PROFILE, if set.
func toolProfileEnv() ToolProfile {
	return normalizeToolProfile(os.Getenv("CODEHELPER_TOOL_PROFILE"))
}

// minimalToolsEnv reports the legacy CODEHELPER_MINIMAL_TOOLS switch (focused).
func minimalToolsEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_MINIMAL_TOOLS"))) {
	case "1", "true", "on", "yes", "enable", "enabled", "focused", "minimal", "min":
		return true
	}
	return false
}

// minimalToolsEnvForcesFull reports an explicit legacy opt-out to the full
// advertise list (escape hatch when default is focused).
func minimalToolsEnvForcesFull() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_MINIMAL_TOOLS"))) {
	case "0", "false", "off", "no", "disable", "disabled", "full", "all":
		return true
	}
	return false
}

// activeToolProfile resolves which advertise profile applies for this call.
func activeToolProfile(ctx context.Context, reg *registry.Registry) ToolProfile {
	if p := toolProfileEnv(); p != "" {
		return p
	}
	if minimalToolsEnv() {
		return ToolProfileFocused
	}
	if minimalToolsEnvForcesFull() {
		return ToolProfileFull
	}
	if reg != nil {
		root := filterRepoRoot(ctx, reg)
		if root != "" && gateConfig(root).MinimalTools {
			return ToolProfileFocused
		}
	}
	return DefaultToolProfile
}

// minimalModeActive is true when tools/list is trimmed (core or focused).
func minimalModeActive(ctx context.Context, reg *registry.Registry) bool {
	p := activeToolProfile(ctx, reg)
	return p == ToolProfileCore || p == ToolProfileFocused
}

// profileToolSet returns the advertise allow-list for a trimmed profile.
func profileToolSet(p ToolProfile) []string {
	switch p {
	case ToolProfileCore:
		return CoreToolSet
	case ToolProfileFocused:
		return FocusedToolSet
	default:
		return nil
	}
}

// IsProfileTool reports whether name is advertised for the given profile.
func IsProfileTool(profile ToolProfile, name string) bool {
	set := profileToolSet(profile)
	if set == nil {
		return true
	}
	for _, n := range set {
		if n == name {
			return true
		}
	}
	return false
}

// filterRepoRoot resolves the project root for a tools/list call. Unlike a tool
// call there is no repo argument, so it resolves from the session's roots and
// reuses the usage recorder's per-session cache (shared with gateRepoRoot) so
// the resolution round-trip happens once per session, not once per list.
func filterRepoRoot(ctx context.Context, reg *registry.Registry) string {
	session := sessionIDFromContext(ctx)
	return usageRecorder.RepoRoot(session, "", func() string {
		e, err := resolveRepo(ctx, reg, "")
		if err != nil {
			return ""
		}
		return e.RootPath
	})
}

// minimalToolFilter is the server tool filter: when a trimmed profile is active
// it returns that allow-list; otherwise the list is passed through untouched.
func minimalToolFilter(reg *registry.Registry) server.ToolFilterFunc {
	return func(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
		profile := activeToolProfile(ctx, reg)
		set := profileToolSet(profile)
		if set == nil {
			return tools
		}
		allow := make(map[string]struct{}, len(set))
		for _, n := range set {
			allow[n] = struct{}{}
		}
		out := make([]mcp.Tool, 0, len(set))
		for _, t := range tools {
			if _, ok := allow[t.Name]; ok {
				out = append(out, t)
			}
		}
		return out
	}
}
