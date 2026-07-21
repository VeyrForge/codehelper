package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TestWorkflowSmokeMultiTestbed exercises cwd-bind + query/context/impact/kickoff
// + apply_patch/revert for express/laravel/fiber. Writes a report when
// CODEHELPER_WORKFLOW_REPORT is set.
func TestWorkflowSmokeMultiTestbed(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	base := os.Getenv("CODEHELPER_TESTBEDS")
	if base == "" {
		base = filepath.Join("..", "..", ".testbeds")
		if abs, err := filepath.Abs(base); err == nil {
			base = abs
		}
	}
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	handlers := AllToolHandlers(reg)
	beds := []string{
		"express", "laravel", "fiber", "sinatra", "djangorest", "spring-petclinic",
		"nest", "axum", "svelte", "fastapi", "gin", "flask",
	}
	report := map[string]any{}
	for _, name := range beds {
		root := filepath.Join(base, name)
		if _, err := os.Stat(filepath.Join(root, ".codehelper")); err != nil {
			t.Logf("skip %s: not initialized (%v)", name, err)
			continue
		}
		report[name] = smokeWorkflowBed(t, handlers, root, name)
	}
	if p := os.Getenv("CODEHELPER_WORKFLOW_REPORT"); p != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("wrote workflow report %s", p)
	}
	// Fail the test if any critical check failed
	for name, raw := range report {
		m, _ := raw.(map[string]any)
		for _, key := range []string{"cwd_bind_project_context", "query_core", "apply_patch", "revert", "empty_write_rejected"} {
			c, _ := m[key].(map[string]any)
			if c == nil {
				continue
			}
			if key == "empty_write_rejected" {
				// expect is_error true
				if ok, _ := c["ok"].(bool); ok {
					t.Errorf("%s/%s: empty write should be rejected", name, key)
				}
				continue
			}
			if leak, _ := c["wrong_repo_leak"].(bool); leak {
				t.Errorf("%s/%s: wrong-repo leak to codehelper", name, key)
			}
			if ok, _ := c["ok"].(bool); !ok {
				t.Errorf("%s/%s failed: %v", name, key, c["error"])
			}
		}
	}
}

