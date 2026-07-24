package mcpsvc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/security"
)

func TestAnnotateHotspotWhy_PrefersSymbolLine(t *testing.T) {
	rows := []hotspotRow{{File: "backend/internal/api/routes.go", Commits: 3, Centrality: 12, Score: 0.9}}
	best := map[string]int{"backend/internal/api/routes.go": 105}
	annotateHotspotWhy(rows, 50, security.ShapeApp, best, "")
	if rows[0].Line != 105 {
		t.Fatalf("expected symbol line 105, got %d", rows[0].Line)
	}
	if rows[0].Why == "" || rows[0].SuggestedNextQuery == "" {
		t.Fatalf("expected why + next query, got %+v", rows[0])
	}
}

func TestAnnotateHotspotWhy_DiskFallbackNotAlwaysOne(t *testing.T) {
	dir := t.TempDir()
	rel := "handler.go"
	abs := filepath.Join(dir, rel)
	src := "package app\n\n// comment\n\nfunc HandleList() {}\n"
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []hotspotRow{{File: rel, Commits: 0, Centrality: 1, Score: 0.5}}
	annotateHotspotWhy(rows, 1, security.ShapeApp, nil, dir)
	if rows[0].Line <= 1 {
		// LineForHotPath should find func HandleList around line 5.
		t.Fatalf("expected disk hot-path line > 1, got %d", rows[0].Line)
	}
}

func TestIsHotspotNoisePath_NestedCodehelper(t *testing.T) {
	if !isHotspotNoisePath("codehelper/internal/mcpsvc/register.go", "go") {
		t.Fatal("nested codehelper must be hotspot noise")
	}
	if isHotspotNoisePath("backend/internal/api/routes.go", "go") {
		t.Fatal("host app path must not be noise")
	}
}
