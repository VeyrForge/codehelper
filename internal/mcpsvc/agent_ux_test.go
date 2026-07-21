package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDemoteFixtureHits_PrefersProduction(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "CatsService", Path: "sample/06-mongoose/cats.service.ts"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "b", Name: "CatsService", Path: "apps/api/cats.service.ts"}, Score: 0.95},
		{Symbol: types.Symbol{ID: "c", Name: "CatsController", Path: "sample/01-cats/cats.controller.ts"}, Score: 0.9},
	}
	got, demoted := demoteFixtureHits(hits)
	if demoted != 1 {
		t.Fatalf("demoted=%d want 1 (CatsController only; CatsService exact protected)", demoted)
	}
	if got[0].Symbol.Path != "apps/api/cats.service.ts" {
		t.Fatalf("top after demote = %s, want production path", got[0].Symbol.Path)
	}
	if got[1].Symbol.Path != "sample/06-mongoose/cats.service.ts" {
		t.Fatalf("[1]=%s want protected fixture CatsService", got[1].Symbol.Path)
	}
	if note := fixtureCollisionNote(demoted); !strings.Contains(note, "Demoted 1") {
		t.Errorf("collision note = %q", note)
	}
}

func TestDemoteFixtureHits_AllSamplesElevate01(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "CatsService", Path: "sample/06-mongoose/cats.service.ts"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "b", Name: "CatsService", Path: "sample/01-cats-app/cats.service.ts"}, Score: 0.9},
	}
	got, demoted := demoteFixtureHits(hits)
	if demoted != 0 {
		t.Fatalf("all-noise demoted=%d want 0", demoted)
	}
	if got[0].Symbol.Path != "sample/01-cats-app/cats.service.ts" {
		t.Fatalf("canonical sample not elevated: %s", got[0].Symbol.Path)
	}
}

// Nest query CatsService: weak production BM25 must not outrank exact_name
// fixture hits, and sample/01-cats-app must win among fixture-only exacts.
func TestDemoteFixtureHits_ProtectsExactNameOverWeakProd(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "w1", Name: "CreateCatDto", Path: "packages/common/dto.ts"}, Score: 0.42, Reasons: []string{"bm25"}},
		{Symbol: types.Symbol{ID: "w2", Name: "ValidationPipe", Path: "packages/common/pipes/validation.pipe.ts"}, Score: 0.38, Reasons: []string{"bm25"}},
		{Symbol: types.Symbol{ID: "w3", Name: "Reflector", Path: "packages/core/services/reflector.ts"}, Score: 0.35, Reasons: []string{"bm25"}},
		{Symbol: types.Symbol{ID: "s1", Name: "CatsService", Path: "sample/01-cats-app/src/cats/cats.service.ts"}, Score: 0.95, Reasons: []string{"bm25", "exact_name"}},
		{Symbol: types.Symbol{ID: "s6", Name: "CatsService", Path: "sample/06-mongoose/src/cats/cats.service.ts"}, Score: 0.90, Reasons: []string{"bm25", "exact_name"}},
		{Symbol: types.Symbol{ID: "s7", Name: "CatsService", Path: "sample/07-sequelize/src/cats/cats.service.ts"}, Score: 0.88, Reasons: []string{"bm25", "exact_name"}},
	}
	before := demoteFixtureHitsPathOnly(hits)
	if !strings.Contains(before[0].Symbol.Path, "packages/") {
		t.Fatalf("sanity: path-only demotion should top with weak prod, got %s", before[0].Symbol.Path)
	}
	var beforeRank int
	for i, h := range before {
		if strings.Contains(h.Symbol.Path, "sample/01-cats-app/") && h.Symbol.Name == "CatsService" {
			beforeRank = i + 1
			break
		}
	}
	if beforeRank != 4 {
		t.Fatalf("sanity: path-only rank of 01-cats-app CatsService = %d, want 4 (Nest overshoot)", beforeRank)
	}

	got, demoted := demoteFixtureHits(hits)
	if demoted != 0 {
		t.Fatalf("exact_name fixtures are protected; demoted=%d want 0", demoted)
	}
	if got[0].Symbol.Name != "CatsService" || !strings.Contains(got[0].Symbol.Path, "sample/01-cats-app/") {
		t.Fatalf("top after protect = %s %s, want sample/01-cats-app CatsService", got[0].Symbol.Name, got[0].Symbol.Path)
	}
	if got[1].Symbol.Name != "CatsService" || !strings.Contains(got[1].Symbol.Path, "sample/06-mongoose/") {
		t.Fatalf("second exact should keep relative order: %s", got[1].Symbol.Path)
	}
	if got[3].Symbol.Path != "packages/common/dto.ts" {
		t.Fatalf("weak prod should follow exact fixtures, got %s at [3]", got[3].Symbol.Path)
	}

	scrambled := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "w1", Name: "CreateCatDto", Path: "packages/common/dto.ts"}, Score: 0.42, Reasons: []string{"bm25"}},
		{Symbol: types.Symbol{ID: "s6", Name: "CatsService", Path: "sample/06-mongoose/src/cats/cats.service.ts"}, Score: 0.95, Reasons: []string{"exact_name"}},
		{Symbol: types.Symbol{ID: "s1", Name: "CatsService", Path: "sample/01-cats-app/src/cats/cats.service.ts"}, Score: 0.90, Reasons: []string{"exact_name"}},
	}
	got2, _ := demoteFixtureHits(scrambled)
	if !strings.Contains(got2[0].Symbol.Path, "sample/01-cats-app/") {
		t.Fatalf("canonical elevate failed under weak prod: top=%s", got2[0].Symbol.Path)
	}
}

