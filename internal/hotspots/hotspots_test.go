package hotspots

import "testing"

func TestRank_ProductOfChurnAndCentrality(t *testing.T) {
	churn := map[string]int{
		"core.go":   50, // high churn, high centrality → top hotspot
		"active.go": 50, // high churn, low centrality → active but not central
		"stable.go": 1,  // low churn, high centrality → stable infrastructure
		"config.go": 30, // churn but no centrality → dropped
	}
	centrality := map[string]int{
		"core.go":   200,
		"active.go": 2,
		"stable.go": 200,
		// config.go absent
	}
	got := Rank(churn, centrality, 0)

	if len(got) != 3 {
		t.Fatalf("expected 3 scored files (config.go dropped: no centrality), got %d: %+v", len(got), got)
	}
	if got[0].File != "core.go" {
		t.Errorf("expected core.go ranked #1 (high churn × high centrality), got %q", got[0].File)
	}
	for _, f := range got {
		if f.File == "config.go" {
			t.Errorf("config.go has no centrality and must be dropped, but appeared: %+v", f)
		}
		if f.Score <= 0 || f.Score > 1 {
			t.Errorf("score for %s out of (0,1]: %v", f.File, f.Score)
		}
	}
}

func TestRank_TopKCap(t *testing.T) {
	churn := map[string]int{"a": 10, "b": 8, "c": 6, "d": 4}
	cent := map[string]int{"a": 10, "b": 8, "c": 6, "d": 4}
	got := Rank(churn, cent, 2)
	if len(got) != 2 {
		t.Fatalf("topK=2 should cap to 2, got %d", len(got))
	}
	if got[0].File != "a" || got[1].File != "b" {
		t.Errorf("expected top-2 a,b by score, got %q,%q", got[0].File, got[1].File)
	}
}

func TestRank_EmptySignalReturnsNil(t *testing.T) {
	if got := Rank(map[string]int{"a": 5}, map[string]int{}, 0); got != nil {
		t.Errorf("no centrality at all → nil, got %+v", got)
	}
	if got := Rank(map[string]int{}, map[string]int{"a": 5}, 0); got != nil {
		t.Errorf("no churn at all → nil, got %+v", got)
	}
}

func TestChurnFromCommits_CountsPerFileDedupedPerCommit(t *testing.T) {
	commits := [][]string{
		{"a.go", "b.go"},
		{"a.go"},
		{"a.go", "a.go"}, // duplicate within one commit counts once
	}
	churn := ChurnFromCommits(commits)
	if churn["a.go"] != 3 {
		t.Errorf("a.go touched in 3 commits (dup within a commit counts once), got %d", churn["a.go"])
	}
	if churn["b.go"] != 1 {
		t.Errorf("b.go touched in 1 commit, got %d", churn["b.go"])
	}
}
