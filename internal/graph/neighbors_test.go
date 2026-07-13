package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestNeighbors(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	syms := []types.Symbol{
		{ID: "sym:r:hub.go:1:Hub", RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: "hub.go", LineStart: 1},
		{ID: "sym:r:a.go:1:A", RepoID: repo, Name: "A", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 1},
		{ID: "sym:r:b.go:1:B", RepoID: repo, Name: "B", Kind: types.SymbolKindFunction, Path: "b.go", LineStart: 1},
		{ID: "sym:r:g.go:1:Global", RepoID: repo, Name: "Global", Kind: types.SymbolKindFunction, Path: "g.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	edges := []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:a.go:1:A", TargetID: "sym:r:hub.go:1:Hub", Confidence: 0.9},
		{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:b.go:1:B", TargetID: "sym:r:hub.go:1:Hub", Confidence: 0.8},
		{ID: "e3", RepoID: repo, Kind: types.RefKindReads, SourceID: "sym:r:hub.go:1:Hub", TargetID: "sym:r:g.go:1:Global", Confidence: 0.7},
		// Edge to a non-symbol node (a file) — must be excluded by the JOIN.
		{ID: "e4", RepoID: repo, Kind: types.RefKindImports, SourceID: "sym:r:hub.go:1:Hub", TargetID: "file:r:other.go", Confidence: 1},
	}
	for _, e := range edges {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	// Incoming: Hub's callers (A, B via calls).
	in, err := st.Neighbors(ctx, repo, "sym:r:hub.go:1:Hub", true, string(types.RefKindCalls), string(types.RefKindReads), string(types.RefKindImports))
	if err != nil {
		t.Fatalf("Neighbors incoming: %v", err)
	}
	if len(in) != 2 {
		t.Fatalf("Hub should have 2 incoming symbol neighbors (A,B), got %d: %+v", len(in), in)
	}
	byName := map[string]NeighborSymbol{}
	for _, n := range in {
		byName[n.Symbol.Name] = n
	}
	if byName["A"].Confidence != 0.9 || byName["A"].EdgeKind != string(types.RefKindCalls) {
		t.Errorf("A neighbor edge wrong: %+v", byName["A"])
	}

	// Outgoing: Hub reads Global; the import to a file node is excluded.
	out, err := st.Neighbors(ctx, repo, "sym:r:hub.go:1:Hub", false, string(types.RefKindCalls), string(types.RefKindReads), string(types.RefKindImports))
	if err != nil {
		t.Fatalf("Neighbors outgoing: %v", err)
	}
	if len(out) != 1 || out[0].Symbol.Name != "Global" || out[0].EdgeKind != string(types.RefKindReads) {
		t.Errorf("outgoing should be exactly Global via reads (file import excluded), got %+v", out)
	}

	// No kinds → nothing.
	if got, _ := st.Neighbors(ctx, repo, "sym:r:hub.go:1:Hub", true); got != nil {
		t.Errorf("no kinds must return nil, got %+v", got)
	}
}

func TestCallersOfLimitedAndCount(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	hub := "sym:r:hub.go:1:Hub"
	if err := st.UpsertSymbol(ctx, types.Symbol{ID: hub, RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: "hub.go", LineStart: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id := "sym:r:c.go:" + string(rune('1'+i)) + ":C"
		if err := st.UpsertSymbol(ctx, types.Symbol{ID: id, RepoID: repo, Name: "C", Kind: types.SymbolKindFunction, Path: "c.go", LineStart: i + 1}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddEdge(ctx, types.Reference{ID: "e" + string(rune('1'+i)), RepoID: repo, Kind: types.RefKindCalls, SourceID: id, TargetID: hub, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.CountCallers(ctx, repo, hub); err != nil || n != 5 {
		t.Errorf("CountCallers = %d, %v; want 5", n, err)
	}
	if cs, err := st.CallersOfLimited(ctx, repo, hub, 2); err != nil || len(cs) != 2 {
		t.Errorf("CallersOfLimited(2) = %d, %v; want 2", len(cs), err)
	}
	if cs, _ := st.CallersOfLimited(ctx, repo, hub, 10); len(cs) != 5 {
		t.Errorf("CallersOfLimited(10) = %d; want 5 (all)", len(cs))
	}
	if cs, _ := st.CallersOfLimited(ctx, repo, hub, 0); len(cs) != 5 {
		t.Errorf("CallersOfLimited(0) should be unbounded (5), got %d", len(cs))
	}
}
