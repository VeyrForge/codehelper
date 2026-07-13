package graph

import (
	"context"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// UnreferencedSymbols returns symbols of the given kinds that have no inbound
// `calls` or `reads` edge in the resolved graph — i.e. nothing the indexer could
// resolve points at them. This is the raw signal behind the dead_code tool.
//
// It is intentionally a candidate list, not a verdict: the call graph is
// incomplete (dynamic dispatch, reflection, build-tagged code, route/handler
// registration, and cross-repo callers are not all resolved), so a symbol with
// no inbound edge is "unreferenced in the index", which over-approximates dead
// code. Callers apply name/visibility heuristics on top and must verify before
// deleting. `contains` and `imports` edges are deliberately excluded: every
// symbol is `contains`-targeted by its parent, and `imports` target module ids.
func (s *Store) UnreferencedSymbols(ctx context.Context, repoID string, kinds []string) ([]types.Symbol, error) {
	if len(kinds) == 0 {
		kinds = []string{string(types.SymbolKindFunction), string(types.SymbolKindMethod)}
	}
	ph := placeholders(len(kinds))
	args := []interface{}{repoID}
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, repoID)
	q := `
SELECT s.id, s.repo_id, s.name, s.kind, s.path, s.line_start, s.line_end, s.language, COALESCE(s.signature,''), COALESCE(s.parent_id,'')
FROM symbols s
WHERE s.repo_id=? AND s.kind IN (` + ph + `)
  AND NOT EXISTS (
    SELECT 1 FROM edges e
    WHERE e.repo_id=? AND e.dst_id=s.id AND e.kind IN ('calls','reads')
  )
ORDER BY s.path, s.line_start, s.name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}
