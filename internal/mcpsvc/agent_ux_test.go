package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDominantDuplicatedName_IgnoresWeakMethodDupes(t *testing.T) {
	t.Parallel()
	// Nest starter: AppService is the clear query top; getHello appears twice
	// at low score and must not become the "exact" dominant.
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "1", Name: "AppService", Path: "src/app.service.ts"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "2", Name: "getHello", Path: "src/app.service.ts"}, Score: 0.19},
		{Symbol: types.Symbol{ID: "3", Name: "getHello", Path: "src/app.controller.ts"}, Score: 0.03},
		{Symbol: types.Symbol{ID: "4", Name: "AppController", Path: "src/app.controller.ts"}, Score: 0.03},
	}
	if got := dominantDuplicatedName(hits); got != "" {
		t.Fatalf("dominant=%q want empty (weak getHello dupes ignored)", got)
	}
	got, _ := demoteFixtureHits(hits)
	if got[0].Symbol.Name != "AppService" {
		t.Fatalf("top after demote=%s want AppService", got[0].Symbol.Name)
	}
	// Nest sample collision: CatsService×3 near top score still dominates.
	nest := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "CatsService", Path: "src/cats/cats.service.ts"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "b", Name: "CatsService", Path: "sample/01-cats-app/src/cats/cats.service.ts"}, Score: 0.36},
		{Symbol: types.Symbol{ID: "c", Name: "CatsService", Path: "sample/06-mongoose/src/cats/cats.service.ts"}, Score: 0.36},
		{Symbol: types.Symbol{ID: "d", Name: "findAll", Path: "src/cats/cats.service.ts"}, Score: 0.24},
	}
	if got := dominantDuplicatedName(nest); got != "CatsService" {
		t.Fatalf("nest dominant=%q want CatsService", got)
	}
}

func TestUpgradeHitDefLines_ClassPastMagicComment(t *testing.T) {
	dir := t.TempDir()
	rel := "app/controllers/users_controller.rb"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "# frozen_string_literal: true\n\nclass UsersController < ApplicationController\nend\n"
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{
			Name: "UsersController", Kind: types.SymbolKindClass,
			Path: rel, LineStart: 1, LineEnd: 1,
		}, Score: 1.0},
		{Symbol: types.Symbol{
			Name: "index", Kind: types.SymbolKindMethod,
			Path: rel, LineStart: 1, LineEnd: 1,
		}, Score: 0.5},
	}
	upgradeHitDefLines(dir, hits)
	if hits[0].Symbol.LineStart != 3 {
		t.Fatalf("class cite before→after: 1→%d want 3", hits[0].Symbol.LineStart)
	}
	if hits[1].Symbol.LineStart != 1 {
		t.Fatalf("method line=1 must not invent a def, got %d", hits[1].Symbol.LineStart)
	}
}

func TestContextView_UpgradesClassLocPastPreamble(t *testing.T) {
	dir := t.TempDir()
	rel := "app/controllers/users_controller.rb"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "# frozen_string_literal: true\n\nclass UsersController < ApplicationController\n  def index\n  end\nend\n"
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bun := &retrieval.ContextBundle{
		Symbol: &types.Symbol{
			Name: "UsersController", Kind: types.SymbolKindClass,
			Path: rel, LineStart: 1, LineEnd: 6,
		},
		Callers: []types.Symbol{{
			Name: "index", Kind: types.SymbolKindMethod,
			Path: rel, LineStart: 1, LineEnd: 1,
		}},
	}
	view, ok := contextView(bun, false, dir, "none").(compactContext)
	if !ok {
		t.Fatalf("expected compactContext, got %T", contextView(bun, false, dir, "none"))
	}
	if view.Symbol.Loc != rel+":3" {
		t.Fatalf("class Loc before→after want %s:3 got %q", rel, view.Symbol.Loc)
	}
	if len(view.Callers) != 1 || view.Callers[0].Loc != rel+":1" {
		t.Fatalf("method Loc must stay line=1, got %+v", view.Callers)
	}
}

