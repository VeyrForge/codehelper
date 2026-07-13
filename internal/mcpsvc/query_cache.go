package mcpsvc

import (
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/retrieval"
)

// queryHitsCache memoizes the expensive QueryHybridWithOptions retrieval (the
// ~100ms+ cost per query) for a brief window. Agents frequently re-issue the same
// query within a session; this turns the repeat into a map lookup.
//
// Correctness: the cache key folds in the index identity (indexed commit + build
// time) so any reindex invalidates it, and a short TTL bounds how long an
// uncommitted edit (which does NOT bump indexed_at) can serve a slightly old
// result. Only the deterministic hit slice is cached — freshness, context-pack,
// and the rendered view are recomputed on every call, so the freshness/index_lag
// signal the agent sees is always current.
type queryHitsCache struct {
	mu      sync.Mutex
	entries map[string]queryHitsEntry
}

type queryHitsEntry struct {
	hits []retrieval.RankedSymbol
	at   time.Time
}

const (
	queryCacheTTL     = 15 * time.Second
	queryCacheMaxSize = 256
)

var globalQueryHitsCache = &queryHitsCache{entries: make(map[string]queryHitsEntry)}

// get returns cached hits for key when present and within TTL.
func (c *queryHitsCache) get(key string) ([]retrieval.RankedSymbol, bool) {
	if key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > queryCacheTTL {
		delete(c.entries, key)
		return nil, false
	}
	return e.hits, true
}

// put stores hits under key, evicting wholesale if the cache has grown too large
// (a deliberately simple bound — entries are cheap to rebuild on the next miss).
func (c *queryHitsCache) put(key string, hits []retrieval.RankedSymbol) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= queryCacheMaxSize {
		c.entries = make(map[string]queryHitsEntry, queryCacheMaxSize)
	}
	c.entries[key] = queryHitsEntry{hits: hits, at: time.Now()}
}
