package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestResolveSymrefs_AliasImportDisambiguates covers the JS/TS path-alias case:
// two `db` helpers exist, the caller imports `@/lib/db`, and the indexer has
// expanded that alias to the repo-relative `src/lib/db`. Resolution must pick the
// imported one instead of giving up as ambiguous.
func TestResolveSymrefs_AliasImportDisambiguates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	syms := []types.Symbol{
		{ID: "sym:repo:src/pages/users.ts:1:listUsers", RepoID: repoID, Name: "listUsers",
			Kind: types.SymbolKindFunction, Path: "src/pages/users.ts", LineStart: 1},
		{ID: "sym:repo:src/lib/db.ts:1:query", RepoID: repoID, Name: "query",
			Kind: types.SymbolKindFunction, Path: "src/lib/db.ts", LineStart: 1},
		{ID: "sym:repo:legacy/db.ts:1:query", RepoID: repoID, Name: "query",
			Kind: types.SymbolKindFunction, Path: "legacy/db.ts", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Both the raw alias edge and the expanded repo-relative edge, exactly as
	// indexer.ExpandAliasImportEdges emits them.
	for i, mod := range []string{"@/lib/db", "src/lib/db"} {
		if err := st.AddEdge(ctx, types.Reference{
			ID: "e:imp" + string(rune('a'+i)), RepoID: repoID, Kind: types.RefKindImports,
			SourceID: "file:repo:src/pages/users.ts",
			TargetID: "mod:repo:" + mod, Confidence: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	caller := "sym:repo:src/pages/users.ts:1:listUsers"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:call", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:src/pages/users.ts:query", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["import"] != 1 {
		t.Fatalf("expected alias import resolution, got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:src/lib/db.ts:1:query", "calls"); len(c) != 1 {
		t.Errorf("want inbound to src/lib/db query, callers=%d", len(c))
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:legacy/db.ts:1:query", "calls"); len(c) != 0 {
		t.Errorf("must not resolve to legacy/db, callers=%d", len(c))
	}
}

// TestResolveSymrefs_PHPIncludeDisambiguates covers the WordPress case: a plugin
// wires files with require_once, and that include edge must disambiguate a class
// name that also exists in another plugin.
func TestResolveSymrefs_PHPIncludeDisambiguates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	syms := []types.Symbol{
		{ID: "sym:repo:wp-content/plugins/probe/probe.php:1:probe_boot", RepoID: repoID,
			Name: "probe_boot", Kind: types.SymbolKindFunction,
			Path: "wp-content/plugins/probe/probe.php", LineStart: 1},
		{ID: "sym:repo:wp-content/plugins/probe/includes/class-loader.php:1:Loader", RepoID: repoID,
			Name: "Loader", Kind: types.SymbolKindClass,
			Path: "wp-content/plugins/probe/includes/class-loader.php", LineStart: 1},
		{ID: "sym:repo:wp-content/plugins/other/includes/class-loader.php:1:Loader", RepoID: repoID,
			Name: "Loader", Kind: types.SymbolKindClass,
			Path: "wp-content/plugins/other/includes/class-loader.php", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:inc", RepoID: repoID, Kind: types.RefKindImports,
		SourceID: "file:repo:wp-content/plugins/probe/probe.php",
		TargetID: "mod:repo:wp-content/plugins/probe/includes/class-loader.php", Confidence: 0.85,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:call", RepoID: repoID, Kind: types.RefKindCalls,
		SourceID: "sym:repo:wp-content/plugins/probe/probe.php:1:probe_boot",
		TargetID: "symref:repo:wp-content/plugins/probe/probe.php:Loader", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveSymrefs(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	want := "sym:repo:wp-content/plugins/probe/includes/class-loader.php:1:Loader"
	if c, _ := st.EdgesTo(ctx, repoID, want, "calls"); len(c) != 1 {
		t.Errorf("include edge should bind the same plugin's Loader, callers=%d", len(c))
	}
	bad := "sym:repo:wp-content/plugins/other/includes/class-loader.php:1:Loader"
	if c, _ := st.EdgesTo(ctx, repoID, bad, "calls"); len(c) != 0 {
		t.Errorf("must not bind another plugin's Loader, callers=%d", len(c))
	}
}

func TestLooksRepoRootedImport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		imp  string
		want bool
	}{
		{"src/lib/db", true},
		{"wp-content/plugins/probe/includes/class-loader.php", true},
		{"internal/graph", true},
		{"@/lib/db", false}, // unexpanded alias
		{"#cache/redis", false},
		{"~/lib/db", false},
		{`Illuminate\Support\Str`, false}, // PHP namespace
		{"react", false},                  // bare package
		{"/abs/path", false},
		{"https://example.com/x", false},
		{"example.com/proj/pkgb", false}, // Go module path
		{"github.com/foo/bar", false},
	} {
		if got := looksRepoRootedImport(tc.imp); got != tc.want {
			t.Errorf("looksRepoRootedImport(%q) = %v want %v", tc.imp, got, tc.want)
		}
	}
}

func TestImportPathMatchesCandidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		resolved, cand string
		want           bool
	}{
		{"src/lib/db", "src/lib/db.ts", true},
		{"src/lib/db.ts", "src/lib/db.ts", true},
		{"src/lib", "src/lib/index.ts", true},
		{"src/lib/db", "db/index.ts", false},
		{"src/lib/db", "legacy/db.ts", false},
		{"", "src/lib/db.ts", false},
	} {
		if got := importPathMatchesCandidate(tc.resolved, tc.cand); got != tc.want {
			t.Errorf("importPathMatchesCandidate(%q, %q) = %v want %v", tc.resolved, tc.cand, got, tc.want)
		}
	}
}

// TestResolveSymrefs_RepoRootedBeatsPackageSuffix ensures a repo-relative import
// like `src/lib/db` cannot also latch onto an unrelated top-level `db/` package
// via trailing-suffix matching (which would leave the call ambiguous).
func TestResolveSymrefs_RepoRootedBeatsPackageSuffix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	syms := []types.Symbol{
		{ID: "sym:repo:src/pages/users.ts:1:listUsers", RepoID: repoID, Name: "listUsers",
			Kind: types.SymbolKindFunction, Path: "src/pages/users.ts", LineStart: 1},
		{ID: "sym:repo:src/lib/db.ts:1:query", RepoID: repoID, Name: "query",
			Kind: types.SymbolKindFunction, Path: "src/lib/db.ts", LineStart: 1},
		{ID: "sym:repo:db/index.ts:1:query", RepoID: repoID, Name: "query",
			Kind: types.SymbolKindFunction, Path: "db/index.ts", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:imp", RepoID: repoID, Kind: types.RefKindImports,
		SourceID: "file:repo:src/pages/users.ts",
		TargetID: "mod:repo:src/lib/db", Confidence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	caller := "sym:repo:src/pages/users.ts:1:listUsers"
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:call", RepoID: repoID, Kind: types.RefKindCalls, SourceID: caller,
		TargetID: "symref:repo:src/pages/users.ts:query", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["import"] != 1 {
		t.Fatalf("expected unique import resolution (not ambiguous), got %+v", stats)
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:src/lib/db.ts:1:query", "calls"); len(c) != 1 {
		t.Errorf("want cross-file edge to src/lib/db query, callers=%d", len(c))
	}
	if c, _ := st.EdgesTo(ctx, repoID, "sym:repo:db/index.ts:1:query", "calls"); len(c) != 0 {
		t.Errorf("must not bind top-level db/ via package suffix, callers=%d", len(c))
	}
}

// TestResolveSymrefs_RepoRootedDirectoryImport covers alias/dir imports that
// resolve to an index module (src/components/ui → src/components/ui/index.ts).
func TestResolveSymrefs_RepoRootedDirectoryImport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoID := "repo"
	st, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	syms := []types.Symbol{
		{ID: "sym:repo:src/app/page.ts:1:Page", RepoID: repoID, Name: "Page",
			Kind: types.SymbolKindFunction, Path: "src/app/page.ts", LineStart: 1},
		{ID: "sym:repo:src/components/ui/index.ts:1:Button", RepoID: repoID, Name: "Button",
			Kind: types.SymbolKindFunction, Path: "src/components/ui/index.ts", LineStart: 1},
		{ID: "sym:repo:vendor/ui/index.ts:1:Button", RepoID: repoID, Name: "Button",
			Kind: types.SymbolKindFunction, Path: "vendor/ui/index.ts", LineStart: 1},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:imp", RepoID: repoID, Kind: types.RefKindImports,
		SourceID: "file:repo:src/app/page.ts",
		TargetID: "mod:repo:src/components/ui", Confidence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e:call", RepoID: repoID, Kind: types.RefKindCalls,
		SourceID: "sym:repo:src/app/page.ts:1:Page",
		TargetID: "symref:repo:src/app/page.ts:Button", Confidence: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ResolveSymrefs(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByStrategy["import"] != 1 {
		t.Fatalf("expected directory-import resolution, got %+v", stats)
	}
	want := "sym:repo:src/components/ui/index.ts:1:Button"
	if c, _ := st.EdgesTo(ctx, repoID, want, "calls"); len(c) != 1 {
		t.Errorf("want cross-file edge to ui/index Button, callers=%d", len(c))
	}
	bad := "sym:repo:vendor/ui/index.ts:1:Button"
	if c, _ := st.EdgesTo(ctx, repoID, bad, "calls"); len(c) != 0 {
		t.Errorf("must not bind vendor/ui, callers=%d", len(c))
	}
}
