package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWalkSourceFilesExcludesIgnored verifies the per-file skip callback keeps
// ignored paths out of the output. (Directory-level pruning is intentionally NOT
// done here via the generic skip — that conflates exclusion with inclusion-style
// filters like ast_query's path_glob; gitignore dir pruning lives in the layered
// matcher / walk callers instead.)
func TestWalkSourceFilesExcludesIgnored(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "keep", "a.go"), "package a")
	mustWrite(t, filepath.Join(root, "skipme", "b.go"), "package b")
	mustWrite(t, filepath.Join(root, "skipme", "deep", "c.go"), "package c")

	skip := func(rel string) bool { return strings.HasPrefix(rel, "skipme") }
	got, err := WalkSourceFiles(root, nil, skip)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "keep/a.go") {
		t.Fatalf("expected keep/a.go, got %v", got)
	}
	if strings.Contains(joined, "skipme") {
		t.Fatalf("ignored file was not excluded: %v", got)
	}
}

// TestWalkSourceFilesSkipDirPrunesSubtree verifies skipDir prunes a whole subtree
// (exclusion), while an inclusion-style filter must NOT be passed as skipDir.
func TestWalkSourceFilesSkipDirPrunesSubtree(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "keep", "a.go"), "package a")
	mustWrite(t, filepath.Join(root, "vendored", "deep", "b.go"), "package b")

	skipDir := func(rel string) bool { return rel == "vendored" || strings.HasPrefix(rel, "vendored/") }
	got, err := WalkSourceFiles(root, skipDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "keep/a.go") || strings.Contains(joined, "vendored") {
		t.Fatalf("skipDir did not prune vendored subtree: %v", got)
	}
}

// TestCollectNestedGitignorePrune verifies the nested-.gitignore collector does
// not descend into pruned directories — so a vendored fixture tree's ignore
// rules never bloat the layered matcher.
func TestCollectNestedGitignorePrune(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".gitignore"), "big/")
	mustWrite(t, filepath.Join(root, "big", ".gitignore"), "*.x")
	mustWrite(t, filepath.Join(root, "big", "sub", ".gitignore"), "*.y")
	mustWrite(t, filepath.Join(root, "keep", ".gitignore"), "*.z")

	prune := func(absDir string) bool { return filepath.Base(absDir) == "big" }
	got, err := collectNestedGitignorePaths(root, prune)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if strings.Contains(filepath.ToSlash(p), "/big/") {
			t.Fatalf("descended into pruned dir: %s", p)
		}
	}
	var sawKeep bool
	for _, p := range got {
		if strings.Contains(filepath.ToSlash(p), "/keep/") {
			sawKeep = true
		}
	}
	if !sawKeep {
		t.Fatalf("expected keep/.gitignore collected, got %v", got)
	}
}

// TestLayeredMatcherIgnoresViaDefaultSkipDirsToo confirms defaultSkipDirs are
// pruned during collection (node_modules etc. never contribute rules).
func TestCollectSkipsDefaultSkipDirs(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "node_modules", "pkg", ".gitignore"), "*.q")
	mustWrite(t, filepath.Join(root, "src", ".gitignore"), "*.q")
	got, err := collectNestedGitignorePaths(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if strings.Contains(filepath.ToSlash(p), "node_modules") {
			t.Fatalf("collected from node_modules: %s", p)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
