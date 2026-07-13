package mcpimpact

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// Analyze walks call graph up to maxDepth from target symbol id or name-resolved id.
func Analyze(ctx context.Context, st *graph.Store, repoID, target string, maxDepth int, direction string) (*types.ImpactResult, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	sym, err := resolveSymbol(ctx, st, repoID, target)
	if err != nil || sym == nil {
		return nil, fmt.Errorf("symbol not found: %s", target)
	}
	seen := map[string]int{}
	var queue []struct {
		id    string
		depth int
	}
	queue = append(queue, struct {
		id    string
		depth int
	}{sym.ID, 0})
	seen[sym.ID] = 0
	var nodes []types.ImpactNode
	nodes = append(nodes, types.ImpactNode{
		SymbolID: sym.ID, Name: sym.Name, Path: sym.Path, Depth: 0, Confidence: 1, Kind: string(sym.Kind),
	})
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		// One indexed JOIN per BFS node for its neighbor SYMBOLS, not EdgesTo/
		// EdgesFrom + a SymbolByID per edge (an N+1 that fired a query per neighbor —
		// tens of thousands for a hub in the blast radius).
		incoming := direction == "upstream" || direction == "callers"
		neighbors, err := st.Neighbors(ctx, repoID, cur.id, incoming, string(types.RefKindCalls), string(types.RefKindReads), string(types.RefKindImports))
		if err != nil {
			return nil, err
		}
		for _, nb := range neighbors {
			next := nb.Symbol.ID
			if _, ok := seen[next]; ok {
				continue
			}
			seen[next] = cur.depth + 1
			queue = append(queue, struct {
				id    string
				depth int
			}{next, cur.depth + 1})
			nodes = append(nodes, types.ImpactNode{
				SymbolID:   nb.Symbol.ID,
				Name:       nb.Symbol.Name,
				Path:       nb.Symbol.Path,
				Depth:      cur.depth + 1,
				Confidence: nb.Confidence,
				Kind:       string(nb.Symbol.Kind),
			})
		}
	}
	risk := "low"
	if len(nodes) > 10 {
		risk = "medium"
	}
	if len(nodes) > 40 {
		risk = "high"
	}
	candidates := buildMustUpdateCandidates(nodes)
	return &types.ImpactResult{
		Target:               target,
		Direction:            direction,
		Nodes:                nodes,
		MustUpdateCandidates: candidates,
		RiskTier:             risk,
	}, nil
}

func resolveSymbol(ctx context.Context, st *graph.Store, repoID, target string) (*types.Symbol, error) {
	if strings.HasPrefix(target, "sym:") {
		return st.SymbolByID(ctx, repoID, target)
	}
	syms, err := st.SymbolsByName(ctx, repoID, target, 1)
	if err != nil || len(syms) == 0 {
		return nil, err
	}
	return &syms[0], nil
}

func buildMustUpdateCandidates(nodes []types.ImpactNode) []types.ImpactNode {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]types.ImpactNode, 0, 8)
	for _, n := range nodes {
		if n.Depth == 0 {
			continue
		}
		if n.Depth > 1 {
			continue
		}
		switch n.Kind {
		case "function", "method", "class", "variable":
			out = append(out, n)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}
