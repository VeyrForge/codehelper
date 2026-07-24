package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterCompositeTools wires fused multi-step MCP tools (investigate, edit_cycle, preflight).
func RegisterCompositeTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	h := coreToolHandlers(regRef)

	s.AddTool(mcp.NewTool("investigate",
		mcp.WithDescription("Fused investigation: by default query + context + impact + test_impact. Pass recipe=architecture|dead_code|security|perf for specialized audits (architect Q&A pack, dead_code candidates, whole-repo security sink scan + review_diff, or hotspots+N+1 query pack+impact). Returns a compact TOON bundle by default (format=json for JSON) — replaces chained MCP calls."),
		mcp.WithString("query", mcp.Description("What to find / investigate (required unless recipe=architecture|dead_code|security|perf or target is set; aliases: q, prompt)")),
		mcp.WithString("recipe", mcp.Description("Optional audit recipe: architecture | dead_code | security | perf (aliases: architect, design, unused, vuln, performance)")),
		mcp.WithString("target", mcp.Description("Optional symbol name or sym: id (skips query when set; aliases: name, symbol, sym)")),
		mcp.WithString("path", mcp.Description("Disambiguate target by definition file path")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("investigate", investigateHandler(regRef, h)))

	s.AddTool(mcp.NewTool("edit_cycle",
		mcp.WithDescription("Post-edit loop: optional change_kit preview, apply_patch, index refresh, since (changed symbols + blast radius), and diagnostics. Fuses the edit → verify → re-index → respond workflow."),
		mcp.WithString("target", mcp.Description("Symbol for change_kit preview")),
		mcp.WithString("patch", mcp.Description("Unified diff to apply via apply_patch_workspace_file")),
		mcp.WithString("path", mcp.Description("File path for apply_patch when patch is set")),
		mcp.WithBoolean("refresh_index", mcp.Description("Run a local index refresh after patch (default true when patch set)"), mcp.DefaultBool(true)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotTaskMutate(),
	), timedTool("edit_cycle", editCycleHandler(regRef, h)))

	s.AddTool(mcp.NewTool("preflight",
		mcp.WithDescription("Release gate bundle in one call: detect_changes + review_diff + finish_check. Use immediately before claiming done to avoid missing a gate step. Read-only except finish_check verify flags you pass in."),
		mcp.WithString("base_ref", mcp.Description("Git ref for detect_changes (default HEAD~1)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("preflight", preflightHandler(regRef, h)))
}

func investigateHandler(reg *registry.Registry, h map[string]server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repoName := argString(args, "repo")
		format := resolveFormat(args)
		recipe := normalizeInvestigateRecipe(argString(args, "recipe"))
		if recipe != "" {
			return investigateRecipe(ctx, reg, h, recipe, repoName, format, args)
		}
		target := strings.TrimSpace(argFirst(args, "target", "name", "symbol", "sym"))
		pathHint := strings.TrimSpace(argString(args, "path"))
		out := map[string]any{"steps": []string{}}
		appendStep := func(name string, body any) {
			out["steps"] = append(out["steps"].([]string), name)
			out[name] = body
		}

		if target == "" {
			q := strings.TrimSpace(argFirst(args, "query", "q", "prompt"))
			if q == "" {
				return mcp.NewToolResultError("query is required when target/recipe is not set"), nil
			}
			raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
				"repo": repoName, "query": q, "top_k": 8, "format": "json",
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			appendStep("query", jsonRaw(raw))
			target = firstSymbolFromJSON(raw)
			if id := firstSymbolIDFromJSON(raw); id != "" {
				target = id
			}
		}
		if target == "" {
			return mustToolResultFormatted(map[string]any{
				"error": "no symbol found — refine query or pass target/sym: id",
				"query": out["query"],
			}, format)
		}

		ctxArgs := map[string]any{"repo": repoName, "name": target, "format": "json"}
		if pathHint != "" {
			ctxArgs["path"] = pathHint
		}
		if raw, err := callHandlerJSON(ctx, h, "context", ctxArgs); err == nil {
			appendStep("context", jsonRaw(raw))
		} else {
			out["context_error"] = err.Error()
		}

		impArgs := map[string]any{"repo": repoName, "target": target, "direction": "upstream", "depth": 2, "format": "json"}
		if pathHint != "" {
			impArgs["path"] = pathHint
		}
		if raw, err := callHandlerJSON(ctx, h, "impact", impArgs); err == nil {
			appendStep("impact", jsonRaw(raw))
		}

		if raw, err := callHandlerJSON(ctx, h, "test_impact", map[string]any{
			"repo": repoName, "target": target, "format": "json",
		}); err == nil {
			appendStep("test_impact", jsonRaw(raw))
		}

		out["target"] = target
		out["next_queries"] = []string{
			"context on " + target + " — read source + callers before editing",
			"impact target=" + target + " direction=upstream — check blast radius",
			"change_kit target=" + target + " — then diagnostics → review_diff → verify",
		}
		out["what_next"] = "Inspect `" + target + "` via `context`, then `impact` before any edit."
		return mustToolResultFormatted(out, format)
	}
}

func normalizeInvestigateRecipe(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "architecture", "architect", "arch", "design", "architecture_qa":
		return "architecture"
	case "dead_code", "deadcode", "unused", "dead":
		return "dead_code"
	case "security", "vuln", "vulnerability", "secure":
		return "security"
	case "perf", "performance", "hotspot", "hotspots":
		return "perf"
	default:
		return ""
	}
}

func investigateRecipe(ctx context.Context, reg *registry.Registry, h map[string]server.ToolHandlerFunc, recipe, repoName, format string, args map[string]any) (*mcp.CallToolResult, error) {
	out := map[string]any{"recipe": recipe, "steps": []string{}}
	appendStep := func(name string, body any) {
		out["steps"] = append(out["steps"].([]string), name)
		out[name] = body
	}
	switch recipe {
	case "architecture":
		q := strings.TrimSpace(argFirst(args, "query", "target"))
		if q == "" {
			q = "entrypoint"
		}
		if raw, err := callHandlerJSON(ctx, h, "kickoff", map[string]any{
			"repo": repoName, "task": q, "role": "architect", "format": "json",
		}); err == nil {
			appendStep("kickoff", jsonRaw(raw))
		} else {
			out["kickoff_error"] = err.Error()
		}
		if raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
			"repo": repoName, "query": q, "top_k": 8, "include_context_pack": true, "format": "json",
		}); err == nil {
			appendStep("query", jsonRaw(raw))
			target := firstSymbolIDFromJSON(raw)
			if target == "" {
				target = firstSymbolFromJSON(raw)
			}
			pathHint := strings.TrimSpace(argString(args, "path"))
			if target != "" {
				ctxArgs := map[string]any{"repo": repoName, "name": target, "format": "json"}
				if pathHint != "" {
					ctxArgs["path"] = pathHint
				}
				if raw2, err2 := callHandlerJSON(ctx, h, "context", ctxArgs); err2 == nil {
					appendStep("context", jsonRaw(raw2))
				}
				impArgs := map[string]any{
					"repo": repoName, "target": target, "direction": "upstream", "depth": 2, "format": "json",
				}
				if pathHint != "" {
					impArgs["path"] = pathHint
				}
				if raw2, err2 := callHandlerJSON(ctx, h, "impact", impArgs); err2 == nil {
					appendStep("impact", jsonRaw(raw2))
				}
				if raw2, err2 := callHandlerJSON(ctx, h, "trace", map[string]any{
					"repo": repoName, "target": target, "format": "json",
				}); err2 == nil {
					appendStep("trace", jsonRaw(raw2))
				}
			}
		}
		out["note"] = "Architect mode: cite symbols/paths from kickoff+context+impact. Do not edit until the user accepts the design. If impact is self-only, retry a method target or direction=upstream."
		attachInvestigateVibe(out, "architecture", nil, false)
	case "dead_code":
		if raw, err := callHandlerJSON(ctx, h, "dead_code", map[string]any{
			"repo": repoName, "top_k": 20, "format": "json",
		}); err == nil {
			appendStep("dead_code", jsonRaw(raw))
		} else {
			out["dead_code_error"] = err.Error()
		}
		q := strings.TrimSpace(argString(args, "query"))
		if q == "" {
			q = "unused helper"
		}
		if raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
			"repo": repoName, "query": q, "top_k": 5, "format": "json",
		}); err == nil {
			appendStep("query", jsonRaw(raw))
		}
		out["note"] = "Prefer confidence=high dead_code rows; confirm each with impact(upstream) + a name search before deleting."
		attachInvestigateVibe(out, "dead_code", nil, false)
	case "security":
		// Whole-repo sink scan FIRST — clean trees must still surface candidates.
		repo, rerr := resolveRepoInitialized(ctx, reg, repoName)
		var sinkFindings []auditFinding
		if rerr == nil {
			sinkFindings = collectRepoSecurityFindings(repo.RootPath, 20)
		}
		if len(sinkFindings) > 0 {
			sinkFindings = labelFrameworkFootguns(sinkFindings, security.DetectProjectShape(repo.RootPath))
			appendStep("repo_sink_scan", map[string]any{
				"findings": sinkFindings,
				"count":    len(sinkFindings),
				"note":     "Ranked sink/config candidates with severity, confidence, exploitability. Confirm before treating as confirmed vulns. kind=config_hardening is checklist-only. library_guidance = FRAMEWORK FOOTGUN (not an app CVE).",
			})
		} else {
			out["repo_sink_scan"] = map[string]any{
				"findings": []any{},
				"note":     "No sink or config-hardening patterns matched in a bounded repo walk — not a clean bill of health. Pass a concrete module query or edit then re-run.",
			}
			out["steps"] = append(out["steps"].([]string), "repo_sink_scan")
		}
		if raw, err := callHandlerJSON(ctx, h, "review_diff", map[string]any{
			"repo": repoName, "include_security": true, "format": "json",
		}); err == nil {
			appendStep("review_diff", jsonRaw(raw))
		} else {
			out["review_diff_error"] = err.Error()
		}
		if raw, err := callHandlerJSON(ctx, h, "review", map[string]any{
			"repo": repoName, "format": "json",
		}); err == nil {
			appendStep("review", jsonRaw(raw))
		}
		q := strings.TrimSpace(argFirst(args, "query", "target"))
		if q == "" || isVagueSecurityQuery(q) {
			for _, pq := range securityQueryPack()[:3] {
				if raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
					"repo": repoName, "query": pq, "top_k": 5, "format": "json",
				}); err == nil {
					appendStep("query_pack:"+pq, jsonRaw(raw))
					break // one pack query is enough signal; avoid payload bloat
				}
			}
		} else if raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
			"repo": repoName, "query": q, "top_k": 8, "format": "json",
		}); err == nil {
			appendStep("query", jsonRaw(raw))
		}
		abstainSec := len(sinkFindings) == 0
		if len(sinkFindings) > 0 {
			out["note"] = fmt.Sprintf("Security recipe: %d repo sink candidate(s) plus review_diff/review. Prefer repo_sink_scan file:line over empty HEAD diffs. Demote CSS/DI/Schema false friends. Treat library_guidance as footguns, not CVEs.", len(sinkFindings))
		} else {
			out["note"] = "No high-signal app sinks ranked — that is an honest ABSTAIN, not an empty answer. Treat any library_guidance as footguns (not CVEs). Paste next_queries to hunt auth/SQL/XSS sinks."
			out["abstain"] = "ABSTAIN — no confirmed app exploit path found (OK on frameworks/clean apps). Footguns ≠ CVEs. Paste next_queries."
		}
		attachInvestigateVibe(out, "security", sinkFindings, abstainSec)
	case "perf":
		var hotspotsRaw string
		if raw, err := callHandlerJSON(ctx, h, "hotspots", map[string]any{
			"repo": repoName, "top_k": 10, "format": "json",
		}); err == nil {
			hotspotsRaw = raw
			appendStep("hotspots", jsonRaw(raw))
		} else {
			out["hotspots_error"] = err.Error()
		}
		q := strings.TrimSpace(argFirst(args, "query", "target", "name", "symbol", "sym", "q"))
		shape := security.ShapeApp
		if repo, rerr := resolveRepoInitialized(ctx, reg, repoName); rerr == nil {
			shape = security.DetectProjectShape(repo.RootPath)
		}
		pack := perfQueryPack()
		if shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore {
			pack = perfQueryPackLibrary()
		}
		ranPack := false
		if q == "" || isVaguePerfQuery(q) {
			for _, pq := range pack {
				if len(pq) == 0 {
					continue
				}
				if raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
					"repo": repoName, "query": pq, "top_k": 5, "format": "json",
				}); err == nil {
					appendStep("query_pack:"+pq, jsonRaw(raw))
					ranPack = true
					if id := firstSymbolIDFromJSON(raw); id != "" {
						q = id // use for impact below
					} else if n := firstSymbolFromJSON(raw); n != "" {
						q = n
					}
					break // one pack query is enough signal
				}
			}
			if q == "" || isVaguePerfQuery(q) {
				q = "n+1 prefetch select_related"
			}
		}
		if !ranPack {
			if raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
				"repo": repoName, "query": q, "top_k": 5, "format": "json",
			}); err == nil {
				appendStep("query", jsonRaw(raw))
				if id := firstSymbolIDFromJSON(raw); id != "" {
					q = id
				} else if n := firstSymbolFromJSON(raw); n != "" {
					q = n
				}
			}
		}
		impactTarget := q
		if id := firstHotspotSuggestedTarget(hotspotsRaw); id != "" && (impactTarget == "" || isVaguePerfQuery(impactTarget)) {
			impactTarget = id
		}
		if impactTarget != "" && !isVaguePerfQuery(impactTarget) {
			if raw2, err2 := callHandlerJSON(ctx, h, "impact", map[string]any{
				"repo": repoName, "target": impactTarget, "direction": "upstream", "depth": 2, "format": "json",
			}); err2 == nil {
				appendStep("impact", jsonRaw(raw2))
			}
			if raw2, err2 := callHandlerJSON(ctx, h, "test_impact", map[string]any{
				"repo": repoName, "target": impactTarget, "format": "json",
			}); err2 == nil {
				appendStep("test_impact", jsonRaw(raw2))
			}
		}
		out["note"] = "Optimize hotspots files (churn × centrality) and N+1/hot-path query_pack hits only after impact/test_impact; measure before/after. On sparse graphs, do not trust empty blast_radius. Read why + rewrite_hint on each hotspot row."
		out["perf_query_pack"] = pack[:min(4, len(pack))]
		attachInvestigateVibe(out, "performance", nil, false)
	}
	return mustToolResultFormatted(out, format)
}

