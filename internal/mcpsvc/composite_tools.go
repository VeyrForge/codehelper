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
		mcp.WithDescription("Fused investigation: query + context + impact + test_impact in one call. Returns a compact JSON bundle with symbol source, blast radius, and tests to run — replaces 4 chained MCP calls."),
		mcp.WithString("query", mcp.Required(), mcp.Description("What to find / investigate")),
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
				return mcp.NewToolResultError("query is required when target is not set"), nil
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
