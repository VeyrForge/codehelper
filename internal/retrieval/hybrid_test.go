package retrieval

import (
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestRRFMergesBothLists(t *testing.T) {
	a := []RankedSymbol{{Symbol: types.Symbol{ID: "1", Name: "a"}, Score: 1}}
	b := []RankedSymbol{{Symbol: types.Symbol{ID: "2", Name: "b"}, Score: 1}}
	out := RRF(a, b, 60)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
}

func TestRRF_DeterministicTieBreakOrder(t *testing.T) {
	a := RankedSymbol{Symbol: types.Symbol{ID: "b-id", Name: "b", Path: "z/path.go"}, Score: 1}
	b := RankedSymbol{Symbol: types.Symbol{ID: "a-id", Name: "a", Path: "a/path.go"}, Score: 1}
	if rankedLess(a, b) {
		t.Fatalf("expected a(path=z) to sort after b(path=a)")
	}
	if !rankedLess(b, a) {
		t.Fatalf("expected b(path=a) to sort before a(path=z)")
	}
}

func TestRRF_DeduplicatesSharedSymbolID(t *testing.T) {
	a := []RankedSymbol{{Symbol: types.Symbol{ID: "same", Name: "n", Path: "a.go"}, Score: 1}}
	b := []RankedSymbol{{Symbol: types.Symbol{ID: "same", Name: "n", Path: "a.go"}, Score: 1}}
	out := RRF(a, b, 60)
	if len(out) != 1 {
		t.Fatalf("expected one merged symbol, got %d", len(out))
	}
	if out[0].Symbol.ID != "same" {
		t.Fatalf("unexpected merged symbol id: %s", out[0].Symbol.ID)
	}
	if out[0].Score <= 0 {
		t.Fatalf("expected positive fused score, got %f", out[0].Score)
	}
}

func TestMergeHitReasonsFallsBackToRRF(t *testing.T) {
	got := mergeHitReasons(nil, nil, "missing-id")
	if len(got) != 1 || got[0] != "rrf" {
		t.Fatalf("expected fallback reason [rrf], got %v", got)
	}
}

func TestDedupeReasons_StripsEmptyAndPreservesOrder(t *testing.T) {
	got := dedupeReasons([]string{"bm25", "", "vector", "bm25", "vector", "rrf"})
	want := []string{"bm25", "vector", "rrf"}
	if len(got) != len(want) {
		t.Fatalf("unexpected len: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestTrigramScore_PositiveForOverlap(t *testing.T) {
	q := "login rate limit"
	got := trigramScore(meaningfulQueryTokens(tokenize(q)), q, "AuthController login rate limiter")
	if got <= 0 {
		t.Fatalf("expected positive trigram score, got %f", got)
	}
}

func TestRerankWithSignals_BoostsDiffAndTests(t *testing.T) {
	in := []RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Path: "src/auth/service.go"}, Score: 1},
		{Symbol: types.Symbol{ID: "b", Path: "tests/auth/login_test.go"}, Score: 1},
	}
	out := rerankWithSignals(in, QueryOptions{
		ChangedSymbolIDs: map[string]struct{}{"a": {}},
		Intent:           "debug",
	})
	if len(out) != 2 {
		t.Fatalf("unexpected output len: %d", len(out))
	}
	var gotA, gotB float64
	for _, x := range out {
		if x.Symbol.ID == "a" {
			gotA = x.Score
		}
		if x.Symbol.ID == "b" {
			gotB = x.Score
		}
	}
	if gotA <= 1 {
		t.Fatalf("expected diff boost for symbol a, got %f", gotA)
	}
	if gotB <= 1 {
		t.Fatalf("expected test boost for symbol b, got %f", gotB)
	}
}

func TestPathHintBoost(t *testing.T) {
	h := extractPathHints("internal/watcher/watcher.go scheduleFlushLocked")
	got := pathHintBoost("internal/watcher/watcher.go", h)
	if got <= 0 {
		t.Fatalf("expected positive path hint boost, got %f", got)
	}
	if pathHintBoost("internal/verify/verify.go", h) != 0 {
		t.Fatalf("expected no boost for unrelated path")
	}
}

func TestQueryWantsScaffold_MetaRankingQuery(t *testing.T) {
	toks := tokenize("demote seeders and tests in ranking")
	if queryWantsScaffold(toks) {
		t.Fatal("meta ranking query must not disable scaffold demotion")
	}
	if !queryIsAboutRanking(toks) {
		t.Fatal("expected ranking meta detection")
	}
	if !queryWantsScaffold(tokenize("add a database seeder")) {
		t.Fatal("explicit seeder task should want scaffold")
	}
}

func TestRerankWithSignals_CloseVerbPrefersClose(t *testing.T) {
	in := []RankedSymbol{
		{Symbol: types.Symbol{ID: "dl", Name: "downloadFile", Path: "internal/ghrelease/ghrelease.go"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "cl", Name: "Close", Path: "internal/graph/store.go"}, Score: 0.95},
	}
	out := applyQueryIntentBoosts(rerankWithSignals(in, QueryOptions{
		QueryTokens: tokenize("shut down the connection pool release"),
	}), QueryOptions{
		QueryTokens: tokenize("shut down the connection pool release"),
	})
	if out[0].Symbol.Name != "Close" {
		t.Fatalf("expected Close first, got %s (score %.3f vs %.3f)", out[0].Symbol.Name, out[0].Score, out[1].Score)
	}
}

func TestRerankWithSignals_TypoQueryDemotesCrossRepo(t *testing.T) {
	in := []RankedSymbol{
		{Symbol: types.Symbol{ID: "x", Name: "resolveCrossRepoCandidates", Path: "internal/mcpsvc/register.go"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "y", Name: "candidatesForTokens", Path: "internal/retrieval/hybrid.go"}, Score: 0.98},
	}
	out := applyQueryIntentBoosts(rerankWithSignals(in, QueryOptions{
		QueryTokens: tokenize("fuzzy candidate generation for typos"),
	}), QueryOptions{
		QueryTokens: tokenize("fuzzy candidate generation for typos"),
	})
	if out[0].Symbol.Name != "candidatesForTokens" {
		t.Fatalf("expected candidatesForTokens first, got %s", out[0].Symbol.Name)
	}
}
