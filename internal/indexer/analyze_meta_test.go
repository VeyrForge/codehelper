package indexer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestRefreshMetaCounts_WritesLiveGraphCounts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repoID := "testrepo"
	if err := meta.Write(root, &meta.Data{
		RepoName:    repoID,
		RootPath:    root,
		SymbolCount: 9999,
		EdgeCount:   1,
		FileCount:   1,
	}); err != nil {
		t.Fatalf("meta.Write: %v", err)
	}
	st, err := graph.Open(paths.DBPath(root))
	if err != nil {
		t.Fatalf("graph.Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.UpsertSymbol(ctx, types.Symbol{
		ID: repoID + ":sym:a", RepoID: repoID, Name: "A", Kind: types.SymbolKindFunction,
		Path: "a.go", LineStart: 1, LineEnd: 2, Language: "go",
	}); err != nil {
		t.Fatalf("UpsertSymbol: %v", err)
	}
	prev, _ := meta.Read(root)
	if err := refreshMetaCounts(ctx, st, root, repoID, prev); err != nil {
		t.Fatalf("refreshMetaCounts: %v", err)
	}
	got, err := meta.Read(root)
	if err != nil {
		t.Fatalf("meta.Read: %v", err)
	}
	if got.SymbolCount != 1 {
		t.Fatalf("SymbolCount=%d want 1", got.SymbolCount)
	}
	_ = filepath.Join(root, ".codehelper")
}
