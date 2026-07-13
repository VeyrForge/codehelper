package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// gateMiddleware enforces the per-project ToolsEnabled switch on every tool call.
//
// The MCP server is a single global process that resolves the target project per
// call, so tools cannot be hidden from tools/list per project — the gate has to
// act at call time. For a project with tools disabled it shadow-executes: the
// real handler still runs so its full result and token cost are recorded (the
// "what codehelper would have injected" baseline), but the agent is handed a
// short redirect notice instead, so it falls back to its built-in tools. Enabled
// projects pass straight through and are logged by the usage hook as before.
func gateMiddleware(reg *registry.Registry) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			repoRoot := gateRepoRoot(ctx, reg, &req)
			// Unresolved project, or tools enabled: behave exactly as before. The
			// usage hook handles recording for the enabled path.
			if repoRoot == "" {
				return next(ctx, req)
			}
			cfg := gateConfig(repoRoot)
			if cfg.ToolsEnabled {
				return next(ctx, req)
			}

			start := time.Now()
			res, err := next(ctx, req)
			latency := time.Since(start).Milliseconds()

			// Record the real (shadow) result against the project, marked disabled,
			// before masking it. Best-effort, like all telemetry.
			if cfg.Recording() {
				recordCall(ctx, repoRoot, &req, usageResultText(res), latency, err != nil || callResultIsError(res), true)
			}
			// The agent never sees the tool output (or the handler error): it gets a
			// redirect so the project genuinely runs without codehelper's context.
			return disabledNotice(req.Params.Name), nil
		}
	}
}

// disabledNotice is what a gated project returns to the agent in place of every
// tool result: a short, deterministic steer back to the built-in tools, naming
// the tool so the transcript still shows what was attempted.
func disabledNotice(tool string) *mcp.CallToolResult {
	return mcp.NewToolResultText(fmt.Sprintf(
		"codehelper tools are disabled for this project (baseline / tracking-only mode). "+
			"Use your built-in tools (Read, Grep, Glob, Bash) for this task — the %q call was "+
			"recorded for comparison but intentionally returns no result. "+
			"Re-enable with: codehelper config project --tools on", tool))
}

// gateRepoRoot resolves the project root for a call, reusing the usage
// recorder's per-session cache so resolution (which may round-trip roots) runs
// once per session, not once per call — the same cache the usage hook uses.
func gateRepoRoot(ctx context.Context, reg *registry.Registry, req *mcp.CallToolRequest) string {
	session := sessionIDFromContext(ctx)
	repoArg := argRepo(req)
	return usageRecorder.RepoRoot(session, repoArg, func() string {
		e, err := resolveRepo(ctx, reg, repoArg)
		if err != nil {
			return ""
		}
		return e.RootPath
	})
}

// projCfgEntry caches a project's config keyed by the file's mtime so a CLI
// toggle (written by a different process) is picked up without restarting the
// long-lived MCP server, while a tool call doesn't re-read disk every time.
type projCfgEntry struct {
	mod time.Time
	cfg projcfg.Config
}

var projCfgCache sync.Map // repoRoot -> projCfgEntry

// gateConfig returns the project's config, re-reading only when the file's mtime
// changed since the cached read. An absent file maps to a zero mtime and the
// Default() config, so un-configured projects keep the historical behavior.
func gateConfig(repoRoot string) projcfg.Config {
	if repoRoot == "" {
		return projcfg.Default()
	}
	var mod time.Time
	if fi, err := os.Stat(projcfg.Path(repoRoot)); err == nil {
		mod = fi.ModTime()
	}
	if v, ok := projCfgCache.Load(repoRoot); ok {
		if e, _ := v.(projCfgEntry); e.mod.Equal(mod) {
			return e.cfg
		}
	}
	cfg, err := projcfg.Load(repoRoot)
	if err != nil {
		cfg = projcfg.Default()
	}
	projCfgCache.Store(repoRoot, projCfgEntry{mod: mod, cfg: cfg})
	return cfg
}
