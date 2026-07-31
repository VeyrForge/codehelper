package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
)

func TestGinContextJSONQualifiedRank(t *testing.T) {
	root := findRepoRoot(t)
	// Prefer .testbeds/active (and .eval-projects) over legacy real-oss stubs —
	// a tiny leftover graph.db under real-oss/gin yields 0 hits.
	var db string
	for _, cand := range []string{
		filepath.Join(root, ".testbeds", "active", "gin", ".codehelper", "graph.db"),
		filepath.Join(root, ".eval-projects", "gin", ".codehelper", "graph.db"),
		filepath.Join(root, ".testbeds", "real-oss", "gin", ".codehelper", "graph.db"),
	} {
		if fi, err := os.Stat(cand); err == nil && fi.Size() > 256*1024 {
			db = cand
			break
		}
	}
	if db == "" {
		t.Skip("no usable gin graph.db under .testbeds/active, .eval-projects, or real-oss")
	}
	st, err := graph.Open(db)
	if err != nil {
		t.Skip(err)
	}
	defer st.Close()
	hits, err := retrieval.QueryHybrid(context.Background(), st, "gin", "Context.JSON", 12)
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range hits {
		t.Logf("%d %s parent=%s path=%s score=%.3f reasons=%v", i, h.Symbol.Name, h.Symbol.ParentID, h.Symbol.Path, h.Score, h.Reasons)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	top := hits[0]
	if !strings.EqualFold(top.Symbol.Name, "JSON") || !strings.EqualFold(top.Symbol.ParentID, "Context") {
		t.Fatalf("top=%s parent=%s path=%s want Context.JSON", top.Symbol.Name, top.Symbol.ParentID, top.Symbol.Path)
	}
}
