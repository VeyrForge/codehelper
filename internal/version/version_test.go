package version

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReadWriteVERSION(t *testing.T) {
	dir := t.TempDir()
	if err := WriteToDir(dir, "3.0.0"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "3.0.0" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteToDir_rejectsInvalid(t *testing.T) {
	if err := WriteToDir(t.TempDir(), ""); err == nil {
		t.Fatal("expected error for empty version")
	}
	if err := WriteToDir(t.TempDir(), "1.0 beta"); err == nil {
		t.Fatal("expected error for invalid version")
	}
}

func TestLdflagsX_readsFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteToDir(dir, "2.4.2"); err != nil {
		t.Fatal(err)
	}
	flags, err := LdflagsX(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=2.4.2"
	if flags != want {
		t.Fatalf("got %q want %q", flags, want)
	}
}

func TestCurrent_findsVERSIONInParent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteToDir(root, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	linkVersion = ""
	once = sync.Once{}
	resolved = ""
	if got := findVERSIONFile(); got != "9.9.9" {
		t.Fatalf("findVERSIONFile: %q", got)
	}
}
