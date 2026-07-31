package graph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// planFor returns the edges-table access line of a query's plan.
func planFor(t *testing.T, st *Store, query string, args ...any) string {
	t.Helper()
	rows, err := st.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		// The edges table is aliased "e" in the structural queries.
		if strings.Contains(detail, " e ") || strings.HasSuffix(detail, " e") {
			return detail
		}
	}
	return ""
}

// TestStructuralQueriesUseSelectiveIndex guards the ANALYZE performance fix on the
// LIVE index: callers/callees must reach their edges through the selective
// src_id/dst_id index, never the low-selectivity kind index (which scans every
// call edge) or a full table SCAN. If ANALYZE stops running at index time, or the
// edge indexes change, this regresses and fails. Skipped when no index is present.
func TestStructuralQueriesUseSelectiveIndex(t *testing.T) {
	root, _ := filepath.Abs("../..")
	if _, err := os.Stat(paths.DBPath(root)); err != nil {
		t.Skip("no workspace index (.codehelper/graph.db) — skipping live query-plan guard")
	}
	st, err := Open(paths.DBPath(root))
	if err != nil {
		t.Skipf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if !st.HasStats(context.Background()) {
		t.Skip("index has no ANALYZE stats yet (re-index to populate) — skipping")
	}

	const hub = "sym:codehelper:internal/graph/store.go:53:Close"
	callersPlan := planFor(t, st,
		`SELECT s.id FROM edges e JOIN symbols s ON s.id=e.src_id AND s.repo_id=e.repo_id WHERE e.repo_id=? AND e.dst_id=? AND e.kind=?`,
		"codehelper", hub, "calls")
	if !strings.Contains(callersPlan, "idx_edges_dst") {
		t.Errorf("CallersOf must use idx_edges_dst (selective), got: %q", callersPlan)
	}
	if strings.Contains(callersPlan, "idx_edges_kind") || strings.HasPrefix(callersPlan, "SCAN") {
		t.Errorf("CallersOf regressed to scanning all call edges: %q", callersPlan)
	}

	calleesPlan := planFor(t, st,
		`SELECT s.id FROM edges e JOIN symbols s ON s.id=e.dst_id AND s.repo_id=e.repo_id WHERE e.repo_id=? AND e.src_id=? AND e.kind=?`,
		"codehelper", hub, "calls")
	if !strings.Contains(calleesPlan, "idx_edges_src") {
		t.Errorf("CalleesOf must use idx_edges_src (selective), got: %q", calleesPlan)
	}
}