// attachInvestigateVibe adds the Cursor-agent vibe contract (what_next + next_queries)
// so investigate recipes match kickoff/plan/scout response shape.
func attachInvestigateVibe(out map[string]any, role string, findings []auditFinding, abstain bool) {
	shape := security.ShapeApp
	vibeRole := role
	switch role {
	case "architecture", "dead_code":
		vibeRole = "feature"
	case "perf":
		vibeRole = "performance"
	}
	next := vibeNextQueries(vibeRole, shape, abstain, role, "")
	abstainNote := ""
	if abstain {
		if s, _ := out["abstain"].(string); s != "" {
			abstainNote = s
		} else {
			abstainNote = "ABSTAIN"
		}
	}
	out["next_queries"] = next
	out["what_next"] = buildWhatNext(vibeRole, nil, findings, abstainNote, next)
	out["recommended_next_tools"] = vibeRecommendedTools(vibeRole, nil, abstain)
}

func editCycleHandler(reg *registry.Registry, h map[string]server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repoName := argString(args, "repo")
		format := resolveFormat(args)
		out := map[string]any{}

		target := strings.TrimSpace(argString(args, "target"))
		if target != "" {
			if raw, err := callHandlerJSON(ctx, h, "change_kit", map[string]any{
				"repo": repoName, "target": target, "format": "json",
			}); err == nil {
				out["change_kit"] = jsonRaw(raw)
			}
		}

		patch := strings.TrimSpace(argString(args, "patch"))
		path := strings.TrimSpace(argString(args, "path"))
		if patch != "" && path != "" {
			if raw, err := callHandlerJSON(ctx, h, "apply_patch_workspace_file", map[string]any{
				"repo": repoName, "path": path, "patch": patch,
			}); err == nil {
				out["apply_patch"] = jsonRaw(raw)
			} else {
				out["apply_patch_error"] = err.Error()
			}
			if argBool(args, "refresh_index", true) {
				if repo, err := resolveRepoInitialized(ctx, reg, repoName); err == nil {
					_ = indexer.Run(ctx, repo.RootPath, indexer.Options{Force: true, RepoName: repo.Name})
					out["index_refresh"] = "complete"
				}
			}
		}

		if raw, err := callHandlerJSON(ctx, h, "since", map[string]any{"repo": repoName, "format": "json"}); err == nil {
			out["since"] = jsonRaw(raw)
		}
		if raw, err := callHandlerJSON(ctx, h, "diagnostics", map[string]any{"repo": repoName, "format": "json"}); err == nil {
			out["diagnostics"] = jsonRaw(raw)
		}

		return mustToolResultFormatted(out, format)
	}
}

