package retrieval

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestCentralityBoost_DisambiguatesByCallers is a deterministic, self-contained
// proof of the mechanism: when several symbols share a name (so lexical scores
// tie), the call-graph centrality boost must lift the most-depended-on
// definition to rank #1, while the no-boost baseline does not. Ground truth is
// unambiguous by construction (we wire the edges), so this doubles as a
// regression guard for DefaultCentralityWeight.
func TestCentralityBoost_DisambiguatesByCallers(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "repo"

	// Three functions all named fetchData in different packages — a realistic
	// ambiguous query. "core" is load-bearing (10 callers), the others are not.
	// Paths are chosen so the central definition LOSES the lexical tie-break
	// (equal scores sort by path alphabetically, and "zcore" sorts last). No
	// path contains the query token or "test", so only centrality can move it.
	central := types.Symbol{ID: "sym:core", RepoID: repo, Name: "fetchData", Kind: types.SymbolKindFunction, Path: "internal/zcore/data.go", Language: "go"}
	legacy := types.Symbol{ID: "sym:legacy", RepoID: repo, Name: "fetchData", Kind: types.SymbolKindFunction, Path: "internal/alpha/old.go", Language: "go"}
	helper := types.Symbol{ID: "sym:helper", RepoID: repo, Name: "fetchData", Kind: types.SymbolKindFunction, Path: "internal/mid/help.go", Language: "go"}
	for _, s := range []types.Symbol{central, legacy, helper} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	// 10 distinct call sites into the central definition; 1 into legacy; 0 helper.
	mkCaller := func(i int, dst string) {
		id := fmt.Sprintf("sym:caller:%s:%d", dst, i)
		if err := st.UpsertSymbol(ctx, types.Symbol{ID: id, RepoID: repo, Name: fmt.Sprintf("caller%d", i), Kind: types.SymbolKindFunction, Path: "internal/app/use.go", Language: "go"}); err != nil {
			t.Fatalf("upsert caller: %v", err)
		}
		if err := st.AddEdge(ctx, types.Reference{ID: fmt.Sprintf("e:%s:%d", dst, i), RepoID: repo, Kind: types.RefKindCalls, SourceID: id, TargetID: dst, Confidence: 1}); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	for i := 0; i < 10; i++ {
		mkCaller(i, central.ID)
	}
	mkCaller(0, legacy.ID)

	rankOf := func(weight float64, id string) int {
		hits, err := QueryHybridWithOptions(ctx, st, repo, "fetchData", 10, QueryOptions{
			QueryTokens:      []string{"fetchdata"},
			CentralityWeight: weight,
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for i, h := range hits {
			if h.Symbol.ID == id {
				return i + 1
			}
		}
		return 0
	}

	offRank := rankOf(0, central.ID)
	onRank := rankOf(DefaultCentralityWeight, central.ID)
	t.Logf("central symbol rank: baseline(off)=%d centrality(on)=%d", offRank, onRank)

	if onRank != 1 {
		t.Errorf("with centrality, the 10-caller definition should rank #1, got %d", onRank)
	}
	if offRank == 1 {
		t.Errorf("baseline unexpectedly ranked the central symbol #1 — test no longer isolates the centrality effect (off=%d)", offRank)
	}
	if onRank > offRank {
		t.Errorf("centrality regressed the central symbol: off=%d on=%d", offRank, onRank)
	}
}
