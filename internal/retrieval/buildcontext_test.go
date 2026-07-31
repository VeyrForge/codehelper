package retrieval

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestBuildContextCallers guards the N+1 fix: BuildContext must return every
// caller of a symbol (via a single CallersOf JOIN, not EdgesTo + per-edge
// SymbolByID) and its callees.
func TestBuildContextCallers(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	syms := []types.Symbol{
		{ID: "sym:r:hub.go:1:Hub", RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: "hub.go", LineStart: 1},
		{ID: "sym:r:a.go:1:A", RepoID: repo, Name: "A", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 1},
		{ID: "sym:r:b.go:1:B", RepoID: repo, Name: "B", Kind: types.SymbolKindFunction, Path: "b.go", LineStart: 1},
		{ID: "sym:r:c.go:1:C", RepoID: repo, Name: "C", Kind: types.SymbolKindFunction, Path: "c.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	// A and B call Hub; Hub calls C.
	edges := []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:a.go:1:A", TargetID: "sym:r:hub.go:1:Hub", Confidence: 1},
		{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:b.go:1:B", TargetID: "sym:r:hub.go:1:Hub", Confidence: 1},
		{ID: "e3", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:hub.go:1:Hub", TargetID: "sym:r:c.go:1:C", Confidence: 1},
	}
	for _, e := range edges {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	b, err := BuildContext(ctx, st, repo, "sym:r:hub.go:1:Hub")
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if len(b.Callers) != 2 {
		t.Errorf("Hub should have 2 callers (A, B), got %d: %+v", len(b.Callers), b.Callers)
	}
	names := map[string]bool{}
	for _, c := range b.Callers {
		names[c.Name] = true
	}
	if !names["A"] || !names["B"] {
		t.Errorf("callers must be A and B, got %v", names)
	}
	if len(b.Callees) != 1 {
		t.Errorf("Hub should have 1 callee edge (C), got %d", len(b.Callees))
	}
}

func TestBuildContextLimited(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "bl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	hub := "sym:r:hub.go:1:Hub"
	if err := st.UpsertSymbol(ctx, types.Symbol{ID: hub, RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: "hub.go", LineStart: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("sym:r:c%d.go:1:C%d", i, i)
		st.UpsertSymbol(ctx, types.Symbol{ID: id, RepoID: repo, Name: fmt.Sprintf("C%d", i), Kind: types.SymbolKindFunction, Path: fmt.Sprintf("c%d.go", i), LineStart: 1})
		st.AddEdge(ctx, types.Reference{ID: fmt.Sprintf("e%d", i), RepoID: repo, Kind: types.RefKindCalls, SourceID: id, TargetID: hub, Confidence: 1})
	}
	b, err := BuildContextLimited(ctx, st, repo, hub, 10)
	if err != nil {
		t.Fatalf("BuildContextLimited: %v", err)
	}
	if len(b.Callers) != 10 {
		t.Errorf("caller list should be capped at 10, got %d", len(b.Callers))
	}
	if b.CallersTotal != 30 {
		t.Errorf("CallersTotal must be the true count 30 even when capped, got %d", b.CallersTotal)
	}
}
