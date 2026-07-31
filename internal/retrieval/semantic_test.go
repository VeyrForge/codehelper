package retrieval

import (
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestHumanizeIdent confirms snake_case / camelCase identifiers become real words
// so the embedding model sees language rather than one opaque token.
func TestHumanizeIdent(t *testing.T) {
	cases := map[string]string{
		"schedule_experts": "schedule experts",
		"computeExpert":    "compute expert",
		"WeightManifest":   "weight manifest",
		"HTTPServer":       "httpserver", // acronym run stays together (no lower->upper boundary)
		"model":            "model",
	}
	for in, want := range cases {
		if got := humanizeIdent(in); got != want {
			t.Errorf("humanizeIdent(%q)=%q want %q", in, got, want)
		}
	}
}

// TestCandidateTextEnriched confirms the embedded text carries split words and the
// captured signature/doc — not just the bare identifier — and falls back to the
// module stem for grounding when a symbol has no signature.
func TestCandidateTextEnriched(t *testing.T) {
	withSig := candidateText(types.Symbol{Name: "compute_expert", Signature: "Schedules experts -> Vec<u32>", Path: "sched/pool.rs"})
	if !contains(withSig, "compute expert") || !contains(withSig, "Schedules experts") {
		t.Errorf("enriched text missing words/signature: %q", withSig)
	}
	noSig := candidateText(types.Symbol{Name: "model", Path: "core/decode_stall.rs"})
	if !contains(noSig, "decode stall") {
		t.Errorf("sig-less symbol should be grounded by module stem: %q", noSig)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestBinaryQuant verifies the compressed-vector primitive on full-width (64-dim)
// vectors — the realistic case (real embeddings are 384/768-dim, multiples of 64).
// Identical signs -> sim 1, fully opposite -> sim 0.
func TestBinaryQuant(t *testing.T) {
	mk := func(sign float32) []float32 {
		v := make([]float32, 64)
		for i := range v {
			v[i] = sign * float32(i%7+1)
		}
		return v
	}
	pos := QuantizeBinary(mk(1))
	pos2 := QuantizeBinary(mk(1))
	neg := QuantizeBinary(mk(-1))
	if s := hammingSim(pos, pos2); s != 1 {
		t.Errorf("identical signs -> 1, got %v", s)
	}
	if s := hammingSim(pos, neg); s > 0.02 {
		t.Errorf("opposite signs -> ~0, got %v", s)
	}
	if SemanticEnabled() {
		t.Error("semantic must be OFF by default (pure-Go path preserved)")
	}
}
