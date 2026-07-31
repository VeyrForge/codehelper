package review

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasSiblingTestFile_Go(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasSiblingTestFile(dir, "foo.go") {
		t.Fatalf("expected sibling test detected for foo.go")
	}
}

func TestHasSiblingTestFile_GoPackageLevel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasSiblingTestFile(dir, "foo.go") {
		t.Fatalf("expected package-level test to count for foo.go")
	}
}

func TestHasSiblingTestFile_TSDirect(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Widget.ts"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Widget.test.ts"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasSiblingTestFile(dir, "Widget.ts") {
		t.Fatalf("expected Widget.test.ts to be detected")
	}
}

func TestHasSiblingTestFile_NoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if HasSiblingTestFile(dir, "foo.go") {
		t.Fatalf("expected no sibling tests")
	}
}
