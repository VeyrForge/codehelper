package mcpsvc

import (
	"fmt"
	"path"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// demoteFixtureHits reorders ranked hits so production definitions beat
// sample/test/fixture/style trees (Nest samples, FastAPI docs_src, Svelte
// expected.css). Relative order within each partition is preserved.
//
// Exact matches are protected (exact_name reason, or the dominant duplicated
// clean symbol name when hybrid omits the reason): never pushed below weak
// non-exact production BM25. When every exact match is fixture-only, elevate
// canonical sample/01-*. demoted counts non-exact noise hits only.
func demoteFixtureHits(hits []retrieval.RankedSymbol) (out []retrieval.RankedSymbol, demoted int) {
	if len(hits) == 0 {
		return hits, 0
	}
	dominant := dominantDuplicatedName(hits)
	var exactProd, exactFixture, primary, noise []retrieval.RankedSymbol
	for _, h := range hits {
		// Nested foreign codehelper checkouts must never win exact-name protection —
		// stale indexes still surface vscode-extension session helpers above host auth.
		if review.IsNestedForeignToolTree(h.Symbol.Path) {
			noise = append(noise, h)
			continue
		}
		// CSS selectors / custom properties pollute hubs and auth/health locate
		// even when they live inside .vue/.svelte/.tsx (not only .css paths).
		if isStyleSelectorName(h.Symbol.Name) {
			noise = append(noise, h)
			continue
		}
		exact := isExactNameHit(h, dominant)
		noisePath := isReuseNoisePath(h.Symbol.Path)
		switch {
		case exact && !noisePath:
			exactProd = append(exactProd, h)
		case exact && noisePath:
			exactFixture = append(exactFixture, h)
		case noisePath:
			noise = append(noise, h)
		default:
			primary = append(primary, h)
		}
	}
	if len(exactProd) == 0 && len(exactFixture) == 0 && len(primary) == 0 {
		// Framework monorepos often only ship samples — elevate sample/01-*.
		return elevateCanonicalSampleHit(hits), 0
	}
	if len(exactProd) == 0 && len(exactFixture) > 1 {
		exactFixture = elevateCanonicalSampleHit(exactFixture)
	}
	if len(exactProd) == 0 && len(exactFixture) == 0 && len(noise) == 0 {
		return hits, 0
	}
	if len(primary) == 0 && len(exactFixture) == 0 && len(noise) == 0 {
		return hits, 0
	}
	if len(exactProd) == 0 && len(primary) == 0 && len(noise) == 0 {
		return exactFixture, 0
	}
	out = make([]retrieval.RankedSymbol, 0, len(hits))
	out = append(out, exactProd...)
	out = append(out, exactFixture...)
	out = append(out, primary...)
	out = append(out, noise...)
	return out, len(noise)
}

func hasExactNameReason(h retrieval.RankedSymbol) bool {
	for _, r := range h.Reasons {
		if r == "exact_name" {
			return true
		}
	}
	return false
}

func isExactNameHit(h retrieval.RankedSymbol, dominant string) bool {
	if hasExactNameReason(h) {
		return true
	}
	return dominant != "" && h.Symbol.Name == dominant
}

// dominantDuplicatedName picks the clean identifier that appears most often
// among hits (Nest CatsService×N over this.catsService). Used when hybrid
// omits exact_name reasons but the query clearly targeted that symbol.
func dominantDuplicatedName(hits []retrieval.RankedSymbol) string {
	if len(hits) == 0 {
		return ""
	}
	topScore := hits[0].Score
	counts := make(map[string]int, len(hits))
	bestScore := make(map[string]float64, len(hits))
	for _, h := range hits {
		n := h.Symbol.Name
		if n == "" || strings.Contains(n, ".") {
			continue
		}
		counts[n]++
		if h.Score > bestScore[n] {
			bestScore[n] = h.Score
		}
	}
	best, bestN := "", 0
	for name, n := range counts {
		if n < 2 {
			continue
		}
		// Weak method duplicates (Nest getHello×2 at ~0.2) must not dominate a
		// unique high-score class (AppService at 1.0) during exact-name protect.
		if topScore > 0 && bestScore[name] < topScore*0.55 {
			continue
		}
		if n > bestN || (n == bestN && name < best) {
			best, bestN = name, n
		}
	}
	return best
}

// elevateCanonicalSampleHit moves a stable tutorial path (sample/01-*, Nest
// …/src/, Express examples/hello*) to the front when the top name has multiple
// fixture-only definitions. Scoring mirrors preferCanonicalSample.
func elevateCanonicalSampleHit(hits []retrieval.RankedSymbol) []retrieval.RankedSymbol {
	if len(hits) < 2 {
		return hits
	}
	topName := dominantDuplicatedName(hits)
	if topName == "" {
		topName = hits[0].Symbol.Name
	}
	var same []retrieval.RankedSymbol
	for _, h := range hits {
		if h.Symbol.Name == topName {
			same = append(same, h)
		}
	}
	if len(same) < 2 {
		return hits
	}
	best := same[0]
	bestScore := 1 << 30
	for _, h := range same {
		score := canonicalSampleScore(h.Symbol.Path)
		if score < bestScore {
			bestScore = score
			best = h
		}
	}
	if best.Symbol.ID == hits[0].Symbol.ID {
		return hits
	}
	out := make([]retrieval.RankedSymbol, 0, len(hits))
	out = append(out, best)
	for _, h := range hits {
		if h.Symbol.ID == best.Symbol.ID {
			continue
		}
		out = append(out, h)
	}
	return out
}

// canonicalSampleScore ranks fixture paths for Nest/Express sample-only trees
// (lower wins). Keep in sync with preferCanonicalSample.
func canonicalSampleScore(path string) int {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	score := 1000 + len(p)
	switch {
	case strings.Contains(p, "/sample/01-") || strings.HasPrefix(p, "sample/01-"):
		score = 1
	case strings.Contains(p, "/sample/") || strings.HasPrefix(p, "sample/"):
		if strings.Contains(p, "/src/") {
			score = 5 + len(p)/4
		} else {
			score = 40 + len(p)
		}
	case strings.Contains(p, "/examples/hello") || strings.HasPrefix(p, "examples/hello"):
		score = 40 + len(p)
	case strings.Contains(p, "/examples/") || strings.HasPrefix(p, "examples/"):
		score = 50 + len(p)
	case strings.Contains(p, "/integration/"):
		score = 100 + len(p)
	}
	return score
}

func isReuseNoisePath(p string) bool {
	if p == "" {
		return false
	}
	if review.IsTestPath(p) || review.IsSecondaryNoisePath(p) || isFixtureSymbolPath(p) {
		return true
	}
	return isStyleAssetPath(p)
}

// isStyleAssetPath demotes CSS/stylesheet symbols from hubs and reuse lists
// (mirrors graph.isStyleHubPath without importing unexported helpers).
func isStyleAssetPath(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, ".css"), strings.HasSuffix(base, ".scss"),
		strings.HasSuffix(base, ".sass"), strings.HasSuffix(base, ".less"),
		strings.HasSuffix(base, ".styl"):
		return true
	case strings.Contains(p, "/styles/"), strings.Contains(p, "/css/"),
		strings.HasPrefix(p, "styles/"), strings.HasPrefix(p, "css/"):
		return true
	}
	return false
}

