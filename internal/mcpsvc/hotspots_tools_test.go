package mcpsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/security"
)

func TestAnnotateHotspotWhy_PrefersSymbolLine(t *testing.T) {
	rows := []hotspotRow{{File: "backend/internal/api/routes.go", Commits: 3, Centrality: 12, Score: 0.9}}
	best := map[string]int{"backend/internal/api/routes.go": 105}
	annotateHotspotWhy(rows, 50, security.ShapeApp, best, nil, nil, "")
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
	annotateHotspotWhy(rows, 1, security.ShapeApp, nil, nil, nil, dir)
	if rows[0].Line <= 1 {
		// LineForHotPath should find func HandleList around line 5.
		t.Fatalf("expected disk hot-path line > 1, got %d", rows[0].Line)
	}
}

func TestAnnotateHotspotWhy_UpgradesLineOneFromDisk(t *testing.T) {
	dir := t.TempDir()
	rel := "application.js"
	abs := filepath.Join(dir, rel)
	src := "'use strict';\nvar app = exports = module.exports = {};\napp.init = function init() {}\n"
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a bad graph cite at line 1 (license/header).
	rows := []hotspotRow{{File: rel, Line: 1, Commits: 2, Centrality: 10, Score: 0.8}}
	annotateHotspotWhy(rows, 20, security.ShapeFrameworkCore, nil, nil, nil, dir)
	if rows[0].Line <= 1 {
		t.Fatalf("expected upgrade past line 1, got %d", rows[0].Line)
	}
}

func TestAnnotateHotspotWhy_LibraryNeedleBeatsLaterGraphCite(t *testing.T) {
	expressRoot := filepath.Join(t.TempDir(), "express")
	rel := "lib/application.js"
	abs := filepath.Join(expressRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	// Preamble + early helper + real hot path later (mirrors Express layout).
	src := "'use strict';\n" +
		"var app = exports = module.exports = {};\n" +
		"app.init = function init() { return this; };\n" +
		"// filler\n// filler\n// filler\n" +
		"app.handle = function handle(req, res, callback) { return this; };\n"
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// Graph "best" line points at early init — PreferLibrary must still pick handle.
	rows := []hotspotRow{{File: rel, Line: 3, Commits: 2, Centrality: 10, Score: 0.8}}
	annotateHotspotWhy(rows, 20, security.ShapeFrameworkCore, nil, nil, nil, expressRoot)
	lines := strings.Split(src, "\n")
	if rows[0].Line < 1 || rows[0].Line > len(lines) || !strings.Contains(lines[rows[0].Line-1], "app.handle") {
		t.Fatalf("expected app.handle needle over early graph cite, got line %d", rows[0].Line)
	}
}

func TestAnnotateHotspotWhy_NameAwareClassPastModule(t *testing.T) {
	dir := t.TempDir()
	rel := "users_controller.rb"
	abs := filepath.Join(dir, rel)
	src := "# frozen_string_literal: true\n\nmodule Api\n  class UsersController < ApplicationController\n  end\nend\n"
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []hotspotRow{{File: rel, Line: 1, Commits: 2, Centrality: 10, Score: 0.8}}
	names := map[string]string{rel: "UsersController"}
	kinds := map[string]string{rel: "class"}
	annotateHotspotWhy(rows, 20, security.ShapeApp, nil, names, kinds, dir)
	if rows[0].Line != 4 {
		t.Fatalf("expected UsersController class line 4 (not module), got %d", rows[0].Line)
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
