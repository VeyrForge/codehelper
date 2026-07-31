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

func TestResolveSymrefs_PublicAPIPreference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	syms := []types.Symbol{
		{ID: "sym:repo:docs_src/app.py:1:read_items", RepoID: repoID, Name: "read_items", Kind: types.SymbolKindFunction, Path: "docs_src/app.py", LineStart: 1},
		{ID: "sym:repo:fastapi/params.py:1:Depends", RepoID: repoID, Name: "Depends", Kind: types.SymbolKindClass, Path: "fastapi/params.py", LineStart: 1},
		{ID: "sym:repo:fastapi/param_functions.py:1:Depends", RepoID: repoID, Name: "Depends", Kind: types.SymbolKindFunction, Path: "fastapi/param_functions.py", LineStart: 1},
		{ID: "sym:repo:fastapi/applications.py:1:include_router", RepoID: repoID, Name: "include_router", Kind: types.SymbolKindMethod, Path: "fastapi/applications.py", LineStart: 1},
		{ID: "sym:repo:fastapi/routing.py:1:include_router", RepoID: repoID, Name: "include_router", Kind: types.SymbolKindMethod, Path: "fastapi/routing.py", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:docs_src/app.py:1:read_items"
	for _, e := range []types.Reference{
		{ID: "e1", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:docs_src/app.py:Depends", Confidence: 0.5},
		{ID: "e2", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller, TargetID: "symref:repo:docs_src/app.py:include_router", Confidence: 0.5},
	} {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["public_api"] < 2 {
		t.Fatalf("expected public_api resolutions, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:fastapi/param_functions.py:1:Depends", "calls"); len(c) != 1 {
		t.Errorf("Depends should resolve to param_functions, callers=%d", len(c))
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:fastapi/applications.py:1:include_router", "calls"); len(c) != 1 {
		t.Errorf("include_router should resolve to applications, callers=%d", len(c))
	}
}

func TestResolveSymrefs_DottedAliasNotSplitRecv(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Express app.use is a unique dotted symbol name — must NOT be split into
	// recv_type(app)+use and then fail as ambiguous bare "use".
	syms := []types.Symbol{
		{ID: "sym:repo:examples/hello.js:1:express_use_1", RepoID: repoID, Name: "express_use_1", Kind: types.SymbolKindFunction, Path: "examples/hello.js", LineStart: 1},
		{ID: "sym:repo:lib/application.js:1:app.use", RepoID: repoID, Name: "app.use", Kind: types.SymbolKindFunction, Path: "lib/application.js", LineStart: 1},
		{ID: "sym:repo:lib/router.js:1:use", RepoID: repoID, Name: "use", Kind: types.SymbolKindFunction, Path: "lib/router.js", LineStart: 1},
		{ID: "sym:repo:lib/utils.js:1:use", RepoID: repoID, Name: "use", Kind: types.SymbolKindFunction, Path: "lib/utils.js", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:examples/hello.js:1:express_use_1"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e1", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:examples/hello.js:app.use", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Resolved != 1 || stats.ByStrategy["unique"] != 1 {
		t.Fatalf("expected unique resolve of app.use, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:lib/application.js:1:app.use", "calls"); len(c) != 1 {
		t.Errorf("app.use should have 1 caller, got %d", len(c))
	}
}

func TestResolveSymrefs_RelativeImportJS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Nest-style: CoreModule imports ./interceptors/transform.interceptor while
	// two TransformInterceptor defs exist across samples.
	syms := []types.Symbol{
		{ID: "sym:repo:sample/01-cats/core/core.module.ts:1:CoreModule", RepoID: repoID, Name: "CoreModule", Kind: types.SymbolKindClass, Path: "sample/01-cats/core/core.module.ts", LineStart: 1},
		{ID: "sym:repo:sample/01-cats/core/interceptors/transform.interceptor.ts:1:TransformInterceptor", RepoID: repoID, Name: "TransformInterceptor", Kind: types.SymbolKindClass, Path: "sample/01-cats/core/interceptors/transform.interceptor.ts", LineStart: 1},
		{ID: "sym:repo:sample/10-fastify/core/interceptors/transform.interceptor.ts:1:TransformInterceptor", RepoID: repoID, Name: "TransformInterceptor", Kind: types.SymbolKindClass, Path: "sample/10-fastify/core/interceptors/transform.interceptor.ts", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:imp", RepoID: repoID, Kind: types.RefKindImports,
		SourceID: "file:repo:sample/01-cats/core/core.module.ts",
		TargetID: "mod:repo:./interceptors/transform.interceptor", Confidence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	caller := "sym:repo:sample/01-cats/core/core.module.ts:1:CoreModule"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:call", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:sample/01-cats/core/core.module.ts:TransformInterceptor", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["import"] != 1 {
		t.Fatalf("expected relative import resolution, got %+v", stats)
	}
	want := "sym:repo:sample/01-cats/core/interceptors/transform.interceptor.ts:1:TransformInterceptor"
	if c, _ := st.EdgesTo(ctx, repoID, want, "calls"); len(c) != 1 {
		t.Errorf("want inbound to cats TransformInterceptor, callers=%d", len(c))
	}
	bad := "sym:repo:sample/10-fastify/core/interceptors/transform.interceptor.ts:1:TransformInterceptor"
	if c, _ := st.EdgesTo(ctx, repoID, bad, "calls"); len(c) != 0 {
		t.Errorf("must not resolve to fastify sample, callers=%d", len(c))
	}
}

func TestResolveSymrefs_SameSubtree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// No imports: same_subtree should pick the interceptor under the same sample app.
	syms := []types.Symbol{
		{ID: "sym:repo:sample/01-cats/core/core.module.ts:1:CoreModule", RepoID: repoID, Name: "CoreModule", Kind: types.SymbolKindClass, Path: "sample/01-cats/core/core.module.ts", LineStart: 1},
		{ID: "sym:repo:sample/01-cats/core/interceptors/logging.interceptor.ts:1:LoggingInterceptor", RepoID: repoID, Name: "LoggingInterceptor", Kind: types.SymbolKindClass, Path: "sample/01-cats/core/interceptors/logging.interceptor.ts", LineStart: 1},
		{ID: "sym:repo:sample/10-fastify/core/interceptors/logging.interceptor.ts:1:LoggingInterceptor", RepoID: repoID, Name: "LoggingInterceptor", Kind: types.SymbolKindClass, Path: "sample/10-fastify/core/interceptors/logging.interceptor.ts", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:sample/01-cats/core/core.module.ts:1:CoreModule"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:call", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:sample/01-cats/core/core.module.ts:LoggingInterceptor", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["same_subtree"] != 1 {
		t.Fatalf("expected same_subtree, got %+v", stats)
	}
	want := "sym:repo:sample/01-cats/core/interceptors/logging.interceptor.ts:1:LoggingInterceptor"
	if c, _ := st.EdgesTo(ctx, repoID, want, "calls"); len(c) != 1 {
		t.Errorf("want inbound to cats LoggingInterceptor, callers=%d", len(c))
	}
}

func TestResolveSymrefs_NonFixturePreference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	syms := []types.Symbol{
		{ID: "sym:repo:packages/core/app.ts:1:caller", RepoID: repoID, Name: "caller", Kind: types.SymbolKindFunction, Path: "packages/core/app.ts", LineStart: 1},
		{ID: "sym:repo:sample/01-cats/cats.service.ts:1:CatsService", RepoID: repoID, Name: "CatsService", Kind: types.SymbolKindClass, Path: "sample/01-cats/cats.service.ts", LineStart: 1},
		{ID: "sym:repo:packages/cats/cats.service.ts:1:CatsService", RepoID: repoID, Name: "CatsService", Kind: types.SymbolKindClass, Path: "packages/cats/cats.service.ts", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:packages/core/app.ts:1:caller"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e1", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:packages/core/app.ts:CatsService", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["non_fixture"] != 1 {
		t.Fatalf("expected non_fixture resolution, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:packages/cats/cats.service.ts:1:CatsService", "calls"); len(c) != 1 {
		t.Errorf("should prefer packages/ over sample/, callers=%d", len(c))
	}
}

func TestResolveSymrefs_GodotAddonPreference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	gameReady := "sym:repo:scripts/player.gd:1:_ready"
	addonReady := "sym:repo:addons/vendor/plugin.gd:1:_ready"
	caller := "sym:repo:autoload/boot.gd:1:boot"
	syms := []types.Symbol{
		{ID: caller, RepoID: repoID, Name: "boot", Kind: types.SymbolKindFunction, Path: "autoload/boot.gd", LineStart: 1},
		{ID: gameReady, RepoID: repoID, Name: "_ready", Kind: types.SymbolKindFunction, Path: "scripts/player.gd", LineStart: 1, ParentID: "Player"},
		{ID: addonReady, RepoID: repoID, Name: "_ready", Kind: types.SymbolKindFunction, Path: "addons/vendor/plugin.gd", LineStart: 1, ParentID: "VendorPlugin"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e1", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:autoload/boot.gd:_ready", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["non_fixture"] != 1 {
		t.Fatalf("expected non_fixture resolution for addons/, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, gameReady, "calls"); len(c) != 1 {
		t.Errorf("should prefer scripts/ over addons/, callers=%d", len(c))
	}
}

func TestResolveSymrefs_UnityGetComponentInbound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	health := "sym:repo:Assets/Scripts/Health.cs:1:Health"
	start := "sym:repo:Assets/Scripts/Player.cs:10:Start"
	syms := []types.Symbol{
		{ID: health, RepoID: repoID, Name: "Health", Kind: types.SymbolKindClass, Path: "Assets/Scripts/Health.cs", LineStart: 1},
		{ID: start, RepoID: repoID, Name: "Start", Kind: types.SymbolKindMethod, Path: "Assets/Scripts/Player.cs", LineStart: 10, ParentID: "Player"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "r1", RepoID: repoID, Kind: types.RefKindReads, SourceID: start,
		TargetID: "symref:repo:Assets/Scripts/Player.cs:Health", Confidence: 0.8,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveSymrefs(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	callers, err := st.CallersOf(ctx, repoID, health)
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Name != "Start" {
		t.Fatalf("Health inbound via GetComponent reads want Start, got %#v", callers)
	}
}

func TestResolveSymrefs_UnrealHealthInbound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	health := "sym:repo:Source/MyGame/HealthComponent.h:1:UHealthComponent"
	apply := "sym:repo:Source/MyGame/HealthComponent.h:10:ApplyDamage"
	begin := "sym:repo:Source/MyGame/MyGameCharacter.cpp:8:BeginPlay"
	syms := []types.Symbol{
		{ID: health, RepoID: repoID, Name: "UHealthComponent", Kind: types.SymbolKindClass, Path: "Source/MyGame/HealthComponent.h", LineStart: 1},
		{ID: apply, RepoID: repoID, Name: "ApplyDamage", Kind: types.SymbolKindMethod, Path: "Source/MyGame/HealthComponent.h", LineStart: 10, ParentID: "UHealthComponent"},
		{ID: begin, RepoID: repoID, Name: "BeginPlay", Kind: types.SymbolKindMethod, Path: "Source/MyGame/MyGameCharacter.cpp", LineStart: 8, ParentID: "AMyGameCharacter"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "r1", RepoID: repoID, Kind: types.RefKindReads, SourceID: begin,
		TargetID: "symref:repo:Source/MyGame/MyGameCharacter.cpp:UHealthComponent", Confidence: 0.85,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "c1", RepoID: repoID, Kind: types.RefKindCalls, SourceID: begin,
		TargetID: "symref:repo:Source/MyGame/MyGameCharacter.cpp:UHealthComponent.ApplyDamage", Confidence: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveSymrefs(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	callers, err := st.CallersOf(ctx, repoID, health)
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Name != "BeginPlay" {
		t.Fatalf("UHealthComponent inbound via Cast reads want BeginPlay, got %#v", callers)
	}
	ac, err := st.CallersOf(ctx, repoID, apply)
	if err != nil {
		t.Fatal(err)
	}
	if len(ac) != 1 || ac[0].Name != "BeginPlay" {
		t.Fatalf("ApplyDamage inbound via typed call want BeginPlay, got %#v", ac)
	}
}

func TestResolveSymrefs_TypeKindPrefersClassOverCtor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cls := "sym:repo:Health.h:1:UHealth"
	ctor := "sym:repo:Health.h:5:UHealth"
	begin := "sym:repo:Char.cpp:1:BeginPlay"
	for _, s := range []types.Symbol{
		{ID: cls, RepoID: repoID, Name: "UHealth", Kind: types.SymbolKindClass, Path: "Health.h", LineStart: 1},
		{ID: ctor, RepoID: repoID, Name: "UHealth", Kind: types.SymbolKindMethod, Path: "Health.h", LineStart: 5, ParentID: "UHealth"},
		{ID: begin, RepoID: repoID, Name: "BeginPlay", Kind: types.SymbolKindMethod, Path: "Char.cpp", LineStart: 1, ParentID: "AChar"},
	} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "r1", RepoID: repoID, Kind: types.RefKindReads, SourceID: begin,
		TargetID: "symref:repo:Char.cpp:UHealth", Confidence: 0.85,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["type_kind"] != 1 {
		t.Fatalf("expected type_kind prefer class, got %+v", stats)
	}
	callers, _ := st.CallersOf(ctx, repoID, cls)
	if len(callers) != 1 {
		t.Fatalf("class inbound want 1, got %#v", callers)
	}
}

func TestResolveSymrefs_GodotTakeHitInbound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	take := "sym:repo:scripts/enemy.gd:9:take_hit"
	ready := "sym:repo:scripts/player.gd:7:_ready"
	enemy := "sym:repo:scripts/enemy.gd:3:Enemy"
	syms := []types.Symbol{
		{ID: enemy, RepoID: repoID, Name: "Enemy", Kind: types.SymbolKindClass, Path: "scripts/enemy.gd", LineStart: 3},
		{ID: take, RepoID: repoID, Name: "take_hit", Kind: types.SymbolKindMethod, Path: "scripts/enemy.gd", LineStart: 9, ParentID: "Enemy"},
		{ID: ready, RepoID: repoID, Name: "_ready", Kind: types.SymbolKindMethod, Path: "scripts/player.gd", LineStart: 7, ParentID: "Player"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "c1", RepoID: repoID, Kind: types.RefKindCalls, SourceID: ready,
		TargetID: "symref:repo:scripts/player.gd:Enemy.take_hit", Confidence: 0.65,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["recv_type"] != 1 {
		t.Fatalf("expected recv_type for Enemy.take_hit, got %+v", stats)
	}
	callers, err := st.CallersOf(ctx, repoID, take)
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Name != "_ready" {
		t.Fatalf("take_hit inbound want _ready, got %#v", callers)
	}
}

func TestResolveSymrefs_PHPAliasExtendsExcludesSelf(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	appUser := "sym:repo:app/Models/User.php:1:User"
	illumUser := "sym:repo:vendor/laravel/framework/src/Illuminate/Foundation/Auth/User.php:1:User"
	syms := []types.Symbol{
		{ID: appUser, RepoID: repoID, Name: "User", Kind: types.SymbolKindClass, Path: "app/Models/User.php", LineStart: 1},
		{ID: illumUser, RepoID: repoID, Name: "User", Kind: types.SymbolKindClass,
			Path: "vendor/laravel/framework/src/Illuminate/Foundation/Auth/User.php", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "imp", RepoID: repoID, Kind: types.RefKindImports,
		SourceID: "file:repo:app/Models/User.php",
		TargetID: `mod:repo:Illuminate\Foundation\Auth\User`, Confidence: 0.85,
	}); err != nil {
		t.Fatal(err)
	}
	// Parser rewrote Authenticatable → User leaf.
	if err := st.AddEdge(ctx, types.Reference{
		ID: "inh", RepoID: repoID, Kind: types.RefKindInherits, SourceID: appUser,
		TargetID: "symref:repo:app/Models/User.php:User", Confidence: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Resolved != 1 {
		t.Fatalf("expected resolve to Illuminate User, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, illumUser, "inherits"); len(c) != 1 {
		t.Fatalf("want inbound inherits on Illuminate User, got %d (stats=%+v)", len(c), stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, appUser, "inherits"); len(c) != 0 {
		t.Fatalf("must not resolve inherits to self, got %d", len(c))
	}
}

func TestResolveSymrefs_PHPTraitPromotedMethod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// User uses Notifiable; notify lives on the trait. $this->notify → User.notify
	// must resolve via embeds (implements edge), not the ambiguous Other.notify.
	user := "sym:repo:app/Models/User.php:1:User"
	notifyTrait := "sym:repo:app/Traits/Notifiable.php:5:notify"
	otherNotify := "sym:repo:app/Other.php:1:notify"
	idMeth := "sym:repo:app/Models/User.php:10:id"
	syms := []types.Symbol{
		{ID: user, RepoID: repoID, Name: "User", Kind: types.SymbolKindClass, Path: "app/Models/User.php", LineStart: 1,
			Signature: "embeds=Notifiable"},
		{ID: "sym:repo:app/Traits/Notifiable.php:1:Notifiable", RepoID: repoID, Name: "Notifiable", Kind: types.SymbolKindClass,
			Path: "app/Traits/Notifiable.php", LineStart: 1},
		{ID: notifyTrait, RepoID: repoID, Name: "notify", Kind: types.SymbolKindMethod,
			Path: "app/Traits/Notifiable.php", LineStart: 5, ParentID: "Notifiable"},
		{ID: otherNotify, RepoID: repoID, Name: "notify", Kind: types.SymbolKindMethod,
			Path: "app/Other.php", LineStart: 1, ParentID: "Other"},
		{ID: idMeth, RepoID: repoID, Name: "id", Kind: types.SymbolKindMethod,
			Path: "app/Models/User.php", LineStart: 10, ParentID: "User"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "impl", RepoID: repoID, Kind: types.RefKindImplements, SourceID: user,
		TargetID: "symref:repo:app/Models/User.php:Notifiable", Confidence: 0.85,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "call", RepoID: repoID, Kind: types.RefKindCalls, SourceID: idMeth,
		TargetID: "symref:repo:app/Models/User.php:User.notify", Confidence: 0.8,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["embedded"] < 1 {
		t.Fatalf("expected embedded promotion for trait method, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, notifyTrait, "calls"); len(c) != 1 {
		t.Fatalf("trait notify should have 1 caller, got %d (stats=%+v)", len(c), stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, otherNotify, "calls"); len(c) != 0 {
		t.Fatalf("Other.notify must stay unresolved, got %d", len(c))
	}
}

func TestResolveSymrefs_RubyMixinPromotedMethod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	app := "sym:repo:app.rb:1:App"
	authRun := "sym:repo:auth.rb:5:authorize"
	otherAuth := "sym:repo:other.rb:1:authorize"
	runMeth := "sym:repo:app.rb:10:run"
	syms := []types.Symbol{
		{ID: app, RepoID: repoID, Name: "App", Kind: types.SymbolKindClass, Path: "app.rb", LineStart: 1,
			Signature: "embeds=AuthHelper"},
		{ID: "sym:repo:auth.rb:1:AuthHelper", RepoID: repoID, Name: "AuthHelper", Kind: types.SymbolKindClass,
			Path: "auth.rb", LineStart: 1},
		{ID: authRun, RepoID: repoID, Name: "authorize", Kind: types.SymbolKindMethod,
			Path: "auth.rb", LineStart: 5, ParentID: "AuthHelper"},
		{ID: otherAuth, RepoID: repoID, Name: "authorize", Kind: types.SymbolKindMethod,
			Path: "other.rb", LineStart: 1, ParentID: "Other"},
		{ID: runMeth, RepoID: repoID, Name: "run", Kind: types.SymbolKindMethod,
			Path: "app.rb", LineStart: 10, ParentID: "App"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "mix", RepoID: repoID, Kind: types.RefKindImplements, SourceID: app,
		TargetID: "symref:repo:app.rb:AuthHelper", Confidence: 0.85,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "call", RepoID: repoID, Kind: types.RefKindCalls, SourceID: runMeth,
		TargetID: "symref:repo:app.rb:App.authorize", Confidence: 0.8,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["embedded"] < 1 {
		t.Fatalf("expected embedded mixin promotion, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, authRun, "calls"); len(c) != 1 {
		t.Fatalf("AuthHelper.authorize should have 1 caller, got %d", len(c))
	}
}

func TestResolveSymrefs_PHPFactoryFixtureDemotion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	model := "sym:repo:app/Models/User.php:1:User"
	factory := "sym:repo:database/factories/UserFactory.php:1:User"
	caller := "sym:repo:app/Http/Controllers/UserController.php:5:show"
	syms := []types.Symbol{
		{ID: model, RepoID: repoID, Name: "User", Kind: types.SymbolKindClass, Path: "app/Models/User.php", LineStart: 1},
		{ID: factory, RepoID: repoID, Name: "User", Kind: types.SymbolKindClass,
			Path: "database/factories/UserFactory.php", LineStart: 1},
		{ID: caller, RepoID: repoID, Name: "show", Kind: types.SymbolKindMethod,
			Path: "app/Http/Controllers/UserController.php", LineStart: 5, ParentID: "UserController"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "c", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:app/Http/Controllers/UserController.php:User", Confidence: 0.7,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["non_fixture"] != 1 {
		t.Fatalf("expected non_fixture demotion of factory User, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, model, "calls"); len(c) != 1 {
		t.Fatalf("want app Model User, got callers=%d", len(c))
	}
}

func TestResolveSymrefs_PreferNonVendorCalls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	appLog := "sym:repo:app/Support/Logger.php:5:info"
	vendorLog := "sym:repo:vendor/monolog/src/Logger.php:5:info"
	caller := "sym:repo:app/Http/Controllers/Home.php:10:index"
	syms := []types.Symbol{
		{ID: appLog, RepoID: repoID, Name: "info", Kind: types.SymbolKindMethod,
			Path: "app/Support/Logger.php", LineStart: 5, ParentID: "Logger"},
		{ID: vendorLog, RepoID: repoID, Name: "info", Kind: types.SymbolKindMethod,
			Path: "vendor/monolog/src/Logger.php", LineStart: 5, ParentID: "Logger"},
		{ID: caller, RepoID: repoID, Name: "index", Kind: types.SymbolKindMethod,
			Path: "app/Http/Controllers/Home.php", LineStart: 10, ParentID: "Home"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "c", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:app/Http/Controllers/Home.php:info", Confidence: 0.55,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["non_vendor"] != 1 {
		t.Fatalf("expected non_vendor for bare call, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, appLog, "calls"); len(c) != 1 {
		t.Fatalf("want app Logger.info, got %d", len(c))
	}
}