func TestSymbolDefLoc_AndImpactNodeLoc_UpgradeTypeLikeOnly(t *testing.T) {
	dir := t.TempDir()
	rel := "models/article.py"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "from django.db import models\n\n\nclass Article(models.Model):\n    pass\n"
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := symbolDefLoc(dir, types.Symbol{
		Name: "Article", Kind: types.SymbolKindClass, Path: rel, LineStart: 1,
	})
	if got != rel+":4" {
		t.Fatalf("class Loc want %s:4 got %q", rel, got)
	}
	method := symbolDefLoc(dir, types.Symbol{
		Name: "save", Kind: types.SymbolKindMethod, Path: rel, LineStart: 1,
	})
	if method != rel+":1" {
		t.Fatalf("method Loc must stay :1, got %q", method)
	}
	n := types.ImpactNode{
		SymbolID: "sym:repo:" + rel + ":1:Article",
		Name:     "Article", Kind: "class", Path: rel,
	}
	if loc := impactNodeLoc(dir, n); loc != rel+":4" {
		t.Fatalf("impact node Loc want %s:4 got %q", rel, loc)
	}
	res := &types.ImpactResult{
		Nodes:                []types.ImpactNode{n},
		MustUpdateCandidates: []types.ImpactNode{n},
	}
	stampImpactDefLocs(dir, res)
	if res.Nodes[0].Loc != rel+":4" || res.MustUpdateCandidates[0].Loc != rel+":4" {
		t.Fatalf("stamped Loc want %s:4 got nodes=%q cands=%q",
			rel, res.Nodes[0].Loc, res.MustUpdateCandidates[0].Loc)
	}
}

func TestDemoteFixtureHits_PrefersGodotScriptsOverAddons(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "_ready", Path: "addons/vendor_ui/plugin.gd"}, Score: 1.0, Reasons: []string{"bm25"}},
		{Symbol: types.Symbol{ID: "b", Name: "_ready", Path: "scripts/player.gd", ParentID: "Player"}, Score: 0.95, Reasons: []string{"bm25"}},
	}
	got, _ := demoteFixtureHits(hits)
	if got[0].Symbol.Path != "scripts/player.gd" {
		t.Fatalf("top=%s want scripts/player.gd (addons demoted as fixture)", got[0].Symbol.Path)
	}
	if got[1].Symbol.Path != "addons/vendor_ui/plugin.gd" {
		t.Fatalf("[1]=%s want addon after production", got[1].Symbol.Path)
	}
	if !review.IsEngineAddonPath("addons/vendor_ui/plugin.gd") {
		t.Fatal("IsEngineAddonPath should match Godot addons/")
	}
}

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

func TestElevateCanonicalSample_NestSrcAndExpressHello(t *testing.T) {
	t.Parallel()
	nest := elevateCanonicalSampleHit([]retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "CatsService", Path: "sample/07-sequelize/cats.service.ts"}},
		{Symbol: types.Symbol{ID: "b", Name: "CatsService", Path: "sample/07-sequelize/src/cats/cats.service.ts"}},
	})
	if !strings.Contains(nest[0].Symbol.Path, "/src/") {
		t.Fatalf("nest src prefer: %s", nest[0].Symbol.Path)
	}
	ex := elevateCanonicalSampleHit([]retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "express_get_1", Path: "examples/auth/index.js"}},
		{Symbol: types.Symbol{ID: "b", Name: "express_get_1", Path: "examples/hello-world/index.js"}},
	})
	if ex[0].Symbol.Path != "examples/hello-world/index.js" {
		t.Fatalf("express hello prefer: %s", ex[0].Symbol.Path)
	}
}

func TestCollisionHonestyNote_SampleAmbiguity(t *testing.T) {
	t.Parallel()
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "CatsService", Path: "sample/01-cats-app/src/cats/cats.service.ts"}},
		{Symbol: types.Symbol{ID: "b", Name: "CatsService", Path: "sample/06-mongoose/src/cats/cats.service.ts"}},
	}
	note := collisionHonestyNote(hits, 0)
	if !strings.Contains(note, "Ambiguous") || !strings.Contains(note, "path=") {
		t.Fatalf("expected sample ambiguity note, got %q", note)
	}
	// Production present → no Ambiguous note; soft path= honesty when samples remain.
	mixed := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "p", Name: "CatsService", Path: "apps/api/cats.service.ts"}},
		{Symbol: types.Symbol{ID: "a", Name: "CatsService", Path: "sample/01-cats-app/src/cats/cats.service.ts"}},
		{Symbol: types.Symbol{ID: "b", Name: "CatsService", Path: "sample/06-mongoose/src/cats/cats.service.ts"}},
	}
	if n := sampleAmbiguityNote(mixed); n != "" {
		t.Fatalf("mixed prod+sample should not ambiguity-warn: %q", n)
	}
	soft := collisionHonestyNote(mixed, 0)
	if !strings.Contains(soft, "path=") || !strings.Contains(soft, "production") {
		t.Fatalf("expected remaining-fixture path= honesty, got %q", soft)
	}
	if strings.Contains(soft, "Ambiguous") {
		t.Fatalf("soft honesty must not say Ambiguous when prod tops: %q", soft)
	}
}

