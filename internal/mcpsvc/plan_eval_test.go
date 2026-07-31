package mcpsvc

import (
	"context"
	"os"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestProjectContext_RealRepo prints the enriched project_context for this repo.
func TestProjectContext_RealRepo(t *testing.T) {
	if os.Getenv("CODEHELPER_PLAN_EVAL") == "" {
		t.Skip("set CODEHELPER_PLAN_EVAL=1 to print project_context")
	}
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": "codehelper", "format": "json"}
	res, err := projectContextHandler(reg)(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s\n", resultText(res))
}

// TestReview_RealRepo prints the deterministic diff audit for this repo's recent commits.
func TestReview_RealRepo(t *testing.T) {
	if os.Getenv("CODEHELPER_PLAN_EVAL") == "" {
		t.Skip("set CODEHELPER_PLAN_EVAL=1 to run the real-repo review eval")
	}
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": "codehelper", "base_ref": "HEAD~3", "format": "json"}
	res, err := reviewHandler(reg)(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s\n", resultText(res))
}

// TestPlan_RealRepos runs `plan` against this machine's actually-indexed repos for
// a spread of tasks/roles and prints the output — a manual "does it actually help"
// eval. Gated by CODEHELPER_PLAN_EVAL=1 so it never runs in CI.
func TestPlan_RealRepos(t *testing.T) {
	if os.Getenv("CODEHELPER_PLAN_EVAL") == "" {
		t.Skip("set CODEHELPER_PLAN_EVAL=1 to run the real-repo plan eval")
	}
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Per-project tools are workspace-scoped, so the eval targets the current
	// workspace (codehelper). Run from another project's root to eval it there.
	samples := []struct{ repo, task, role string }{
		{"codehelper", "add a new MCP tool that returns the callers of a symbol", "architect"},
		{"codehelper", "rate-limit the docs/web fetch so it can't be abused", "security"},
		{"codehelper", "speed up query ranking on large repositories", "performance"},
		{"codehelper", "extract the manifest parsing in project_brief into a reusable helper", "refactor"},
		{"codehelper", "add a tool that summarizes a package's public API", "feature"},
	}
	for _, s := range samples {
		if _, ok := reg.Get(s.repo); !ok {
			t.Logf("skip %s (not registered)", s.repo)
			continue
		}
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"repo": s.repo, "task": s.task, "role": s.role, "format": "json"}
		res, err := planHandler(reg)(context.Background(), req)
		if err != nil {
			t.Errorf("%s/%s: %v", s.repo, s.role, err)
			continue
		}
		t.Logf("\n===== [%s | role=%s] %s =====\n%s\n", s.repo, s.role, s.task, resultText(res))
	}
}
