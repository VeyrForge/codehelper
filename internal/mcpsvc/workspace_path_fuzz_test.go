package mcpsvc

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePathBlocked(t *testing.T) {
	cases := []struct {
		path     string
		forWrite bool
		blocked  bool
	}{
		{"src/main.go", false, false},
		{".git/config", false, true},
		{"vendor/.git/hooks", false, true},
		{"node_modules/pkg/index.js", false, false},
		{"node_modules/pkg/index.js", true, true},
		{".env", true, true},
		{".env", false, false},
		{".env.local", true, true},
		{".env.example", true, false},
		{".env.sample", true, false},
		{"config/.env.prod", true, true},
		{"README.md", true, false},
	}
	for _, c := range cases {
		got := workspacePathBlocked(c.path, c.forWrite)
		if got != c.blocked {
			t.Errorf("workspacePathBlocked(%q, write=%v)=%v want %v", c.path, c.forWrite, got, c.blocked)
		}
	}
}

func FuzzWorkspacePathBlocked(f *testing.F) {
	seeds := []struct {
		path     string
		forWrite bool
	}{
		{"src/main.go", false},
		{".git/config", false},
		{"a/b/.git/c", true},
		{"node_modules/x", true},
		{".env", true},
		{".env.example", true},
		{"../.env.secret", true},
		{`pkg\types\types.go`, false},
		{"", true},
	}
	for _, s := range seeds {
		f.Add(s.path, s.forWrite)
	}
	f.Fuzz(func(t *testing.T, path string, forWrite bool) {
		got := workspacePathBlocked(path, forWrite)
		norm := filepath.ToSlash(filepath.Clean(path))
		parts := strings.Split(norm, "/")
		hasGit := false
		hasNodeModules := false
		for _, p := range parts {
			if p == ".git" {
				hasGit = true
			}
			if p == "node_modules" {
				hasNodeModules = true
			}
		}
		if hasGit && !got {
			t.Fatalf("path with .git segment must be blocked: %q", path)
		}
		if forWrite && hasNodeModules && !got {
			t.Fatalf("write under node_modules must be blocked: %q", path)
		}
		base := filepath.Base(norm)
		if forWrite && isBlockedEnvFilename(base) && !got {
			t.Fatalf("blocked env filename must be blocked for write: %q", path)
		}
	})
}
