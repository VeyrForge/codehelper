package mcpsvc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestVibeRecommendedTools_IncludesApplyPatch(t *testing.T) {
	top := &reuseCandidate{Name: "Helper", Loc: "a.go:1"}
	got := vibeRecommendedTools("feature", top, false)
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "change_kit") || !strings.Contains(joined, "apply_patch_workspace_file") {
		t.Fatalf("expected change_kit + apply_patch, got %#v", got)
	}
}

func TestEnrichImpactResponse_SparseGraphLowersConfidence(t *testing.T) {
	out := &impactMCPResponse{
		Freshness:           freshness.Report{},
		CallGraphConfidence: "LOW — sparse call graph (0 call edges / 10 symbols = 0.00/sym)",
	}
	res := &types.ImpactResult{Target: "Foo", RiskTier: "low", Nodes: []types.ImpactNode{{Name: "Foo"}}}
	enrichImpactResponse(out, res)
	if out.Confidence >= 0.8 {
		t.Fatalf("sparse graph should lower confidence, got %v", out.Confidence)
	}
	if out.WhatNext == "" || !strings.Contains(out.WhatNext, "change_kit") {
		t.Fatalf("what_next: %q", out.WhatNext)
	}
	if out.Note == "" || !strings.Contains(out.Note, "SPARSE") {
		t.Fatalf("expected SPARSE note: %q", out.Note)
	}
	foundWarn := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "sparse call graph") {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Fatalf("expected sparse warning: %#v", out.Warnings)
	}
}

func TestEnrichImpactResponse_DenseGraphKeepsHighConfidence(t *testing.T) {
	out := &impactMCPResponse{Freshness: freshness.Report{}}
	res := &types.ImpactResult{
		Target:   "Foo",
		RiskTier: "medium",
		Nodes:    []types.ImpactNode{{Name: "Foo"}, {Name: "Bar"}},
	}
	enrichImpactResponse(out, res)
	if out.Confidence < 0.9 {
		t.Fatalf("dense multi-node should be high confidence, got %v", out.Confidence)
	}
	if out.CallGraphConfidence != "" {
		t.Fatalf("should not invent sparse confidence")
	}
}
