package retrieval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/enrich"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestRerankWithSignals_EnrichmentFieldBoost(t *testing.T) {
	in := []RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "Close", Path: "store.go"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "b", Name: "Release", Path: "pool.go"}, Score: 1.0},
	}
	out := rerankWithSignals(in, QueryOptions{
		QueryTokens: []string{"shutdown"},
		EnrichmentTexts: map[string]string{
			"a": "closes the store and releases resources shutdown",
		},
	})
	var scoreA, scoreB float64
	for _, x := range out {
		switch x.Symbol.ID {
		case "a":
			scoreA = x.Score
		case "b":
			scoreB = x.Score
		}
	}
	if scoreA <= 1.0 {
		t.Fatalf("expected enrichment boost for Close, got %f", scoreA)
	}
	if scoreB != 1.0 {
		t.Fatalf("Release should be unchanged without enrichment text, got %f", scoreB)
	}
}

func TestResolveEnrichmentTexts_LoadsStore(t *testing.T) {
	dir := t.TempDir()
	path := enrich.DefaultPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := map[string]enrich.Enrichment{
		"sym:1": {SymbolID: "sym:1", Purpose: "shuts down resources", Aliases: []string{"shutdown"}},
	}
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	texts := resolveEnrichmentTexts(dir)
	if texts == nil || texts["sym:1"] == "" {
		t.Fatalf("expected enrichment text loaded, got %v", texts)
	}
}

func TestRerankWithSignals_NoEnrichmentWhenNil(t *testing.T) {
	in := []RankedSymbol{{Symbol: types.Symbol{ID: "a", Name: "Close"}, Score: 1.0}}
	out := rerankWithSignals(in, QueryOptions{QueryTokens: []string{"shutdown"}})
	if out[0].Score != 1.0 {
		t.Fatalf("expected unchanged score without enrichment, got %f", out[0].Score)
	}
}
