package memory

import "testing"

func TestAddDecisionRecord_RoundTripWithRationale(t *testing.T) {
	s := Open(t.TempDir())
	if err := s.AddDecisionRecord(Decision{
		Text:      "Cache the graph read store per process",
		Rationale: "opening SQLite per tool call cost ~200us each; caching is ~133x cheaper",
		Tags:      []string{"perf", " graph ", ""},
	}); err != nil {
		t.Fatalf("AddDecisionRecord: %v", err)
	}
	// Search on a rationale-only term must find it, and the summary must carry the WHY.
	hits, err := s.Search("caching graph store", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var found *RelevantMemory
	for i := range hits {
		if hits[i].Type == "decision" {
			found = &hits[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a decision hit, got %+v", hits)
	}
	if !contains(found.Summary, "why:") {
		t.Fatalf("decision summary should include the rationale, got %q", found.Summary)
	}
}

func TestAddDecision_BlankIsNoOp(t *testing.T) {
	s := Open(t.TempDir())
	if err := s.AddDecision("   "); err != nil {
		t.Fatalf("AddDecision blank: %v", err)
	}
	hits, err := s.Search("anything", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("blank decision should persist nothing, got %+v", hits)
	}
}

func TestTrimTags_DropsEmpty(t *testing.T) {
	got := trimTags([]string{" a ", "", "  ", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("trimTags = %v", got)
	}
	if trimTags([]string{"", " "}) != nil {
		t.Fatal("all-empty tags should trim to nil")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
