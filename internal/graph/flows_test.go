package graph

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestClassifyFlowLayer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sym  types.Symbol
		want FlowLayer
	}{
		{types.Symbol{Name: "route_get_3", Signature: "frameworks=laravel;role=entrypoint", Path: "routes/web.php"}, FlowLayerRoute},
		{types.Symbol{Name: "Index", Path: "app/Http/Controllers/UserController.php"}, FlowLayerController},
		{types.Symbol{Name: "UserService", Path: "app/Services/UserService.php"}, FlowLayerService},
		{types.Symbol{Name: "FindByID", Path: "internal/repository/user_repo.go"}, FlowLayerQuery},
		{types.Symbol{Name: "helper", Path: "pkg/util/x.go"}, FlowLayerOther},
	}
	for _, tc := range cases {
		if got := ClassifyFlowLayer(tc.sym); got != tc.want {
			t.Fatalf("%s/%s: got %s want %s", tc.sym.Path, tc.sym.Name, got, tc.want)
		}
	}
}

func TestBuildRequestFlows_RouteControllerServiceQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "flows.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "demo"

	syms := []types.Symbol{
		{ID: "s:route", RepoID: repo, Name: "route_get_1", Kind: types.SymbolKindFunction, Path: "routes/api.php", LineStart: 1, Language: "php", Signature: "frameworks=laravel;role=entrypoint"},
		{ID: "s:ctrl", RepoID: repo, Name: "index", Kind: types.SymbolKindMethod, Path: "app/Http/Controllers/UserController.php", LineStart: 10, Language: "php"},
		{ID: "s:svc", RepoID: repo, Name: "ListUsers", Kind: types.SymbolKindMethod, Path: "app/Services/UserService.php", LineStart: 5, Language: "php"},
		{ID: "s:repo", RepoID: repo, Name: "All", Kind: types.SymbolKindMethod, Path: "app/Repositories/UserRepository.php", LineStart: 8, Language: "php"},
		{ID: "s:noise", RepoID: repo, Name: "log", Kind: types.SymbolKindFunction, Path: "app/Support/Log.php", LineStart: 1, Language: "php"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	edges := []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "s:route", TargetID: "s:ctrl", Confidence: 1},
		{ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: "s:ctrl", TargetID: "s:svc", Confidence: 1},
		{ID: "e3", RepoID: repo, Kind: types.RefKindCalls, SourceID: "s:svc", TargetID: "s:repo", Confidence: 1},
		{ID: "e4", RepoID: repo, Kind: types.RefKindCalls, SourceID: "s:ctrl", TargetID: "s:noise", Confidence: 1},
	}
	for _, e := range edges {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	nProcs, nClus, err := st.PersistRequestFlows(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if nProcs < 1 {
		t.Fatalf("expected processes, got %d", nProcs)
	}
	if nClus < 1 {
		t.Fatalf("expected clusters, got %d", nClus)
	}

	procs, err := st.ListProcesses(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range procs {
		if p.EntrySymbol != "s:route" {
			continue
		}
		found = true
		if !strings.Contains(p.Name, "route") || !strings.Contains(p.Name, "controller") {
			t.Fatalf("name should encode layers: %q", p.Name)
		}
		joined := strings.Join(p.StepSymbols, ",")
		if !strings.Contains(joined, "s:ctrl") || !strings.Contains(joined, "s:svc") || !strings.Contains(joined, "s:repo") {
			t.Fatalf("spine missing layers: %#v", p.StepSymbols)
		}
	}
	if !found {
		t.Fatalf("no process for route entry: %#v", procs)
	}

	clusters, err := st.ListClusters(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]types.Cluster{}
	for _, c := range clusters {
		byName[c.Name] = c
	}
	if _, ok := byName["layer:controller"]; !ok {
		t.Fatalf("missing layer:controller cluster: %#v", clusters)
	}
	if _, ok := byName["layer:query"]; !ok {
		t.Fatalf("missing layer:query cluster: %#v", clusters)
	}
}

func TestBuildRequestFlows_NoFalsePositivesOnHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "nofalse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "lib"
	for _, s := range []types.Symbol{
		{ID: "s:a", RepoID: repo, Name: "Helper", Kind: types.SymbolKindFunction, Path: "pkg/util/a.go", Language: "go"},
		{ID: "s:b", RepoID: repo, Name: "Other", Kind: types.SymbolKindFunction, Path: "pkg/util/b.go", Language: "go"},
	} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e1", RepoID: repo, Kind: types.RefKindCalls, SourceID: "s:a", TargetID: "s:b", Confidence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	procs, err := st.BuildRequestFlows(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 0 {
		t.Fatalf("expected no request flows for helpers, got %#v", procs)
	}
}
