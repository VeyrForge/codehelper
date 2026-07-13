package mcpsvc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func hit(name string, score float64) retrieval.RankedSymbol {
	return retrieval.RankedSymbol{Symbol: types.Symbol{Name: name}, Score: score}
}

// TestAmbiguityGuard: warn only when the top two are near-tied; stay silent (no
// token cost) for a clear winner, a single hit, or duplicate-named hits.
func TestAmbiguityGuard(t *testing.T) {
	// near-tie (within 8%) -> warn, naming both
	if g := ambiguityGuard([]retrieval.RankedSymbol{hit("LayerCache", 1.00), hit("PrefixCache", 0.96)}); !strings.Contains(g, "LayerCache") || !strings.Contains(g, "PrefixCache") {
		t.Errorf("near-tie should warn naming both, got %q", g)
	}
	// clear winner -> silent
	if g := ambiguityGuard([]retrieval.RankedSymbol{hit("forward_layer", 1.00), hit("dense_reference", 0.40)}); g != "" {
		t.Errorf("clear winner should be silent, got %q", g)
	}
	// single hit -> silent
	if g := ambiguityGuard([]retrieval.RankedSymbol{hit("only", 1.0)}); g != "" {
		t.Errorf("single hit should be silent, got %q", g)
	}
	// duplicate names (same symbol via two paths) -> silent, not "ambiguous"
	if g := ambiguityGuard([]retrieval.RankedSymbol{hit("model", 1.0), hit("model", 0.99)}); g != "" {
		t.Errorf("duplicate-named hits should be silent, got %q", g)
	}
}

// TestNextToolsForQuery: the next-step hint adapts to the result state so the agent
// takes the cheapest correct move instead of guessing.
func TestNextToolsForQuery(t *testing.T) {
	// nothing found -> escalate structurally
	if got := nextToolsForQuery(nil); got[0] != "ast_query" {
		t.Errorf("0 hits should suggest ast_query first, got %v", got)
	}
	// clear winner -> go deep with one context call
	clear := []retrieval.RankedSymbol{hit("forward_layer", 1.0), hit("dense_reference", 0.3)}
	if got := nextToolsForQuery(clear); len(got) != 2 || got[0] != "context" || got[1] != "similar" {
		t.Errorf("clear winner should suggest context+similar, got %v", got)
	}
	// near-tie -> disambiguate + reuse
	tie := []retrieval.RankedSymbol{hit("LayerCache", 1.0), hit("PrefixCache", 0.97)}
	if got := nextToolsForQuery(tie); len(got) != 3 || got[1] != "scout" || got[2] != "similar" {
		t.Errorf("near-tie should suggest context+scout+similar, got %v", got)
	}
}
