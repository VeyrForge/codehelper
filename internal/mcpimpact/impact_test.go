package mcpimpact

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestAnalyze_IncludesMustUpdateCandidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "impact.db")
	st, err := graph.Open(db)
	if err != nil {
		t.Fatalf("open graph: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	repo := "repo"
	a := types.Symbol{ID: "sym:repo:a.go:1:A", RepoID: repo, Name: "A", Kind: types.SymbolKindFunction, Path: "a.go", LineStart: 1, LineEnd: 2, Language: "go"}
	b := types.Symbol{ID: "sym:repo:b.go:1:B", RepoID: repo, Name: "B", Kind: types.SymbolKindVariable, Path: "b.go", LineStart: 1, LineEnd: 1, Language: "go"}
	c := types.Symbol{ID: "sym:repo:c.go:1:C", RepoID: repo, Name: "C", Kind: types.SymbolKindMethod, Path: "c.go", LineStart: 1, LineEnd: 3, Language: "go"}
	for _, s := range []types.Symbol{a, b, c} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert symbol: %v", err)
		}
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e1", RepoID: repo, Kind: types.RefKindReads, SourceID: a.ID, TargetID: b.ID, Confidence: 0.5,
	}); err != nil {
		t.Fatalf("add reads edge: %v", err)
	}
	if err := st.AddEdge(ctx, types.Reference{
		ID: "e2", RepoID: repo, Kind: types.RefKindCalls, SourceID: b.ID, TargetID: c.ID, Confidence: 0.8,
	}); err != nil {
		t.Fatalf("add calls edge: %v", err)
	}

	res, err := Analyze(ctx, st, repo, "A", 2, "downstream")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(res.Nodes) < 2 {
		t.Fatalf("expected multiple nodes, got %d", len(res.Nodes))
	}
	if len(res.MustUpdateCandidates) == 0 {
		t.Fatalf("expected must_update_candidates, got none")
	}
}
