package freshness

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/meta"
)

// makeChangedRepo writes a meta record and a source file whose mtime is AFTER the
// index build, so WorkingTreeChangedSince reports a change.
func makeChangedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := meta.Write(dir, &meta.Data{RepoName: "x"}); err != nil {
		t.Fatal(err)
	}
	// Force the source file newer than the just-written IndexedAt.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInspect_WatchOff_TreeChanged_IsStale(t *testing.T) {
	dir := makeChangedRepo(t)
	r := Inspect(dir)
	if !r.Stale {
		t.Fatalf("watch off + tree changed should be stale, got %#v", r)
	}
	if r.IndexLag != "" {
		t.Fatalf("watch off should not set index_lag, got %q", r.IndexLag)
	}
}

func TestInspect_WatchOn_TreeChanged_SetsIndexLagNotStale(t *testing.T) {
	dir := makeChangedRepo(t)
	// Simulate a live watch daemon by writing a state file with THIS process's PID
	// (guaranteed alive), which daemon.ProcessAlive will confirm.
	if err := os.WriteFile(daemon.StatePath(dir), []byte(fmt.Sprintf(`{"pid": %d}`, os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Inspect(dir)
	if !r.WatchRunning {
		t.Fatalf("expected watch_running, got %#v", r)
	}
	if r.Stale {
		t.Fatalf("watch on should NOT mark stale (it converges), got %#v", r)
	}
	if r.IndexLag != "possible" {
		t.Fatalf("watch on + tree changed should set index_lag=possible, got %q", r.IndexLag)
	}
}
