package graph

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestMergeGroupSnapshots_AggregatesAndEdges(t *testing.T) {
	t.Parallel()
	a := Snapshot{
		FormatVersion: SnapshotFormatVersion,
		RepoID:        "api",
		Symbols:       10,
		Edges:         20,
		Files:         3,
		Processes: []types.Process{
			{ID: "p1", RepoID: "api", Name: "flow:route→controller:index", EntrySymbol: "s1", StepSymbols: []string{"s1", "s2"}},
		},
		Clusters: []types.Cluster{
			{ID: "c1", RepoID: "api", Name: "layer:controller", Members: []string{"s2"}, Cohesion: 0.7},
		},
	}
	b := Snapshot{
		FormatVersion: SnapshotFormatVersion,
		RepoID:        "web",
		Symbols:       5,
		Edges:         8,
		Files:         2,
		ImportRoots:   []string{"@acme/web"},
	}
	edges := []CrossRepoEdgeSummary{
		{ImportPath: "github.com/acme/api", FromRepo: "web", OwnerName: "api", ViaRoot: "github.com/acme/api", SameGroup: true},
	}
	merged := MergeGroupSnapshots("platform", "Platform", []Snapshot{a, b}, edges)
	if merged.GroupID != "platform" || merged.Symbols != 15 || merged.Edges != 28 || merged.Files != 5 {
		t.Fatalf("merged totals wrong: %#v", merged)
	}
	if len(merged.Members) != 2 || len(merged.Processes) != 1 || len(merged.CrossRepoEdges) != 1 {
		t.Fatalf("merged shape: %#v", merged)
	}
	if !merged.CrossRepoEdges[0].SameGroup {
		t.Fatal("expected same-group edge")
	}

	out := filepath.Join(t.TempDir(), "group", "merged.json")
	if err := WriteMergedSnapshotJSON(out, merged); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var round MergedGroupSnapshot
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	if round.GroupID != "platform" || len(round.Members) != 2 {
		t.Fatalf("roundtrip %#v", round)
	}
}

func TestDistinctImportModulePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "imps.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "web"
	if err := st.UpsertSymbol(ctx, types.Symbol{
		ID: "s:1", RepoID: repo, Name: "main", Kind: types.SymbolKindFunction, Path: "main.go", Language: "go",
	}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []types.Reference{
		{ID: "e1", RepoID: repo, Kind: types.RefKindImports, SourceID: "file:web:main.go", TargetID: "mod:web:github.com/acme/api/handlers", Confidence: 1},
		{ID: "e2", RepoID: repo, Kind: types.RefKindImports, SourceID: "file:web:main.go", TargetID: "mod:web:./local", Confidence: 1},
		{ID: "e3", RepoID: repo, Kind: types.RefKindImports, SourceID: "file:web:main.go", TargetID: "mod:web:github.com/acme/api/handlers", Confidence: 1},
	} {
		if err := st.AddEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.DistinctImportModulePaths(ctx, repo, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "github.com/acme/api/handlers" {
		t.Fatalf("got %#v", got)
	}
}

func TestBuildMergedGroupSnapshot_Resolve(t *testing.T) {
	t.Parallel()
	members := []Snapshot{
		{RepoID: "api", Symbols: 1},
		{RepoID: "web", Symbols: 2},
	}
	imps := map[string][]string{
		"web": {"github.com/acme/api"},
	}
	merged := BuildMergedGroupSnapshot(context.Background(), "g1", "G", members, imps,
		func(fromRepo string, importPaths []string) []CrossRepoEdgeSummary {
			var out []CrossRepoEdgeSummary
			for _, imp := range importPaths {
				out = append(out, CrossRepoEdgeSummary{
					ImportPath: imp, FromRepo: fromRepo, OwnerName: "api", SameGroup: true,
				})
			}
			return out
		})
	if len(merged.CrossRepoEdges) != 1 || merged.CrossRepoEdges[0].OwnerName != "api" {
		t.Fatalf("%#v", merged.CrossRepoEdges)
	}
}