func preflightHandler(reg *registry.Registry, h map[string]server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repoName := argString(args, "repo")
		format := resolveFormat(args)
		baseRef := argString(args, "base_ref")
		if baseRef == "" {
			baseRef = "HEAD~1"
		}
		out := map[string]any{}

		if raw, err := callHandlerJSON(ctx, h, "detect_changes", map[string]any{
			"repo": repoName, "base_ref": baseRef, "format": "json",
		}); err == nil {
			out["detect_changes"] = jsonRaw(raw)
		}
		if raw, err := callHandlerJSON(ctx, h, "review_diff", map[string]any{
			"repo": repoName, "format": "json",
		}); err == nil {
			out["review_diff"] = jsonRaw(raw)
		}
		if raw, err := callHandlerJSON(ctx, h, "finish_check", map[string]any{
			"repo": repoName, "format": "json",
		}); err == nil {
			out["finish_check"] = jsonRaw(raw)
		}

		return mustToolResultFormatted(out, format)
	}
}

func callHandlerJSON(ctx context.Context, h map[string]server.ToolHandlerFunc, name string, args map[string]any) (string, error) {
	fn, ok := h[name]
	if !ok {
		return "", fmt.Errorf("tool %q not available", name)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := fn(ctx, req)
	if err != nil {
		return "", err
	}
	if res != nil && res.IsError {
		return "", fmt.Errorf("%s: %s", name, toolResultText(res))
	}
	return toolResultText(res), nil
}

func toolResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
		if tc, ok := c.(*mcp.TextContent); ok && tc != nil {
			return tc.Text
		}
	}
	return ""
}

