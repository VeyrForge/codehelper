package enrich

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Store persists per-symbol enrichments as a single JSON map under
// .codehelper/enrich/. It is loaded once, mutated in memory, and flushed atomically
// — small enough for one file at codehelper's symbol scale, and trivial to diff and
// inspect. Concurrency-safe so a parallel enrich pass can share one store.
type Store struct {
	path string
	mu   sync.Mutex
	data map[string]Enrichment
}

// DefaultPath returns the canonical enrichment store location for a repo root.
func DefaultPath(repoRoot string) string {
	return paths.EnrichmentPath(repoRoot)
}

// OpenStore loads the store at path (an empty store if the file is absent). It does
// not create directories until Flush, so a read-only inspection never writes.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]Enrichment{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		// A corrupt store is non-fatal: start fresh rather than block indexing.
		s.data = map[string]Enrichment{}
	}
	return s, nil
}

// Get returns the stored enrichment for a symbol id.
func (s *Store) Get(symbolID string) (Enrichment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[symbolID]
	return e, ok
}

// Put records (or replaces) an enrichment in memory; persisted on Flush.
func (s *Store) Put(e Enrichment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[e.SymbolID] = e
}

// Len reports how many enrichments are held.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data)
}

// Flush writes the store atomically (temp file + rename) so a crash mid-write can
// never leave a torn JSON file.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
