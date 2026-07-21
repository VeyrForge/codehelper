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
