package indexer

import (
	"context"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// InvalidationMode controls how far transitive invalidation expands.
type InvalidationMode string

const (
	InvalidationLazy  InvalidationMode = "lazy"
	InvalidationEager InvalidationMode = "eager"
)

// ExpandInvalidationPaths adds dependent files via reverse imports/calls edges.
func ExpandInvalidationPaths(ctx context.Context, st *graph.Store, repoID string, seedPaths []string, mode InvalidationMode, budget int) ([]string, error) {
	if len(seedPaths) == 0 || st == nil {
		return nil, nil
	}
	if budget <= 0 {
		if mode == InvalidationLazy {
			budget = 80
		} else {
			budget = 400
		}
	}
	seenPath := map[string]struct{}{}
	for _, p := range seedPaths {
		seenPath[p] = struct{}{}
	}
	queue := []string{}
	visitedSym := map[string]struct{}{}

	for _, p := range seedPaths {
		syms, err := st.SymbolsForPath(ctx, repoID, p)
		if err != nil {
			return nil, err
		}
		for _, s := range syms {
			if _, ok := visitedSym[s.ID]; ok {
				continue
			}
			visitedSym[s.ID] = struct{}{}
			queue = append(queue, s.ID)
		}
	}

	steps := 0
	for len(queue) > 0 && steps < budget {
		id := queue[0]
		queue = queue[1:]
		steps++
		refs, err := st.EdgesTo(ctx, repoID, id, string(types.RefKindImports), string(types.RefKindCalls))
		if err != nil {
			return nil, err
		}
		for _, e := range refs {
			src := e.SourceID
			if src == "" {
				continue
			}
			if _, ok := visitedSym[src]; ok {
				continue
			}
			visitedSym[src] = struct{}{}
			queue = append(queue, src)
			sym, err := st.SymbolByID(ctx, repoID, src)
			if err != nil || sym == nil {
				continue
			}
			if sym.Path == "" {
				continue
			}
			if _, ok := seenPath[sym.Path]; !ok {
				seenPath[sym.Path] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(seenPath))
	for p := range seenPath {
		out = append(out, p)
	}
	return out, nil
}
