package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestSearchSymbolsPath_DeterministicOrder(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	repoID := "repo"
	symbols := []types.Symbol{
		{ID: "s3", RepoID: repoID, Name: "alpha", Kind: types.SymbolKindFunction, Path: "z/file.go", LineStart: 10, LineEnd: 10, Language: "go"},
		{ID: "s2", RepoID: repoID, Name: "alpha", Kind: types.SymbolKindFunction, Path: "a/file.go", LineStart: 20, LineEnd: 20, Language: "go"},
		{ID: "s1", RepoID: repoID, Name: "beta", Kind: types.SymbolKindFunction, Path: "a/file.go", LineStart: 5, LineEnd: 5, Language: "go"},
	}
	for _, sym := range symbols {
		if err := st.UpsertSymbol(ctx, sym); err != nil {
			t.Fatalf("upsert symbol %s: %v", sym.ID, err)
		}
	}

	got, err := st.SearchSymbolsPath(ctx, repoID, "file.go", 10)
	if err != nil {
		t.Fatalf("search symbols path: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 symbols, got %d", len(got))
	}

	// Path/name/line_start/id ordering must be stable for deterministic retrieval candidates.
	wantIDs := []string{"s2", "s1", "s3"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("at %d: got %s, want %s", i, got[i].ID, want)
		}
	}
}
