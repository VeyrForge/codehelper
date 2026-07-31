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
// act at call time. When tools are disabled:
//   - a small explicit allowlist of pure graph/index tools may still shadow-execute
//     so baseline token cost is recorded;
//   - everything else (writes, verify, browser, remote, diagnostics, orchestration,
//     DB/network/LSP, composites that can reach those) is NOT invoked — only a
//     redirect notice is returned.
//
// Enabled projects pass straight through and are logged by the usage hook as before.
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

			tool := req.Params.Name
			start := time.Now()
			var res *mcp.CallToolResult
			var err error
			var respText string
			isErr := false
			if toolAllowsDisabledShadow(tool) {
				// Shadow-benchmark allowlisted pure graph tools only; never run side effects.
				res, err = next(ctx, req)
				respText = usageResultText(res)
				isErr = err != nil || callResultIsError(res)
			} else {
				respText = "skipped: side-effect tool not shadow-executed while tools disabled"
			}
			latency := time.Since(start).Milliseconds()

			if cfg.Recording() {
				recordCall(ctx, repoRoot, &req, respText, latency, isErr, true)
			}
			// The agent never sees the tool output (or the handler error): it gets a
			// redirect so the project genuinely runs without codehelper's context.
			return disabledNotice(tool), nil
		}
	}
}

// toolAllowsDisabledShadow reports whether a tool may run for baseline measurement
// while ToolsEnabled is false. Fail-closed: only the explicit disabledShadowAllowlist
// may run. ReadOnlyHint is NOT the source of truth — many annotReadOnly* tools still
// run builds, DB/network I/O, or orchestration that reaches those.
func toolAllowsDisabledShadow(name string) bool {
	_, ok := disabledShadowAllowlist[name]
	return ok
}

// disabledShadowAllowlist is the ONLY source for disabled-mode shadow execution.
// Pure graph/index reads only. Do NOT derive this from ReadOnlyHint: diagnostics
// (go build/vet, cargo check, custom command=), orchestrate, db_*, kickoff/docs/
// web_search, lsp, and ci_status are annotReadOnly* but must not run when disabled.
var disabledShadowAllowlist = map[string]struct{}{
	"project_context": {},
	"query":           {},
	"context":         {},
	"impact":          {},
	"trace":           {},
	"scout":           {},
	"similar":         {},
	"hotspots":        {},
	"test_impact":     {},
	"api_surface":     {},
	"ast_query":       {},
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
