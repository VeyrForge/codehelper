package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterCompositeTools wires fused multi-step MCP tools (investigate, edit_cycle, preflight).
func RegisterCompositeTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	h := coreToolHandlers(regRef)

	s.AddTool(mcp.NewTool("investigate",
		mcp.WithDescription("Fused investigation: by default query + context + impact + test_impact. Pass recipe=architecture|dead_code|security|perf for specialized audits (architect Q&A pack, dead_code candidates, review_diff security smells, or hotspots+impact). Returns a compact JSON bundle — replaces chained MCP calls."),
		mcp.WithString("query", mcp.Description("What to find / investigate (required unless recipe=architecture|dead_code|security|perf or target is set)")),
		mcp.WithString("recipe", mcp.Description("Optional audit recipe: architecture | dead_code | security | perf (aliases: architect, design, unused, vuln, performance)")),
		mcp.WithString("target", mcp.Description("Optional symbol name or sym: id (skips query when set)")),
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
		mcp.WithDescription("Release gate bundle: detect_changes + review_diff + finish_check in one call — use before claiming done."),
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
			return investigateRecipe(ctx, h, recipe, repoName, format, args)
		}
		target := strings.TrimSpace(argFirst(args, "target", "name"))
		pathHint := strings.TrimSpace(argString(args, "path"))
		out := map[string]any{"steps": []string{}}
		appendStep := func(name string, body any) {
			out["steps"] = append(out["steps"].([]string), name)
			out[name] = body
		}

		if target == "" {
			q := strings.TrimSpace(argString(args, "query"))
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

func investigateRecipe(ctx context.Context, h map[string]server.ToolHandlerFunc, recipe, repoName, format string, args map[string]any) (*mcp.CallToolResult, error) {
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
	case "security":
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
		out["note"] = "Address security_findings / high-severity smells (SQL concat, eval, secrets) before claiming done."
	case "perf":
		if raw, err := callHandlerJSON(ctx, h, "hotspots", map[string]any{
			"repo": repoName, "top_k": 10, "format": "json",
		}); err == nil {
			appendStep("hotspots", jsonRaw(raw))
		} else {
			out["hotspots_error"] = err.Error()
		}
		q := strings.TrimSpace(argFirst(args, "query", "target"))
		if q == "" {
			q = "handler"
		}
		if raw, err := callHandlerJSON(ctx, h, "query", map[string]any{
			"repo": repoName, "query": q, "top_k": 5, "format": "json",
		}); err == nil {
			appendStep("query", jsonRaw(raw))
			if id := firstSymbolIDFromJSON(raw); id != "" {
				if raw2, err2 := callHandlerJSON(ctx, h, "impact", map[string]any{
					"repo": repoName, "target": id, "direction": "upstream", "depth": 2, "format": "json",
				}); err2 == nil {
					appendStep("impact", jsonRaw(raw2))
				}
				if raw2, err2 := callHandlerJSON(ctx, h, "test_impact", map[string]any{
					"repo": repoName, "target": id, "format": "json",
				}); err2 == nil {
					appendStep("test_impact", jsonRaw(raw2))
				}
			}
		}
		out["note"] = "Optimize hotspots files (churn × centrality) only after impact/test_impact; measure before/after."
	}
	return mustToolResultFormatted(out, format)
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
