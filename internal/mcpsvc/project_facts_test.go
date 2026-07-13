package mcpsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectFacts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ".git"), "HEAD", "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(dir, ".git"), "config", "[remote \"origin\"]\n\turl = https://user:secrettoken@github.com/acme/proj.git\n")
	mustWrite(t, dir, "package.json", `{"scripts":{"dev":"vite","build":"vite build"}}`)
	mustWrite(t, dir, "index.html", "<html></html>")
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}

	g := gitInfo(dir)
	if g == nil || g.Branch != "main" {
		t.Fatalf("git branch: %+v", g)
	}
	if !strings.Contains(g.Remote, "github.com/acme/proj") || strings.Contains(g.Remote, "secrettoken") {
		t.Errorf("remote should be sanitized (no creds): %q", g.Remote)
	}
	if scripts := projectScripts(dir); !containsSub(scripts, "npm run dev") {
		t.Errorf("expected npm run dev in scripts: %v", scripts)
	}
	surf := projectSurfaces(dir, []string{"go"}, nil)
	if !briefContains(surf, "frontend") || !briefContains(surf, "backend") {
		t.Errorf("expected frontend+backend surfaces: %v", surf)
	}
	if hostOS() == "" {
		t.Error("hostOS() returned empty")
	}
}

func containsSub(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
