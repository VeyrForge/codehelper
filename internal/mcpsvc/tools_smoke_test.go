// Smoke tests covering every MCP tool registered by the codehelper server.
//
// The intent is not unit-level correctness (each handler's underlying package
// already has its own tests) but to confirm every tool actually wires up,
// accepts realistic arguments, and returns a non-error CallToolResult.
//
// The test requires the codehelper registry on disk to contain at least one
// indexed repository. When no repos are indexed the whole file is skipped so
// CI on cold machines doesn't fail spuriously.
//
// To run only this file:
//
//	go test ./internal/mcpsvc -run TestAllToolsSmoke -v
package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// liveRegistryWithIndexedRepo loads the global registry and picks an entry
// whose root_path still exists and is git-tracked. Returns nil if nothing usable.
func liveRegistryWithIndexedRepo(t *testing.T) (*registry.Registry, registry.Entry) {
	t.Helper()
	reg, err := registry.Load()
	if err != nil {
		t.Skipf("registry load failed: %v", err)
	}
	var candidates []registry.Entry
	for _, e := range reg.List() {
		if e.RootPath == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.RootPath, ".git")); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.RootPath, ".codehelper")); err != nil {
			continue
		}
		candidates = append(candidates, e)
	}
	if wd, err := os.Getwd(); err == nil {
		wd, _ = filepath.Abs(wd)
		for _, e := range candidates {
			if filepath.Clean(e.RootPath) == filepath.Clean(wd) {
				return reg, e
			}
		}
	}
	for _, e := range candidates {
		if e.Name == "codehelper" {
			return reg, e
		}
	}
	if len(candidates) > 0 {
		return reg, candidates[0]
	}
	t.Skip("no indexed repository with .git + .codehelper directory found in registry")
	return nil, registry.Entry{}
}

// callTool invokes a registered tool handler directly. We bypass the server
// transport and operate on the handler function the same way the mcp-go
// server would, so any change to handler signatures or registration is caught.
func callTool(
	t *testing.T,
	srv *server.MCPServer,
	handlers map[string]server.ToolHandlerFunc,
	name string,
	args map[string]any,
) (*mcp.CallToolResult, error) {
	t.Helper()
	h, ok := handlers[name]
	if !ok {
		return nil, fmt.Errorf("tool not registered: %s", name)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return h(ctx, req)
}

// allToolHandlers wires every MCP tool handler for smoke tests (mirrors RegisterAll).
func allToolHandlers(reg *registry.Registry) map[string]server.ToolHandlerFunc {
	return AllToolHandlers(reg)
}

// hookedRegister mirrors RegisterAll but captures every handler in a map so
// tests can dispatch tools by name without going through the server transport.
func hookedRegister(reg *registry.Registry) (*server.MCPServer, map[string]server.ToolHandlerFunc) {
	srv := server.NewMCPServer("codehelper-smoke", "0")
	return srv, allToolHandlers(reg)
}

func resultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func mustOK(t *testing.T, tool string, res *mcp.CallToolResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s returned error: %v", tool, err)
	}
	if res == nil {
		t.Fatalf("%s returned nil result", tool)
	}
	if res.IsError {
		t.Fatalf("%s returned IsError=true: %s", tool, resultText(res))
	}
}

// shouldError asserts the handler reports a tool-level error via IsError.
func shouldError(t *testing.T, tool string, res *mcp.CallToolResult, err error) {
	t.Helper()
	if err != nil {
		return
	}
	if res == nil || !res.IsError {
		t.Fatalf("%s expected IsError=true, got %v / %q", tool, res, resultText(res))
	}
}

func TestAllToolsSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping all-tools smoke in -short mode")
	}
	reg, repo := liveRegistryWithIndexedRepo(t)
	_, handlers := hookedRegister(reg)

	if len(handlers) != len(AllMCPToolNames()) {
		t.Fatalf("expected %d registered tools, got %d", len(AllMCPToolNames()), len(handlers))
	}
	for _, name := range AllMCPToolNames() {
		if _, ok := handlers[name]; !ok {
			t.Fatalf("missing handler for %s", name)
		}
	}

	common := func(extra map[string]any) map[string]any {
		out := map[string]any{"repo": repo.Name}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	t.Run("project_context", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "project_context", common(nil))
		mustOK(t, "project_context", res, err)
	})

	t.Run("query", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "query", common(map[string]any{
			"query": "register",
		}))
		mustOK(t, "query", res, err)
		// Default encoding is TOON, returned as the text block (no structuredContent)
		// so the model reads the compact form directly.
		if res.StructuredContent != nil {
			t.Fatalf("query should not attach structuredContent in TOON mode; got: %v", res.StructuredContent)
		}
		if !strings.Contains(resultText(res), "hits[") {
			t.Fatalf("query TOON text should contain a hits array marker; got: %s", resultText(res))
		}
	})

	t.Run("docs_offline", func(t *testing.T) {
		// No network approval -> offline resolution, must still succeed with sources.
		res, err := callTool(t, nil, handlers, "docs", common(map[string]any{
			"library": "react", "format": "json",
		}))
		mustOK(t, "docs", res, err)
		sb, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(sb), "\"resolved\"") {
			t.Fatalf("docs response missing resolved sources: %s", string(sb))
		}
	})

	t.Run("web_local", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("hello world"))
		}))
		defer srv.Close()
		res, err := callTool(t, nil, handlers, "web", map[string]any{
			"url": srv.URL, "expect_status": float64(200), "expect_contains": []any{"hello"}, "format": "json",
		})
		mustOK(t, "web", res, err)
		sb, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(sb), "\"passed\":true") {
			t.Fatalf("web check should pass: %s", string(sb))
		}
	})

	t.Run("query_empty_q", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "query", common(nil))
		shouldError(t, "query", res, err)
	})

	t.Run("context", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "context", common(map[string]any{
			"name": "RegisterAll",
		}))
		mustOK(t, "context", res, err)
	})

	t.Run("impact", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "impact", common(map[string]any{
			"target":    "RegisterAll",
			"direction": "downstream",
			"depth":     float64(2),
		}))
		mustOK(t, "impact", res, err)
	})

	t.Run("dead_code", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "dead_code", common(map[string]any{
			"format": "json",
		}))
		mustOK(t, "dead_code", res, err)
		sb, _ := json.Marshal(res.StructuredContent)
		var body map[string]any
		_ = json.Unmarshal(sb, &body)
		if _, ok := body["safety"].(string); !ok {
			t.Fatalf("dead_code response missing safety note; structured: %s", string(sb))
		}
	})

	t.Run("hotspots", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "hotspots", common(map[string]any{
			"format": "json",
		}))
		mustOK(t, "hotspots", res, err)
		sb, _ := json.Marshal(res.StructuredContent)
		var body map[string]any
		_ = json.Unmarshal(sb, &body)
		if _, ok := body["note"].(string); !ok {
			t.Fatalf("hotspots response missing note; structured: %s", string(sb))
		}
	})

	t.Run("detect_changes", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "detect_changes", common(map[string]any{
			"base_ref": "HEAD~1",
		}))
		mustOK(t, "detect_changes", res, err)
	})

	t.Run("since", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "since", common(map[string]any{
			"base_ref": "HEAD~1",
		}))
		mustOK(t, "since", res, err)
	})

	t.Run("verify_argv_noop", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "verify", map[string]any{
			"repo_root": repo.RootPath,
		})
		mustOK(t, "verify", res, err)
	})

	t.Run("review_diff", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "review_diff", common(map[string]any{
			"base":             "HEAD~1",
			"severity_floor":   "low",
			"include_tests":    true,
			"include_security": true,
		}))
		mustOK(t, "review_diff", res, err)
	})

	t.Run("list_workspace_directory_root", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "list_workspace_directory", common(map[string]any{
			"path":        ".",
			"max_entries": float64(50),
		}))
		mustOK(t, "list_workspace_directory", res, err)
	})

	t.Run("read_workspace_file_known_file", func(t *testing.T) {
		readme := pickFirstExisting(repo.RootPath, []string{"README.md", "Readme.md", "readme.md"})
		if readme == "" {
			t.Skip("repo has no README")
		}
		res, err := callTool(t, nil, handlers, "read_workspace_file", common(map[string]any{
			"path":      readme,
			"max_bytes": float64(4096),
		}))
		mustOK(t, "read_workspace_file", res, err)
		// Default encoding is TOON now, so the field renders as `content:` (unquoted).
		if !strings.Contains(resultText(res), "content") {
			t.Fatalf("read_workspace_file response missing content field; got: %s", resultText(res))
		}
	})

	t.Run("write_then_patch_then_revert", func(t *testing.T) {
		rel := filepath.ToSlash(filepath.Join(".codehelper", "_smoke", fmt.Sprintf("smoke-%d.txt", time.Now().UnixNano())))
		defer os.Remove(filepath.Join(repo.RootPath, rel))

		wres, werr := callTool(t, nil, handlers, "write_workspace_file", common(map[string]any{
			"path":               rel,
			"content":            "line one\nline two\n",
			"create_directories": true,
		}))
		mustOK(t, "write_workspace_file", wres, werr)

		pres, perr := callTool(t, nil, handlers, "apply_patch_workspace_file", common(map[string]any{
			"path": rel,
			"hunks": []any{
				map[string]any{"old_string": "line one\n", "new_string": "line one PATCHED\n"},
			},
		}))
		mustOK(t, "apply_patch_workspace_file", pres, perr)

		var patchBody map[string]any
		if err := json.Unmarshal([]byte(resultText(pres)), &patchBody); err != nil {
			t.Fatalf("apply_patch body not JSON: %v / %s", err, resultText(pres))
		}
		token, _ := patchBody["revert_token"].(string)
		if token == "" {
			t.Fatalf("apply_patch did not return revert_token; body: %s", resultText(pres))
		}

		rres, rerr := callTool(t, nil, handlers, "revert_workspace_edit", map[string]any{
			"revert_token": token,
		})
		mustOK(t, "revert_workspace_edit", rres, rerr)
	})

	t.Run("finish_check", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "finish_check", common(map[string]any{
			"base_ref": "HEAD~1",
		}))
		mustOK(t, "finish_check", res, err)
	})

	t.Run("agent_plan", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "agent_plan", common(map[string]any{
			"request":       "Add structured logging to HTTP handlers",
			"approve_todos": true,
		}))
		mustOK(t, "agent_plan", res, err)
	})

	t.Run("agent_memory_search", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "agent_memory", common(map[string]any{
			"action": "search",
			"query":  "test",
			"limit":  float64(4),
		}))
		mustOK(t, "agent_memory", res, err)
	})

	t.Run("scout", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "scout", common(map[string]any{
			"task": "add MCP tool registration",
		}))
		mustOK(t, "scout", res, err)
	})

	t.Run("test_impact", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "test_impact", common(map[string]any{
			"target": "RegisterAll",
		}))
		mustOK(t, "test_impact", res, err)
	})

	t.Run("ast_query", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "ast_query", common(map[string]any{
			"language": "go",
			"pattern":  "(function_declaration name: (identifier) @name)",
			"path":     "internal/mcpsvc/toolcatalog.go",
		}))
		mustOK(t, "ast_query", res, err)
	})

	t.Run("api_surface", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "api_surface", common(map[string]any{
			"path": "internal/mcpsvc",
		}))
		mustOK(t, "api_surface", res, err)
	})

	t.Run("change_kit", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "change_kit", common(map[string]any{
			"target": "RegisterAll",
		}))
		mustOK(t, "change_kit", res, err)
	})

	t.Run("find_implementations", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "find_implementations", common(map[string]any{
			"interface": "ToolHandlerFunc",
		}))
		// May return empty for this symbol; wiring must not panic.
		if err != nil {
			t.Fatalf("find_implementations: %v", err)
		}
		if res == nil {
			t.Fatal("find_implementations returned nil")
		}
	})

	t.Run("similar", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "similar", common(map[string]any{
			"name": "queryHandler",
		}))
		mustOK(t, "similar", res, err)
	})

	t.Run("trace", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "trace", common(map[string]any{
			"from": "projectContextHandler",
			"to":   "compactProjectContext",
		}))
		mustOK(t, "trace", res, err)
	})

	t.Run("diagnostics", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "diagnostics", common(nil))
		mustOK(t, "diagnostics", res, err)
	})

	t.Run("scope", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "scope", common(map[string]any{
			"idea": "let agents discover MCP tools automatically",
		}))
		mustOK(t, "scope", res, err)
	})

	t.Run("plan", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "plan", common(map[string]any{
			"task": "document MCP bootstrap fields",
			"role": "feature",
		}))
		mustOK(t, "plan", res, err)
	})

	t.Run("kickoff", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "kickoff", common(map[string]any{
			"task": "improve project_context bootstrap",
			"role": "feature",
		}))
		mustOK(t, "kickoff", res, err)
	})

	t.Run("orchestration_status", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "orchestration", common(map[string]any{
			"action": "status",
		}))
		mustOK(t, "orchestration", res, err)
	})

	t.Run("investigate", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "investigate", common(map[string]any{
			"query": "RegisterAll MCP tools",
		}))
		mustOK(t, "investigate", res, err)
	})

	t.Run("preflight", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "preflight", common(nil))
		mustOK(t, "preflight", res, err)
	})

	t.Run("review", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "review", common(map[string]any{
			"base_ref": "HEAD~1",
		}))
		mustOK(t, "review", res, err)
	})

	t.Run("glossary_list", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "glossary", common(map[string]any{
			"action": "list",
		}))
		mustOK(t, "glossary", res, err)
	})

	t.Run("hints_list", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "hints", map[string]any{
			"action": "list",
		})
		mustOK(t, "hints", res, err)
	})

	t.Run("usage_report", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "usage_report", common(map[string]any{
			"limit": float64(5),
		}))
		mustOK(t, "usage_report", res, err)
	})

	t.Run("project_context_sections_tools", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "project_context", common(map[string]any{
			"format":   "json",
			"sections": "tools",
		}))
		mustOK(t, "project_context", res, err)
		sb, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(sb), "mcp_tools_by_group") {
			t.Fatalf("sections=tools should return grouped catalog: %s", string(sb))
		}
	})

	t.Run("web_search_empty", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "web_search", map[string]any{})
		shouldError(t, "web_search", res, err)
	})

	t.Run("browser_empty_url", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "browser", map[string]any{})
		shouldError(t, "browser", res, err)
	})

	t.Run("agent_execute_todo_missing", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "agent_execute_todo", common(nil))
		shouldError(t, "agent_execute_todo", res, err)
	})

	t.Run("rename_symbol_missing", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "rename_symbol", common(map[string]any{
			"old_name": "doesNotExist",
			"new_name": "alsoMissing",
		}))
		shouldError(t, "rename_symbol", res, err)
	})

	t.Run("remote_list", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "remote_list", common(map[string]any{"format": "json"}))
		mustOK(t, "remote_list", res, err)
	})

	t.Run("env_context", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "env_context", common(map[string]any{"format": "json"}))
		mustOK(t, "env_context", res, err)
	})

	t.Run("log_read_missing", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "log_read", common(map[string]any{"source": "does-not-exist"}))
		shouldError(t, "log_read", res, err)
	})

	t.Run("db_query_missing", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "db_query", common(map[string]any{
			"connection": "missing", "sql": "SELECT 1",
		}))
		shouldError(t, "db_query", res, err)
	})

	t.Run("ci_status_unconfigured", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "ci_status", common(map[string]any{"format": "json"}))
		mustOK(t, "ci_status", res, err)
	})
}

func pickFirstExisting(root string, candidates []string) string {
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(root, c)); err == nil {
			return c
		}
	}
	return ""
}
