package mcpsvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Without ResolveWalkRoot, WalkDir on a junction root returns 0 textual sites.
func TestScanTextualOccurrences_WindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	target := filepath.Join(base, "proj")
	link := filepath.Join(base, "bed")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "package x\n\n// OldName lives here\nconst note = \"OldName\"\n"
	if err := os.WriteFile(filepath.Join(target, "notes.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	files, warn := scanTextualOccurrences(link, "OldName", "NewName", nil)
	if warn != "" {
		t.Fatalf("unexpected warn: %s", warn)
	}
	if len(files) == 0 || len(files[0].Sites) < 2 {
		t.Fatalf("junction textual scan missed sites: files=%#v", files)
	}
	if files[0].Path != "notes.go" {
		t.Fatalf("path=%q want notes.go", files[0].Path)
	}
}
