package mcpsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseDiagnostics_Python covers the two Python output shapes diagnostics
// now understands: ruff/flake8/mypy "path:line:col: message" and the compileall
// "File ..., line N" + "SyntaxError: ..." pair.
func TestParseDiagnostics_Python(t *testing.T) {
	ruff := "app/handlers.py:12:5: F401 'os' imported but unused\n"
	got := parseDiagnostics(ruff)
	if len(got) != 1 || got[0].File != "app/handlers.py" || got[0].Line != 12 || got[0].Col != 5 {
		t.Fatalf("ruff parse failed: %+v", got)
	}

	compileall := `*** Error compiling './app/broken.py'...
  File "./app/broken.py", line 3
    def f(:
          ^
SyntaxError: invalid syntax
`
	got = parseDiagnostics(compileall)
	if len(got) != 1 {
		t.Fatalf("compileall: expected 1 diagnostic, got %d (%+v)", len(got), got)
	}
	if got[0].File != "app/broken.py" || got[0].Line != 3 {
		t.Errorf("compileall location wrong: %+v", got[0])
	}
	if got[0].Message == "" || got[0].Severity != "error" {
		t.Errorf("compileall message/severity wrong: %+v", got[0])
	}
}

// TestParseDiagnostics_PHP covers phpstan --error-format=raw output.
func TestParseDiagnostics_PHP(t *testing.T) {
	out := parseDiagnostics("app/Models/User.php:25:Method App\\Models\\User::scope() has no return type specified.\n")
	if len(out) != 1 || out[0].File != "app/Models/User.php" || out[0].Line != 25 || out[0].Message == "" {
		t.Fatalf("php parse failed: %+v", out)
	}
}

func TestParseDiagnostics_PyrightJSON(t *testing.T) {
	raw := `{
  "version": "1.1.0",
  "generalDiagnostics": [
    {
      "file": "app/main.py",
      "severity": "error",
      "message": "\"x\" is not defined",
      "range": {"start": {"line": 9, "character": 4}, "end": {"line": 9, "character": 5}}
    }
  ]
}`
	got := parseDiagnostics(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 pyright diagnostic, got %d (%+v)", len(got), got)
	}
	if got[0].File != "app/main.py" || got[0].Line != 10 || got[0].Col != 5 {
		t.Errorf("location wrong: %+v", got[0])
	}
	if got[0].Severity != "error" || !strings.Contains(got[0].Message, "not defined") {
		t.Errorf("message/severity wrong: %+v", got[0])
	}
}

func TestPhpDiagCmds_WithoutNeonUsesComposerOrLint(t *testing.T) {
	dir := t.TempDir()
	// composer.json alone → php -l path (no vendor phpstan).
	writeFile(t, dir, "composer.json", `{"name":"app/app"}`)
	writeFile(t, dir, "public/index.php", "<?php\n")
	cmds, allowed, ok := phpDiagCmds(dir)
	if !ok || len(cmds) == 0 {
		t.Fatalf("expected php cmds without neon, ok=%v cmds=%v", ok, cmds)
	}
	if !containsAny(allowed, "php") {
		t.Errorf("allowed missing php: %v", allowed)
	}
	joined := strings.Join(cmds, " ")
	if !strings.Contains(joined, "php -l") && !strings.Contains(joined, "phpstan") {
		t.Errorf("expected php -l or phpstan, got %v", cmds)
	}

	// With vendor phpstan but no neon, still run phpstan.
	dir2 := t.TempDir()
	writeFile(t, dir2, "composer.json", `{"name":"app/app"}`)
	if err := os.MkdirAll(filepath.Join(dir2, "vendor", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir2, "vendor/bin/phpstan", "#!/bin/sh\n")
	cmds2, _, ok2 := phpDiagCmds(dir2)
	if !ok2 || len(cmds2) != 1 || !strings.Contains(cmds2[0], "phpstan") {
		t.Fatalf("expected phpstan without neon, got ok=%v cmds=%v", ok2, cmds2)
	}
}

func TestPythonDiagCmds_FallsBackToCompileall(t *testing.T) {
	dir := t.TempDir()
	cmds, allowed := pythonDiagCmds(dir)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "compileall") {
		t.Fatalf("expected compileall fallback, got %v", cmds)
	}
	if !containsAny(allowed, "python3") {
		t.Errorf("allowed=%v", allowed)
	}
}

func TestToolchainAt_PHPComposerWithoutNeon(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"name":"x/y"}`)
	writeFile(t, dir, "artisan", "#!/usr/bin/env php\n")
	name, cmds, _, ok := toolchainAt(dir)
	if !ok || name != "php" || len(cmds) == 0 {
		t.Fatalf("toolchainAt php composer: ok=%v name=%q cmds=%v", ok, name, cmds)
	}
}

func TestToolchainAt_PackageJSONNodeCheck(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"express-like","main":"index.js"}`)
	writeFile(t, dir, "index.js", "module.exports = {}\n")
	name, cmds, _, ok := toolchainAt(dir)
	if !ok || name != "javascript" {
		t.Fatalf("toolchainAt package.json: ok=%v name=%q cmds=%v", ok, name, cmds)
	}
	found := false
	for _, c := range cmds {
		if strings.Contains(c, "node --check") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected node --check fallback, got %v", cmds)
	}
}

func TestToolchainAt_TsconfigBeatsPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"ts-app"}`)
	writeFile(t, dir, "tsconfig.json", `{}`)
	name, cmds, _, ok := toolchainAt(dir)
	if !ok || name != "typescript" {
		t.Fatalf("expected typescript over javascript, got ok=%v name=%q cmds=%v", ok, name, cmds)
	}
}

// TestOrderedToolchains_CoversCommonLanguages guards that the universality
// expansion stays wired (Go/Rust/TS/JS/Python/JVM all detectable).
func TestOrderedToolchains_CoversCommonLanguages(t *testing.T) {
	want := map[string]bool{"go": false, "rust": false, "typescript": false, "javascript": false, "python": false, "java-maven": false, "java-gradle": false, "php": false}
	for _, tc := range orderedToolchains {
		if _, ok := want[tc.name]; ok {
			want[tc.name] = true
		}
		// php/composer.json and dynamic python/js may have nil default cmds — that's OK.
		if tc.marker != "composer.json" && tc.marker != "package.json" && len(tc.cmds) == 0 {
			t.Errorf("toolchain %q marker %q has empty cmds", tc.name, tc.marker)
		}
		if len(tc.allowed) == 0 {
			t.Errorf("toolchain %q has empty allowed", tc.name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("toolchain %q not registered", name)
		}
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsAny(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
