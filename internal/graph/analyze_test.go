package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestAnalyzeAndHasStats verifies Analyze populates query statistics (so the
// planner uses the selective edge index) and that structural results are
// unaffected by it.
func TestAnalyzeAndHasStats(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	syms := []types.Symbol{
		{ID: "sym:r:a.go:1:Hub", RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 1},
		{ID: "sym:r:b.go:1:C1", RepoID: repo, Name: "C1", Kind: types.SymbolKindFunction, Path: "b.go", LineStart: 1},
		{ID: "sym:r:c.go:1:C2", RepoID: repo, Name: "C2", Kind: types.SymbolKindFunction, Path: "c.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	for i, src := range []string{"sym:r:b.go:1:C1", "sym:r:c.go:1:C2"} {
		if err := st.AddEdge(ctx, types.Reference{ID: "e" + string(rune('0'+i)), RepoID: repo, Kind: types.RefKindCalls, SourceID: src, TargetID: "sym:r:a.go:1:Hub", Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}

	if st.HasStats(ctx) {
		t.Error("a fresh DB must report no stats before Analyze")
	}
	if err := st.Analyze(ctx); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !st.HasStats(ctx) {
		t.Error("after Analyze, HasStats must be true")
	}
	// Analyze changes only the query plan, never results.
	callers, err := st.CallersOf(ctx, repo, "sym:r:a.go:1:Hub")
	if err != nil {
		t.Fatalf("CallersOf: %v", err)
	}
	if len(callers) != 2 {
		t.Errorf("Hub should have 2 callers after Analyze, got %d", len(callers))
	}
}
