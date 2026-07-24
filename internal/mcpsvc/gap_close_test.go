package mcpsvc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestCallGraphConfidence_DynamicLanguageTightens(t *testing.T) {
	if !isDynamicSparseLanguage("php") || !isDynamicSparseLanguage("Ruby") || !isDynamicSparseLanguage("c") {
		t.Fatal("expected php/ruby/c as dynamic sparse languages")
	}
	if isDynamicSparseLanguage("go") {
		t.Fatal("go should not be treated as dynamic-sparse")
	}
}

func TestResolveFormat_DefaultsToToon(t *testing.T) {
	t.Setenv("CODEHELPER_MCP_FORMAT", "")
	if got := resolveFormat(map[string]any{}); got != "toon" {
		t.Fatalf("default format=%q want toon", got)
	}
	if got := resolveFormat(map[string]any{"format": "json"}); got != "json" {
		t.Fatalf("explicit json got %q", got)
	}
}

func TestFirstHotspotSuggestedTarget(t *testing.T) {
	raw := `{"hotspots":[{"file":"src/server.c","suggested_next_query":"processCommand"}]}`
	if got := firstHotspotSuggestedTarget(raw); got != "processCommand" {
		t.Fatalf("got %q", got)
	}
	raw2 := `{"hotspots":[{"file":"app/models/user.rb"}]}`
	if got := firstHotspotSuggestedTarget(raw2); got != "user" {
		t.Fatalf("basename got %q want user", got)
	}
}

func TestMCPParamKeys_CoversInvestigateAndMemory(t *testing.T) {
	if !strings.Contains(MCPParamKeys, "investigate") || !strings.Contains(MCPParamKeys, "agent_memory") {
		t.Fatalf("MCPParamKeys incomplete: %s", MCPParamKeys)
	}
}

func TestEnrichImpact_SparseNeverImpliesSafe(t *testing.T) {
	out := &impactMCPResponse{
		CallGraphConfidence: "LOW — sparse call graph (0 call edges / 10 symbols = 0.00/sym)",
	}
	res := &types.ImpactResult{Target: "Foo", RiskTier: "low", Nodes: []types.ImpactNode{{Name: "Foo"}}}
	enrichImpactResponse(out, res)
	if out.Confidence >= 0.8 {
		t.Fatalf("sparse confidence too high: %v", out.Confidence)
	}
	if out.Note == "" || !strings.Contains(out.Note, "SPARSE") {
		t.Fatalf("expected SPARSE note, got %q", out.Note)
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "NEVER treat empty blast_radius") || strings.Contains(w, "sparse call graph") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected overtrust warning, got %#v", out.Warnings)
	}
}
