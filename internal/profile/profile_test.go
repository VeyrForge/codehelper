package profile

import (
	"os"
	"path/filepath"
	"testing"
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
