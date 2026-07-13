package indexer

import (
	"context"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
)

// refreshMetaCounts writes meta with live symbol/edge/file counts from the graph store.
func refreshMetaCounts(ctx context.Context, st *graph.Store, indexRoot, repoID string, base *meta.Data) error {
	m := base
	if m == nil {
		m = &meta.Data{RepoName: repoID, RootPath: indexRoot}
	}
	syms, edges, files, err := st.Counts(ctx, repoID)
	if err != nil {
		return err
	}
	m.SymbolCount = syms
	m.EdgeCount = edges
	m.FileCount = files
	return meta.Write(indexRoot, m)
}

// graphSymbolCount returns symbol count for repoID, or 0 if the store cannot be opened.
func graphSymbolCount(indexRoot, repoID string) int {
	st, err := graph.Open(paths.DBPath(indexRoot))
	if err != nil {
		return 0
	}
	defer st.Close()
	n, _, _, err := st.Counts(context.Background(), repoID)
	if err != nil {
		return 0
	}
	return n
}