// isStyleSelectorName demotes CSS class/id/custom-property symbols that live
// inside component files (Vue/Svelte/TSX) where the path is not a stylesheet.
func isStyleSelectorName(name string) bool {
	n := strings.TrimSpace(name)
	return strings.HasPrefix(n, ".") || strings.HasPrefix(n, "#") ||
		strings.HasPrefix(n, "@keyframes") || strings.HasPrefix(n, "--")
}

// demoteNoiseForQuery applies fixture demotion then framework-hub demotion so
// kickoff/query steer toward app/tutorial symbols when Depends-style hubs crowd
// the ranking (FastAPI OSS: get_db/docs_src over fastapi/dependencies utils).
func demoteNoiseForQuery(q string, hits []retrieval.RankedSymbol) ([]retrieval.RankedSymbol, int) {
	hits, d1 := demoteFixtureHits(hits)
	hits, d2 := demoteFrameworkHubHits(q, hits)
	return hits, d1 + d2
}

// reuseHitPool sizes the hybrid candidate pool so demotion can see app/tutorial
// hits that rank below framework hubs (kickoff used to fetch only 5 = all hubs).
func reuseHitPool(want int) int {
	if want <= 0 {
		want = 5
	}
	pool := want * 4
	if pool < 24 {
		pool = 24
	}
	if pool > 48 {
		pool = 48
	}
	return pool
}

func capRankedHits(hits []retrieval.RankedSymbol, n int) []retrieval.RankedSymbol {
	if n <= 0 || len(hits) <= n {
		return hits
	}
	return hits[:n]
}

