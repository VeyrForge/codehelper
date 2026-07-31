package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveWalkRoot_WindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(target, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	got := ResolveWalkRoot(link)
	if got != target {
		t.Fatalf("ResolveWalkRoot(%q)=%q want %q", link, got, target)
	}
	// Plain dirs are unchanged.
	if got := ResolveWalkRoot(target); got != target {
		t.Fatalf("plain dir changed: %q → %q", target, got)
	}
}
