package retrieval

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// PublicAPIMapOptions controls PageRank/hub-biased public API packing for
// library-style packages (Aider repomap / Cody sparse-lib pattern).
type PublicAPIMapOptions struct {
	PathPrefix        string
	Limit             int
	IncludeUnexported bool
	HubWeight         float64
}

// PublicAPIEntry is one exported surface symbol with hub bias.
type PublicAPIEntry struct {
	Symbol   types.Symbol `json:"symbol"`
	Score    float64      `json:"score"`
	Callers  int          `json:"callers"`
	Reasons  []string     `json:"reasons,omitempty"`
	Exported bool         `json:"exported"`
}

// BuildPublicAPIMap ranks a package's public surface by hub centrality
// (log1p inbound calls — a cheap PageRank-style spine bias).
func BuildPublicAPIMap(ctx context.Context, st *graph.Store, repoID string, opts PublicAPIMapOptions) ([]PublicAPIEntry, error) {
	if st == nil {
		return nil, nil
	}
	prefix := strings.TrimSpace(opts.PathPrefix)
	if prefix == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 40
	}
	hubW := opts.HubWeight
	if hubW <= 0 {
		hubW = 1.0
	}
	syms, err := st.SymbolsByPathPrefix(ctx, repoID, prefix, 4000)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(syms))
	for _, s := range syms {
		if pathLooksLikeTest(strings.ToLower(s.Path), s.Path) || isScaffoldSymbol(s.Path, s.Name) {
			continue
		}
		ids = append(ids, s.ID)
	}
	deg, _ := st.InDegreesFor(ctx, repoID, "calls", ids)
	if deg == nil {
		deg = map[string]int{}
	}
	out := make([]PublicAPIEntry, 0, limit)
	for _, s := range syms {
		if pathLooksLikeTest(strings.ToLower(s.Path), s.Path) || isScaffoldSymbol(s.Path, s.Name) {
			continue
		}
		if !plausibleSymbolName(s.Name) {
			continue
		}
		exported := isPublicAPISymbol(s)
		if !exported && !opts.IncludeUnexported {
			continue
		}
		callers := deg[s.ID]
		score := hubW * math.Log1p(float64(callers))
		reasons := []string{"hub_bias"}
		if exported {
			score += 0.35
			reasons = append(reasons, "exported")
		}
		switch s.Kind {
		case types.SymbolKindClass, types.SymbolKindInterface, types.SymbolKindTypeAlias, types.SymbolKindEnum, types.SymbolKindNamespace:
			score += 0.2
			reasons = append(reasons, "type_spine")
		case types.SymbolKindFunction, types.SymbolKindMethod:
			score += 0.1
		}
		out = append(out, PublicAPIEntry{
			Symbol:   s,
			Score:    score,
			Callers:  callers,
			Reasons:  reasons,
			Exported: exported,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Callers != out[j].Callers {
			return out[i].Callers > out[j].Callers
		}
		if out[i].Symbol.Name != out[j].Symbol.Name {
			return out[i].Symbol.Name < out[j].Symbol.Name
		}
		return out[i].Symbol.ID < out[j].Symbol.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func isPublicAPISymbol(s types.Symbol) bool {
	if strings.HasPrefix(s.Name, "_") {
		return false
	}
	lang := strings.ToLower(strings.TrimSpace(s.Language))
	if lang == "go" {
		r := []rune(s.Name)
		return len(r) > 0 && unicode.IsUpper(r[0])
	}
	return true
}
