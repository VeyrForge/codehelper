package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidateRefRejectsOptionInjection(t *testing.T) {
	t.Parallel()
	bad := []string{"--output=/tmp/evil", "-O/tmp/x", "--ext-diff", "HEAD\n--output=x", "ref\x00"}
	for _, r := range bad {
		if err := validateRef(r); err == nil {
			t.Errorf("validateRef(%q) = nil, want rejection", r)
		}
	}
	good := []string{"HEAD", "HEAD~1", "main", "origin/main", "v1.2.3", "abc123", "feature/x~2^1"}
	for _, r := range good {
		if err := validateRef(r); err != nil {
			t.Errorf("validateRef(%q) = %v, want ok", r, err)
		}
	}
}

// DiffAgainst must reject an injected ref BEFORE invoking git.
func TestDiffAgainstRejectsInjection(t *testing.T) {
	t.Parallel()
	_, err := DiffAgainst(t.TempDir(), "--output=/tmp/pwned")
	if err == nil {
		t.Fatal("DiffAgainst accepted an option-injection ref")
	}
}

// End-to-end against a real temp git repo: incremental change detection works,
// and a non-git directory degrades cleanly.
func TestGitIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if sha, err := HeadCommit(dir); err != nil || sha != UnbornHEAD {
		t.Fatalf("HeadCommit on unborn repo = %q err=%v, want %s", sha, err, UnbornHEAD)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")

	if !IsGitRepo(dir) {
		t.Error("IsGitRepo=false for an initialized repo")
	}
	if sha, err := HeadCommit(dir); err != nil || len(sha) < 7 {
		t.Errorf("HeadCommit=%q err=%v", sha, err)
	}
	// Modify the tracked file; ChangedFiles (working tree vs HEAD) must see it.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ChangedFiles(dir)
	if err != nil || len(changed) != 1 || changed[0] != "a.txt" {
		t.Errorf("ChangedFiles=%v err=%v want [a.txt]", changed, err)
	}

	// A brand-new untracked file is reported by UntrackedFiles but NOT by the
	// diff-based change detection (which only sees tracked files).
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked, err := UntrackedFiles(dir)
	if err != nil {
		t.Fatalf("UntrackedFiles err=%v", err)
	}
	if len(untracked) != 1 || untracked[0] != "new.go" {
		t.Errorf("UntrackedFiles=%v want [new.go]", untracked)
	}

	// Non-git directory degrades cleanly (no panic, IsGitRepo=false).
	if IsGitRepo(t.TempDir()) {
		t.Error("IsGitRepo=true for a non-git directory")
	}
}
