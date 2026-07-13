package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
)

// TestQrelsCoreRegressionGate runs the 8-query core set against the local index.
// Skipped when no index; fails if R@1 drops below the documented baseline.
func TestQrelsCoreRegressionGate(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := meta.Read(root); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Skip("no indexed repo root found — run codehelper analyze")
		}
		root = parent
	}
	qf, err := LoadQrels(filepath.Join("testdata", "qrels", "codehelper-core.json"))
	if err != nil {
		t.Fatalf("load core qrels: %v", err)
	}
	m, err := meta.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := graph.Open(paths.DBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	detail, err := QrelsEvalDetailed(context.Background(), st, m.RepoName, root, qf, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("core qrels: R@1=%.3f MRR=%.3f nDCG@10=%.3f", detail.Recall1, detail.MRR, detail.NDCG10)
	for _, pq := range detail.PerQuery {
		if !pq.Hit {
			t.Logf("  MISS %s top=%s", pq.ID, pq.TopName)
		} else if pq.FirstRank > 1 {
			t.Logf("  rank=%d %s top=%s", pq.FirstRank, pq.ID, pq.TopName)
		}
	}
	const minRecall1 = 0.70
	if detail.Recall1 < minRecall1 {
		t.Errorf("core R@1=%.3f below gate %.2f — ranking regression", detail.Recall1, minRecall1)
	}
}
