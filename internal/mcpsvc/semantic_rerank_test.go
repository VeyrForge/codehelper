package mcpsvc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/retrieval"
)

type fakeEmb struct{}

func (fakeEmb) EmbedBatch(texts []string) ([][]float32, error) { return nil, nil }

// TestSemanticRerankStatus locks the agent-facing "was the local LLM handled?"
// signal: empty when off (the default), "active" when it reranked, "inactive" when
// configured but it didn't apply.
func TestSemanticRerankStatus(t *testing.T) {
	retrieval.SetEmbedder(nil)
	if s := semanticRerankStatus(nil); s != "" {
		t.Errorf("off (default) should report nothing, got %q", s)
	}

	retrieval.SetEmbedder(fakeEmb{})
	defer retrieval.SetEmbedder(nil)

	active := []retrieval.RankedSymbol{{Reasons: []string{"bm25", "vector"}}}
	if s := semanticRerankStatus(active); !strings.Contains(s, "active") || !strings.Contains(s, "CONCEPTUAL") {
		t.Errorf("reranked query should report active + verify reminder, got %q", s)
	}

	inactive := []retrieval.RankedSymbol{{Reasons: []string{"bm25"}}}
	if s := semanticRerankStatus(inactive); !strings.Contains(s, "inactive") {
		t.Errorf("configured-but-unapplied should report inactive, got %q", s)
	}
}
