package mcpsvc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/green"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/mark3labs/mcp-go/server"
)

// Run starts MCP over stdio when httpAddr is empty, else Streamable HTTP.
// metricsAddr may be empty; falls back to CODEHELPER_METRICS_ADDR when unset.
func Run(reg *registry.Registry, httpAddr, metricsAddr string) error {
	if strings.TrimSpace(metricsAddr) == "" {
		metricsAddr = strings.TrimSpace(os.Getenv("CODEHELPER_METRICS_ADDR"))
	}
	StartMetricsServer(metricsAddr)
	startGreenEngine()

	// Diagnostic trace of the whole session to ~/.codehelper/logs/mcp.log: which
	// client connected, what it advertised (esp. the roots capability), every
	// tool call + duration, and any error. This is how we see why a client that
	// shows "connected" still isn't usable. Set CODEHELPER_MCP_LOG=off to disable.
	mcpLog = newMCPDebugLogger()
	cwd, _ := os.Getwd()
	exe, _ := os.Executable()
	mcpLog.event("server_start", map[string]any{
		"version":   version.Current(),
		"transport": transportLabel(httpAddr),
		"cwd":       cwd,
		"exe":       exe,
	})

	// Capability tracking is always on (it gates the roots round-trip); debug
	// logging layers onto the same hook set when enabled.
	hooks := &server.Hooks{}
	registerCapabilityHooks(hooks)
	registerUsageHooks(hooks, reg)
	if mcpLog != nil {
		mcpLog.addHooks(hooks)
	}

	opts := []server.ServerOption{
		server.WithToolCapabilities(false),
		// Without this a panic in any tool handler tears down the stdio loop —
		// the client keeps its "connected" status but every later call fails.
		server.WithRecovery(),
		server.WithHooks(hooks),
		// Per-project tools on/off: for a project in baseline mode this shadow-runs
		// each tool (records it) but returns a redirect so the agent uses built-ins.
		server.WithToolHandlerMiddleware(gateMiddleware(reg)),
		// Minimal-tools mode: trim tools/list to the main tools (env or per-project)
		// to cut tool-definition token cost; hidden tools stay callable by name.
		server.WithToolFilter(minimalToolFilter(reg)),
	}
	s := server.NewMCPServer("codehelper", version.Current(), opts...)
	RegisterAll(s, reg)
	if httpAddr != "" {
		return server.NewStreamableHTTPServer(s).Start(httpAddr)
	}
	return server.ServeStdio(s)
}

// startGreenEngine points this MCP server at the local green engine and keeps it
// alive for the life of the process: it exports the URL env vars (so query-time
// semantic rerank uses them) and launches the supervisor goroutine (spawn if
// down, respawn if killed). All best-effort — when green is disabled or
// unconfigured this is a no-op and codehelper serves its deterministic path.
func startGreenEngine() {
	cfg, ok, err := green.Load()
	if err != nil {
		slog.Warn("green config", "err", err)
		return
	}
	if !ok || !cfg.Enabled {
		return
	}
	if set := green.ExportEnv(cfg); len(set) > 0 {
		slog.Info("green engine enabled", "wired", strings.Join(set, ","))
	}
	go green.Watch(context.Background(), cfg, 20*time.Second, func(f string, a ...any) {
		slog.Info(fmt.Sprintf(f, a...))
	})
}

func transportLabel(httpAddr string) string {
	if strings.TrimSpace(httpAddr) != "" {
		return "http:" + httpAddr
	}
	return "stdio"
}
