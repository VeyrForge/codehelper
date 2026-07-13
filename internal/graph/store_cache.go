package graph

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Process-wide read cache for graph stores.
//
// The MCP server is a long-lived process that answers many tool calls against
// the same graph.db. Opening a fresh SQLite connection per call (sql.Open +
// pragmas + schema check) costs several ms every time; over a session that
// dominates the latency of otherwise-2–3ms queries. OpenCached keeps one
// read-configured Store per DB path alive for the process and hands it to every
// caller, so the open cost is paid once.
//
// Correctness across re-index: the tools only READ the graph; the indexer writes
// it from a separate connection. In WAL mode a live reader sees the latest
// committed rows on each new query, so incremental writes need no reopen. A full
// rebuild that REPLACES the file (new inode) is caught by the file token
// (mtime+size): on the next OpenCached the cache opens the new file and retires
// the old handle after a grace delay so any in-flight query finishes first.

const (
	// cachedReadConns lets concurrent tool calls read in parallel (WAL permits
	// multiple readers) instead of serializing on a single shared connection.
	cachedReadConns = 4
	// retireDelay is how long a superseded handle lingers before close, comfortably
	// longer than any tool call, so a concurrent caller can't hit a closed DB.
	retireDelay = 30 * time.Second
)

type storeCacheEntry struct {
	store *Store
	token string // file identity (mtime+size); "" or "missing" force a reopen
}

var (
	storeCacheMu sync.Mutex
	storeCache   = map[string]*storeCacheEntry{}
)

// OpenCached returns a process-cached, read-configured Store for dbPath, opening
// it only on first use or after the DB file is replaced. The returned Store is
// shared: its Close() is a no-op. Safe for concurrent use.
func OpenCached(dbPath string) (*Store, error) {
	tok := fileToken(dbPath)

	storeCacheMu.Lock()
	defer storeCacheMu.Unlock()

	if e, ok := storeCache[dbPath]; ok && e.token == tok && e.store != nil {
		return e.store, nil
	}

	s, err := openWithConns(dbPath, cachedReadConns, true)
	if err != nil {
		return nil, err
	}
	if old, ok := storeCache[dbPath]; ok && old.store != nil && old.store != s {
		retireStore(old.store)
	}
	// Re-read the token AFTER opening: creating a fresh DB (InitSQL) changes the
	// file, so a token captured before the open would never match the next call
	// and every call would miss. Post-open the file is stable for read-only use.
	storeCache[dbPath] = &storeCacheEntry{store: s, token: fileToken(dbPath)}
	return s, nil
}

// retireStore closes a superseded cached handle after a grace delay so any
// concurrent in-flight query on it completes first.
func retireStore(s *Store) {
	time.AfterFunc(retireDelay, func() { _ = s.closeReal() })
}

// fileToken identifies a DB file by mtime+size so a full-rebuild file swap forces
// a reopen. A missing file returns a sentinel so the entry re-resolves once it
// appears. WAL writes to the -wal sidecar don't change the main file token, which
// is correct: a live reader already sees committed WAL rows without reopening.
func fileToken(dbPath string) string {
	fi, err := os.Stat(dbPath)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
}
