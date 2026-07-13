package mcpsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectBrief(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n\nrequire (\n\tgithub.com/spf13/cobra v1.8.0\n\tgithub.com/x/indirectdep v0.1.0 // indirect\n)\n")
	mustWrite(t, dir, "package.json", `{"dependencies":{"react":"^18.2.0"},"devDependencies":{"vite":"5.0.0"}}`)
	mustWrite(t, dir, "README.md", "# Cool Project\n\n![badge](x)\n\nDoes amazing things with code.\n\nMore details here.\n")

	fw, deps, summary := projectBrief(dir)
	joined := strings.Join(deps, " ")
	if !strings.Contains(joined, "github.com/spf13/cobra@v1.8.0") {
		t.Errorf("missing cobra dep: %v", deps)
	}
	if strings.Contains(joined, "indirectdep") {
		t.Errorf("indirect dep should be excluded: %v", deps)
	}
	if !strings.Contains(joined, "react@^18.2.0") {
		t.Errorf("missing react dep: %v", deps)
	}
	if !briefContains(fw, "react") {
		t.Errorf("expected react framework, got %v", fw)
	}
	if !strings.Contains(summary, "Cool Project") || !strings.Contains(summary, "amazing things") {
		t.Errorf("summary wrong: %q", summary)
	}
	if strings.Contains(summary, "badge") {
		t.Errorf("summary should strip badges/images: %q", summary)
	}
}

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func briefContains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
