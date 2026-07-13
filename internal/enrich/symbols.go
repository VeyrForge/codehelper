package enrich

import (
	"context"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// SymbolsFromStore loads all plausible symbols from the indexed graph for an
// enrichment pass. Parser noise (tuple names, empty identifiers) is skipped so
// the model is not wasted on junk entries.
func SymbolsFromStore(ctx context.Context, st *graph.Store, repoID string) ([]types.Symbol, error) {
	pathsSet, err := st.AllSymbolPaths(ctx, repoID)
	if err != nil {
		return nil, err
	}
	var out []types.Symbol
	for path := range pathsSet {
		syms, err := st.SymbolsForPath(ctx, repoID, path)
		if err != nil {
			return nil, err
		}
		for _, s := range syms {
			if plausibleSymbolName(s.Name) {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func plausibleSymbolName(n string) bool {
	n = strings.TrimSpace(n)
	if n == "" {
		return false
	}
	return !strings.ContainsAny(n, ", \t\n")
}