// upgradeSymbolDefLine fixes a graph-backed line≤1 cite for type/class symbols
// by scanning the on-disk definition (Rails frozen_string_literal headers,
// Django import blocks, etc.). Methods/functions stay at line=1 — never invents
// a hotspot, only upgrades type-like Loc cites. Mutates s in place.
func upgradeSymbolDefLine(root string, s *types.Symbol) {
	if s == nil || root == "" || s.Path == "" || s.LineStart > 1 {
		return
	}
	abs, err := absPathUnderRepo(root, s.Path)
	if err != nil {
		return
	}
	ln := security.LineForSymbolDef(abs, s.Name, string(s.Kind), s.LineStart)
	if ln <= s.LineStart {
		return
	}
	s.LineStart = ln
	if s.LineEnd > 0 && s.LineEnd < ln {
		s.LineEnd = ln
	}
}

// symbolDefLoc returns path:line after the safe type-like line upgrade.
func symbolDefLoc(root string, s types.Symbol) string {
	upgradeSymbolDefLine(root, &s)
	if s.LineStart <= 0 {
		s.LineStart = 1
	}
	return fmt.Sprintf("%s:%d", s.Path, s.LineStart)
}

// upgradeHitDefLines fixes graph-backed line=1 cites for type/class symbols by
// scanning the on-disk definition (Rails frozen_string_literal headers, Java
// license blocks, etc.). Hotspots already do this via LineForHotPath; query /
// kickoff / scout / plan reuse must too so agents get real file:line cites.
func upgradeHitDefLines(root string, hits []retrieval.RankedSymbol) {
	if root == "" || len(hits) == 0 {
		return
	}
	for i := range hits {
		upgradeSymbolDefLine(root, &hits[i].Symbol)
	}
}

// impactNodeLoc builds an agent-facing path:line for an impact node, upgrading
// bogus line≤1 type-like cites without rewriting symbol_id (lookup key).
func impactNodeLoc(root string, n types.ImpactNode) string {
	line := symIDLine(n.SymbolID)
	s := types.Symbol{
		Name: n.Name, Kind: types.SymbolKind(n.Kind),
		Path: n.Path, LineStart: line,
	}
	return symbolDefLoc(root, s)
}

// stampImpactDefLocs fills Loc on impact nodes/candidates for agent cites.
func stampImpactDefLocs(root string, res *types.ImpactResult) {
	if res == nil || root == "" {
		return
	}
	for i := range res.Nodes {
		res.Nodes[i].Loc = impactNodeLoc(root, res.Nodes[i])
	}
	for i := range res.MustUpdateCandidates {
		res.MustUpdateCandidates[i].Loc = impactNodeLoc(root, res.MustUpdateCandidates[i])
	}
}

// demoteFrameworkHubHits pushes framework DI/router hubs (Depends, FastAPI,
// APIRouter, …) below app/tutorial symbols when the query also names app tokens
// (list_users, UserService, get_db). When the query is hub-only, prefers public
// API defs (param_functions.Depends) over internal package locals (depends vars).
func demoteFrameworkHubHits(q string, hits []retrieval.RankedSymbol) ([]retrieval.RankedSymbol, int) {
	if len(hits) < 2 {
		return hits, 0
	}
	appTokens := appFacingQueryTokens(q)
	if len(appTokens) > 0 {
		return preferAppOverFrameworkHubs(hits, appTokens)
	}
	return preferPublicAPIOverHubNoise(hits)
}

func appFacingQueryTokens(q string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(q)) {
		w = strings.Trim(w, ".,:;!?\"'()`*/\\")
		if len(w) < 2 || retrieval.IsCommonWord(w) || isFrameworkHubName(w) {
			continue
		}
		out[w] = true
	}
	return out
}

func isFrameworkHubName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "depends", "fastapi", "apirouter", "include_router", "apieroute", "approute",
		"flask", "starlette", "apiroute":
		return true
	default:
		return false
	}
}

func isTutorialOrExamplePath(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	return strings.Contains(p, "/docs_src/") || strings.HasPrefix(p, "docs_src/") ||
		strings.Contains(p, "/examples/") || strings.HasPrefix(p, "examples/") ||
		strings.Contains(p, "/example/") || strings.HasPrefix(p, "example/") ||
		strings.Contains(p, "/sample/") || strings.HasPrefix(p, "sample/") ||
		strings.Contains(p, "/samples/") || strings.HasPrefix(p, "samples/")
}

