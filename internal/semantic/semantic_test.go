package semantic

import (
	"context"
	"testing"
)

// fakeEmbedder maps known words to fixed directions so we can simulate a real
// multilingual model deterministically: "prijava" (Slovenian) and "anmeldung"
// (German) sit near "login"/"auth"; "render" is orthogonal.
type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake" }
func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dir := func(t string) []float32 {
		switch t {
		case "login", "auth", "prijava", "anmeldung", "signin":
			return []float32{1, 1, 1, 1, -1, -1, -1, -1} // "auth" direction
		case "payment", "charge", "plačilo":
			return []float32{-1, -1, 1, 1, -1, -1, 1, 1} // "payment" direction
		default: // "render", unrelated — opposite of auth
			return []float32{-1, -1, -1, -1, 1, 1, 1, 1}
		}
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = dir(t)
	}
	return out, nil
}

func TestQuantizeHamming(t *testing.T) {
	// Full bytes (8 dims) so there are no zero-padding bits to skew the result.
	a := Quantize([]float32{1, -1, 1, -1, 1, -1, 1, -1})
	b := Quantize([]float32{1, -1, 1, -1, 1, -1, 1, -1})
	if HammingSim(a, b) != 1 {
		t.Fatalf("identical vectors must have sim 1, got %v", HammingSim(a, b))
	}
	c := Quantize([]float32{-1, 1, -1, 1, -1, 1, -1, 1}) // fully opposite
	if HammingSim(a, c) != 0 {
		t.Errorf("opposite vectors must have sim 0, got %v", HammingSim(a, c))
	}
}

// TestMultilingualRerank is the core proof: a Slovenian query ("prijava") that
// shares NO substring with the English symbol `login` is lexically near-zero, so
// lexical ranks an unrelated high-scoring `renderThing` first. The semantic rerank
// — which a multilingual model enables — lifts `login` to #1. This is exactly the
// gap hardcoded synonym tables cannot close.
func TestMultilingualRerank(t *testing.T) {
	ctx := context.Background()
	emb := fakeEmbedder{}
	qv, _ := emb.Embed(ctx, []string{"prijava"}) // Slovenian for "login"
	queryQ := Quantize(qv[0])

	names := []string{"login", "renderThing"}
	vecs, _ := emb.Embed(ctx, names)
	cands := []Candidate{
		{ID: "login", LexScore: 0.10, QuantVec: Quantize(vecs[0])},       // lexical near-miss
		{ID: "renderThing", LexScore: 0.95, QuantVec: Quantize(vecs[1])}, // lexical winner, wrong
	}

	// Pure lexical (semanticWeight 0) keeps the wrong symbol first.
	if order := HybridRerank(queryQ, cands, 0); cands[order[0]].ID != "renderThing" {
		t.Fatalf("pure lexical should rank renderThing first, got %s", cands[order[0]].ID)
	}
	// Hybrid lifts the semantically-correct `login` to #1 despite low lexical score.
	order := HybridRerank(queryQ, cands, 0.6)
	if cands[order[0]].ID != "login" {
		t.Fatalf("hybrid rerank should lift `login` for Slovenian 'prijava', got %s", cands[order[0]].ID)
	}
}
