package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestReviewDiffFallsBackOnShallowClone verifies HEAD~1 failure (depth-1 clone)
// retries against HEAD so MCP review_diff works without an explicit base=.
func TestReviewDiffFallsBackOnShallowClone(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.go")
	run("commit", "-m", "init")
	// Dirty working tree so HEAD diff is non-empty.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc X() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ReviewDiff(context.Background(), nil, DiffRequest{
		RepoRoot: dir,
		RepoName: "shallow",
		Base:     "HEAD~1",
	})
	if err != nil {
		t.Fatalf("ReviewDiff: %v", err)
	}
	if res == nil || res.Summary == "" {
		t.Fatalf("expected review result, got %+v", res)
	}
}