func isPublicFrameworkHubDef(h retrieval.RankedSymbol) bool {
	n := strings.ToLower(strings.TrimSpace(h.Symbol.Name))
	if !isFrameworkHubName(n) {
		return false
	}
	if h.Symbol.Kind == types.SymbolKindVariable {
		return false
	}
	p := strings.ToLower(strings.ReplaceAll(h.Symbol.Path, "\\", "/"))
	if isTutorialOrExamplePath(p) || review.IsTestPath(p) {
		return false
	}
	switch n {
	case "depends":
		return strings.Contains(p, "param_functions.py") || strings.Contains(p, "params.py")
	case "fastapi":
		return strings.Contains(p, "applications.py")
	case "apirouter", "apieroute", "approute", "apiroute":
		return strings.Contains(p, "routing.py")
	case "include_router":
		return strings.Contains(p, "applications.py") || strings.Contains(p, "routing.py")
	case "flask":
		return strings.Contains(p, "app.py") || strings.HasSuffix(p, "flask/__init__.py")
	case "starlette":
		return strings.Contains(p, "applications.py")
	}
	return false
}

func preferAppOverFrameworkHubs(hits []retrieval.RankedSymbol, appTokens map[string]bool) ([]retrieval.RankedSymbol, int) {
	var appExact, tutorial, other, hubs []retrieval.RankedSymbol
	for _, h := range hits {
		name := strings.ToLower(strings.TrimSpace(h.Symbol.Name))
		switch {
		case appTokens[name]:
			appExact = append(appExact, h)
		case isFrameworkHubName(name):
			hubs = append(hubs, h)
		case isTutorialOrExamplePath(h.Symbol.Path):
			tutorial = append(tutorial, h)
		default:
			other = append(other, h)
		}
	}
	if len(appExact) == 0 && len(tutorial) == 0 {
		return hits, 0
	}
	if len(hubs) == 0 {
		return hits, 0
	}
	out := make([]retrieval.RankedSymbol, 0, len(hits))
	out = append(out, appExact...)
	out = append(out, tutorial...)
	out = append(out, other...)
	out = append(out, hubs...)
	return out, len(hubs)
}

func preferPublicAPIOverHubNoise(hits []retrieval.RankedSymbol) ([]retrieval.RankedSymbol, int) {
	var publicAPI, hubNoise, rest []retrieval.RankedSymbol
	for _, h := range hits {
		name := strings.ToLower(strings.TrimSpace(h.Symbol.Name))
		if !isFrameworkHubName(name) {
			rest = append(rest, h)
			continue
		}
		if isPublicFrameworkHubDef(h) {
			publicAPI = append(publicAPI, h)
		} else {
			hubNoise = append(hubNoise, h)
		}
	}
	if len(publicAPI) == 0 || len(hubNoise) == 0 {
		return hits, 0
	}
	out := make([]retrieval.RankedSymbol, 0, len(hits))
	out = append(out, publicAPI...)
	out = append(out, rest...)
	out = append(out, hubNoise...)
	return out, len(hubNoise)
}

// fixtureCollisionNote tells the agent why sample/test/hub hits were demoted.
func fixtureCollisionNote(demoted int) string {
	if demoted <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"Demoted %d sample/test/fixture/style/addon/framework-hub hit(s) below production or app/tutorial definitions. Prefer the top reuse/query row (app symbols like list_users/UserService/get_db over Depends hubs); pass path= (Nest sample/01-…, Express lib/, FastAPI docs_src or fastapi/param_functions.py, Godot scripts/ vs addons/, or ClassName._ready / ParentID) on context/impact if a tutorial/framework hit is intentional.",
		demoted,
	)
}

// sampleAmbiguityNote warns when multiple same-name Nest/Express sample paths
// remain at the top after ranking (canonical pick is best-effort; agents still
// need path= for non-canonical samples).
func sampleAmbiguityNote(hits []retrieval.RankedSymbol) string {
	if len(hits) < 2 {
		return ""
	}
	name := hits[0].Symbol.Name
	if name == "" {
		return ""
	}
	var fixturePaths []string
	seen := map[string]bool{}
	for _, h := range hits {
		if h.Symbol.Name != name {
			continue
		}
		p := h.Symbol.Path
		if !isReuseNoisePath(p) {
			// Production exact exists — demotion note covers the rest.
			return ""
		}
		key := strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
		if seen[key] {
			continue
		}
		seen[key] = true
		fixturePaths = append(fixturePaths, p)
		if len(fixturePaths) >= 4 {
			break
		}
	}
	if len(fixturePaths) < 2 {
		return ""
	}
	nShow := len(fixturePaths)
	if nShow > 3 {
		nShow = 3
	}
	return fmt.Sprintf(
		"Ambiguous %q across %d sample/fixture paths (e.g. %s). Top hit is a canonical pick only — pass path= (Nest sample/01-… or app subtree; Express lib/ or examples/hello*) on context/impact.",
		name, len(fixturePaths), strings.Join(fixturePaths[:nShow], ", "),
	)
}

