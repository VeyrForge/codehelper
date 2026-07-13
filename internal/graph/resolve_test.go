package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestResolveSymrefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Defined symbols:
	//  - uniqueFn  (unique name, one def)            -> should resolve repo-wide
	//  - dupFn     (two defs in different files)     -> ambiguous, must NOT resolve
	//  - localFn   (two defs, one in caller's file)  -> resolve to same-file def
	syms := []types.Symbol{
		{ID: "sym:repo:a.go:1:caller", RepoID: repoID, Name: "caller", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 1},
		{ID: "sym:repo:b.go:1:uniqueFn", RepoID: repoID, Name: "uniqueFn", Kind: types.SymbolKindFunction, Path: "b.go", LineStart: 1},
		{ID: "sym:repo:b.go:2:dupFn", RepoID: repoID, Name: "dupFn", Kind: types.SymbolKindFunction, Path: "b.go", LineStart: 2},
		{ID: "sym:repo:c.go:1:dupFn", RepoID: repoID, Name: "dupFn", Kind: types.SymbolKindFunction, Path: "c.go", LineStart: 1},
		{ID: "sym:repo:a.go:9:localFn", RepoID: repoID, Name: "localFn", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 9},
		{ID: "sym:repo:d.go:1:localFn", RepoID: repoID, Name: "localFn", Kind: types.SymbolKindFunction, Path: "d.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	caller := "sym:repo:a.go:1:caller"
	symrefs := []types.Reference{
		{ID: "e:repo:" + caller + ":calls:symref:repo:a.go:uniqueFn", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:a.go:uniqueFn", Confidence: 0.5},
		{ID: "e:repo:" + caller + ":calls:symref:repo:a.go:dupFn", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:a.go:dupFn", Confidence: 0.5},
		{ID: "e:repo:" + caller + ":calls:symref:repo:a.go:localFn", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:a.go:localFn", Confidence: 0.5},
		{ID: "e:repo:" + caller + ":calls:symref:repo:a.go:ghost", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:a.go:ghost", Confidence: 0.5},
	}
	for _, e := range symrefs {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if stats.Total != 4 {
		t.Errorf("total=%d want 4", stats.Total)
	}
	if stats.Resolved != 2 { // uniqueFn + localFn(same-file)
		t.Errorf("resolved=%d want 2 (%+v)", stats.Resolved, stats)
	}
	if stats.Ambiguous != 1 { // dupFn
		t.Errorf("ambiguous=%d want 1 (%+v)", stats.Ambiguous, stats)
	}
	if stats.Unresolved != 1 { // ghost
		t.Errorf("unresolved=%d want 1 (%+v)", stats.Unresolved, stats)
	}

	// uniqueFn now has a real incoming caller edge.
	in, err := st.EdgesTo(ctx, repoID, "sym:repo:b.go:1:uniqueFn", "calls")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].SourceID != caller {
		t.Errorf("uniqueFn callers=%+v want one from caller", in)
	}

	// localFn resolved to the SAME-FILE def (a.go), not d.go.
	inLocal, err := st.EdgesTo(ctx, repoID, "sym:repo:a.go:9:localFn", "calls")
	if err != nil {
		t.Fatal(err)
	}
	if len(inLocal) != 1 {
		t.Errorf("localFn(a.go) callers=%+v want 1", inLocal)
	}
	inLocalD, _ := st.EdgesTo(ctx, repoID, "sym:repo:d.go:1:localFn", "calls")
	if len(inLocalD) != 0 {
		t.Errorf("localFn(d.go) should have no callers, got %+v", inLocalD)
	}

	// dupFn stayed ambiguous: no concrete caller edges, symref remains.
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:b.go:2:dupFn", "calls"); len(c) != 0 {
		t.Errorf("dupFn should not be resolved, got %+v", c)
	}

	// Re-running is idempotent (no remaining resolvable symrefs).
	stats2, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Resolved != 0 {
		t.Errorf("second pass should resolve nothing, got %+v", stats2)
	}
}

func TestResolveSymrefsReceiverType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Two unrelated types both define a Get method (bare-name resolution would be
	// ambiguous). The caller invokes store.Get(), with store inferred as *Store,
	// so it must resolve to Store.Get and not Cache.Get.
	syms := []types.Symbol{
		{ID: "sym:repo:main.go:1:run", RepoID: repoID, Name: "run", Kind: types.SymbolKindFunction, Path: "main.go", LineStart: 1},
		{ID: "sym:repo:store.go:1:Get", RepoID: repoID, Name: "Get", Kind: types.SymbolKindMethod, Path: "store.go", LineStart: 1, ParentID: "Store"},
		{ID: "sym:repo:cache.go:1:Get", RepoID: repoID, Name: "Get", Kind: types.SymbolKindMethod, Path: "cache.go", LineStart: 1, ParentID: "Cache"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:main.go:1:run"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:repo:" + caller + ":calls:symref:repo:main.go:Store.Get", RepoID: repoID,
		Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:main.go:Store.Get", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Resolved != 1 || stats.ByStrategy["recv_type"] != 1 {
		t.Fatalf("expected 1 recv_type-resolved, got %+v", stats)
	}
	if in, _ := st.EdgesTo(ctx, repoID, "sym:repo:store.go:1:Get", "calls"); len(in) != 1 {
		t.Errorf("Store.Get should have 1 caller, got %d", len(in))
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:cache.go:1:Get", "calls"); len(c) != 0 {
		t.Errorf("Cache.Get should have 0 callers, got %d", len(c))
	}
}

func TestResolveSymrefsEmbeddedMethod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Derived embeds Base; Base defines Log. A call d.Log() qualified as
	// Derived.Log must resolve to Base.Log via promotion. A second type Other
	// also defines Log, so a bare-name resolution would be ambiguous.
	syms := []types.Symbol{
		{ID: "sym:repo:main.go:1:run", RepoID: repoID, Name: "run", Kind: types.SymbolKindFunction, Path: "main.go", LineStart: 1},
		{ID: "sym:repo:base.go:1:Base", RepoID: repoID, Name: "Base", Kind: types.SymbolKindClass, Path: "base.go", LineStart: 1},
		{ID: "sym:repo:base.go:5:Log", RepoID: repoID, Name: "Log", Kind: types.SymbolKindMethod, Path: "base.go", LineStart: 5, ParentID: "Base"},
		{ID: "sym:repo:derived.go:1:Derived", RepoID: repoID, Name: "Derived", Kind: types.SymbolKindClass, Path: "derived.go", LineStart: 1, Signature: "embeds=Base"},
		{ID: "sym:repo:other.go:1:Log", RepoID: repoID, Name: "Log", Kind: types.SymbolKindMethod, Path: "other.go", LineStart: 1, ParentID: "Other"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:main.go:1:run"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:repo:" + caller + ":calls:symref:repo:main.go:Derived.Log", RepoID: repoID,
		Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:main.go:Derived.Log", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Resolved != 1 || stats.ByStrategy["embedded"] != 1 {
		t.Fatalf("expected 1 embedded-resolved, got %+v", stats)
	}
	if in, _ := st.EdgesTo(ctx, repoID, "sym:repo:base.go:5:Log", "calls"); len(in) != 1 {
		t.Errorf("Base.Log should have 1 promoted caller, got %d", len(in))
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:other.go:1:Log", "calls"); len(c) != 0 {
		t.Errorf("Other.Log should have 0 callers, got %d", len(c))
	}
}

// TestResolveSymrefsReceiverTypeFallback verifies a type-qualified call whose
// receiver type has no matching method falls back to the bare-name cascade.
func TestResolveSymrefsReceiverTypeFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// store.Save() is type-qualified as Store.Save, but no Store.Save method
	// exists — only a single unique Save. The cascade should still resolve it.
	syms := []types.Symbol{
		{ID: "sym:repo:main.go:1:run", RepoID: repoID, Name: "run", Kind: types.SymbolKindFunction, Path: "main.go", LineStart: 1},
		{ID: "sym:repo:other.go:1:Save", RepoID: repoID, Name: "Save", Kind: types.SymbolKindMethod, Path: "other.go", LineStart: 1, ParentID: "Writer"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:main.go:1:run"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:repo:" + caller + ":calls:symref:repo:main.go:Store.Save", RepoID: repoID,
		Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:main.go:Store.Save", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Resolved != 1 || stats.ByStrategy["unique"] != 1 {
		t.Fatalf("expected 1 unique-resolved via fallback, got %+v", stats)
	}
}

// TestRevertEdgesIntoPaths simulates an incremental re-index: a caller in an
// unchanged file calls a function whose file is re-parsed (its symbol ID changes
// because line numbers shift). Reverting + re-resolving must preserve the caller
// edge, pointing it at the NEW symbol ID — not orphan it.
func TestRevertEdgesIntoPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	caller := "sym:repo:caller.go:1:run"
	calleeOld := "sym:repo:target.go:10:Helper"
	syms := []types.Symbol{
		{ID: caller, RepoID: repoID, Name: "run", Kind: types.SymbolKindFunction, Path: "caller.go", LineStart: 1},
		{ID: calleeOld, RepoID: repoID, Name: "Helper", Kind: types.SymbolKindFunction, Path: "target.go", LineStart: 10},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// A resolved caller edge run -> Helper (as ResolveSymrefs would have produced).
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:repo:" + caller + ":calls:" + calleeOld, RepoID: repoID,
		Kind: types.RefKindCalls, SourceID: caller, TargetID: calleeOld, Confidence: 0.8,
	}); err != nil {
		t.Fatal(err)
	}

	// --- incremental edit of target.go: revert, delete old symbol, add at new line ---
	if err := st.RevertEdgesIntoPaths(ctx, repoID, []string{"target.go"}); err != nil {
		t.Fatalf("revert: %v", err)
	}
	// The caller edge is now a symref placeholder again.
	if got, _ := st.EdgesTo(ctx, repoID, calleeOld, "calls"); len(got) != 0 {
		t.Errorf("old resolved edge should be reverted, still has %d callers", len(got))
	}
	// Simulate re-parse: Helper now lives at line 25 (a new symbol ID).
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM symbols WHERE id=?`, calleeOld); err != nil {
		t.Fatal(err)
	}
	calleeNew := "sym:repo:target.go:25:Helper"
	if err := st.UpsertSymbol(ctx, types.Symbol{ID: calleeNew, RepoID: repoID, Name: "Helper", Kind: types.SymbolKindFunction, Path: "target.go", LineStart: 25}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.ResolveSymrefs(ctx, repoID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The caller edge is preserved and now points at the NEW symbol ID.
	got, err := st.EdgesTo(ctx, repoID, calleeNew, "calls")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != caller {
		t.Errorf("caller edge not preserved across re-parse: got %+v", got)
	}
}

func TestResolveSymrefsImportAware(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Two defs of Helper in different packages; the caller (main.go) imports
	// only pkgb, so the call must resolve to pkgb's Helper.
	syms := []types.Symbol{
		{ID: "sym:repo:main.go:1:run", RepoID: repoID, Name: "run", Kind: types.SymbolKindFunction, Path: "main.go", LineStart: 1},
		{ID: "sym:repo:pkga/a.go:1:Helper", RepoID: repoID, Name: "Helper", Kind: types.SymbolKindFunction, Path: "pkga/a.go", LineStart: 1},
		{ID: "sym:repo:pkgb/b.go:1:Helper", RepoID: repoID, Name: "Helper", Kind: types.SymbolKindFunction, Path: "pkgb/b.go", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// main.go imports example.com/proj/pkgb
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:repo:file:repo:main.go:imports:mod:repo:example.com/proj/pkgb", RepoID: repoID,
		Kind: types.RefKindImports, SourceID: "file:repo:main.go", TargetID: "mod:repo:example.com/proj/pkgb", Confidence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	caller := "sym:repo:main.go:1:run"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:repo:" + caller + ":calls:symref:repo:main.go:Helper", RepoID: repoID,
		Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:main.go:Helper", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Resolved != 1 || stats.ByStrategy["import"] != 1 {
		t.Fatalf("expected 1 import-resolved, got %+v", stats)
	}
	// Edge points at pkgb's Helper, not pkga's.
	in, _ := st.EdgesTo(ctx, repoID, "sym:repo:pkgb/b.go:1:Helper", "calls")
	if len(in) != 1 {
		t.Errorf("pkgb.Helper should have 1 caller, got %d", len(in))
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:pkga/a.go:1:Helper", "calls"); len(c) != 0 {
		t.Errorf("pkga.Helper should have 0 callers, got %d", len(c))
	}
}

func TestDedupeSymrefInserts(t *testing.T) {
	t.Parallel()
	got := dedupeSymrefInserts([]symrefInsert{
		{id: "e:1", kind: "calls", src: "a", dst: "b", conf: 0.8},
		{id: "e:1", kind: "calls", src: "a", dst: "b", conf: 0.9},
		{id: "e:2", kind: "calls", src: "c", dst: "d", conf: 0.7},
	})
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[0].conf != 0.9 {
		t.Errorf("e:1 conf=%v want 0.9", got[0].conf)
	}
}
