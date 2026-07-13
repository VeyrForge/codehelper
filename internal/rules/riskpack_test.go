package rules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstalledPacks_Empty(t *testing.T) {
	dir := t.TempDir()
	packs, flat, err := LoadInstalledPacks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 || len(flat) != 0 {
		t.Fatalf("expected empty, got %d %d", len(packs), len(flat))
	}
}

func TestLoadInstalledPacks_File(t *testing.T) {
	dir := t.TempDir()
	ch := filepath.Join(dir, ".codehelper")
	if err := os.MkdirAll(ch, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"name":"t","risk_patterns":[{"id":"x","match":"bad","severity":"high"}]}`
	if err := os.WriteFile(filepath.Join(ch, "rules-test.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, flat, err := LoadInstalledPacks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != 1 || flat[0].ID != "x" {
		t.Fatalf("got %#v", flat)
	}
}
