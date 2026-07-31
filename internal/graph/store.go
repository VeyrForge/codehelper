package graph

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Store wraps sqlite graph persistence.
type Store struct {
	db *sql.DB
	// shared marks a Store owned by the process-wide read cache (OpenCached). Its
	// Close() is a no-op so the many `defer st.Close()` call sites in the MCP
	// handlers don't tear down the cached connection; the cache closes it (see
	// closeReal) only when the DB file is replaced.
	shared bool
}

// Open opens or creates the graph database at dbPath with a single connection —
// the exclusive-writer configuration used by the indexer and tests.
func Open(dbPath string) (*Store, error) {
	return openWithConns(dbPath, 1, false)
}

// openWithConns is the shared constructor. maxConns > 1 is safe only for
// read-mostly use: WAL allows concurrent readers, so the cached read Store used
// by MCP tools raises this to serve concurrent tool calls without serializing on
// one connection (the win from caching would otherwise be eaten by contention).
func openWithConns(dbPath string, maxConns int, shared bool) (*Store, error) {
	// WAL + synchronous=NORMAL keeps bulk edge/symbol ingestion fast (the graph
	// is a rebuildable local cache, so durability of the last commit is not
	// critical) and lets the MCP server read while the indexer writes.
	// Read-heavy tuning: a larger page cache (64MB) plus memory-mapped I/O keep hot
	// pages in RAM so structural queries don't round-trip the OS, and an in-memory
	// temp store keeps GROUP BY / ORDER BY (hub aggregations) off disk. These matter
	// most on large repos whose graph.db exceeds the default ~2MB page cache; they're
	// no-ops on small DBs already held in the OS cache.
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)" +
		"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
		"&_pragma=cache_size(-65536)&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	s := &Store{db: db, shared: shared}
	if _, err := s.db.ExecContext(context.Background(), InitSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Analyze refreshes SQLite's table statistics (sqlite_stat1) so the query planner
// picks the SELECTIVE index for structural lookups. Without stats it defaulted to
// the low-selectivity (repo_id, kind) index and scanned every call edge for
// callers/callees/impact — O(all edges), ~19× slower. With stats those become
// sub-ms index lookups. Cheap (sampled); run once per index build.
func (s *Store) Analyze(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "ANALYZE")
	return err
}

// HasStats reports whether ANALYZE has populated sqlite_stat1 — used to backfill
// stats once for indexes built before Analyze ran at index time.
func (s *Store) HasStats(ctx context.Context) bool {
	var n int
	if err := s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'").Scan(&n); err != nil || n == 0 {
		return false
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_stat1").Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// Close releases the connection, EXCEPT for cache-owned (shared) stores, whose
// lifecycle the cache manages — callers may `defer st.Close()` unconditionally.
func (s *Store) Close() error {
	if s == nil || s.db == nil || s.shared {
		return nil
	}
	return s.db.Close()
}

// closeReal unconditionally closes the underlying connection. Only the cache
// calls it, to retire a superseded handle after the DB file was replaced.
func (s *Store) closeReal() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

// ExecCypher runs a restricted subset: only MATCH..RETURN read-only patterns, no mutations.
func (s *Store) ExecCypher(ctx context.Context, repoID, cypher string) (string, error) {
	q := strings.TrimSpace(cypher)
	upper := strings.ToUpper(q)
	if strings.Contains(upper, "DELETE") || strings.Contains(upper, "CREATE") ||
		strings.Contains(upper, "MERGE") || strings.Contains(upper, "SET ") ||
		strings.Contains(upper, "REMOVE") || strings.Contains(upper, "DROP") {
		return "", fmt.Errorf("cypher: only read-only queries allowed")
	}
	// Minimal passthrough: if looks like our internal SQL, reject; else try simple MATCH (n) RETURN
	if strings.HasPrefix(upper, "SELECT") {
		rows, err := s.db.QueryContext(ctx, q)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		return rowsToJSON(rows)
	}
	return "", fmt.Errorf("cypher: translate complex Cypher to supported SQL or use tools query/context")
}

func rowsToJSON(rows *sql.Rows) (string, error) {
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	buf.WriteString("[")
	first := true
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		if !first {
			buf.WriteString(",")
		}
		first = false
		buf.WriteString("{")
		for i, c := range cols {
			if i > 0 {
				buf.WriteString(",")
			}
			fmt.Fprintf(&buf, "%q:", c)
			v := vals[i]
			if v == nil {
				buf.WriteString("null")
			} else {
				fmt.Fprintf(&buf, "%q", fmt.Sprint(v))
			}
		}
		buf.WriteString("}")
	}
	buf.WriteString("]")
	return buf.String(), rows.Err()
}
