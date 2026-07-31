package ops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectEnv_FindsGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := DetectEnv(root)
	if err != nil {
		t.Fatal(err)
	}
	if out.Toolchain["go"] != "1.22" {
		t.Fatalf("go version=%q want 1.22", out.Toolchain["go"])
	}
}
