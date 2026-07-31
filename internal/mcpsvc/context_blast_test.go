package mcpsvc

import (
	"encoding/json"
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

// TestContextImpactRiskTierAgree ensures context.blast_radius.risk_tier matches
// impact.risk_tier for the same symbol under default depth.
func TestContextImpactRiskTierAgree(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\nfunc Hub() int { return 1 }\n",
		"a.go":      "package x\n\nfunc useA() int { return Hub() }\n",
		"b.go":      "package x\n\nfunc useB() int { return Hub() + 1 }\n",
		"c.go":      "package x\n\nfunc useC() int { return Hub() + 2 }\n",
	})
	creq := mcp.CallToolRequest{}
	creq.Params.Arguments = map[string]any{"repo": repo.Name, "name": "Hub", "format": "json"}
	cres, err := contextHandler(reg)(ctx, creq)
	if err != nil || cres.IsError {
		t.Fatalf("context: %v %s", err, resultText(cres))
	}
	ireq := mcp.CallToolRequest{}
	ireq.Params.Arguments = map[string]any{"repo": repo.Name, "target": "Hub", "format": "json"}
	ires, err := impactHandler(reg)(ctx, ireq)
	if err != nil || ires.IsError {
		t.Fatalf("impact: %v %s", err, resultText(ires))
	}
	var cOut struct {
		BlastRadius struct {
			RiskTier string `json:"risk_tier"`
		} `json:"blast_radius"`
	}
	var iOut struct {
		Impact struct {
			RiskTier string `json:"risk_tier"`
		} `json:"impact"`
	}
	if err := json.Unmarshal([]byte(resultText(cres)), &cOut); err != nil {
		t.Fatalf("context json: %v\n%s", err, resultText(cres))
	}
	if err := json.Unmarshal([]byte(resultText(ires)), &iOut); err != nil {
		t.Fatalf("impact json: %v\n%s", err, resultText(ires))
	}
	if cOut.BlastRadius.RiskTier == "" || iOut.Impact.RiskTier == "" {
		t.Fatalf("missing risk_tier context=%q impact=%q", cOut.BlastRadius.RiskTier, iOut.Impact.RiskTier)
	}
	if cOut.BlastRadius.RiskTier != iOut.Impact.RiskTier {
		t.Fatalf("risk_tier mismatch: context=%q impact=%q", cOut.BlastRadius.RiskTier, iOut.Impact.RiskTier)
	}
}