// TestFeatureLifecycleSmoke runs add→impact→patch→review→revert on a tiny
// indexed fixture so the feature-lifecycle loop is covered without needing a
// full testbed clone.
func TestFeatureLifecycleSmoke(t *testing.T) {
	reg, repo, _ := buildIndexedRepo(t, map[string]string{
		"helper.go": "package demo\n\n// Helper is the lifecycle smoke target.\nfunc Helper() int {\n\treturn 1\n}\n",
		"use.go":    "package demo\n\nfunc Use() int {\n\treturn Helper()\n}\n",
	})
	handlers := AllToolHandlers(reg)
	wctx := workspacectx.WithRoots(repo.RootPath)

	pc := workflowCall(wctx, handlers, "project_context", map[string]any{"repo": repo.Name, "format": "json"}, repo.Name)
	if ok, _ := pc["ok"].(bool); !ok {
		t.Fatalf("project_context failed: %v", pc)
	}
	pcFull := workflowCallFull(wctx, handlers, "project_context", map[string]any{"repo": repo.Name, "format": "json"}, repo.Name)
	for _, want := range []string{"add_feature", "remove_feature", "security_review", "dead_code", "performance", "review_changes", "architecture_qa", "locate_symbol", "vibe_fix", "vibe_ui", "programmer_ui", "browser_qa", "verify_finish_gate"} {
		if !strings.Contains(pcFull, want) {
			t.Errorf("project_context missing workflow recipe %q", want)
		}
	}

	impact := workflowCall(wctx, handlers, "impact", map[string]any{
		"repo": repo.Name, "name": "Helper", "format": "json",
	}, repo.Name)
	if ok, _ := impact["ok"].(bool); !ok {
		t.Fatalf("impact failed: %v", impact)
	}

	kit := workflowCall(wctx, handlers, "change_kit", map[string]any{
		"repo": repo.Name, "target": "Helper", "format": "json",
	}, repo.Name)
	if ok, _ := kit["ok"].(bool); !ok {
		t.Fatalf("change_kit failed: %v", kit)
	}
	kitText, _ := kit["snippet"].(string)
	if !strings.Contains(kitText, "Use") && !strings.Contains(strings.ToLower(kitText), "caller") {
		t.Logf("change_kit callers may be thin (graph quality): %s", truncateSmoke(kitText, 400))
	}

	patch := workflowCall(wctx, handlers, "apply_patch_workspace_file", map[string]any{
		"repo": repo.Name,
		"path": "helper.go",
		"hunks": []any{
			map[string]any{
				"old_string": "\treturn 1\n",
				"new_string": "\treturn 2\n",
			},
		},
	}, repo.Name)
	if ok, _ := patch["ok"].(bool); !ok {
		t.Fatalf("apply_patch failed: %v", patch)
	}
	token := extractWorkflowRevertToken(patch)
	if token == "" {
		t.Fatal("expected revert_token from apply_patch")
	}

	review := workflowCall(wctx, handlers, "review_diff", map[string]any{
		"repo": repo.Name, "format": "json",
	}, repo.Name)
	if ok, _ := review["ok"].(bool); !ok {
		t.Fatalf("review_diff failed: %v", review)
	}

	finish := workflowCall(wctx, handlers, "finish_check", map[string]any{
		"repo": repo.Name, "format": "json",
	}, repo.Name)
	// finish_check may fail on ephemeral fixture repos (no remote / shallow git);
	// the tool must still be reachable and return a structured response.
	if finish["error"] == "missing tool" {
		t.Fatalf("finish_check missing from handlers")
	}

	revert := workflowCall(wctx, handlers, "revert_workspace_edit", map[string]any{
		"revert_token": token,
	}, repo.Name)
	if ok, _ := revert["ok"].(bool); !ok {
		t.Fatalf("revert failed: %v", revert)
	}
	got, _ := os.ReadFile(filepath.Join(repo.RootPath, "helper.go"))
	if !strings.Contains(string(got), "return 1") {
		t.Fatalf("revert did not restore helper.go: %q", got)
	}

	// dry-run must not corrupt banner-style single-space indents
	bannerPath := "banner.go"
	banner := "package demo\n\n/*!\n * demo\n */\n"
	_ = os.WriteFile(filepath.Join(repo.RootPath, bannerPath), []byte(banner), 0o644)
	dry := workflowCall(wctx, handlers, "apply_patch_workspace_file", map[string]any{
		"repo":    repo.Name,
		"path":    bannerPath,
		"dry_run": true,
		"hunks": []any{
			map[string]any{
				"old_string": " * demo\n",
				"new_string": " * demo-x\n",
			},
		},
	}, repo.Name)
	if ok, _ := dry["ok"].(bool); !ok {
		t.Fatalf("dry_run patch failed: %v", dry)
	}
	dryText, _ := dry["snippet"].(string)
	if strings.Contains(dryText, "    * demo") {
		t.Fatalf("dry_run corrupted banner indent: %s", dryText)
	}
	onDisk, _ := os.ReadFile(filepath.Join(repo.RootPath, bannerPath))
	if string(onDisk) != banner {
		t.Fatalf("dry_run mutated disk: %q", onDisk)
	}
}

