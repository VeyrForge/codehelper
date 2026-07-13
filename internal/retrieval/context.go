package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/parser"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// ContextBundle is 360-degree view for one symbol.
type ContextBundle struct {
	Symbol       *types.Symbol     `json:"symbol"`
	Callers      []types.Symbol    `json:"callers"`
	CallersTotal int               `json:"callers_total"` // exact count; Callers may be capped
	Callees      []types.Reference `json:"callees"`
	Imports      []types.Reference `json:"imports"`
}

type ContextPackItem struct {
	Kind   string        `json:"kind"`
	Path   string        `json:"path"`
	Symbol string        `json:"symbol,omitempty"`
	Reason []string      `json:"reason,omitempty"`
	Score  float64       `json:"score,omitempty"`
	Raw    *types.Symbol `json:"raw,omitempty"`
}

type ContextPack struct {
	Query       string            `json:"query"`
	Intent      string            `json:"intent"`
	ContextPack []ContextPackItem `json:"context_pack"`
}

// BuildContext loads a symbol's context with an UNBOUNDED caller list. Prefer
// BuildContextLimited on the request path so a hub symbol doesn't materialize tens
// of thousands of callers a compact view will cap anyway.
func BuildContext(ctx context.Context, st *graph.Store, repoID, nameOrID string) (*ContextBundle, error) {
	return BuildContextLimited(ctx, st, repoID, nameOrID, 0)
}

// BuildContextLimited caps the caller list at callerLimit (0 = unbounded) while
// still reporting the exact CallersTotal via a cheap COUNT — so "N callers
// (showing 12)" stays accurate without loading all N.
func BuildContextLimited(ctx context.Context, st *graph.Store, repoID, nameOrID string, callerLimit int) (*ContextBundle, error) {
	var sym *types.Symbol
	if strings.HasPrefix(nameOrID, "sym:") {
		s, err := st.SymbolByID(ctx, repoID, nameOrID)
		if err != nil {
			return nil, err
		}
		sym = s
	} else {
		syms, err := st.SymbolsByName(ctx, repoID, nameOrID, 5)
		if err != nil {
			return nil, err
		}
		if len(syms) == 0 {
			return nil, fmt.Errorf("symbol not found: %s", nameOrID)
		}
		sym = &syms[0]
	}
	// One indexed JOIN for the callers, not EdgesTo + a SymbolByID query per edge
	// (an N+1 that did 100k+ queries for a mega-hub). Bounded when callerLimit>0 so
	// a hub with thousands of callers doesn't materialize them all for a 12-row view.
	var callers []types.Symbol
	var callersTotal int
	var err error
	if callerLimit > 0 {
		callers, err = st.CallersOfLimited(ctx, repoID, sym.ID, callerLimit)
		if err != nil {
			return nil, err
		}
		if len(callers) < callerLimit {
			callersTotal = len(callers) // fewer than the cap: no separate COUNT needed
		} else if callersTotal, err = st.CountCallers(ctx, repoID, sym.ID); err != nil {
			return nil, err
		}
	} else {
		callers, err = st.CallersOf(ctx, repoID, sym.ID)
		if err != nil {
			return nil, err
		}
		callersTotal = len(callers)
	}
	out, err := st.EdgesFrom(ctx, repoID, sym.ID, string(types.RefKindCalls))
	if err != nil {
		return nil, err
	}
	fileEdges, _ := st.EdgesFrom(ctx, repoID, parser.FileNodeID(repoID, sym.Path), string(types.RefKindImports))
	if out == nil {
		out = []types.Reference{}
	}
	if fileEdges == nil {
		fileEdges = []types.Reference{}
	}
	b := &ContextBundle{Symbol: sym, Callees: out, Callers: callers, CallersTotal: callersTotal, Imports: fileEdges}
	return b, nil
}

// ToJSON renders bundle.
func ToJSON(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// BuildContextPack builds a task-oriented, source-cited retrieval package.
func BuildContextPack(ctx context.Context, st *graph.Store, repoID, query, intent string, hits []RankedSymbol, limit int) (*ContextPack, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	if limit <= 0 {
		limit = 24
	}
	items := make([]ContextPackItem, 0, limit)
	seen := map[string]struct{}{}
	for _, h := range hits {
		if len(items) >= limit {
			break
		}
		key := h.Symbol.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, ContextPackItem{
			Kind:   classifyContextKind(h.Symbol.Path),
			Path:   h.Symbol.Path,
			Symbol: h.Symbol.Name,
			Reason: dedupeReasons(h.Reasons),
			Score:  h.Score,
			Raw:    &h.Symbol,
		})
	}
	items = expandDependencyNeighbors(ctx, st, repoID, items, limit)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Path < items[j].Path
	})
	_ = ctx
	return &ContextPack{
		Query:       query,
		Intent:      strings.TrimSpace(intent),
		ContextPack: items,
	}, nil
}

func expandDependencyNeighbors(ctx context.Context, st *graph.Store, repoID string, items []ContextPackItem, limit int) []ContextPackItem {
	if len(items) == 0 || len(items) >= limit || st == nil {
		return items
	}
	seen := map[string]struct{}{}
	for _, it := range items {
		key := it.Path + "#" + it.Symbol
		seen[key] = struct{}{}
	}
	maxSeeds := len(items)
	if maxSeeds > 4 {
		maxSeeds = 4
	}
	for i := 0; i < maxSeeds && len(items) < limit; i++ {
		seed := items[i]
		if seed.Raw == nil {
			continue
		}
		out, _ := st.EdgesFrom(ctx, repoID, seed.Raw.ID, string(types.RefKindCalls))
		in, _ := st.EdgesTo(ctx, repoID, seed.Raw.ID, string(types.RefKindCalls))
		for _, e := range append(out, in...) {
			if len(items) >= limit {
				break
			}
			var sid string
			if e.SourceID == seed.Raw.ID {
				sid = e.TargetID
			} else {
				sid = e.SourceID
			}
			sym, err := st.SymbolByID(ctx, repoID, sid)
			if err != nil || sym == nil {
				continue
			}
			key := sym.Path + "#" + sym.Name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			items = append(items, ContextPackItem{
				Kind:   "dependency",
				Path:   sym.Path,
				Symbol: sym.Name,
				Reason: []string{"dependency_distance_1", "call_graph"},
				Score:  seed.Score * 0.9,
				Raw:    sym,
			})
		}
	}
	return items
}

func classifyContextKind(path string) string {
	p := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.Contains(p, "test"):
		return "test"
	case strings.Contains(p, "route"), strings.Contains(p, "app/api/"), strings.Contains(p, "pages/api/"), strings.Contains(p, "+server."), strings.Contains(p, "urls.py"), strings.Contains(p, "routes/"), strings.Contains(p, "functions.php"):
		return "entrypoint"
	case strings.Contains(p, "config"), strings.Contains(p, "nuxt.config"), strings.Contains(p, "next.config"), strings.Contains(p, "capacitor.config"):
		return "config"
	case strings.Contains(p, "/service/"), strings.Contains(p, "/services/"),
		strings.Contains(p, "\\service\\"), strings.Contains(p, "\\services\\"),
		strings.Contains(p, "/lib/"), strings.Contains(p, "\\lib\\"),
		strings.Contains(p, "/repository/"), strings.Contains(p, "/repos/"),
		strings.Contains(p, "/hooks/"), strings.Contains(p, "/hook/"),
		strings.Contains(p, "composables/"):
		return "dependency"
	default:
		return "implementation"
	}
}
