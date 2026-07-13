package retrieval

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestExpandSynonyms verifies the query-enrichment mechanism: typed terms keep
// weight 1.0, cluster members are added at the synonym discount, and a term that
// is both typed and another's synonym keeps the literal weight.
func TestExpandSynonyms(t *testing.T) {
	toks, w := expandSynonyms([]string{"close", "store"})
	has := map[string]bool{}
	for _, x := range toks {
		has[x] = true
	}
	if !has["close"] || w["close"] != 1.0 {
		t.Errorf("typed term 'close' must keep weight 1.0, got %v", w["close"])
	}
	if !has["shutdown"] || w["shutdown"] != synonymWeight {
		t.Errorf("expected expansion 'shutdown' at synonymWeight, got %v", w["shutdown"])
	}
	if !has["release"] {
		t.Error("expected 'close' to expand to 'release'")
	}
	// A typed term that is also a synonym of another typed term keeps weight 1.0.
	toks2, w2 := expandSynonyms([]string{"save", "store"})
	_ = toks2
	if w2["store"] != 1.0 || w2["save"] != 1.0 {
		t.Errorf("typed terms must stay 1.0 even when cluster-related: save=%v store=%v", w2["save"], w2["store"])
	}
}

// TestSynonymExpansion_FindsCrossVerbSymbol proves end-to-end that a query
// phrased with a different verb than the symbol still finds it, and that a
// nonsense query still returns nothing (no false recall).
func TestSynonymExpansion_FindsCrossVerbSymbol(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "syn.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "repo"
	// Symbol uses "Save"; query will use the synonym "persist".
	if err := st.UpsertSymbol(ctx, types.Symbol{ID: "sym:save", RepoID: repo, Name: "Save", Kind: types.SymbolKindMethod, Path: "internal/taskstore/store.go", Language: "go"}); err != nil {
		t.Fatal(err)
	}
	hits, err := QueryHybrid(ctx, st, repo, "persist the task", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hits {
		if h.Symbol.ID == "sym:save" {
			found = true
		}
	}
	if !found {
		t.Error("synonym expansion failed: 'persist' should find Save")
	}
	// Control: an unrelated query must not surface Save.
	ctrl, _ := QueryHybrid(ctx, st, repo, "xyzzy nonexistent", 10)
	for _, h := range ctrl {
		if h.Symbol.ID == "sym:save" {
			t.Error("false recall: unrelated query surfaced Save")
		}
	}
}

// TestQuery_AdversarialInputsDoNotPanic feeds empty, oversized, unicode, binary,
// and SQL-injection-shaped queries through the full retrieval path. None may
// panic, error, or hang — the store uses parameterized queries, and the IDF /
// normalization math must stay defined on degenerate token sets.
func TestQuery_AdversarialInputsDoNotPanic(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "repo"
	for i := 0; i < 20; i++ {
		_ = st.UpsertSymbol(ctx, types.Symbol{ID: fmt.Sprintf("s%d", i), RepoID: repo, Name: fmt.Sprintf("Close%d", i), Kind: types.SymbolKindFunction, Path: "internal/graph/store.go", Language: "go"})
	}
	inputs := []string{
		"", "   ",
		strings.Repeat("a", 5000),
		strings.Repeat("the file data close open ", 200),
		"日本語のクエリ テスト",
		"'; DROP TABLE symbols; --",
		"%_%_%_%", "....////....////",
		"\x00\x01\x02 binary",
	}
	for _, q := range inputs {
		start := time.Now()
		if _, err := QueryHybridWithOptions(ctx, st, repo, q, 10, QueryOptions{
			QueryTokens:      strings.Fields(strings.ToLower(q)),
			CentralityWeight: DefaultCentralityWeight,
		}); err != nil {
			t.Errorf("query errored: %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("query too slow: %v", d)
		}
	}
	// The injection-shaped query must not have dropped the table.
	if hits, err := QueryHybrid(ctx, st, repo, "Close0", 5); err != nil || len(hits) == 0 {
		t.Errorf("table integrity / query broken after injection input: err=%v hits=%d", err, len(hits))
	}
}

// TestIDF_DownweightsCommonTokens proves the corpus-IDF mechanism: a token that
// matches almost every candidate (a stopword-like "file") must carry far less
// weight than a rare, discriminating token ("debounce"). Without this, BM25's
// per-term contributions are unweighted and a query's most common word wins by
// sheer match count — the bug that buried real targets under "...File" noise.
func TestIDF_DownweightsCommonTokens(t *testing.T) {
	var cands []types.Symbol
	for i := 0; i < 50; i++ {
		cands = append(cands, types.Symbol{ID: fmt.Sprintf("s%d", i), Name: fmt.Sprintf("readFile%d", i)})
	}
	// Exactly one candidate carries the rare token.
	cands = append(cands, types.Symbol{ID: "rare", Name: "debounceFile"})

	idf := idfForTokens([]string{"file", "debounce"}, cands)
	if idf["debounce"] <= idf["file"] {
		t.Fatalf("rare token must outweigh common token: debounce=%.3f file=%.3f", idf["debounce"], idf["file"])
	}
	// And the common token should be near the floor, not merely smaller.
	if idf["file"] > 0.2 {
		t.Errorf("ubiquitous token weight too high: file=%.3f (want ≈floor)", idf["file"])
	}
}

// TestConceptualQuery_SalientNounBeatsIncidentalFiller is the regression guard
// for the multi-word weak spot: "add an LRU cache for hot searches" used to rank
// a snapshot helper #1 (because "hot" trigrams into "snap-shot") and a path
// helper high (because "searches" appears in its doc comment), while the actual
// `NewCache` was buried. The salient noun in the identifier must win; the filler
// words ("add", "hot") must not decide the ranking.
func TestConceptualQuery_SalientNounBeatsIncidentalFiller(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "repo"

	syms := []types.Symbol{
		{ID: "sym:newcache", RepoID: repo, Name: "NewCache", Kind: types.SymbolKindFunction, Path: "internal/docs/cache.go", Signature: "NewCache builds a bounded cache.", Language: "go"},
		// Noise 1: "hot" only matches via trigram into "snapshot".
		{ID: "sym:snap", RepoID: repo, Name: "snapshotPreEdit", Kind: types.SymbolKindFunction, Path: "internal/mcpsvc/workspace.go", Signature: "snapshotPreEdit records a snapshot before editing.", Language: "go"},
		// Noise 2: "searches" appears only in the doc comment, not the name.
		{ID: "sym:path", RepoID: repo, Name: "PathBetweenSymbols", Kind: types.SymbolKindMethod, Path: "internal/graph/querydsl.go", Signature: "PathBetweenSymbols searches a short calls path.", Language: "go"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	hits, err := QueryHybridWithOptions(ctx, st, repo, "add an LRU cache for hot searches", 10, QueryOptions{
		QueryTokens:      []string{"lru", "cache", "searches"},
		CentralityWeight: DefaultCentralityWeight,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits for conceptual query")
	}
	if hits[0].Symbol.ID != "sym:newcache" {
		t.Errorf("salient noun must rank #1: got %q (reasons %v)", hits[0].Symbol.Name, hits[0].Reasons)
	}
	rankOf := func(id string) int {
		for i, h := range hits {
			if h.Symbol.ID == id {
				return i + 1
			}
		}
		return 0
	}
	if rankOf("sym:newcache") >= rankOf("sym:snap") || rankOf("sym:newcache") >= rankOf("sym:path") {
		t.Errorf("NewCache(%d) must outrank snapshot(%d) and path(%d) noise",
			rankOf("sym:newcache"), rankOf("sym:snap"), rankOf("sym:path"))
	}
}

// TestTestDemotion_RanksImplementationOverItsTest guards the intent-gated test
// handling: for the default (reuse/explain) intent, a test whose name contains
// the query term must NOT outrank the implementation it covers; for test/debug
// intent, the test is the target and is boosted instead.
func TestTestDemotion_RanksImplementationOverItsTest(t *testing.T) {
	ctx := context.Background()
	st, err := graph.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "repo"

	// Match the query via PATH, not name, so neither symbol gets the exact_name /
	// name_prefix boost — isolating the test-handling logic under test.
	impl := types.Symbol{ID: "sym:impl", RepoID: repo, Name: "DoWork", Kind: types.SymbolKindFunction, Path: "internal/daemon/acquire.go", Language: "go"}
	test := types.Symbol{ID: "sym:test", RepoID: repo, Name: "VerifyWork", Kind: types.SymbolKindFunction, Path: "internal/daemon/acquire_test.go", Language: "go"}
	for _, s := range []types.Symbol{impl, test} {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	rankOf := func(intent, id string) int {
		hits, err := QueryHybridWithOptions(ctx, st, repo, "acquire", 10, QueryOptions{
			QueryTokens: []string{"acquire"}, Intent: intent,
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for i, h := range hits {
			if h.Symbol.ID == id {
				return i + 1
			}
		}
		return 0
	}

	if r := rankOf("", impl.ID); r != 1 {
		t.Errorf("default intent: implementation should rank #1, got %d", r)
	}
	if rankOf("", impl.ID) >= rankOf("", test.ID) {
		t.Errorf("default intent: implementation must outrank its test")
	}
	if rankOf("test", test.ID) >= rankOf("test", impl.ID) {
		t.Errorf("test intent: the test should be boosted above the implementation")
	}
}
