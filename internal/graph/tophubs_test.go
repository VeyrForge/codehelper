package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestTopHubs(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"

	syms := []types.Symbol{
		{ID: "sym:r:hub.go:5:Hub", RepoID: repo, Name: "Hub", Kind: types.SymbolKindFunction, Path: "hub.go", LineStart: 5},
		{ID: "sym:r:mid.go:1:Mid", RepoID: repo, Name: "Mid", Kind: types.SymbolKindFunction, Path: "mid.go", LineStart: 1},
		{ID: "sym:r:leaf.go:1:Leaf", RepoID: repo, Name: "Leaf", Kind: types.SymbolKindFunction, Path: "leaf.go", LineStart: 1},
		{ID: "sym:r:c1.go:1:C1", RepoID: repo, Name: "C1", Kind: types.SymbolKindFunction, Path: "c1.go", LineStart: 1},
		{ID: "sym:r:c2.go:1:C2", RepoID: repo, Name: "C2", Kind: types.SymbolKindFunction, Path: "c2.go", LineStart: 1},
		{ID: "sym:r:c3.go:1:C3", RepoID: repo, Name: "C3", Kind: types.SymbolKindFunction, Path: "c3.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	// C1,C2,C3 all call Hub (3 callers); only C1 calls Mid (1 caller); Leaf: none.
	edges := []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:c1.go:1:C1", TargetID: "sym:r:hub.go:5:Hub", Confidence: 1},
		{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:c2.go:1:C2", TargetID: "sym:r:hub.go:5:Hub", Confidence: 1},
		{ID: "e3", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:c3.go:1:C3", TargetID: "sym:r:hub.go:5:Hub", Confidence: 1},
		{ID: "e4", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:c1.go:1:C1", TargetID: "sym:r:mid.go:1:Mid", Confidence: 1},
	}
	for _, e := range edges {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}

	hubs, err := st.TopHubs(ctx, repo, 5)
	if err != nil {
		t.Fatalf("TopHubs: %v", err)
	}
	if len(hubs) != 2 {
		t.Fatalf("expected 2 hubs (Hub, Mid; Leaf has no callers), got %d: %+v", len(hubs), hubs)
	}
	// Most-called first, with the right count and loc.
	if hubs[0].Name != "Hub" || hubs[0].Callers != 3 || hubs[0].Line != 5 || hubs[0].Path != "hub.go" {
		t.Errorf("hub #1 wrong: %+v", hubs[0])
	}
	if hubs[1].Name != "Mid" || hubs[1].Callers != 1 {
		t.Errorf("hub #2 wrong: %+v", hubs[1])
	}
	// A leaf symbol with no inbound calls must not appear.
	for _, h := range hubs {
		if h.Name == "Leaf" {
			t.Errorf("Leaf has no callers and must not be a hub: %+v", h)
		}
	}
}

func TestIsVendorPath(t *testing.T) {
	vendor := []string{"third_party/x/a.go", "vendor/y/b.go", "internal/vendor/c.go",
		"web/node_modules/d.js", "app/dist/bundle.js", "src/app.min.js",
		"frontend/.output/server/chunks/nitro.mjs", ".output/x.js",
		"web/.next/page.js", "rust/target/debug/x.rs", "py/__pycache__/m.pyc"}
	for _, p := range vendor {
		if !isVendorPath(p) {
			t.Errorf("%q should be vendor", p)
		}
	}
	for _, p := range []string{"internal/graph/store.go", "cmd/main.go", "src/app.ts"} {
		if isVendorPath(p) {
			t.Errorf("%q should NOT be vendor", p)
		}
	}
}

func TestTopPackages(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"
	syms := []types.Symbol{
		{ID: "sym:r:core/store.go:1:Store", RepoID: repo, Name: "Store", Kind: types.SymbolKindFunction, Path: "core/store.go", LineStart: 1},
		{ID: "sym:r:core/helper.go:1:H", RepoID: repo, Name: "H", Kind: types.SymbolKindFunction, Path: "core/helper.go", LineStart: 1},
		{ID: "sym:r:app/a.go:1:A", RepoID: repo, Name: "A", Kind: types.SymbolKindFunction, Path: "app/a.go", LineStart: 1},
		{ID: "sym:r:app/b.go:1:B", RepoID: repo, Name: "B", Kind: types.SymbolKindFunction, Path: "app/b.go", LineStart: 1},
		{ID: "sym:r:web/w.go:1:W", RepoID: repo, Name: "W", Kind: types.SymbolKindFunction, Path: "web/w.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	// A(app), B(app), W(web) call core/Store (cross-package). H(core) also calls
	// Store but is SAME package — must not count.
	edges := []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:app/a.go:1:A", TargetID: "sym:r:core/store.go:1:Store", Confidence: 1},
		{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:app/b.go:1:B", TargetID: "sym:r:core/store.go:1:Store", Confidence: 1},
		{ID: "e3", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:web/w.go:1:W", TargetID: "sym:r:core/store.go:1:Store", Confidence: 1},
		{ID: "e4", RepoID: repo, Kind: types.RefKindCalls, SourceID: "sym:r:core/helper.go:1:H", TargetID: "sym:r:core/store.go:1:Store", Confidence: 1},
	}
	for _, e := range edges {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	pkgs, err := st.TopPackages(ctx, repo, 6)
	if err != nil {
		t.Fatalf("TopPackages: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package hub (core), got %d: %+v", len(pkgs), pkgs)
	}
	// 3 cross-package callers (A,B,W); the same-package H excluded.
	if pkgs[0].Dir != "core" || pkgs[0].Callers != 3 {
		t.Errorf("core hub wrong: %+v (want Dir=core Callers=3)", pkgs[0])
	}
	// From 2 distinct packages: app and web.
	if pkgs[0].FromPkgs != 2 {
		t.Errorf("FromPkgs=%d want 2 (app, web)", pkgs[0].FromPkgs)
	}
}
