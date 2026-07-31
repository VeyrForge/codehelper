package lsp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnabled_Env(t *testing.T) {
	t.Setenv("CODEHELPER_LSP", "0")
	if Enabled() {
		t.Fatal("expected disabled")
	}
	t.Setenv("CODEHELPER_LSP", "1")
	if !Enabled() {
		t.Fatal("expected enabled")
	}
	t.Setenv("CODEHELPER_LSP", "")
	if !Enabled() {
		t.Fatal("auto mode should still allow attempts")
	}
}

func TestDetectServer_UnsupportedExt(t *testing.T) {
	_, _, _, err := DetectServer(t.TempDir(), "readme.md")
	if err == nil {
		t.Fatal("expected error for .md")
	}
}

func TestDetectServer_GoWhenGoplsMissing(t *testing.T) {
	// Force PATH empty-ish so gopls is missing — still returns a clear error.
	t.Setenv("PATH", t.TempDir())
	if runtime.GOOS == "windows" {
		t.Setenv("Path", t.TempDir())
	}
	_, _, lang, err := DetectServer(t.TempDir(), "main.go")
	if err == nil {
		t.Fatal("expected gopls missing error")
	}
	if lang != "go" {
		t.Errorf("lang=%q", lang)
	}
}

func TestPathURIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.go")
	if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(p)
	back := uriToPath(uri)
	// Compare basenames — drive letter / slash normalization differs by OS.
	if filepath.Base(back) != "x.go" {
		t.Errorf("uriToPath(%q)=%q", uri, back)
	}
}

func TestExtractHoverAndLocations(t *testing.T) {
	h := extractHover([]byte(`{"contents":{"kind":"markdown","value":"**Func**\n\ndetail"}}`))
	if h == "" || h[:4] != "**Fu" {
		t.Errorf("hover=%q", h)
	}
	repo := t.TempDir()
	abs := filepath.Join(repo, "a.go")
	_ = os.WriteFile(abs, []byte("package a\n"), 0o644)
	uri := pathToURI(abs)
	raw := []byte(`[{"uri":"` + uri + `","range":{"start":{"line":2,"character":1},"end":{"line":2,"character":5}}}]`)
	locs := parseLocations(raw, repo)
	if len(locs) != 1 || locs[0].Line != 3 || locs[0].Path != "a.go" {
		t.Errorf("locs=%+v", locs)
	}
}

func TestParseWorkspaceEditLocations(t *testing.T) {
	repo := t.TempDir()
	abs := filepath.Join(repo, "b.go")
	_ = os.WriteFile(abs, []byte("package b\n"), 0o644)
	uri := pathToURI(abs)
	raw := []byte(`{"changes":{"` + uri + `":[{"range":{"start":{"line":0,"character":8},"end":{"line":0,"character":9}},"newText":"X"}]}}`)
	locs := parseWorkspaceEditLocations(raw, repo)
	if len(locs) != 1 || locs[0].Path != "b.go" || locs[0].Line != 1 {
		t.Fatalf("workspace edit locs=%+v", locs)
	}
}

func TestQuery_UnknownAction(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	_, err := Query(t.Context(), dir, Position{Path: "main.go", Line: 1, Col: 1}, Action("nope"))
	if err == nil {
		t.Fatal("expected unknown action error")
	}
}

func TestSiblingOfNpxAndNpxArgs(t *testing.T) {
	dir := t.TempDir()
	npxName := "npx"
	shimName := "typescript-language-server"
	script := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		npxName = "npx.cmd"
		shimName = "typescript-language-server.cmd"
		script = "@echo off\n"
	}
	npx := filepath.Join(dir, npxName)
	shim := filepath.Join(dir, shimName)
	if err := os.WriteFile(npx, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("Path", dir)
	}
	got := siblingOfNpx("typescript-language-server")
	if got == "" || filepath.Base(got) != shimName {
		t.Fatalf("siblingOfNpx=%q", got)
	}
	args := npxServerArgs("typescript-language-server", "--stdio")
	if len(args) < 3 || args[len(args)-2] != "typescript-language-server" || args[len(args)-1] != "--stdio" {
		t.Fatalf("npxServerArgs=%v", args)
	}
}

func TestQuery_DisabledFallsBack(t *testing.T) {
	t.Setenv("CODEHELPER_LSP", "0")
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	res, err := Query(t.Context(), dir, Position{Path: "main.go", Line: 1, Col: 1}, ActionHover)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fallback || res.OK {
		t.Fatalf("expected fallback: %+v", res)
	}
}

func TestQuery_MissingServerFallsBack(t *testing.T) {
	t.Setenv("CODEHELPER_LSP", "1")
	t.Setenv("PATH", t.TempDir())
	if runtime.GOOS == "windows" {
		t.Setenv("Path", t.TempDir())
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	res, err := Query(t.Context(), dir, Position{Path: "main.go", Line: 1, Col: 1}, ActionDefinition)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fallback || res.OK {
		t.Fatalf("expected missing-server fallback: %+v", res)
	}
}
