package detect

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/graph"
)

// ChangedSymbols returns symbol ids in files touched by git diff vs baseRef.
func ChangedSymbols(ctx context.Context, root, repoID, baseRef string, st *graph.Store) ([]string, error) {
	lines, err := gitutil.DiffAgainst(root, baseRef)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]struct{}{}
	for _, line := range lines {
		p := filepath.ToSlash(strings.TrimSpace(line))
		if p == "" {
			continue
		}
		syms, err := st.SymbolsForPath(ctx, repoID, p)
		if err != nil {
			continue
		}
		for _, s := range syms {
			if _, ok := seen[s.ID]; ok {
				continue
			}
			seen[s.ID] = struct{}{}
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

// ChangedSymbolSet returns changed symbol ids as a set for ranking signals.
func ChangedSymbolSet(ctx context.Context, root, repoID, baseRef string, st *graph.Store) (map[string]struct{}, error) {
	ids, err := ChangedSymbols(ctx, root, repoID, baseRef, st)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}
