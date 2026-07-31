package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
)

// TestIncrementalReindexPreservesCallers is the end-to-end guard for the
// incremental-resolution fix: after editing a file so its symbol line numbers
// shift, an incremental re-index (driven by a new commit) must keep the
// cross-file caller edge — not orphan it. Before the fix this dropped the edge.
func TestIncrementalReindexPreservesCallers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q")
	write("caller.go", "package x\n\nfunc run() { Helper() }\n")
	write("target.go", "package x\n\nfunc Helper() {}\n")
	// Filler files so that changing one file stays under fullReindexRatio (0.5)
	// and actually exercises the INCREMENTAL path rather than a full re-index.
	for _, n := range []string{"f1", "f2", "f3", "f4", "f5", "f6"} {
		write(n+".go", "package x\n\nfunc "+n+"() {}\n")
	}
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	ctx := context.Background()
	if err := Run(ctx, dir, Options{}); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	repoID := mustRepoID(t, dir)
	helperCallersAfterIndex := callerCount(t, dir, repoID, "Helper")
	if helperCallersAfterIndex != 1 {
		t.Fatalf("after full index: Helper callers=%d want 1", helperCallersAfterIndex)
	}

	// Edit target.go so Helper moves to a new line (new symbol ID), then commit so
	// the incremental path engages.
	write("target.go", "package x\n\n// a new line that shifts Helper down\n// and another\nfunc Helper() {}\n")
	git("commit", "-qa", "-m", "c2")

	if err := Run(ctx, dir, Options{}); err != nil {
		t.Fatalf("incremental index: %v", err)
	}
	if got := callerCount(t, dir, repoID, "Helper"); got != 1 {
		t.Fatalf("after incremental re-index: Helper callers=%d want 1 (caller edge was orphaned)", got)
	}
}

func mustRepoID(t *testing.T, dir string) string {
	t.Helper()
	m, err := meta.Read(dir)
	if err != nil || m == nil {
		t.Fatalf("meta.Read: %v", err)
	}
	return m.RepoName
}

// callerCount returns how many resolved `calls` edges point at any symbol named
// `name` in the indexed graph.
func callerCount(t *testing.T, dir, repoID, name string) int {
	t.Helper()
	st, err := graph.Open(paths.DBPath(dir))
	if err != nil {
		t.Fatalf("graph.Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	var n int
	err = st.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM edges e
		JOIN symbols s ON s.id = e.dst_id AND s.repo_id = e.repo_id
		WHERE e.repo_id=? AND e.kind='calls' AND s.name=?`, repoID, name).Scan(&n)
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}
