package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestRerankWithSignals_PrimaryLanguageBoost(t *testing.T) {
	in := []RankedSymbol{
		{Symbol: types.Symbol{Name: "cssBtn", Language: "css", Path: "media/panel.css"}, Score: 0.5},
		{Symbol: types.Symbol{Name: "fetchData", Language: "go", Path: "internal/data.go"}, Score: 0.48},
	}
	out := rerankWithSignals(in, QueryOptions{
		QueryTokens:     []string{"fetch"},
		PrimaryLanguage: "go",
	})
	if out[0].Symbol.Language != "go" {
		t.Errorf("primary-language boost should rank go symbol first, got %q", out[0].Symbol.Name)
	}
	for _, r := range out[0].Reasons {
		if r == "primary_lang" {
			return
		}
	}
	t.Errorf("expected primary_lang reason on top hit, got %v", out[0].Reasons)
}

func TestSearchFileSnippets_FindsConfigLine(t *testing.T) {
	ctx := context.Background()
	repoRoot := t.TempDir()
	dbDir := t.TempDir()
	st, err := graph.Open(filepath.Join(dbDir, "snip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "repo"

	cfgDir := filepath.Join(repoRoot, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "app.yaml")
	if err := os.WriteFile(cfgPath, []byte("rate_limit: 100\nother: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFile(ctx, types.FileMeta{ID: "f1", RepoID: repo, Path: "config/app.yaml", Language: "yaml"}); err != nil {
		t.Fatal(err)
	}

	snips, err := SearchFileSnippets(ctx, st, repoRoot, repo, []string{"rate", "limit"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(snips) == 0 {
		t.Fatal("expected at least one snippet")
	}
	if snips[0].Path != "config/app.yaml" {
		t.Errorf("path = %q", snips[0].Path)
	}
}

func TestFindSimilarSymbols_ExcludesSelf(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "sim.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "repo"

	target := types.Symbol{
		ID: "sym:a", RepoID: repo, Name: "SaveCache", Kind: types.SymbolKindFunction,
		Path: "internal/cache/save.go", Signature: "SaveCache persists entries to disk.", Language: "go",
	}
	peer := types.Symbol{
		ID: "sym:b", RepoID: repo, Name: "LoadCache", Kind: types.SymbolKindFunction,
		Path: "internal/cache/load.go", Signature: "LoadCache reads persisted entries.", Language: "go",
	}
	for _, s := range []types.Symbol{target, peer} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := FindSimilarSymbols(ctx, st, repo, "", "SaveCache", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Symbol.ID == target.ID {
			t.Error("target must not appear in similar results")
		}
	}
}
