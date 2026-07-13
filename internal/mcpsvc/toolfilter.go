package mcpsvc

import (
	"context"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Minimal-tools mode. A large tool catalog costs tokens before the task even
// starts — every tool's name, description, and JSON schema ship in the model's
// context on connect, and Cursor's ~40-tool soft cap / VS Code's per-request
// budget make that pressure real. Minimal mode trims the advertised surface
// (tools/list) down to the focused set — the high-frequency main tools plus the
// graph-navigation specialists (see MinimalToolSet) — so the model spends fewer
// tokens up front and selects tools more reliably without losing the
// callers/impact/tests navigation that is codehelper's whole point.
//
// It is a *listing* filter, not a kill switch: hidden tools remain fully
// callable by name (a client that cached the full list, or that calls a
// specialist tool directly, still works). To keep the hidden tools discoverable
// the project_context bootstrap always emits the full grouped catalog while
// minimal mode is active (see projectContextHandler), so the agent can call any
// of them on demand.
//
// Two switches turn it on, checked in this order:
//   - CODEHELPER_MINIMAL_TOOLS (global env): forces it for every project.
//   - projcfg.MinimalTools (per-project): opt-in for one repo via
//     `codehelper config project --minimal on`.

// minimalToolsEnv reports the global CODEHELPER_MINIMAL_TOOLS switch.
func minimalToolsEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_MINIMAL_TOOLS"))) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true
	}
	return false
}

// minimalModeActive reports whether the advertised tool surface should be
// trimmed for this call's resolved project: global env wins, else the resolved
// project's config. An unresolved project falls back to the full surface.
func minimalModeActive(ctx context.Context, reg *registry.Registry) bool {
	if minimalToolsEnv() {
		return true
	}
	if reg == nil {
		return false
	}
	root := filterRepoRoot(ctx, reg)
	if root == "" {
		return false
	}
	return gateConfig(root).MinimalTools
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

// minimalToolFilter is the server tool filter: when minimal mode is active it
// returns the focused tool set (main tools + graph-navigation specialists),
// otherwise the list is passed through untouched.
func minimalToolFilter(reg *registry.Registry) server.ToolFilterFunc {
	return func(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
		if !minimalModeActive(ctx, reg) {
			return tools
		}
		out := make([]mcp.Tool, 0, len(MinimalToolSet))
		for _, t := range tools {
			if IsFocusedTool(t.Name) {
				out = append(out, t)
			}
		}
		return out
	}
}
