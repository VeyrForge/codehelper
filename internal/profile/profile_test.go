package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/VeyrForge/codehelper/internal/paths"
)

func TestGenerate_GoMod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.ProjectType != "go" {
		t.Fatalf("project type: %s", p.ProjectType)
	}
}

func TestGenerate_FollowsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	// Avoid basename "target" — collectLangStats skips build dirs named target.
	realRoot := filepath.Join(base, "proj")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(realRoot, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "go.mod"), []byte("module example.com/junc\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "pkg", "a.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, realRoot)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	if got := paths.ResolveWalkRoot(link); filepath.Clean(got) != filepath.Clean(realRoot) {
		t.Fatalf("ResolveWalkRoot(%q)=%q want %q", link, got, realRoot)
	}
	p, err := Generate(link)
	if err != nil {
		t.Fatal(err)
	}
	if p.ProjectType != "go" {
		t.Fatalf("junction profile project type: %s", p.ProjectType)
	}
	foundGo := false
	for _, s := range p.LanguageStats {
		if s.Language == "go" && s.Bytes > 0 {
			foundGo = true
			break
		}
	}
	if !foundGo {
		t.Fatalf("junction profile missed go language bytes: %+v", p.LanguageStats)
	}
}
