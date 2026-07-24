package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrichAndRankFindings(t *testing.T) {
	in := []ContextFinding{
		{Rule: "raw-html-xss", Severity: "medium", File: "a.py", Line: 2, Evidence: "mark_safe(x)"},
		{Rule: "sql-string-concat", Severity: "high", File: "b.py", Line: 1, Evidence: "SELECT + id"},
		{Rule: "config-debug-enabled", Severity: "medium", File: ".env.example", Line: 4, Kind: "config_hardening", Evidence: "APP_DEBUG=true"},
	}
	got := EnrichAndRankFindings(in)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Rule != "sql-string-concat" || got[0].Rank != 1 {
		t.Fatalf("expected sql first rank=1, got %+v", got[0])
	}
	if got[0].Confidence != "high" || got[0].Exploitability != "possible" {
		t.Fatalf("sql enrich: %+v", got[0])
	}
	var cfg ContextFinding
	for _, f := range got {
		if f.Kind == "config_hardening" {
			cfg = f
		}
	}
	if cfg.Exploitability != "config-only" {
		t.Fatalf("config exploitability=%q", cfg.Exploitability)
	}
}

func TestDetectProjectShape(t *testing.T) {
	dir := t.TempDir()
	// Express-like: package name express + lib/
	_ = os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"express","description":"web framework","main":"index.js"}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "lib", "index.js"), []byte("module.exports = {}"), 0o644)
	if got := DetectProjectShape(dir); got != ShapeFrameworkCore && got != ShapeLibrary {
		// name isn't "express" (temp dir) — package.json name should still classify
		if got != ShapeFrameworkCore {
			t.Fatalf("express-like shape=%s", got)
		}
	}

	skel := t.TempDir()
	_ = os.WriteFile(filepath.Join(skel, "nest-cli.json"), []byte(`{}`), 0o644)
	_ = os.MkdirAll(filepath.Join(skel, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(skel, "src", "main.ts"), []byte("bootstrap()"), 0o644)
	_ = os.WriteFile(filepath.Join(skel, "package.json"), []byte(`{"name":"my-starter","description":"NestJS starter"}`), 0o644)
	if got := DetectProjectShape(skel); got != ShapeSkeleton {
		t.Fatalf("starter shape=%s want skeleton", got)
	}

	// Renamed flask checkout: layout probe, not basename.
	flask := t.TempDir()
	_ = os.MkdirAll(filepath.Join(flask, "src", "flask"), 0o755)
	_ = os.WriteFile(filepath.Join(flask, "src", "flask", "app.py"), []byte("class Flask:\n    pass\n"), 0o644)
	if got := DetectProjectShape(flask); got != ShapeFrameworkCore {
		t.Fatalf("renamed flask layout shape=%s want framework_core", got)
	}
	hints := LibraryPerfGuidance(flask, ShapeFrameworkCore, 4)
	if len(hints) == 0 {
		t.Fatal("expected layout-based flask perf hints")
	}
	sawApp := false
	for _, h := range hints {
		if strings.Contains(h.File, "app.py") || strings.Contains(h.File, "wrappers.py") {
			sawApp = true
		}
	}
	if !sawApp {
		t.Fatalf("expected flask app/wrappers hint, got %+v", hints)
	}
}

func TestScanConfigHardening(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("APP_NAME=X\nAPP_DEBUG=true\nSESSION_ENCRYPT=false\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.ts"), []byte("import { NestFactory } from '@nestjs/core';\nasync function bootstrap() {\n  const app = await NestFactory.create(AppModule);\n  await app.listen(3000);\n}\n"), 0o644)

	got := ScanConfigHardening(dir, 10)
	if len(got) == 0 {
		t.Fatal("expected config hardening findings")
	}
	var sawDebug, sawNest bool
	for _, f := range got {
		if f.Kind != "config_hardening" {
			t.Errorf("kind=%q want config_hardening", f.Kind)
		}
		if f.Line <= 0 || f.File == "" {
			t.Errorf("missing file:line %+v", f)
		}
		if f.Rule == "config-debug-enabled" {
			sawDebug = true
		}
		if f.Rule == "config-missing-validation" {
			sawNest = true
		}
	}
	if !sawDebug {
		t.Error("expected APP_DEBUG finding")
	}
	if !sawNest {
		t.Error("expected Nest ValidationPipe guidance")
	}
}

