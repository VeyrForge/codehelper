package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// SymbolsByName must resolve a bare name to the symbol a human means: the exact,
// non-test match — not a test whose name merely contains the query. Regression
// for "projectBrief" resolving to "TestProjectBrief" (which broke context and
// test_impact, both of which take SymbolsByName(...)[0]).
func TestSymbolsByName_PrefersExactNonTest(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"

	// Ingest the TEST symbol FIRST (lower rowid) so the old, ORDER-BY-less query
	// would have returned it first — the exact bug this guards against.
	if _, _, err := st.IngestFiles(ctx, []FileIngest{
		{
			File:    types.FileMeta{ID: "f:test", RepoID: repo, Path: "x_test.go", Language: "go"},
			Symbols: []types.Symbol{{ID: "s:test", RepoID: repo, Name: "TestProjectBrief", Kind: types.SymbolKindFunction, Path: "x_test.go", Language: "go"}},
		},
		{
			File:    types.FileMeta{ID: "f:impl", RepoID: repo, Path: "x.go", Language: "go"},
			Symbols: []types.Symbol{{ID: "s:impl", RepoID: repo, Name: "projectBrief", Kind: types.SymbolKindFunction, Path: "x.go", Language: "go"}},
		},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	syms, err := st.SymbolsByName(ctx, repo, "projectBrief", 5)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(syms) == 0 {
		t.Fatal("no results")
	}
	if syms[0].Name != "projectBrief" || syms[0].Path != "x.go" {
		t.Fatalf("expected exact non-test projectBrief first, got %q in %s", syms[0].Name, syms[0].Path)
	}
}
