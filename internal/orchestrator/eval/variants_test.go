package eval

import "testing"

func TestResolveVariantsAll(t *testing.T) {
	got := ResolveVariants([]string{"all"})
	if len(got) != len(AllVariants()) {
		t.Fatalf("got %d want %d", len(got), len(AllVariants()))
	}
}

func TestAnalyzeVariantsFormatDelta(t *testing.T) {
	vr := []VariantResult{
		{Name: "fresh_index_toon", Report: Report{Overall: Summary{OrchestrateAvg: 0.9, ManualTokens: 100000, OrchestrateAgentTokens: 30000}}},
		{Name: "fresh_index_manual_json", Report: Report{Overall: Summary{ManualTokens: 200000}}},
		{Name: "skip_index_toon", Report: Report{Overall: Summary{OrchestrateAvg: 0.85}}},
	}
	a := analyzeVariants(vr)
	if len(a.FormatImpact) == 0 || len(a.IndexImpact) == 0 {
		t.Fatalf("analysis=%+v", a)
	}
}

func TestStructureScoreTOONSignals(t *testing.T) {
	s := structureScore("hits[3]{id,name}:\nInvestigation brief", 2)
	if s < 0.5 {
		t.Fatalf("score=%f", s)
	}
}