func TestLibraryPerfGuidance(t *testing.T) {
	dir := t.TempDir()
	express := filepath.Join(dir, "express")
	_ = os.MkdirAll(filepath.Join(express, "lib", "router"), 0o755)
	_ = os.WriteFile(filepath.Join(express, "lib", "router", "index.js"), []byte("exports.handle = function() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(express, "lib", "application.js"), []byte("exports=1"), 0o644)

	got := LibraryPerfGuidance(express, ShapeFrameworkCore, 4)
	if len(got) == 0 {
		t.Fatal("expected library perf guidance")
	}
	if got[0].Kind != "library_guidance" {
		t.Fatalf("kind=%s", got[0].Kind)
	}
	if got[0].File == "" {
		t.Fatal("missing file")
	}
}

func TestLibrarySecurityGuidance(t *testing.T) {
	dir := t.TempDir()
	express := filepath.Join(dir, "express")
	_ = os.MkdirAll(filepath.Join(express, "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(express, "lib", "response.js"), []byte("exports.redirect = function redirect(url) {\n  return url;\n};\n"), 0o644)
	_ = os.WriteFile(filepath.Join(express, "package.json"), []byte(`{"name":"express","description":"web framework"}`), 0o644)
	got := LibrarySecurityGuidance(express, ShapeFrameworkCore, 4)
	if len(got) == 0 {
		t.Fatal("expected library security guidance")
	}
	if got[0].Kind != "library_guidance" {
		t.Fatalf("kind=%s", got[0].Kind)
	}
	if got[0].Line < 1 {
		t.Fatal("expected real line")
	}
	if !strings.Contains(got[0].File, "response.js") {
		t.Fatalf("unexpected file %s", got[0].File)
	}
}

func TestLibrarySecurityGuidance_LayoutFallback(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src", "flask"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "flask", "app.py"), []byte("class Flask:\n    secret_key = None\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "src", "flask"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "flask", "helpers.py"), []byte("def redirect(loc):\n    return loc\n"), 0o644)
	got := LibrarySecurityGuidance(dir, ShapeFrameworkCore, 4)
	if len(got) == 0 {
		t.Fatal("expected layout-based flask security hints")
	}
	if !strings.Contains(got[0].Hint, "framework-core") {
		t.Fatalf("expected framework-core label, hint=%q", got[0].Hint)
	}
}

func TestAppSecurityGuidance_SpringActuator(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join("src", "main", "resources")
	_ = os.MkdirAll(filepath.Join(dir, rel), 0o755)
	body := "# web\nmanagement.endpoints.web.exposure.include=*\n"
	_ = os.WriteFile(filepath.Join(dir, rel, "application.properties"), []byte(body), 0o644)
	got := AppSecurityGuidance(dir, 6)
	if len(got) == 0 {
		t.Fatal("expected app security guidance for actuator")
	}
	saw := false
	for _, f := range got {
		if f.Rule == "app-actuator-exposure" && f.Line >= 2 {
			saw = true
		}
		if !strings.Contains(f.Hint, "app-trust-boundary") {
			t.Fatalf("expected app-trust-boundary label, hint=%q", f.Hint)
		}
	}
	if !saw {
		t.Fatalf("expected actuator footgun, got %+v", got)
	}
	cfg := ScanConfigHardening(dir, 6)
	sawCfg := false
	for _, f := range cfg {
		if strings.Contains(strings.ToLower(f.Evidence), "management.endpoints") {
			sawCfg = true
		}
	}
	if !sawCfg {
		t.Fatalf("expected config hardening actuator hit, got %+v", cfg)
	}
}

func TestMergeUniqueFindings(t *testing.T) {
	a := []ContextFinding{{Rule: "a", File: "x.go", Line: 1}}
	b := []ContextFinding{{Rule: "a", File: "x.go", Line: 1}, {Rule: "b", File: "y.go", Line: 2}}
	got := MergeUniqueFindings(a, b, 10)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
}
