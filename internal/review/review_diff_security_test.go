package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReviewDiff_SecuritySmellsFromUnifiedDiff(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "t@example.com")
	run("git", "config", "user.name", "t")
	mustWrite(t, filepath.Join(dir, "ok.go"), "package main\n\nfunc safe() {}\n")
	run("git", "add", ".")
	run("git", "commit", "-m", "base")

	mustWrite(t, filepath.Join(dir, "auth.go"), "package main\n\nfunc bad() {\n\tapi_key := \"sk_live_abcdefghijklmnopqrstuv\"\n\t_ = api_key\n}\n")
	run("git", "add", ".")
	// uncommitted staged vs HEAD is fine for UnifiedDiff; commit so DiffAgainst sees it
	run("git", "commit", "-m", "bad")

	res, err := ReviewDiff(context.Background(), nil, DiffRequest{
		RepoRoot:        dir,
		RepoName:        "t",
		Base:            "HEAD~1",
		SeverityFloor:   SeverityLow,
		IncludeSecurity: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range res.Findings {
		if f.Category == "security" && f.File == "auth.go" && f.Line > 0 && len(f.Evidence) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected line-level security smell for hard-coded secret, got %+v", res.Findings)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
