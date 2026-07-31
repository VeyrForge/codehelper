package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewDiff_PathHeuristicsDemoted(t *testing.T) {
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

	apiPath := filepath.Join(dir, "internal", "api", "handler.go")
	authPath := filepath.Join(dir, "auth", "login.go")
	if err := os.MkdirAll(filepath.Dir(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, apiPath, "package api\n\nfunc Handle() {}\n")
	mustWrite(t, authPath, "package auth\n\nfunc Login() {}\n")
	run("git", "add", ".")
	run("git", "commit", "-m", "paths")

	res, err := ReviewDiff(context.Background(), nil, DiffRequest{
		RepoRoot:           dir,
		Base:               "HEAD~1",
		SeverityFloor:      SeverityLow,
		IncludeContracts:   true,
		IncludeSecurity:    true,
		IncludePerformance: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundPath := false
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "path-heuristic") {
			foundPath = true
			if f.Severity == SeverityHigh || f.Severity == SeverityCritical {
				t.Fatalf("path-heuristic must not be high/critical: %+v", f)
			}
		}
	}
	if !foundPath {
		t.Fatalf("expected demoted path-heuristic findings, got %+v", res.Findings)
	}
}

func TestReviewDiff_SuppressesPathHeuristicsOnLargeDirtyTree(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			_ = os.Chmod(path, 0o755)
			return nil
		})
		_ = os.RemoveAll(filepath.Join(dir, ".git"))
	})
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

	apiDir := filepath.Join(dir, "api")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pathHeuristicFileCap+3; i++ {
		p := filepath.Join(apiDir, fmt.Sprintf("f%d.go", i))
		mustWrite(t, p, fmt.Sprintf("package api\n\nfunc F%d() {}\n", i))
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "many")

	res, err := ReviewDiff(context.Background(), nil, DiffRequest{
		RepoRoot:           dir,
		Base:               "HEAD~1",
		SeverityFloor:      SeverityLow,
		IncludeContracts:   true,
		IncludeSecurity:    true,
		IncludePerformance: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "path-heuristic") {
			t.Fatalf("path heuristics should be suppressed on large diffs: %+v", f)
		}
	}
	if !strings.Contains(res.Summary, "path-heuristic findings suppressed") {
		t.Fatalf("expected suppression note in summary: %s", res.Summary)
	}
}
