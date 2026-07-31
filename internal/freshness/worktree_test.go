package freshness

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Without ResolveWalkRoot, WalkDir on a junction root never sees newer sources.
func TestWorkingTreeChangedSince_WindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	target := filepath.Join(base, "proj")
	link := filepath.Join(base, "bed")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(target, "main.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}
	if !WorkingTreeChangedSince(link, past) {
		t.Fatal("junction worktree scan missed newer source")
	}
	if WorkingTreeChangedSince(link, future.Add(time.Minute)) {
		t.Fatal("expected no change when since is after mtime")
	}
}
