package hubs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestWriteReadRoundtrip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(paths.RepoIndexDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := graph.Open(paths.DBPath(root))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const repo = "r"
	// core/Store is called cross-package by app/A → both a symbol hub and a
	// package hub for "core".
	syms := []types.Symbol{
		{ID: "sym:r:core/store.go:1:Store", RepoID: repo, Name: "Store", Kind: types.SymbolKindFunction, Path: "core/store.go", LineStart: 1},
		{ID: "sym:r:app/a.go:1:A", RepoID: repo, Name: "A", Kind: types.SymbolKindFunction, Path: "app/a.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:app/a.go:1:A", TargetID: "sym:r:core/store.go:1:Store", Confidence: 1}); err != nil {
		t.Fatal(err)
	}

	if err := Write(ctx, root, repo, st); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = st.Close()

	// The artifact exists on disk.
	if _, err := os.Stat(paths.HubsPath(root)); err != nil {
		t.Fatalf("hubs.json not written: %v", err)
	}
	d, err := Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if d.RepoID != repo {
		t.Errorf("repo_id=%q want %q", d.RepoID, repo)
	}
	if len(d.SymbolHubs) != 1 || d.SymbolHubs[0].Name != "Store" || d.SymbolHubs[0].Callers != 1 {
		t.Errorf("symbol hubs wrong: %+v", d.SymbolHubs)
	}
	if len(d.PackageHubs) != 1 || d.PackageHubs[0].Dir != "core" || d.PackageHubs[0].Callers != 1 {
		t.Errorf("package hubs wrong: %+v", d.PackageHubs)
	}
}

func TestReadMissing(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Read of a missing artifact must error so the caller falls back")
	}
}
