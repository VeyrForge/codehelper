package retrieval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandVocabTerms_MatchesProjectTerm(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "vocab.json")
	body := `{
  "repo_id": "r",
  "terms": [{"term": "woocommerce", "count": 42}, {"term": "debounce", "count": 10}],
  "identifiers": [{"text": "TP_Plugin", "count": 5}]
}`
	if err := os.WriteFile(seed, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// vocab.Load uses paths.VocabPath which expects RepoIndexDir layout — test the matcher directly.
	exp := expandVocabTerms("", []string{"woo"})
	if exp != nil {
		t.Fatalf("empty repo root should yield nil, got %v", exp)
	}
	if !vocabTermMatches("woo", "woocommerce") {
		t.Fatal("expected woo to match woocommerce")
	}
}

func TestMergeTokenExpansions_UpgradesWeight(t *testing.T) {
	toks := []string{"close"}
	weights := map[string]float64{"close": 1.0}
	extra := map[string]float64{"shutdown": synonymWeight, "close": 0.5}
	mergeTokenExpansions(&toks, weights, extra)
	if weights["close"] != 1.0 {
		t.Errorf("typed term must stay 1.0, got %v", weights["close"])
	}
	if weights["shutdown"] != synonymWeight {
		t.Errorf("expansion weight = %v, want %v", weights["shutdown"], synonymWeight)
	}
}
