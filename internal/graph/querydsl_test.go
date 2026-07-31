package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDependencyDistance(t *testing.T) {
	t.Parallel()
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	repo := "repo"
	syms := []types.Symbol{
		{ID: "a", RepoID: repo, Name: "a", Kind: types.SymbolKindFunction, Path: "a.go"},
		{ID: "b", RepoID: repo, Name: "b", Kind: types.SymbolKindFunction, Path: "b.go"},
		{ID: "c", RepoID: repo, Name: "c", Kind: types.SymbolKindFunction, Path: "c.go"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("UpsertSymbol: %v", err)
		}
	}
	edges := []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "a", TargetID: "b", Confidence: 1},
		{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: "b", TargetID: "c", Confidence: 1},
	}
	for _, e := range edges {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatalf("AddEdge: %v", err)
		}
	}
	d, err := st.DependencyDistance(ctx, repo, "a", "c", 4)
	if err != nil {
		t.Fatalf("DependencyDistance: %v", err)
	}
	if d != 2 {
		t.Fatalf("expected distance 2, got %d", d)
	}
}
