package retrieval

import (
	"context"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/parser"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// GraphExpandOptions bounds 1–2 hop expansion from lexical seeds.
type GraphExpandOptions struct {
	// MaxHops is 1 or 2 (default 2). Values ≤0 become 2; >2 are capped at 2.
	MaxHops int
	// MaxSeeds is how many top lexical hits seed the walk (default 12).
	MaxSeeds int
	// MaxPerSeed caps neighbors fetched per seed per direction (default 24).
	MaxPerSeed int
	// MaxTotal caps the expanded neighbor list before RRF (default 120).
	MaxTotal int
}

func normalizeGraphExpandOptions(o GraphExpandOptions) GraphExpandOptions {
	if o.MaxHops <= 0 {
		o.MaxHops = 2
	}
	if o.MaxHops > 2 {
		o.MaxHops = 2
	}
	if o.MaxSeeds <= 0 {
		o.MaxSeeds = 12
	}
	if o.MaxPerSeed <= 0 {
		o.MaxPerSeed = 24
	}
	if o.MaxTotal <= 0 {
		o.MaxTotal = 120
	}
	return o
}

// ExpandGraphNeighbors walks 1–2 hops (callers/callees + file imports) from the
// top lexical seeds and returns a ranked list for RRF fusion with BM25/FTS hits.
// Seeds themselves are included at the front so reciprocal-rank fusion reinforces
// lexical winners while still surfacing structurally related symbols that never
// matched the query text.
func ExpandGraphNeighbors(ctx context.Context, st *graph.Store, repoID string, seeds []RankedSymbol, opts GraphExpandOptions) []RankedSymbol {
	if st == nil || len(seeds) == 0 {
		return nil
	}
	opts = normalizeGraphExpandOptions(opts)
	nSeeds := len(seeds)
	if nSeeds > opts.MaxSeeds {
		nSeeds = opts.MaxSeeds
	}

	type scored struct {
		sym    types.Symbol
		score  float64
		reason string
	}
	byID := map[string]scored{}

	add := func(sym types.Symbol, score float64, reason string) {
		if sym.ID == "" || !plausibleSymbolName(sym.Name) {
			return
		}
		cur, ok := byID[sym.ID]
		if !ok || score > cur.score {
			byID[sym.ID] = scored{sym: sym, score: score, reason: reason}
		}
	}

	kinds := []string{string(types.RefKindCalls), string(types.RefKindReads)}
	frontier := make([]RankedSymbol, 0, nSeeds)
	for i := 0; i < nSeeds; i++ {
		s := seeds[i]
		add(s.Symbol, 1.0/(float64(i)+1.0), "graph_seed")
		frontier = append(frontier, s)
	}

	for hop := 1; hop <= opts.MaxHops; hop++ {
		next := make([]RankedSymbol, 0, len(frontier)*2)
		decay := 1.0 / float64(hop+1)
		reason := "graph_hop_1"
		if hop == 2 {
			reason = "graph_hop_2"
		}
		for _, seed := range frontier {
			if seed.Symbol.ID == "" {
				continue
			}
			base := seed.Score
			if base <= 0 {
				base = 1
			}
			for _, incoming := range []bool{true, false} {
				ns, err := st.Neighbors(ctx, repoID, seed.Symbol.ID, incoming, kinds...)
				if err != nil {
					continue
				}
				limit := opts.MaxPerSeed
				if len(ns) > limit {
					ns = ns[:limit]
				}
				for _, n := range ns {
					sc := base * decay * (0.5 + 0.5*n.Confidence)
					add(n.Symbol, sc, reason)
					if hop < opts.MaxHops {
						next = append(next, RankedSymbol{Symbol: n.Symbol, Score: sc})
					}
				}
			}
			// File-level imports: one hop from the seed's defining file.
			if hop == 1 && seed.Symbol.Path != "" {
				fid := parser.FileNodeID(repoID, seed.Symbol.Path)
				if outs, err := st.EdgesFrom(ctx, repoID, fid, string(types.RefKindImports)); err == nil {
					limit := opts.MaxPerSeed
					if len(outs) > limit {
						outs = outs[:limit]
					}
					for _, e := range outs {
						if !strings.HasPrefix(e.TargetID, "sym:") {
							continue
						}
						if sym, err := st.SymbolByID(ctx, repoID, e.TargetID); err == nil && sym != nil {
							add(*sym, base*decay*0.7, "graph_import")
						}
					}
				}
			}
		}
		frontier = next
		if len(byID) >= opts.MaxTotal*2 {
			break
		}
	}

	out := make([]RankedSymbol, 0, len(byID))
	for _, s := range byID {
		out = append(out, RankedSymbol{
			Symbol:  s.sym,
			Score:   s.score,
			Reasons: []string{s.reason},
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return rankedLess(out[i], out[j]) })
	if len(out) > opts.MaxTotal {
		out = out[:opts.MaxTotal]
	}
	return out
}

// FuseRRF merges two ranked lists with reciprocal rank fusion and preserves
// per-list hit reasons (falls back to "rrf" when neither list tagged the id).
func FuseRRF(a, b []RankedSymbol, k int) []RankedSymbol {
	out := RRF(a, b, k)
	for i := range out {
		out[i].Reasons = dedupeReasons(mergeHitReasons(a, b, out[i].Symbol.ID))
	}
	return out
}
