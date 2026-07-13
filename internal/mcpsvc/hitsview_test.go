package mcpsvc

import (
	"encoding/json"
	"testing"

	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func sampleHits(n int) []retrieval.RankedSymbol {
	reasons := []string{"bm25", "trigram", "name_prefix", "centrality", "name_field", "path_proximity", "primary_lang", "semantic"}
	out := make([]retrieval.RankedSymbol, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, retrieval.RankedSymbol{
			Symbol: types.Symbol{
				ID:        "sym:codehelper:internal/ops/logread.go:112:tailFile",
				Name:      "tailFile",
				Kind:      "function",
				Path:      "internal/ops/logread.go",
				LineStart: 112,
			},
			Score:   1.409,
			Reasons: reasons,
		})
	}
	return out
}

// TestHitsViewCapsReasons verifies concise hits cap ranking signals (token lean)
// while detailed mode keeps the full record, and reports the payload reduction.
func TestHitsViewCapsReasons(t *testing.T) {
	concise := hitsView(sampleHits(10), false).([]compactHit)
	for i, h := range concise {
		if len(h.Reasons) > maxConciseReasons {
			t.Fatalf("hit %d has %d reasons, want <= %d", i, len(h.Reasons), maxConciseReasons)
		}
	}
	// Discriminating signals are preferred over ubiquitous ones (bm25/trigram fire
	// on nearly every hit, so they must NOT crowd out the informative signals).
	got := concise[0].Reasons
	for _, r := range got {
		if ubiquitousReasons[r] {
			t.Fatalf("ubiquitous reason %q kept while discriminating signals were dropped: %v", r, got)
		}
	}
	// sampleHits carries name_prefix/centrality/name_field/path_proximity/semantic
	// as discriminating signals — the top 3 by original order should be kept.
	want := []string{"name_prefix", "centrality", "name_field"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("expected discriminating signals %v, got %v", want, got)
		}
	}

	// Detailed mode is unchanged — full reasons retained.
	detailed := hitsView(sampleHits(10), true).([]retrieval.RankedSymbol)
	if len(detailed[0].Reasons) != 8 {
		t.Fatalf("detailed mode must keep all reasons, got %d", len(detailed[0].Reasons))
	}

	// Measure the concise payload reduction vs. the old uncapped shape.
	capped, _ := json.Marshal(concise)
	old := make([]compactHit, len(concise))
	copy(old, concise)
	full := sampleHits(10)
	for i := range old {
		old[i].Reasons = full[i].Reasons // restore all 8
	}
	uncapped, _ := json.Marshal(old)
	t.Logf("concise hits JSON: capped=%d bytes, uncapped=%d bytes, saved=%d (%.0f%%)",
		len(capped), len(uncapped), len(uncapped)-len(capped),
		100*float64(len(uncapped)-len(capped))/float64(len(uncapped)))
}

func TestDedupePathsCap(t *testing.T) {
	in := []string{"a.go", "a.go", "", "b.go", "a.go", "c.go"}
	got := dedupePathsCap(in, 8)
	want := []string{"a.go", "b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order/content mismatch: got %v, want %v", got, want)
		}
	}
	// Cap is respected.
	if capped := dedupePathsCap([]string{"a", "b", "c", "d"}, 2); len(capped) != 2 {
		t.Fatalf("cap not applied: %v", capped)
	}
	// All-empty in => empty out (omitempty drops the field entirely).
	if empty := dedupePathsCap([]string{"", ""}, 8); len(empty) != 0 {
		t.Fatalf("expected empty, got %v", empty)
	}
}