func TestDemoteFixtureHits_ExactProdStillBeatsExactFixture(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "s", Name: "CatsService", Path: "sample/01-cats-app/cats.service.ts"}, Score: 1.1, Reasons: []string{"exact_name"}},
		{Symbol: types.Symbol{ID: "p", Name: "CatsService", Path: "apps/api/cats.service.ts"}, Score: 1.05, Reasons: []string{"exact_name"}},
		{Symbol: types.Symbol{ID: "w", Name: "Helper", Path: "apps/api/helper.ts"}, Score: 0.2, Reasons: []string{"bm25"}},
	}
	got, demoted := demoteFixtureHits(hits)
	if demoted != 0 {
		t.Fatalf("demoted=%d want 0", demoted)
	}
	if got[0].Symbol.Path != "apps/api/cats.service.ts" {
		t.Fatalf("top = %s, want production exact_name", got[0].Symbol.Path)
	}
	if got[1].Symbol.Path != "sample/01-cats-app/cats.service.ts" {
		t.Fatalf("[1] = %s, want protected fixture exact", got[1].Symbol.Path)
	}
}

func TestDemoteFixtureHits_NestBedCatsService(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	base := testbedsRoot()
	if base == "" {
		t.Skip("no testbeds")
	}
	dbPath := filepath.Join(base, "nest", ".codehelper", "graph.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("nest bed not indexed: %v", err)
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatalf("open nest graph: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hits, err := retrieval.QueryHybrid(context.Background(), st, "nest", "CatsService", 20)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits for CatsService")
	}
	before := demoteFixtureHitsPathOnly(hits)
	after, _ := demoteFixtureHits(hits)
	rankOf := func(list []retrieval.RankedSymbol) int {
		for i, h := range list {
			if h.Symbol.Name == "CatsService" && strings.Contains(h.Symbol.Path, "sample/01-cats-app/") {
				return i + 1
			}
		}
		return -1
	}
	beforeRank, afterRank := rankOf(before), rankOf(after)
	t.Logf("BEFORE path-only top5:")
	for i, h := range before {
		if i >= 5 {
			break
		}
		t.Logf("  #%d %s %s %v", i+1, h.Symbol.Name, h.Symbol.Path, h.Reasons)
	}
	t.Logf("AFTER exact_name-protect top5:")
	for i, h := range after {
		if i >= 5 {
			break
		}
		t.Logf("  #%d %s %s %v", i+1, h.Symbol.Name, h.Symbol.Path, h.Reasons)
	}
	t.Logf("01-cats-app CatsService rank before=#%d after=#%d", beforeRank, afterRank)
	if afterRank != 1 {
		t.Fatalf("Nest query CatsService top want sample/01-cats-app (rank 1), got rank=%d top=%s %s",
			afterRank, after[0].Symbol.Name, after[0].Symbol.Path)
	}
	if !strings.Contains(after[0].Symbol.Path, "sample/01-cats-app/") || after[0].Symbol.Name != "CatsService" {
		t.Fatalf("top = %s %s", after[0].Symbol.Name, after[0].Symbol.Path)
	}
}

func demoteFixtureHitsPathOnly(hits []retrieval.RankedSymbol) []retrieval.RankedSymbol {
	var primary, noise []retrieval.RankedSymbol
	for _, h := range hits {
		if isReuseNoisePath(h.Symbol.Path) {
			noise = append(noise, h)
		} else {
			primary = append(primary, h)
		}
	}
	if len(primary) == 0 || len(noise) == 0 {
		return hits
	}
	out := make([]retrieval.RankedSymbol, 0, len(hits))
	out = append(out, primary...)
	out = append(out, noise...)
	return out
}

func TestFormatHubs_DropsStyleAndFixtures(t *testing.T) {
	in := []graph.Hub{
		{Name: "Marshal", Path: "internal/toon/toon.go", Line: 52, Callers: 60},
		{Name: ".btn", Path: "assets/app.css", Line: 1, Callers: 99},
		{Name: "Demo", Path: "sample/01-demo/app.ts", Line: 3, Callers: 40},
		{Name: "", Path: "x.go", Line: 1, Callers: 3},
	}
	got := formatHubs(in)
	if len(got) != 1 || !strings.Contains(got[0], "Marshal") {
		t.Fatalf("expected only Marshal hub, got %v", got)
	}
}

func TestIndexGraphQualityWarnings_ContainsOnly(t *testing.T) {
	w := indexGraphQualityWarnings(100, 102)
	if len(w) == 0 || !strings.Contains(w[0], "inventory/contains-only") {
		t.Fatalf("expected contains-only warning, got %v", w)
	}
	if w2 := indexGraphQualityWarnings(100, 500); len(w2) != 0 {
		t.Fatalf("dense graph should stay silent, got %v", w2)
	}
	if w3 := indexGraphQualityWarnings(50, 0); len(w3) == 0 || !strings.Contains(w3[0], "0 edges") {
		t.Fatalf("zero-edge warning missing: %v", w3)
	}
}
