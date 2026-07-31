package retrieval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDiscoverLikelyEntrypoints_RootPHP(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "translate-plugin.php")
	if err := os.WriteFile(main, []byte("<?php\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := discoverLikelyEntrypoints(dir)
	if len(got) != 1 || got[0] != "translate-plugin.php" {
		t.Fatalf("got %v want [translate-plugin.php]", got)
	}
}

func TestApplyQueryIntentBoosts_LocatePrefersEntrypoint(t *testing.T) {
	in := []RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "ensure_registered", Path: "includes/content/class-nav-menu-handler.php", Language: "php"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "b", Name: "tp_run_plugin", Path: "translate-plugin.php", Language: "php", Signature: "role=entrypoint plugins_loaded"}, Score: 0.95},
	}
	out := applyQueryIntentBoosts(in, QueryOptions{
		QueryTokens:           tokenize("where hooks registered plugins_loaded bootstrap"),
		PrimaryLanguage:       "php",
		LikelyEntrypointFiles: []string{"translate-plugin.php"},
	})
	if out[0].Symbol.Name != "tp_run_plugin" {
		t.Fatalf("expected tp_run_plugin first, got %s", out[0].Symbol.Name)
	}
}

func TestApplyQueryIntentBoosts_VocabPrefersExpand(t *testing.T) {
	in := []RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "glossaryKeys", Path: "internal/mcpsvc/glossary_tools.go"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "b", Name: "expandVocabTerms", Path: "internal/retrieval/vocab_expand.go"}, Score: 0.98},
	}
	out := applyQueryIntentBoosts(in, QueryOptions{
		QueryTokens: tokenize("project vocabulary seed glossary terms"),
	})
	if out[0].Symbol.Name != "expandVocabTerms" {
		t.Fatalf("expected expandVocabTerms first, got %s", out[0].Symbol.Name)
	}
}
