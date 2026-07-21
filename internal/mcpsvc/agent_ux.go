package mcpsvc

import (
	"fmt"
	"path"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
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
	counts := make(map[string]int, len(hits))
	for _, h := range hits {
		n := h.Symbol.Name
		if n == "" || strings.Contains(n, ".") {
			continue
		}
		counts[n]++
	}
	best, bestN := "", 0
	for name, n := range counts {
		if n < 2 {
			continue
		}
		if n > bestN || (n == bestN && name < best) {
			best, bestN = name, n
		}
	}
	return best
}

// elevateCanonicalSampleHit moves a stable tutorial path (sample/01-*) to the
// front when the top name has multiple fixture-only definitions.
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
		p := strings.ToLower(strings.ReplaceAll(h.Symbol.Path, "\\", "/"))
		score := 1000 + len(p)
		if strings.Contains(p, "/sample/01-") || strings.HasPrefix(p, "sample/01-") {
			score = 1
		} else if strings.Contains(p, "/sample/") || strings.HasPrefix(p, "sample/") {
			score = 10 + len(p)
		} else if strings.Contains(p, "/examples/") || strings.HasPrefix(p, "examples/") {
			score = 50 + len(p)
		}
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

// fixtureCollisionNote tells the agent why sample/test hits were demoted.
func fixtureCollisionNote(demoted int) string {
	if demoted <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"Demoted %d sample/test/fixture/style hit(s) below production definitions. Prefer the top reuse/query row; pass path= on context/impact if a tutorial sample is intentional.",
		demoted,
	)
}

// indexGraphQualityWarnings turns meta symbol/edge counts into actionable MCP
// warnings (agents rarely run CLI doctor). Mirrors doctor contains-only nuance.
func indexGraphQualityWarnings(symbols, edges int) []string {
	if symbols <= 0 {
		return nil
	}
	var out []string
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
			fmt.Sprintf("graph looks inventory/contains-only (edge_count=%d ≈ symbol_count=%d) — impact/context fanout will be thin; prefer path-scoped query + read_workspace_file; do NOT treat 0 callers as proof a change is isolated",
				edges, symbols),
		)
	}
	if edges == 0 && symbols > 20 {
		out = append(out,
			fmt.Sprintf("index has %d symbols but 0 edges — blast-radius tools are unreliable until reanalyze after a parser upgrade", symbols),
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
