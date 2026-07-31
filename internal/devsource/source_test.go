package devsource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSourceTree(t *testing.T) {
	dir := t.TempDir()
	if IsSourceTree(dir) {
		t.Fatal("empty dir should not be source")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsSourceTree(dir) {
		t.Fatal("wrong module should not match")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/VeyrForge/codehelper\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsSourceTree(dir) {
		t.Fatal("missing cmd/codehelper should not match")
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsSourceTree(dir) {
		t.Fatal("expected source tree")
	}
}

func TestRememberAndLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/foo/codehelper\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Remember(dir); err != nil {
		t.Fatal(err)
	}
	got := LoadRemembered()
	abs, _ := filepath.Abs(dir)
	if got != abs {
		t.Fatalf("LoadRemembered = %q, want %q", got, abs)
	}
}

func TestResolve_envAndExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module github.com/x/codehelper\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "cmd", "codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvSource, src)
	got, err := Resolve("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(src)
	if got != abs {
		t.Fatalf("Resolve env = %q, want %q", got, abs)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module github.com/y/codehelper\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(other, "cmd", "codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = Resolve(other, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oabs, _ := filepath.Abs(other)
	if got != oabs {
		t.Fatalf("Resolve explicit = %q, want %q", got, oabs)
	}
}

func TestResolve_errorWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvSource, "")
	_, err := Resolve("", t.TempDir())
	if err == nil {
		t.Fatal("expected error when no source found")
	}
}
