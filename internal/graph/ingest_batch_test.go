package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// IngestFiles must persist the same rows as the per-row path, be idempotent on
// re-ingest, and return accurate counts.
func TestIngestFiles_WritesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"

	batch := []FileIngest{
		{
			File:    types.FileMeta{ID: "f:1", RepoID: repo, Path: "a.go", Language: "go"},
			Symbols: []types.Symbol{{ID: "s:1", RepoID: repo, Name: "A", Kind: types.SymbolKindFunction, Path: "a.go", Language: "go"}},
			Edges:   []types.Reference{{ID: "e:1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "s:1", TargetID: "s:2", Confidence: 1}},
		},
		{
			File:    types.FileMeta{ID: "f:2", RepoID: repo, Path: "b.go", Language: "go"},
			Symbols: []types.Symbol{{ID: "s:2", RepoID: repo, Name: "B", Kind: types.SymbolKindFunction, Path: "b.go", Language: "go"}},
		},
	}

	sc, ec, err := st.IngestFiles(ctx, batch)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if sc != 2 || ec != 1 {
		t.Errorf("counts wrong: syms=%d edges=%d (want 2,1)", sc, ec)
	}
	s, e, f, _ := st.Counts(ctx, repo)
	if s != 2 || e != 1 || f != 2 {
		t.Fatalf("store counts wrong: s=%d e=%d f=%d (want 2,1,2)", s, e, f)
	}

	// Re-ingest the same batch: upserts, so totals must not double.
	if _, _, err := st.IngestFiles(ctx, batch); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	s2, e2, f2, _ := st.Counts(ctx, repo)
	if s2 != 2 || e2 != 1 || f2 != 2 {
		t.Errorf("re-ingest not idempotent: s=%d e=%d f=%d", s2, e2, f2)
	}

	// An empty batch is a no-op.
	if sc, ec, err := st.IngestFiles(ctx, nil); err != nil || sc != 0 || ec != 0 {
		t.Errorf("empty batch should no-op: sc=%d ec=%d err=%v", sc, ec, err)
	}

	// The symbol is queryable (rows really landed, not just counted).
	sym, err := st.SymbolByID(ctx, repo, "s:1")
	if err != nil || sym == nil || sym.Name != "A" {
		t.Errorf("symbol not retrievable after ingest: %v %+v", err, sym)
	}
}
