package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodehelperGitignored_createsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	added, err := EnsureCodehelperGitignored(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("expected create when .gitignore missing")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range expectedIgnoreLines() {
		if !strings.Contains(got, want) {
			t.Fatalf("missing ignore line %q in: %q", want, got)
		}
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
	for _, want := range expectedIgnoreLines() {
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
	existing := strings.Join(expectedIgnoreLines(), "\n") + "\n"
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
	if !strings.Contains(got, "AGENTS.md") || !strings.Contains(got, ".mcp.json") || !strings.Contains(got, ".codex/") {
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
	full := strings.Join(expectedIgnoreLines(), "\n") + "\n"
	if got := missingCodehelperIgnores(full); len(got) != 0 {
		t.Fatalf("want none missing, got %v", got)
	}
}

func expectedIgnoreLines() []string {
	out := make([]string, len(codehelperIgnoreEntries))
	for i, e := range codehelperIgnoreEntries {
		out[i] = e.line
	}
	return out
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
