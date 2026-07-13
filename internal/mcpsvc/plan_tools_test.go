package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestPlanHandler(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\n// Helper returns a number.\nfunc Helper() int { return 1 }\n",
		"caller.go": "package x\n\nfunc run() int { return Helper() }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "task": "add a helper that returns a number",
		"role": "security", "format": "json",
	}
	res, err := planHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	out := decodeStructured[planResponse](t, res)

	if out.Role != "security" {
		t.Errorf("role = %q", out.Role)
	}
	if len(out.Steps) == 0 || len(out.Considerations) == 0 || len(out.DecisionPoints) == 0 {
		t.Fatalf("expected steps/considerations/decision_points, got %+v", out)
	}
	found := false
	for _, c := range out.ReuseCandidates {
		if c.Name == "Helper" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Helper among reuse candidates: %+v", out.ReuseCandidates)
	}
	// Security role must surface authz/injection guidance.
	joined := strings.ToLower(strings.Join(out.Considerations, " "))
	if !strings.Contains(joined, "authz") && !strings.Contains(joined, "injection") {
		t.Errorf("security considerations missing authz/injection: %v", out.Considerations)
	}
	// Performance basics are always included.
	if !strings.Contains(strings.ToLower(strings.Join(out.Considerations, " ")), "n+1") {
		t.Errorf("expected always-on performance consideration: %v", out.Considerations)
	}
}

func TestPlanHandler_RequiresTask(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name}
	res, err := planHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error when task is missing")
	}
}
