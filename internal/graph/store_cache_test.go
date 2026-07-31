package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newTempDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "graph.db")
	t.Cleanup(func() { _ = CloseCached(db) })
	return db
}

func TestOpenCached_ReusesHandle(t *testing.T) {
	db := newTempDB(t)
	a, err := OpenCached(db)
	if err != nil {
		t.Fatalf("OpenCached: %v", err)
	}
	b, err := OpenCached(db)
	if err != nil {
		t.Fatalf("OpenCached: %v", err)
	}
	if a != b {
		t.Fatal("expected the same cached *Store for the same unchanged DB file")
	}
	if !a.shared {
		t.Fatal("cached store must be marked shared")
	}
}

func TestOpenCached_CloseIsNoOp(t *testing.T) {
	db := newTempDB(t)
	s, err := OpenCached(db)
	if err != nil {
		t.Fatalf("OpenCached: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("shared Close should be a no-op, got %v", err)
	}
	// Still usable after a caller's defer Close(): a query must succeed.
	if _, err := s.db.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("shared store closed by a no-op Close: %v", err)
	}
}

func TestOpenCached_ReopensOnFileReplace(t *testing.T) {
	db := newTempDB(t)
	first, err := OpenCached(db)
	if err != nil {
		t.Fatalf("OpenCached: %v", err)
	}
	// Drop the cached handle so Windows can replace the file (full rebuild).
	if err := CloseCached(db); err != nil {
		t.Fatalf("CloseCached: %v", err)
	}
	if err := os.Remove(db); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Materialize a real sqlite file (not raw bytes) so OpenCached can reopen.
	fresh, err := Open(db)
	if err != nil {
		t.Fatalf("Open replacement db: %v", err)
	}
	_ = fresh.Close()
	second, err := OpenCached(db)
	if err != nil {
		t.Fatalf("OpenCached after replace: %v", err)
	}
	if first == second {
		t.Fatal("expected a fresh *Store after the DB file was replaced")
	}
}

func TestCloseCached_ReleasesFile(t *testing.T) {
	db := newTempDB(t)
	if _, err := OpenCached(db); err != nil {
		t.Fatalf("OpenCached: %v", err)
	}
	if err := CloseCached(db); err != nil {
		t.Fatalf("CloseCached: %v", err)
	}
	if err := os.Remove(db); err != nil {
		t.Fatalf("expected unlink after CloseCached: %v", err)
	}
}

// BenchmarkOpen measures the per-call cost that caching removes; BenchmarkOpenCached
// is the hit path. The gap is the latency saved on every tool call.
func BenchmarkOpen(b *testing.B) {
	db := filepath.Join(b.TempDir(), "graph.db")
	if s, err := Open(db); err != nil { // materialize schema once
		b.Fatal(err)
	} else {
		_ = s.Close()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := Open(db)
		if err != nil {
			b.Fatal(err)
		}
		_ = s.closeReal()
	}
}

func BenchmarkOpenCached(b *testing.B) {
	db := filepath.Join(b.TempDir(), "graph.db")
	if _, err := OpenCached(db); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := OpenCached(db); err != nil {
			b.Fatal(err)
		}
	}
}
