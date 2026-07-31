package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Phoenix densification on the Elixir graph: Router get/post/live/resources →
// Controller/LiveView actions, Controller/LiveView module roles, and plug
// pipeline filters — so context/impact/trace reach PageController#index from
// router.ex without grepping (mirrors Rails + ASP.NET route densify).

var (
	phoenixRouteTo = regexp.MustCompile(
		`(?i)^\s*(get|post|put|patch|delete|head|options|match)\s+.+?,\s*([A-Za-z0-9_.]+)\s*,\s*:([a-z0-9_?!]+)`)
	phoenixLiveRoute = regexp.MustCompile(
		`(?i)^\s*live\s+.+?,\s*([A-Za-z0-9_.]+)\s*(?:,\s*:([a-z0-9_?!]+))?`)
	phoenixResources = regexp.MustCompile(
		`(?i)^\s*(resources|resource)\s+["'][/A-Za-z0-9_\-:]+["']\s*,\s*([A-Za-z0-9_.]+)`)
	phoenixPlug = regexp.MustCompile(
		`(?i)^\s*plug\s+:([a-z0-9_?!]+)`)
	phoenixPipeThrough = regexp.MustCompile(
		`(?i)^\s*pipe_through\s+:([a-z0-9_?!]+)`)
)

func looksLikePhoenixFile(relPath, content string) bool {
	p := strings.ToLower(filepath.ToSlash(relPath))
	body := content
	lower := strings.ToLower(body)
	if strings.Contains(lower, "phoenix.controller") ||
		strings.Contains(lower, "phoenix.liveview") ||
		strings.Contains(lower, "phoenix.router") ||
		strings.Contains(lower, "phoenix.channel") ||
		strings.Contains(lower, "use phoenix.") ||
		strings.Contains(body, "Phoenix.Controller") ||
		strings.Contains(body, "Phoenix.LiveView") ||
		strings.Contains(body, "Phoenix.Router") ||
		strings.Contains(body, "pipe_through") ||
		strings.Contains(body, "handle_event(") && (strings.Contains(lower, "live_view") || strings.Contains(lower, "liveview") || strings.HasSuffix(p, "_live.ex")) ||
		strings.Contains(p, "/controllers/") || strings.Contains(p, "/live/") ||
		strings.HasSuffix(p, "_controller.ex") || strings.HasSuffix(p, "_live.ex") ||
		strings.HasSuffix(p, "/router.ex") ||
		(strings.Contains(body, " use ") && (strings.Contains(body, ", :controller") ||
			strings.Contains(body, ", :live_view") || strings.Contains(body, ", :router"))) {
		return true
	}
	return false
}

func phoenixModuleRole(modName, content string) string {
	lower := strings.ToLower(content)
	name := modName
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case strings.Contains(lower, "phoenix.liveview") || strings.Contains(lower, ", :live_view") ||
		strings.HasSuffix(name, "Live") || strings.HasSuffix(strings.ToLower(name), "live"):
		return "live_view"
	case strings.Contains(lower, "phoenix.controller") || strings.Contains(lower, ", :controller") ||
		strings.HasSuffix(name, "Controller"):
		return "controller"
	case strings.Contains(lower, "phoenix.router") || strings.Contains(lower, ", :router") ||
		name == "Router" || strings.HasSuffix(name, "Router"):
		return "router"
	case strings.Contains(lower, "phoenix.channel") || strings.HasSuffix(name, "Channel"):
		return "channel"
	default:
		return ""
	}
}

func phoenixDefRole(modRole, defName string) string {
	switch modRole {
	case "controller":
		switch defName {
		case "init", "action", "call":
			return ""
		default:
			return "entrypoint"
		}
	case "live_view":
		switch defName {
		case "mount", "render", "handle_event", "handle_info", "handle_params", "terminate", "update":
			return "entrypoint"
		default:
			return ""
		}
	case "router":
		return ""
	default:
		return ""
	}
}

func emitPhoenixCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) && !strings.Contains(name, ".") {
		return
	}
	leaf := name
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		leaf = name[i+1:]
	}
	if !isCallableName(leaf) {
		return
	}
	tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, fromSym, tgt, "calls"),
		RepoID:     repoID,
		Kind:       types.RefKindCalls,
		SourceID:   fromSym,
		TargetID:   tgt,
		Confidence: conf,
	})
}

// extractPhoenixDSL indexes Phoenix router registrations, plugs, and
// pipe_through so locate/impact can reach PageController / DashboardLive
// from router.ex.
func extractPhoenixDSL(repoID, relPath string, buf []byte, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	if !looksLikePhoenixFile(relPath, src) {
		return
	}
	fw := "frameworks=phoenix"
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if m := phoenixRouteTo.FindStringSubmatch(line); len(m) > 3 {
			verb := strings.ToLower(m[1])
			ctrl := phoenixLeafModule(m[2])
			action := m[3]
			siteName := fmt.Sprintf("phoenix_%s_%s_%s_%d", verb, strings.ToLower(ctrl), action, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "elixir", fw+";role=entrypoint", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPhoenixCall(repoID, relPath, sym.ID, ctrl, 0.9, out)
			emitPhoenixCall(repoID, relPath, sym.ID, action, 0.85, out)
			continue
		}
		if m := phoenixLiveRoute.FindStringSubmatch(line); len(m) > 1 {
			live := phoenixLeafModule(m[1])
			action := "index"
			if len(m) > 2 && m[2] != "" {
				action = m[2]
			}
			siteName := fmt.Sprintf("phoenix_live_%s_%s_%d", strings.ToLower(live), action, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "elixir", fw+";role=entrypoint", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPhoenixCall(repoID, relPath, sym.ID, live, 0.9, out)
			emitPhoenixCall(repoID, relPath, sym.ID, action, 0.8, out)
			continue
		}
		if m := phoenixResources.FindStringSubmatch(line); len(m) > 2 {
			kind := strings.ToLower(m[1])
			ctrl := phoenixLeafModule(m[2])
			siteName := fmt.Sprintf("phoenix_%s_%s_%d", kind, strings.ToLower(ctrl), i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "elixir", fw+";role=entrypoint", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPhoenixCall(repoID, relPath, sym.ID, ctrl, 0.9, out)
			continue
		}
		if m := phoenixPlug.FindStringSubmatch(line); len(m) > 1 {
			filter := m[1]
			siteName := fmt.Sprintf("phoenix_plug_%s_%d", filter, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "elixir", fw+";role=filter", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPhoenixCall(repoID, relPath, sym.ID, filter, 0.88, out)
			continue
		}
		if m := phoenixPipeThrough.FindStringSubmatch(line); len(m) > 1 {
			pipe := m[1]
			siteName := fmt.Sprintf("phoenix_pipe_%s_%d", pipe, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "elixir", fw+";role=filter", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPhoenixCall(repoID, relPath, sym.ID, pipe, 0.75, out)
		}
	}
}

func phoenixLeafModule(mod string) string {
	mod = strings.TrimSpace(mod)
	if mod == "" {
		return ""
	}
	if i := strings.LastIndexByte(mod, '.'); i >= 0 && i+1 < len(mod) {
		return mod[i+1:]
	}
	return mod
}