func TestDemoteFixtureHits_CSSSelectorName(t *testing.T) {
	t.Parallel()
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "css", Name: ".list-unstyled", Path: "src/components/List.vue"}, Score: 1.2},
		{Symbol: types.Symbol{ID: "auth", Name: "authenticate", Path: "src/auth/login.ts"}, Score: 0.9},
		{Symbol: types.Symbol{ID: "id", Name: "#hero", Path: "src/pages/Home.svelte"}, Score: 0.8},
	}
	got, demoted := demoteFixtureHits(hits)
	if demoted < 2 {
		t.Fatalf("expected CSS selectors demoted, demoted=%d got=%+v", demoted, got)
	}
	if got[0].Symbol.Name != "authenticate" {
		t.Fatalf("production symbol must beat CSS selectors, got %+v", got[0])
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
	// CatsService collision fixture stages as nest-starter when dense nest → nestjs-sample.
	dbPath := ""
	repoID := "nest-starter"
	for _, cand := range []struct {
		rel  string
		repo string
	}{
		{filepath.Join("active", "nest-starter"), "nest-starter"},
		{filepath.Join("active", ".stub-src", "nest"), "nest"},
		{"nest-starter", "nest-starter"},
		{filepath.Join(".stub-src", "nest"), "nest"},
		{filepath.Join("active", "nest"), "nest"},
		{"nest", "nest"},
	} {
		p := filepath.Join(base, cand.rel, ".codehelper", "graph.db")
		if _, err := os.Stat(p); err == nil {
			dbPath = p
			repoID = cand.repo
			break
		}
	}
	if dbPath == "" {
		t.Skip("nest-starter / nest CatsService stub not indexed")
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatalf("open nest graph: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hits, err := retrieval.QueryHybrid(context.Background(), st, repoID, "CatsService", 20)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 && repoID != "nest" {
		hits, err = retrieval.QueryHybrid(context.Background(), st, "nest", "CatsService", 20)
		if err != nil {
			t.Fatalf("query nest: %v", err)
		}
	}
	if len(hits) == 0 {
		t.Fatal("no hits for CatsService")
	}
	before := demoteFixtureHitsPathOnly(hits)
	after, demoted := demoteFixtureHits(hits)
	rankOfSample01 := func(list []retrieval.RankedSymbol) int {
		for i, h := range list {
			if h.Symbol.Name == "CatsService" && strings.Contains(h.Symbol.Path, "sample/01-cats-app/") {
				return i + 1
			}
		}
		return -1
	}
	beforeRank, afterRank := rankOfSample01(before), rankOfSample01(after)
	t.Logf("BEFORE path-only top5:")
	for i, h := range before {
		if i >= 5 {
			break
		}
		t.Logf("  #%d %s %s %v", i+1, h.Symbol.Name, h.Symbol.Path, h.Reasons)
	}
	t.Logf("AFTER demote top5 (demoted=%d):", demoted)
	for i, h := range after {
		if i >= 5 {
			break
		}
		t.Logf("  #%d %s %s %v", i+1, h.Symbol.Name, h.Symbol.Path, h.Reasons)
	}
	t.Logf("01-cats-app CatsService rank before=#%d after=#%d", beforeRank, afterRank)
	// Nest stub ships production src/cats PLUS sample/* copies. Production exact
	// must win; sample/01 remains the canonical fixture (rank 2) for path= honesty.
	if after[0].Symbol.Name != "CatsService" {
		t.Fatalf("top name=%s want CatsService", after[0].Symbol.Name)
	}
	topPath := strings.ReplaceAll(after[0].Symbol.Path, "\\", "/")
	if strings.Contains(topPath, "sample/") || !strings.Contains(topPath, "src/cats/") {
		t.Fatalf("production src/cats should beat samples, got %s", topPath)
	}
	if afterRank != 2 {
		t.Fatalf("sample/01-cats-app CatsService rank=%d want 2 (after src/)", afterRank)
	}
	if note := collisionHonestyNote(after, demoted); strings.Contains(note, "Ambiguous") {
		t.Fatalf("no sample-ambiguity warn when production src exists: %q", note)
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

func TestDemoteFixtureHits_NestedForeignCodehelper(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "a", Name: "loadSessions", Path: "codehelper/vscode-extension/src/mainPanelProvider.ts"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "b", Name: "GetSession", Path: "backend/internal/auth/session_store.go"}, Score: 0.8},
		{Symbol: types.Symbol{ID: "c", Name: "SaveSession", Path: "backend/internal/auth/session_store.go"}, Score: 0.7},
	}
	got, demoted := demoteFixtureHits(hits)
	if demoted < 1 {
		t.Fatalf("expected nested codehelper demoted, demoted=%d got=%+v", demoted, got)
	}
	if got[0].Symbol.Path != "backend/internal/auth/session_store.go" {
		t.Fatalf("host auth must rank first, got %s", got[0].Symbol.Path)
	}
	if !review.IsNestedForeignToolTree("codehelper/vscode-extension/src/mainPanelProvider.ts") {
		t.Fatal("expected nested path true")
	}
	if review.IsNestedForeignToolTree("cmd/codehelper/main.go") {
		t.Fatal("cmd/codehelper must stay searchable")
	}
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
	if w4 := indexGraphQualityWarnings(0, 0); len(w4) == 0 || !strings.Contains(w4[0], "0 symbols") {
		t.Fatalf("empty-index warning missing: %v", w4)
	}
}

