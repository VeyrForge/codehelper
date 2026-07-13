package retrieval

import (
	"fmt"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// nonMatchingOpts uses a query token that appears in no name or path, so only the
// diff boost is exercised (no exact_name/name_prefix/name_field/path_proximity).
func nonMatchingOpts(changed map[string]struct{}) QueryOptions {
	return QueryOptions{QueryTokens: []string{"zzqueryzz"}, ChangedSymbolIDs: changed}
}

// TestDiffBoostDecaysOnLargeChangeSet reproduces the regression a big uncommitted
// diff caused: with most candidates changed, a fixed +0.25 floated weakly-relevant
// edited symbols over a strongly-relevant unchanged one. The decayed boost must let
// the unchanged target win.
func TestDiffBoostDecaysOnLargeChangeSet(t *testing.T) {
	changed := map[string]struct{}{}
	in := []RankedSymbol{{Symbol: types.Symbol{ID: "target", Name: "Target", Path: "a/target.go"}, Score: 0.5}}
	for i := 0; i < 9; i++ { // 9 of 10 candidates changed (frac 0.9)
		id := fmt.Sprintf("noise%d", i)
		in = append(in, RankedSymbol{Symbol: types.Symbol{ID: id, Name: fmt.Sprintf("Noise%d", i), Path: "b/n.go"}, Score: 0.4})
		changed[id] = struct{}{}
	}
	out := rerankWithSignals(append([]RankedSymbol(nil), in...), nonMatchingOpts(changed))
	if out[0].Symbol.ID != "target" {
		t.Fatalf("relevant unchanged symbol buried by diff boost: #1=%s score=%.3f", out[0].Symbol.ID, out[0].Score)
	}
	// Sanity: the OLD fixed +0.25 would have buried it (0.4+0.25=0.65 > 0.5).
	if 0.4+diffBoostBase <= 0.5 {
		t.Fatal("test no longer exercises the burying condition")
	}
}

// TestDiffBoostFullWhenDiffSmall guards the value the boost provides: for a normal
// WIP (few candidates changed), the boost stays full, so a slightly weaker changed
// match still surfaces above a stronger unchanged one.
func TestDiffBoostFullWhenDiffSmall(t *testing.T) {
	in := []RankedSymbol{{Symbol: types.Symbol{ID: "strong-unchanged", Name: "A", Path: "a.go"}, Score: 0.5}}
	for i := 0; i < 8; i++ {
		in = append(in, RankedSymbol{Symbol: types.Symbol{ID: fmt.Sprintf("u%d", i), Name: "U", Path: "u.go"}, Score: 0.1})
	}
	in = append(in, RankedSymbol{Symbol: types.Symbol{ID: "changed", Name: "B", Path: "b.go"}, Score: 0.4})
	changed := map[string]struct{}{"changed": {}} // 1 of 10 = 10% < threshold -> full boost
	out := rerankWithSignals(append([]RankedSymbol(nil), in...), nonMatchingOpts(changed))
	if out[0].Symbol.ID != "changed" {
		t.Fatalf("full diff boost should surface the recently-changed symbol, got #1=%s", out[0].Symbol.ID)
	}
}
