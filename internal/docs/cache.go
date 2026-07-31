package docs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// cacheEntry is a stored docs lookup result with a fetch timestamp.
type cacheEntry struct {
	StoredAt time.Time `json:"stored_at"`
	Result   *Result   `json:"result"`
}

// Cache persists fetched docs under <repoIndexDir>/docs-cache keyed by a hash
// of (library, version, topic, source set). Version-keyed so upgrading a
// dependency naturally invalidates stale docs.
type Cache struct {
	dir string
	ttl time.Duration
}

// NewCache returns a Cache rooted at dir with the given TTL (<=0 disables
// expiry). dir is created lazily on write.
func NewCache(dir string, ttl time.Duration) *Cache {
	return &Cache{dir: dir, ttl: ttl}
}

func (c *Cache) key(lib, version, topic string) string {
	h := sha256.Sum256([]byte(lib + "@" + version + "::" + topic))
	return hex.EncodeToString(h[:16])
}

func (c *Cache) path(lib, version, topic string) string {
	return filepath.Join(c.dir, c.key(lib, version, topic)+".json")
}

// Get returns a cached result if present and not expired. now is the engine's
// clock (injectable for tests).
func (c *Cache) Get(lib, version, topic string, now time.Time) (*Result, bool) {
	if c == nil || c.dir == "" {
		return nil, false
	}
	b, err := os.ReadFile(c.path(lib, version, topic))
	if err != nil {
		return nil, false
	}
	var e cacheEntry
	if json.Unmarshal(b, &e) != nil || e.Result == nil {
		return nil, false
	}
	if c.ttl > 0 && now.Sub(e.StoredAt) > c.ttl {
		return nil, false
	}
	e.Result.FromCache = true
	return e.Result, true
}

// Put stores a result. Errors are non-fatal (cache is best-effort).
func (c *Cache) Put(lib, version, topic string, r *Result, now time.Time) {
	if c == nil || c.dir == "" || r == nil {
		return
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(cacheEntry{StoredAt: now, Result: r}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(c.path(lib, version, topic), b, 0o644)
}