func TestDemoteFrameworkHubHits_PrefersAppOverDepends(t *testing.T) {
	t.Parallel()
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "1", Name: "depends", Kind: types.SymbolKindVariable, Path: "fastapi/dependencies/utils.py"}, Score: 1.2},
		{Symbol: types.Symbol{ID: "2", Name: "Depends", Kind: types.SymbolKindFunction, Path: "fastapi/param_functions.py"}, Score: 1.1},
		{Symbol: types.Symbol{ID: "3", Name: "list_users", Kind: types.SymbolKindFunction, Path: "main.py"}, Score: 0.4},
		{Symbol: types.Symbol{ID: "4", Name: "UserService", Kind: types.SymbolKindClass, Path: "main.py"}, Score: 0.35},
		{Symbol: types.Symbol{ID: "5", Name: "get_db", Kind: types.SymbolKindFunction, Path: "docs_src/dependencies/tutorial007.py"}, Score: 0.39},
	}
	got, demoted := demoteFrameworkHubHits("Depends list_users get_db", hits)
	if demoted < 2 {
		t.Fatalf("demoted=%d want >=2 hubs", demoted)
	}
	if got[0].Symbol.Name != "list_users" && got[0].Symbol.Name != "get_db" && got[0].Symbol.Name != "UserService" {
		t.Fatalf("top=%s want app symbol (list_users/get_db/UserService)", got[0].Symbol.Name)
	}
	// Hubs must sit after all exact app token matches.
	hubRank := -1
	appLast := -1
	for i, h := range got {
		n := strings.ToLower(h.Symbol.Name)
		if n == "list_users" || n == "get_db" || n == "userservice" {
			appLast = i
		}
		if isFrameworkHubName(h.Symbol.Name) && hubRank < 0 {
			hubRank = i
		}
	}
	if hubRank >= 0 && appLast >= 0 && hubRank < appLast {
		t.Fatalf("hub at %d before last app at %d: %v", hubRank, appLast, summarizeTop(got, 5))
	}
}

func TestDemoteFrameworkHubHits_HubOnlyPrefersPublicAPI(t *testing.T) {
	t.Parallel()
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "1", Name: "depends", Kind: types.SymbolKindVariable, Path: "fastapi/dependencies/utils.py"}, Score: 1.2},
		{Symbol: types.Symbol{ID: "2", Name: "Depends", Kind: types.SymbolKindFunction, Path: "fastapi/param_functions.py"}, Score: 1.0},
		{Symbol: types.Symbol{ID: "3", Name: "Depends", Kind: types.SymbolKindClass, Path: "fastapi/params.py"}, Score: 1.05},
	}
	got, demoted := demoteFrameworkHubHits("Depends", hits)
	if demoted < 1 {
		t.Fatalf("demoted=%d want internal depends vars demoted", demoted)
	}
	if !isPublicFrameworkHubDef(got[0]) {
		t.Fatalf("top=%s %s want public Depends def", got[0].Symbol.Name, got[0].Symbol.Path)
	}
}

func TestDemoteNoiseForQuery_StubAppSymbols(t *testing.T) {
	t.Parallel()
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "1", Name: "Depends", Kind: types.SymbolKindFunction, Path: "main.py"}, Score: 1.1, Reasons: []string{"exact_name"}},
		{Symbol: types.Symbol{ID: "2", Name: "list_users", Kind: types.SymbolKindFunction, Path: "main.py"}, Score: 0.5},
		{Symbol: types.Symbol{ID: "3", Name: "UserService", Kind: types.SymbolKindClass, Path: "main.py"}, Score: 0.45},
	}
	got, demoted := demoteNoiseForQuery("Depends list_users UserService", hits)
	if demoted < 1 {
		t.Fatalf("demoted=%d", demoted)
	}
	if got[0].Symbol.Name != "list_users" && got[0].Symbol.Name != "UserService" {
		t.Fatalf("top=%s want list_users or UserService", got[0].Symbol.Name)
	}
	note := collisionHonestyNote(got, demoted)
	if !strings.Contains(strings.ToLower(note), "framework-hub") && !strings.Contains(strings.ToLower(note), "depends") {
		t.Fatalf("collision note should mention framework hub demotion: %q", note)
	}
}
