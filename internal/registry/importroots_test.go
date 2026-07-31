package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectImportRoots_GoAndNPM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/acme/core\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@acme/web"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DetectImportRoots(dir)
	if len(got) != 2 {
		t.Fatalf("DetectImportRoots=%v", got)
	}
	if got[0] != "github.com/acme/core" || got[1] != "@acme/web" {
		t.Fatalf("unexpected roots: %v", got)
	}
}

func TestUpsert_PreservesGroupIDsAndDetectsRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/svc\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Registry{Entries: map[string]Entry{
		"svc": {Name: "svc", RootPath: dir, GroupIDs: []string{"platform"}},
	}}
	if err := r.Upsert("svc", dir, "abc", 2); err != nil {
		t.Fatal(err)
	}
	e, ok := r.Get("svc")
	if !ok {
		t.Fatal("missing entry")
	}
	if len(e.GroupIDs) != 1 || e.GroupIDs[0] != "platform" {
		t.Fatalf("GroupIDs=%v", e.GroupIDs)
	}
	if len(e.ImportRoots) != 1 || e.ImportRoots[0] != "example.com/svc" {
		t.Fatalf("ImportRoots=%v", e.ImportRoots)
	}
}
