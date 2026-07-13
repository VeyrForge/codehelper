package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestContextIncludesBlastRadius verifies context folds in the blast radius so
// "understand X + what it affects" is a single call.
func TestContextIncludesBlastRadius(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\n// Helper returns a number.\nfunc Helper() int { return 1 }\n",
		"a.go":      "package x\n\nfunc useA() int { return Helper() }\n",
		"b.go":      "package x\n\nfunc useB() int { return Helper() + 1 }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "name": "Helper", "format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "blast_radius") {
		t.Fatalf("expected blast_radius in context output; got: %s", txt)
	}
	if !strings.Contains(txt, "risk_tier") {
		t.Fatalf("expected risk_tier in blast_radius; got: %s", txt)
	}
}
