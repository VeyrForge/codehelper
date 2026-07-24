package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodehelperGitignored_noGitignore(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	added, err := EnsureCodehelperGitignored(dir)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("expected no write when .gitignore missing")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Fatal("should not create .gitignore")
	}
}

func TestEnsureCodehelperGitignored_appendsOnce(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureCodehelperGitignored(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("expected append")
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	got := string(data)
	for _, want := range []string{".codehelper/", "CLAUDE.md", ".cursor/", ".claude/"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing ignore line %q in: %q", want, got)
		}
	}
	if !strings.Contains(got, codehelperIgnoreBanner) {
		t.Fatalf("missing banner in: %q", got)
	}
	added2, err := EnsureCodehelperGitignored(dir)
	if err != nil {
		t.Fatal(err)
	}
	if added2 {
		t.Fatal("expected idempotent second call")
	}
}

func TestEnsureCodehelperGitignored_respectsExisting(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	existing := ".codehelper/\nCLAUDE.md\n.cursor/\n.claude/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureCodehelperGitignored(dir)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("expected no-op when already ignored")
	}
}

func TestEnsureCodehelperGitignored_fillsPartial(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".codehelper/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureCodehelperGitignored(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("expected append of remaining entries")
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	got := string(data)
	if !strings.Contains(got, "CLAUDE.md") || !strings.Contains(got, ".cursor/") || !strings.Contains(got, ".claude/") {
		t.Fatalf("expected remaining ignores, got: %q", got)
	}
	// Must not duplicate .codehelper/
	if strings.Count(got, ".codehelper/") != 1 {
		t.Fatalf("duplicated .codehelper/: %q", got)
	}
}

func TestCodehelperAlreadyIgnored_variants(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{".codehelper/\n", true},
		{".codehelper\n", true},
		{"**/.codehelper/\n", true},
		{"node_modules/\n", false},
	}
	for _, tc := range cases {
		if got := codehelperAlreadyIgnored(tc.content); got != tc.want {
			t.Errorf("codehelperAlreadyIgnored(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

func TestMissingCodehelperIgnores(t *testing.T) {
	missing := missingCodehelperIgnores("node_modules/\n")
	if len(missing) != len(codehelperIgnoreEntries) {
		t.Fatalf("want %d missing, got %v", len(codehelperIgnoreEntries), missing)
	}
	if got := missingCodehelperIgnores(".codehelper/\nCLAUDE.md\n.cursor/\n.claude/\n"); len(got) != 0 {
		t.Fatalf("want none missing, got %v", got)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