// remainingFixtureHonestyNote tells agents that production topped but same-name
// Nest samples / Express examples still appear below — pass path= if intentional.
func remainingFixtureHonestyNote(hits []retrieval.RankedSymbol) string {
	if len(hits) < 2 {
		return ""
	}
	top := hits[0]
	if top.Symbol.Name == "" || isReuseNoisePath(top.Symbol.Path) || isStyleSelectorName(top.Symbol.Name) {
		return ""
	}
	var fixtures []string
	seen := map[string]bool{}
	for _, h := range hits[1:] {
		if h.Symbol.Name != top.Symbol.Name {
			continue
		}
		if !isReuseNoisePath(h.Symbol.Path) {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(h.Symbol.Path, "\\", "/"))
		if seen[key] {
			continue
		}
		seen[key] = true
		fixtures = append(fixtures, h.Symbol.Path)
		if len(fixtures) >= 3 {
			break
		}
	}
	if len(fixtures) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"Top hit is production %q; same name also under sample/fixture/examples (%s). Pass path= (Nest sample/01-…; Express examples/ or lib/) on context/impact if a tutorial hit is intentional.",
		top.Symbol.Name, strings.Join(fixtures, ", "),
	)
}

// collisionHonestyNote combines demotion + remaining sample-name collisions for
// query/scout/kickoff retrieval_note / collision_note fields.
func collisionHonestyNote(hits []retrieval.RankedSymbol, demoted int) string {
	parts := make([]string, 0, 3)
	if n := fixtureCollisionNote(demoted); n != "" {
		parts = append(parts, n)
	}
	if n := sampleAmbiguityNote(hits); n != "" {
		parts = append(parts, n)
	}
	if n := remainingFixtureHonestyNote(hits); n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, " ")
}

// indexGraphQualityWarnings turns meta symbol/edge counts into actionable MCP
// warnings (agents rarely run CLI doctor). Mirrors doctor contains-only nuance.
func indexGraphQualityWarnings(symbols, edges int) []string {
	var out []string
	if symbols <= 0 {
		return []string{
			"index has 0 symbols — query/context/impact will be empty until analyze succeeds (parser miss, wrong root, or stale prepare)",
		}
	}
	// Inventory / contains-only: edges ≈ symbols (each file owns itself, no fanout).
	tol := symbols / 20
	if tol < 5 {
		tol = 5
	}
	diff := edges - symbols
	if diff < 0 {
		diff = -diff
	}
	if edges > 0 && diff <= tol {
		out = append(out,
			fmt.Sprintf("graph looks inventory/contains-only (edge_count=%d ≈ symbol_count=%d) — impact/context fanout will be thin; prefer path-scoped query + read_workspace_file; AGENT DIRECTIVE: do NOT treat 0 callers as proof a change is isolated",
				edges, symbols),
		)
	}
	if edges == 0 && symbols > 20 {
		out = append(out,
			fmt.Sprintf("index has %d symbols but 0 edges — blast-radius tools are unreliable until reanalyze after a parser upgrade; NEVER treat empty fanout as safe isolation", symbols),
		)
	}
	return out
}

// filterNoiseHubs drops stylesheet / sample / fixture hubs from MCP presentation
// so detailed project_context architecture stays actionable even on stale hubs.json.
func filterNoiseHubs(hubs []graph.Hub) []graph.Hub {
	if len(hubs) == 0 {
		return hubs
	}
	out := make([]graph.Hub, 0, len(hubs))
	for _, h := range hubs {
		if h.Name == "" || isReuseNoisePath(h.Path) {
			continue
		}
		out = append(out, h)
	}
	return out
}

func filterNoisePackageHubs(pkgs []graph.PackageHub) []graph.PackageHub {
	if len(pkgs) == 0 {
		return pkgs
	}
	out := make([]graph.PackageHub, 0, len(pkgs))
	for _, p := range pkgs {
		if p.Dir == "" || isReuseNoisePath(p.Dir) {
			continue
		}
		out = append(out, p)
	}
	return out
}
