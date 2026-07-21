package retrieval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestExpandGraphNeighbors_OneHopCallersCallees(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	seed := types.Symbol{ID: "sym:r:hub.go:1:Hub", RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: "hub.go", LineStart: 1}
	caller := types.Symbol{ID: "sym:r:a.go:1:Caller", RepoID: repo, Name: "Caller", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 1}
	callee := types.Symbol{ID: "sym:r:b.go:1:Callee", RepoID: repo, Name: "Callee", Kind: types.SymbolKindFunction, Path: "b.go", LineStart: 1}
	for _, s := range []types.Symbol{seed, caller, callee} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: caller.ID, TargetID: seed.ID, Confidence: 1},
		{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: seed.ID, TargetID: callee.ID, Confidence: 1},
	} {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	out := ExpandGraphNeighbors(ctx, st, repo, []RankedSymbol{{Symbol: seed, Score: 1, Reasons: []string{"bm25"}}}, GraphExpandOptions{MaxHops: 1})
	ids := map[string]bool{}
	for _, r := range out {
		ids[r.Symbol.ID] = true
	}
	if !ids[seed.ID] || !ids[caller.ID] || !ids[callee.ID] {
		t.Fatalf("expected seed+caller+callee, got %v", ids)
	}
}

func TestFuseRRF_PreservesGraphReason(t *testing.T) {
	a := []RankedSymbol{{Symbol: types.Symbol{ID: "1", Name: "a"}, Score: 1, Reasons: []string{"bm25"}}}
	b := []RankedSymbol{{Symbol: types.Symbol{ID: "2", Name: "b"}, Score: 1, Reasons: []string{"graph_hop_1"}}}
	out := FuseRRF(a, b, 60)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	found := false
	for _, r := range out {
		if r.Symbol.ID == "2" {
			for _, reason := range r.Reasons {
				if reason == "graph_hop_1" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected graph_hop_1 reason on fused neighbor, got %#v", out)
	}
}

func TestQueryHybrid_GraphExpandSurfacesCallee(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	save := types.Symbol{ID: "sym:r:u.go:1:SaveUser", RepoID: repo, Name: "SaveUser", Kind: types.SymbolKindFunction, Path: "u.go", LineStart: 1, Signature: "persists a user"}
	hash := types.Symbol{ID: "sym:r:u.go:20:hashPassword", RepoID: repo, Name: "hashPassword", Kind: types.SymbolKindFunction, Path: "u.go", LineStart: 20, Signature: "hashes password bytes"}
	noise := types.Symbol{ID: "sym:r:z.go:1:Zed", RepoID: repo, Name: "Zed", Kind: types.SymbolKindFunction, Path: "z.go", LineStart: 1, Signature: "unrelated"}
	for _, s := range []types.Symbol{save, hash, noise} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: save.ID, TargetID: hash.ID, Confidence: 1}); err != nil {
		t.Fatal(err)
	}

	hits, err := QueryHybridWithOptions(ctx, st, repo, "SaveUser", 10, QueryOptions{EnableGraphExpand: true})
	if err != nil {
		t.Fatal(err)
	}
	foundHash := false
	for _, h := range hits {
		if h.Symbol.ID == hash.ID {
			foundHash = true
			break
		}
	}
	if !foundHash {
		t.Fatalf("expected graph-expanded hashPassword in hybrid hits, got %#v", hits)
	}
}

func TestBuildPublicAPIMap_HubBias(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	hub := types.Symbol{ID: "sym:r:lib/api.go:1:Client", RepoID: repo, Name: "Client", Kind: types.SymbolKindClass, Path: "lib/api.go", LineStart: 1, Language: "go"}
	leaf := types.Symbol{ID: "sym:r:lib/api.go:40:Helper", RepoID: repo, Name: "Helper", Kind: types.SymbolKindFunction, Path: "lib/api.go", LineStart: 40, Language: "go"}
	priv := types.Symbol{ID: "sym:r:lib/api.go:60:hidden", RepoID: repo, Name: "hidden", Kind: types.SymbolKindFunction, Path: "lib/api.go", LineStart: 60, Language: "go"}
	for _, s := range []types.Symbol{hub, leaf, priv} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("sym:r:c%d.go:1:C%d", i, i)
		c := types.Symbol{ID: id, RepoID: repo, Name: fmt.Sprintf("C%d", i), Kind: types.SymbolKindFunction, Path: fmt.Sprintf("c%d.go", i), LineStart: 1}
		_ = st.UpsertSymbol(ctx, c)
		_ = st.AddEdge(ctx, types.Reference{ID: "e" + id, RepoID: repo, Kind: types.RefKindCalls, SourceID: c.ID, TargetID: hub.ID, Confidence: 1})
	}
	_ = st.AddEdge(ctx, types.Reference{ID: "el", RepoID: repo, Kind: types.RefKindCalls, SourceID: hub.ID, TargetID: leaf.ID, Confidence: 1})

	got, err := BuildPublicAPIMap(ctx, st, repo, PublicAPIMapOptions{PathPrefix: "lib/", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Symbol.ID != hub.ID {
		t.Fatalf("expected Client hub first, got %#v", got)
	}
	for _, e := range got {
		if e.Symbol.Name == "hidden" {
			t.Fatal("unexported Go symbol should be excluded from public API map")
		}
	}
}

func TestBuildContextBundle_BoundsAndSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := graph.Open(filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	srcPath := "hub.go"
	if err := os.WriteFile(filepath.Join(dir, srcPath), []byte("package p\n\nfunc Hub() {\n\tCallee()\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := types.Symbol{ID: "sym:r:hub.go:3:Hub", RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: srcPath, LineStart: 3, LineEnd: 5}
	callee := types.Symbol{ID: "sym:r:hub.go:10:Callee", RepoID: repo, Name: "Callee", Kind: types.SymbolKindFunction, Path: srcPath, LineStart: 10, LineEnd: 12}
	caller := types.Symbol{ID: "sym:r:a.go:1:A", RepoID: repo, Name: "A", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 1}
	for _, s := range []types.Symbol{hub, callee, caller} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.AddEdge(ctx, types.Reference{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: caller.ID, TargetID: hub.ID, Confidence: 1})
	_ = st.AddEdge(ctx, types.Reference{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: hub.ID, TargetID: callee.ID, Confidence: 1})

	b, err := BuildContextBundle(ctx, st, repo, hub.ID, ContextBundleOptions{
		CallerLimit: 10, CalleeLimit: 10, ImportLimit: 10,
		RepoRoot: dir, MaxSourceLines: 10, IncludeTests: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Source == "" || !contains(b.Source, "func Hub") {
		t.Fatalf("expected source snippet, got %q", b.Source)
	}
	if len(b.Callers) != 1 || len(b.Callees) != 1 {
		t.Fatalf("callers=%d callees=%d", len(b.Callers), len(b.Callees))
	}
}
