package graph

import "testing"

func TestConfidenceForStrategy(t *testing.T) {
	t.Parallel()
	tests := map[string]float64{
		"import": ConfExact, "recv_type": ConfExact,
		"same_file": ConfScoped, "same_dir": ConfScoped, "embedded": ConfScoped,
		"unique": ConfNameOnly, "non_fixture": ConfNameOnly,
		"unknown": ConfInferred,
	}
	for strategy, want := range tests {
		if got := ConfidenceForStrategy(strategy); got != want {
			t.Errorf("ConfidenceForStrategy(%q)=%v want %v", strategy, got, want)
		}
	}
}

func TestProvenanceFromConfidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		c    float64
		want Provenance
	}{
		{0.95, Exact},
		{0.90, Exact},
		{0.85, Scoped},
		{0.80, Scoped},
		{0.75, NameOnly},
		{0.70, NameOnly},
		{0.5, Inferred},
		{0, Inferred},
	}
	for _, tt := range tests {
		if got := ProvenanceFromConfidence(tt.c); got != tt.want {
			t.Errorf("ProvenanceFromConfidence(%v)=%q want %q", tt.c, got, tt.want)
		}
	}
}