func truncateSmoke(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func smokeWorkflowBed(t *testing.T, handlers map[string]server.ToolHandlerFunc, root, expectRepo string) map[string]any {
	t.Helper()
	ctx := workspacectx.WithRoots(root)
	res := map[string]any{"root": root}
	type check struct {
		id   string
		tool string
		args map[string]any
	}
	checks := []check{
		{"cwd_bind_project_context", "project_context", map[string]any{}},
		{"query_core", "query", map[string]any{"q": workflowQueryFor(expectRepo)}},
		{"context_core", "context", map[string]any{"name": workflowContextName(expectRepo)}},
		{"impact_core", "impact", map[string]any{"name": workflowImpactName(expectRepo)}},
		{"kickoff", "kickoff", map[string]any{"task": workflowTaskFor(expectRepo)}},
	}
	for _, c := range checks {
		res[c.id] = workflowCall(ctx, handlers, c.tool, c.args, expectRepo)
	}

	scratchRel := ".codehelper/smoke_patch_target.txt"
	scratchAbs := filepath.Join(root, scratchRel)
	_ = os.MkdirAll(filepath.Dir(scratchAbs), 0o755)
	orig := "line-one\nline-two\n"
	_ = os.WriteFile(scratchAbs, []byte(orig), 0o644)

	res["write"] = workflowCall(ctx, handlers, "write_workspace_file", map[string]any{
		"path": scratchRel, "content": orig,
	}, expectRepo)
	patchRes := workflowCall(ctx, handlers, "apply_patch_workspace_file", map[string]any{
		"path":  scratchRel,
		"patch": "@@\n line-one\n-line-two\n+line-two-patched\n",
	}, expectRepo)
	res["apply_patch"] = patchRes
	token := extractWorkflowRevertToken(patchRes)
	if token != "" {
		res["revert"] = workflowCall(ctx, handlers, "revert_workspace_edit", map[string]any{
			"revert_token": token,
		}, expectRepo)
	} else {
		res["revert"] = map[string]any{"ok": false, "error": "no revert_token"}
	}
	empty := workflowCall(ctx, handlers, "write_workspace_file", map[string]any{
		"path": scratchRel + ".empty", "content": "",
	}, expectRepo)
	res["empty_write_rejected"] = empty

	_ = os.Remove(scratchAbs)
	_ = os.Remove(scratchAbs + ".empty")
	return res
}

func workflowQueryFor(repo string) string {
	switch repo {
	case "express":
		return "app.use middleware"
	case "laravel":
		return "User model"
	case "fiber":
		return "App.Use middleware"
	case "sinatra":
		return "Sinatra Base"
	case "djangorest":
		return "APIView"
	case "spring-petclinic":
		return "PetClinicApplication"
	case "nest":
		return "CatsService"
	case "axum":
		return "Router"
	case "svelte":
		return "mount"
	case "fastapi":
		return "Depends"
	case "gin":
		return "Context.JSON"
	case "flask":
		return "Flask application class"
	default:
		return "main"
	}
}

func workflowContextName(repo string) string {
	switch repo {
	case "express":
		return "createApplication"
	case "laravel":
		return "User"
	case "fiber":
		return "Listen"
	case "sinatra":
		return "Base"
	case "djangorest":
		return "APIView"
	case "spring-petclinic":
		return "PetClinicApplication"
	case "nest":
		return "CatsService"
	case "axum":
		return "Router"
	case "svelte":
		return "mount"
	case "fastapi":
		return "Depends"
	case "gin":
		return "JSON"
	case "flask":
		return "Flask"
	default:
		return "main"
	}
}

func workflowImpactName(repo string) string {
	return workflowContextName(repo)
}

func workflowTaskFor(repo string) string {
	switch repo {
	case "express":
		return "How does app.use register middleware?"
	case "laravel":
		return "Add a Form Request for POST /register"
	case "fiber":
		return "Add custom middleware that logs request latency"
	case "sinatra":
		return "Add a GET /health route on Sinatra::Base"
	case "djangorest":
		return "Extend APIView with a custom permission check"
	case "spring-petclinic":
		return "How does PetClinicApplication boot the Spring context?"
	case "nest":
		return "What depends on CatsService before changing it?"
	case "axum":
		return "What depends on axum::Router before changing the routing API?"
	case "svelte":
		return "How does mount attach a Svelte component?"
	case "fastapi":
		return "Where is Depends used before adding a new dependency?"
	case "gin":
		return "How does Context.JSON write a response?"
	case "flask":
		return "How does the Flask application class boot?"
	default:
		return "explore"
	}
}

func workflowCallFull(ctx context.Context, handlers map[string]server.ToolHandlerFunc, tool string, args map[string]any, expectRepo string) string {
	h, ok := handlers[tool]
	if !ok {
		return ""
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := h(cctx, req)
	if err != nil || res == nil || res.IsError {
		return ""
	}
	return resultText(res)
}

func workflowCall(ctx context.Context, handlers map[string]server.ToolHandlerFunc, tool string, args map[string]any, expectRepo string) map[string]any {
	h, ok := handlers[tool]
	out := map[string]any{"tool": tool}
	if !ok {
		out["ok"] = false
		out["error"] = "missing tool"
		return out
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := h(cctx, req)
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
		return out
	}
	text := resultText(res)
	snippet := text
	if len(snippet) > 1200 {
		snippet = snippet[:1200] + "…"
	}
	out["snippet"] = snippet
	out["is_error"] = res != nil && res.IsError
	out["ok"] = res != nil && !res.IsError
	if expectRepo != "codehelper" && (strings.Contains(text, "sym:codehelper:") ||
		strings.Contains(text, `"repo":"codehelper"`) ||
		strings.Contains(text, `"repo": "codehelper"`)) {
		out["wrong_repo_leak"] = true
		out["ok"] = false
	}
	lower := strings.ToLower(text)
	out["binds_expected_repo"] = strings.Contains(lower, strings.ToLower(expectRepo))
	return out
}

func extractWorkflowRevertToken(m map[string]any) string {
	s, _ := m["snippet"].(string)
	var raw map[string]any
	if json.Unmarshal([]byte(s), &raw) == nil {
		if t, ok := raw["revert_token"].(string); ok {
			return t
		}
	}
	idx := strings.Index(s, "revert_token")
	if idx < 0 {
		return ""
	}
	rest := s[idx:]
	for _, sep := range []string{`":"`, `": "`, ": "} {
		if i := strings.Index(rest, sep); i >= 0 {
			rest = rest[i+len(sep):]
			end := strings.IndexAny(rest, "\"\n, ")
			if end < 0 {
				return strings.TrimSpace(rest)
			}
			return strings.Trim(rest[:end], "\"")
		}
	}
	return ""
}
