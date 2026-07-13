package retrieval

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// FindSimilarSymbols ranks symbols whose name/signature/path resemble a target —
// the "similar implementation search" from goal.md. It reuses the hybrid ranker
// over the target's doc+signature, then boosts same-kind neighbors in the same
// package directory while excluding the target itself.
func FindSimilarSymbols(ctx context.Context, st *graph.Store, repoID, repoRoot, name string, limit int) ([]RankedSymbol, error) {
	if limit <= 0 {
		limit = 10
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	cands, err := st.SymbolsByName(ctx, repoID, name, 8)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, nil
	}
	target := cands[0]
	q := strings.TrimSpace(target.Name + " " + target.Signature)
	if q == "" {
		q = target.Name
	}
	opts := MCPQueryOptionsWithProfile(repoRoot, "explore", tokenize(q), nil)
	hits, err := QueryHybridWithOptions(ctx, st, repoID, q, limit+12, opts)
	if err != nil {
		return nil, err
	}
	targetDir := filepath.ToSlash(filepath.Dir(target.Path))
	var out []RankedSymbol
	for _, h := range hits {
		if h.Symbol.ID == target.ID {
			continue
		}
		boostSimilarity(&h, target, targetDir)
		if h.Score > 0 {
			out = append(out, h)
		}
	}
	sortSliceRanked(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func boostSimilarity(h *RankedSymbol, target types.Symbol, targetDir string) {
	if h.Symbol.Kind == target.Kind && h.Symbol.Kind != "" {
		h.Score += 0.08
		h.Reasons = append(h.Reasons, "same_kind")
	}
	if filepath.ToSlash(filepath.Dir(h.Symbol.Path)) == targetDir {
		h.Score += 0.06
		h.Reasons = append(h.Reasons, "same_package")
	}
	overlap := tokenOverlap(tokenize(target.Signature+" "+target.Name), tokenize(h.Symbol.Signature+" "+h.Symbol.Name))
	if overlap >= 2 {
		h.Score += 0.05 * float64(overlap)
		h.Reasons = append(h.Reasons, "signature_overlap")
	}
}

func tokenOverlap(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := map[string]struct{}{}
	for _, t := range a {
		set[t] = struct{}{}
	}
	n := 0
	for _, t := range b {
		if _, ok := set[t]; ok {
			n++
		}
	}
	return n
}

func sortSliceRanked(in []RankedSymbol) {
	for i := range in {
		in[i].Reasons = dedupeReasons(in[i].Reasons)
	}
	sort.SliceStable(in, func(i, j int) bool { return rankedLess(in[i], in[j]) })
}