func jsonRaw(s string) any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

func firstSymbolFromJSON(raw string) string {
	var p map[string]any
	if json.Unmarshal([]byte(raw), &p) != nil {
		return ""
	}
	hits, _ := p["hits"].([]any)
	if len(hits) == 0 {
		return ""
	}
	h0, _ := hits[0].(map[string]any)
	n, _ := h0["name"].(string)
	return n
}

func firstSymbolIDFromJSON(raw string) string {
	var p map[string]any
	if json.Unmarshal([]byte(raw), &p) != nil {
		return ""
	}
	hits, _ := p["hits"].([]any)
	if len(hits) == 0 {
		return ""
	}
	h0, _ := hits[0].(map[string]any)
	id, _ := h0["id"].(string)
	return id
}

// firstHotspotSuggestedTarget pulls a usable impact/query target from a hotspots
// JSON payload (suggested_next_query, or basename of the top file).
func firstHotspotSuggestedTarget(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var p map[string]any
	if json.Unmarshal([]byte(raw), &p) != nil {
		return ""
	}
	rows, _ := p["hotspots"].([]any)
	if len(rows) == 0 {
		return ""
	}
	h0, _ := rows[0].(map[string]any)
	if q, _ := h0["suggested_next_query"].(string); strings.TrimSpace(q) != "" {
		return strings.TrimSpace(q)
	}
	if f, _ := h0["file"].(string); strings.TrimSpace(f) != "" {
		base := f
		if i := strings.LastIndexAny(f, `/\`); i >= 0 {
			base = f[i+1:]
		}
		base = strings.TrimSuffix(base, filepath.Ext(base))
		return base
	}
	return ""
}
