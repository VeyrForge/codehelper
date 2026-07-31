package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
)

// TestFastAPIHubDemotionLive ensures FastAPI OSS query/kickoff prefer app/tutorial
// symbols (get_db / docs_src) over framework Depends hubs crowding what_next.
func TestFastAPIHubDemotionLive(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := findRepoRoot(t)
	bed := filepath.Join(root, ".testbeds", "real-oss", "fastapi")
	db := filepath.Join(bed, ".codehelper", "graph.db")
	if _, err := os.Stat(db); err != nil {
		t.Skip(err)
	}
	st, err := graph.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	q := "Depends list_users get_db"
	hits, err := retrieval.QueryHybrid(context.Background(), st, "fastapi", q, 40)
	if err != nil {
		t.Fatal(err)
	}
	got, demoted := demoteNoiseForQuery(q, hits)
	t.Logf("demoted=%d top5=%v", demoted, summarizeTop(got, 5))
	if len(got) == 0 {
		t.Fatal("no hits")
	}
	top := got[0]
	topName := strings.ToLower(top.Symbol.Name)
	topPath := strings.ToLower(strings.ReplaceAll(top.Symbol.Path, "\\", "/"))
	if isFrameworkHubName(topName) {
		t.Fatalf("framework hub still tops after demote: %s @ %s — want get_db / docs_src app symbol", top.Symbol.Name, top.Symbol.Path)
	}
	if topName != "get_db" && !strings.Contains(topPath, "docs_src/") {
		t.Fatalf("top=%s @ %s want get_db or docs_src tutorial", top.Symbol.Name, top.Symbol.Path)
	}

	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	handlers := AllToolHandlers(reg)
	ctx := workspacectx.WithRoots(bed)
	queryBlob := pairedCall(ctx, handlers, "query", map[string]any{
		"query": q, "format": "json", "top_k": 8,
	})
	if strings.Contains(strings.ToLower(queryBlob), `"name": "depends"`) {
		// First hit must not be a framework hub — allow Depends later in the list.
		idxHub := strings.Index(strings.ToLower(queryBlob), `"name": "depends"`)
		idxApp := strings.Index(strings.ToLower(queryBlob), `"name": "get_db"`)
		if idxApp < 0 || idxHub < idxApp {
			t.Fatalf("query still ranks Depends hub before get_db:\n%s", truncateSmoke(queryBlob, 900))
		}
	}
	kick := pairedCall(ctx, handlers, "kickoff", map[string]any{
		"task": "Where is Depends used with list_users and get_db?", "format": "json", "sections": "orient,reuse",
	})
	wn := strings.ToLower(jsonFieldSnippet(kick, "what_next"))
	if strings.Contains(wn, "dependencies/utils.py") || strings.Contains(wn, "change_kit target=depends`") {
		t.Fatalf("kickoff what_next still points at framework hub: %s", wn)
	}
	if !strings.Contains(wn, "get_db") && !strings.Contains(wn, "list_users") && !strings.Contains(wn, "docs_src") {
		// Soft: reuse top name should appear in what_next via change_kit target=
		t.Logf("what_next=%s (ok if app symbol named differently)", wn)
		reuse := strings.ToLower(excerptJSONField(kick, "reuse_candidates", 500))
		if strings.Contains(reuse, `"name": "depends"`) && !strings.Contains(reuse, "get_db") {
			t.Fatalf("kickoff reuse still hub-first: %s", reuse)
		}
	}
}

func excerptJSONField(s, field string, n int) string {
	low := strings.ToLower(s)
	key := "\"" + strings.ToLower(field) + "\""
	i := strings.Index(low, key)
	if i < 0 {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	end := i + n
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}

func jsonFieldSnippet(s, field string) string {
	low := strings.ToLower(s)
	key := "\"" + strings.ToLower(field) + "\""
	i := strings.Index(low, key)
	if i < 0 {
		return "(missing)"
	}
	end := i + 280
	if end > len(s) {
		end = len(s)
	}
	return strings.ReplaceAll(s[i:end], "\n", " ")
}
