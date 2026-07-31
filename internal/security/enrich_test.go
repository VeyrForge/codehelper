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

func TestEnrichRanksAuthBeforeHealth(t *testing.T) {
	in := []ContextFinding{
		{Rule: "app-healthz", Severity: "medium", Kind: "library_guidance", File: "internal/agentapi/server.go", Line: 63, Evidence: "health"},
		{Rule: "app-auth-middleware", Severity: "medium", Kind: "library_guidance", File: "internal/agentapi/server.go", Line: 75, Evidence: "bearer auth"},
		{Rule: "authz-gap", Severity: "medium", Kind: "sink_candidate", File: "lib/api-auth.ts", Line: 54, Evidence: "fail-open if limiter unavailable"},
	}
	got := EnrichAndRankFindings(in)
	if got[0].Rule != "authz-gap" {
		t.Fatalf("expected authz-gap sink first, got %+v", got[0])
	}
	if got[1].Rule != "app-auth-middleware" {
		t.Fatalf("expected app-auth-middleware before health, got %+v", got)
	}
	if got[2].Rule != "app-healthz" {
		t.Fatalf("expected health last, got %+v", got)
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
		if f.Rule == "nest-missing-validation-pipe" {
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

// TestScanConfigHardening_NestSplitsChecklistRows ensures a bare Nest
// bootstrap (no ValidationPipe/helmet/throttler) emits three distinct,
// independently actionable rows instead of one combined line — a single
// generic row scored as thin/"config-only" in HUMAN-AUDIT-V5.
func TestScanConfigHardening_NestSplitsChecklistRows(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.ts"),
		[]byte("import { NestFactory } from '@nestjs/core';\nasync function bootstrap() {\n  const app = await NestFactory.create(AppModule);\n  await app.listen(3000);\n}\n"), 0o644)

	got := ScanConfigHardening(dir, 10)
	rules := map[string]bool{}
	for _, f := range got {
		rules[f.Rule] = true
	}
	for _, want := range []string{"nest-missing-validation-pipe", "nest-missing-helmet", "nest-missing-rate-limit"} {
		if !rules[want] {
			t.Errorf("expected rule %q, got %+v", want, got)
		}
	}
}

// TestScanConfigHardening_NestValidationPipePresent confirms an already
// present ValidationPipe suppresses just that one row (not the others).
func TestScanConfigHardening_NestValidationPipePresent(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.ts"),
		[]byte("import { NestFactory } from '@nestjs/core';\nasync function bootstrap() {\n  const app = await NestFactory.create(AppModule);\n  app.useGlobalPipes(new ValidationPipe());\n  await app.listen(3000);\n}\n"), 0o644)

	got := ScanConfigHardening(dir, 10)
	for _, f := range got {
		if f.Rule == "nest-missing-validation-pipe" {
			t.Fatalf("did not expect nest-missing-validation-pipe when ValidationPipe present: %+v", got)
		}
	}
}

// TestScanConfigHardening_LaravelSessionSecureCookieNullDefault covers the
// real Laravel-11+ footgun: config/session.php 'secure' => env(...) with no
// explicit boolean default sends the session cookie over plain HTTP unless
// the env var is set in production.
func TestScanConfigHardening_LaravelSessionSecureCookieNullDefault(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "config", "session.php"),
		[]byte("<?php\nreturn [\n    'secure' => env('SESSION_SECURE_COOKIE'),\n];\n"), 0o644)

	got := ScanConfigHardening(dir, 10)
	found := false
	for _, f := range got {
		if f.Rule == "config-session-insecure" && strings.Contains(f.File, "session.php") {
			found = true
			if f.Severity != "medium" {
				t.Errorf("severity=%s want medium", f.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("expected config-session-insecure for null-default secure cookie, got %+v", got)
	}
}

// TestScanConfigHardening_LaravelSessionSecureCookieExplicitDefault ensures
// an explicit boolean default does NOT trigger the footgun (no false positive).
func TestScanConfigHardening_LaravelSessionSecureCookieExplicitDefault(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "config", "session.php"),
		[]byte("<?php\nreturn [\n    'secure' => env('SESSION_SECURE_COOKIE', true),\n];\n"), 0o644)

	got := ScanConfigHardening(dir, 10)
	for _, f := range got {
		if f.Rule == "config-session-insecure" && strings.Contains(f.File, "session.php") {
			t.Fatalf("did not expect config-session-insecure with explicit default: %+v", got)
		}
	}
}

// TestScanLaravelMassAssignment_GuardedEmpty flags Eloquent models that
// unguard every column via `protected $guarded = [];`.
func TestScanLaravelMassAssignment_GuardedEmpty(t *testing.T) {
	dir := t.TempDir()
	modelsDir := filepath.Join(dir, "app", "Models")
	_ = os.MkdirAll(modelsDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelsDir, "User.php"),
		[]byte("<?php\nnamespace App\\Models;\n\nclass User extends Model\n{\n    protected $guarded = [];\n}\n"), 0o644)

	got := scanLaravelMassAssignment(dir, 10)
	if len(got) != 1 {
		t.Fatalf("expected 1 mass-assignment finding, got %+v", got)
	}
	if got[0].Rule != "mass-assignment-open" || got[0].Severity != "high" {
		t.Fatalf("unexpected finding %+v", got[0])
	}
	if got[0].Kind != "sink_candidate" {
		t.Fatalf("mass-assignment must be sink_candidate, got kind=%q", got[0].Kind)
	}
	if !strings.Contains(got[0].File, "User.php") {
		t.Fatalf("unexpected file %+v", got[0])
	}
}

// TestScanLaravelMassAssignment_FillableSafe ensures a properly scoped
// $fillable model does not trigger a false positive.
func TestScanLaravelMassAssignment_FillableSafe(t *testing.T) {
	dir := t.TempDir()
	modelsDir := filepath.Join(dir, "app", "Models")
	_ = os.MkdirAll(modelsDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelsDir, "User.php"),
		[]byte("<?php\nnamespace App\\Models;\n\nclass User extends Model\n{\n    protected $fillable = ['name', 'email'];\n}\n"), 0o644)

	got := scanLaravelMassAssignment(dir, 10)
	if len(got) != 0 {
		t.Fatalf("expected no mass-assignment finding, got %+v", got)
	}
}

// TestScanConfigHardening_LaravelSessionEncryptFalseDefault cites
// config/session.php encrypt => env(..., false) — plaintext session storage.
func TestScanConfigHardening_LaravelSessionEncryptFalseDefault(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "config", "session.php"),
		[]byte("<?php\nreturn [\n    'encrypt' => env('SESSION_ENCRYPT', false),\n    'secure' => env('SESSION_SECURE_COOKIE'),\n];\n"), 0o644)

	got := ScanConfigHardening(dir, 10)
	sawEncrypt, sawSecure := false, false
	for _, f := range got {
		if f.Rule != "config-session-insecure" || !strings.Contains(f.File, "session.php") {
			continue
		}
		ev := strings.ToLower(f.Evidence)
		if strings.Contains(ev, "encrypt") {
			sawEncrypt = true
			if f.Confidence != "high" {
				t.Errorf("encrypt confidence=%s want high", f.Confidence)
			}
		}
		if strings.Contains(ev, "secure") {
			sawSecure = true
		}
	}
	if !sawEncrypt || !sawSecure {
		t.Fatalf("expected encrypt+secure session findings, got %+v", got)
	}
}

// TestScanConfigHardening_LaravelAppKeyConfigCite prefers config/app.php over
// demoted .env.example for the empty APP_KEY deploy blocker.
func TestScanConfigHardening_LaravelAppKeyConfigCite(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "config", "app.php"),
		[]byte("<?php\nreturn [\n    'key' => env('APP_KEY'),\n];\n"), 0o644)

	got := ScanConfigHardening(dir, 10)
	found := false
	for _, f := range got {
		if f.Rule == "config-auth-gap" && strings.Contains(f.File, "app.php") {
			found = true
			if f.Severity == "low" {
				t.Fatalf("config/app.php APP_KEY cite must not be demoted to low: %+v", f)
			}
		}
	}
	if !found {
		t.Fatalf("expected config-auth-gap on config/app.php, got %+v", got)
	}
}

// TestEnrich_EmptyAppKeyInEnvExampleKeepsSignal ensures empty APP_KEY= in
// .env.example is not force-demoted to low (skeleton deploy blocker).
func TestEnrich_EmptyAppKeyInEnvExampleKeepsSignal(t *testing.T) {
	in := []ContextFinding{{
		Tool: "codehelper-config-scan", Severity: "medium", Rule: "config-auth-gap",
		File: ".env.example", Line: 3, Evidence: "APP_KEY=",
		Kind: "config_hardening", Confidence: "high", Exploitability: "config-only",
	}}
	got := EnrichAndRankFindings(in)
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Severity == "low" || got[0].Confidence == "low" {
		t.Fatalf("empty APP_KEY= must keep signal, got %+v", got[0])
	}
}

func TestAppSecurityGuidance_MissingSpringSecurity(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "src", "main", "java", "com", "example")
	_ = os.MkdirAll(pkg, 0o755)
	_ = os.WriteFile(filepath.Join(pkg, "DemoApplication.java"),
		[]byte("package com.example;\nimport org.springframework.boot.autoconfigure.SpringBootApplication;\n@SpringBootApplication\npublic class DemoApplication {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(pkg, "OwnerController.java"),
		[]byte("package com.example;\npublic class OwnerController {}\n"), 0o644)

	got := AppSecurityGuidance(dir, 6)
	saw := false
	for _, f := range got {
		if f.Rule == "app-missing-spring-security" {
			saw = true
			if f.Confidence != "high" {
				t.Errorf("confidence=%s", f.Confidence)
			}
		}
	}
	if !saw {
		t.Fatalf("expected app-missing-spring-security, got %+v", got)
	}

	// Presence of SecurityFilterChain suppresses the finding.
	_ = os.WriteFile(filepath.Join(pkg, "SecurityConfig.java"),
		[]byte("package com.example;\nimport org.springframework.security.web.SecurityFilterChain;\npublic class SecurityConfig { SecurityFilterChain chain; }\n"), 0o644)
	got2 := AppSecurityGuidance(dir, 6)
	for _, f := range got2 {
		if f.Rule == "app-missing-spring-security" {
			t.Fatalf("did not expect missing-security when SecurityFilterChain present: %+v", got2)
		}
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
	_ = os.WriteFile(filepath.Join(express, "lib", "response.js"), []byte("res.redirect = function redirect(url) {\n  return url;\n};\nres.send = function send(body) {\n  return body;\n};\n"), 0o644)
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
	_ = os.WriteFile(filepath.Join(dir, "src", "flask", "app.py"), []byte("default_config = {\n    \"SECRET_KEY\": None,\n}\nclass Flask:\n    pass\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "src", "flask", "helpers.py"), []byte("def redirect(loc):\n    return loc\n"), 0o644)
	got := LibrarySecurityGuidance(dir, ShapeFrameworkCore, 4)
	if len(got) == 0 {
		t.Fatal("expected layout-based flask security hints")
	}
	if !strings.Contains(got[0].Hint, "framework-core") {
		t.Fatalf("expected framework-core label, hint=%q", got[0].Hint)
	}
}

func TestLibrarySecurityGuidance_VueCompilerDomVHtml(t *testing.T) {
	parent := t.TempDir()
	vue := filepath.Join(parent, "vue")
	dir := filepath.Join(vue, "packages", "compiler-dom", "src", "transforms")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "vHtml.ts"), []byte("export const transformVHtml = (dir) => {\n  return { props: [{ key: 'innerHTML' }] }\n}\n"), 0o644)
	got := LibrarySecurityGuidance(vue, ShapeFrameworkCore, 4)
	if len(got) == 0 {
		t.Fatal("expected vue v-html footgun")
	}
	saw := false
	for _, f := range got {
		if strings.Contains(f.File, "vHtml.ts") && f.Rule == "library-xss-api" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected compiler-dom vHtml finding, got %+v", got)
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

func TestAppSecurityGuidance_DescrybeAndDiscordCitesCode(t *testing.T) {
	dir := t.TempDir()

	// descrybe-like: fail-open is only in a comment; RateLimiter is the code cite.
	_ = os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "lib", "api-auth.ts"), []byte(""+
		"import { RateLimiter } from '@/lib/rate-limiter'\n"+
		"export async function auth() {\n"+
		"  // Lightweight rate-limit check (fail-open if limiter unavailable)\n"+
		"  try {\n"+
		"    const limiter = RateLimiter.getInstance();\n"+
		"    await limiter.checkLimits('u', 1)\n"+
		"  } catch (_) {}\n"+
		"}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "middleware.ts"), []byte(""+
		"import { NextResponse } from 'next/server'\n"+
		"export function middleware() {\n"+
		"  return NextResponse.redirect(new URL('/login', 'http://x'))\n"+
		"}\n"), 0o644)

	// discord_mod-like auth surfaces
	authDir := filepath.Join(dir, "backend", "internal", "auth")
	_ = os.MkdirAll(authDir, 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "backend", "cmd", "api"), 0o755)
	_ = os.WriteFile(filepath.Join(authDir, "handler.go"), []byte(""+
		"package auth\n"+
		"type Handler struct { SessionSecret string }\n"+
		"// safeRedirectURL returns a redirect URL safe against open redirects.\n"+
		"func safeRedirectURL(publicBase, redirectParam string) string { return publicBase }\n"+
		"func (h *Handler) setCookie() {\n"+
		"  secure := false\n"+
		"  _ = secure\n"+
		"}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(authDir, "routes.go"), []byte(""+
		"package auth\n"+
		"func Routes(r interface{}, h *Handler) {\n"+
		"  _ = r\n"+
		"}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(authDir, "session_store.go"), []byte(""+
		"package auth\n"+
		"type DBStore struct{}\n"+
		"// SaveSession stores a session.\n"+
		"func (s *DBStore) SaveSession(ctx interface{}, sessionID string) error { return nil }\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "backend", "cmd", "api", "main.go"), []byte(""+
		"package main\n"+
		"func main() {\n"+
		"  r.Get(\"/health\", nil)\n"+
		"  r.Get(\"/ready\", nil)\n"+
		"  _ = srv.ListenAndServe()\n"+
		"}\n"), 0o644)

	got := AppSecurityGuidance(dir, 12)
	checks := map[string]int{
		"app-rate-limit-failopen": 5,
		"app-open-redirect":       4,
		"app-auth-routes":         2,
		"app-session-store":       4,
	}
	for rule, minLine := range checks {
		found := false
		for _, f := range got {
			if f.Rule != rule {
				continue
			}
			found = true
			if f.Line < minLine {
				t.Fatalf("%s cited comment/package line %d (want >= %d) evidence=%q", rule, f.Line, minLine, f.Evidence)
			}
			abs := filepath.Join(dir, filepath.FromSlash(f.File))
			body, err := os.ReadFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(body), "\n")
			if f.Line <= 0 || f.Line > len(lines) {
				t.Fatalf("%s line %d oob", rule, f.Line)
			}
			cited := strings.TrimSpace(lines[f.Line-1])
			if strings.HasPrefix(cited, "//") || strings.HasPrefix(cited, "package ") {
				t.Fatalf("%s must not cite comment/package line %q", rule, cited)
			}
		}
		if !found {
			t.Fatalf("missing rule %s in %+v", rule, got)
		}
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

// TestDetectProjectShape_VueSpaBasenameNotFrameworkCore: a checkout folder
// named "vue" with only SPA components (no packages/*) must be ShapeApp —
// basename alone previously forced framework_core and emptied LibrarySecurityGuidance.
func TestDetectProjectShape_VueSpaBasenameNotFrameworkCore(t *testing.T) {
	parent := t.TempDir()
	vue := filepath.Join(parent, "vue")
	_ = os.MkdirAll(filepath.Join(vue, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(vue, "src", "Greeter.vue"),
		[]byte("<script setup>\nconst open = ref(true)\n</script>\n<template>\n  <h1>{{ open }}</h1>\n</template>\n"), 0o644)
	if got := DetectProjectShape(vue); got != ShapeApp {
		t.Fatalf("vue SPA basename shape=%s want app", got)
	}
}

// TestDetectProjectShape_SvelteSpaNotLibraryViaLibDir: Svelte kits use lib/
// but .svelte components make them SPA apps, not ShapeLibrary.
func TestDetectProjectShape_SvelteSpaNotLibraryViaLibDir(t *testing.T) {
	parent := t.TempDir()
	svelte := filepath.Join(parent, "svelte")
	_ = os.MkdirAll(filepath.Join(svelte, "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(svelte, "lib", "Page.svelte"),
		[]byte("<script>\nexport let title = 'Home';\n</script>\n<div>{title}</div>\n"), 0o644)
	if got := DetectProjectShape(svelte); got != ShapeApp {
		t.Fatalf("svelte SPA shape=%s want app", got)
	}
}

// TestScanConfigHardening_VueSpaSplitsChecklistRows mirrors NestSplitsChecklistRows
// for Vue SPAs: four independently actionable library_guidance rows with file:line.
func TestScanConfigHardening_VueSpaSplitsChecklistRows(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "Greeter.vue"),
		[]byte("<script setup lang=\"ts\">\nconst open = ref(true)\n</script>\n<template>\n  <div v-if=\"open\"><h1>Hi</h1></div>\n</template>\n"), 0o644)

	got := ScanConfigHardening(dir, 12)
	rules := map[string]bool{}
	for _, f := range got {
		rules[f.Rule] = true
		if f.Kind != "library_guidance" {
			t.Errorf("kind=%q want library_guidance for %s", f.Kind, f.Rule)
		}
		if f.Line <= 0 || f.File == "" || strings.TrimSpace(f.Evidence) == "" {
			t.Errorf("ungrounded row %+v", f)
		}
		if !strings.Contains(f.Hint, "spa-footgun") {
			t.Errorf("expected spa-footgun hint, got %q", f.Hint)
		}
	}
	for _, want := range []string{"spa-xss-vhtml", "spa-open-redirect", "spa-secret-frontend", "spa-csrf-client"} {
		if !rules[want] {
			t.Errorf("expected rule %q, got %+v", want, got)
		}
	}
}

// TestListSpaSourceFiles_ReadDirProbeFindsSrcVue ensures SPA discovery uses
// ReadDir probes (WalkDir skips Windows junctions used by LIVE beds).
func TestListSpaSourceFiles_ReadDirProbeFindsSrcVue(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "A.vue"), []byte("<template></template>\n"), 0o644)
	got := listSpaSourceFiles(dir, ".vue", 10)
	if len(got) != 1 || got[0] != "src/A.vue" {
		t.Fatalf("listSpaSourceFiles=%v want [src/A.vue]", got)
	}
}

// TestScanConfigHardening_SvelteSpaSplitsChecklistRows covers the same split
// checklist for Svelte ({@html} / goto / PUBLIC_ env).
func TestScanConfigHardening_SvelteSpaSplitsChecklistRows(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "lib", "Page.svelte"),
		[]byte("<script>\nexport let title = 'Home';\n</script>\n<div class=\"page\">{title}</div>\n"), 0o644)

	got := ScanConfigHardening(dir, 12)
	rules := map[string]bool{}
	for _, f := range got {
		rules[f.Rule] = true
	}
	for _, want := range []string{"spa-xss-html", "spa-open-redirect", "spa-secret-frontend", "spa-csrf-client"} {
		if !rules[want] {
			t.Errorf("expected rule %q, got %+v", want, got)
		}
	}
}

// TestScanConfigHardening_VueVHtmlCitesSink: when v-html is present, cite that
// line with high confidence (real sink surface, still not an invented CVE).
func TestScanConfigHardening_VueVHtmlCitesSink(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "Danger.vue"),
		[]byte("<script setup>\nconst html = props.raw\n</script>\n<template>\n  <div v-html=\"html\"></div>\n</template>\n"), 0o644)

	got := ScanConfigHardening(dir, 12)
	found := false
	for _, f := range got {
		if f.Rule == "spa-xss-vhtml" {
			found = true
			if f.Confidence != "high" {
				t.Errorf("confidence=%s want high when v-html present", f.Confidence)
			}
			if !strings.Contains(strings.ToLower(f.Evidence), "v-html") {
				t.Errorf("evidence should cite v-html line, got %q", f.Evidence)
			}
			if f.Line < 4 {
				t.Errorf("line=%d want template v-html line", f.Line)
			}
		}
	}
	if !found {
		t.Fatalf("expected spa-xss-vhtml sink cite, got %+v", got)
	}
}

// TestScanConfigHardening_VueFrameworkCoreSkipsSpaChecklist: vuejs/core layout
// must not get SPA checklist rows (LibrarySecurityGuidance owns those footguns).
func TestScanConfigHardening_VueFrameworkCoreSkipsSpaChecklist(t *testing.T) {
	parent := t.TempDir()
	vue := filepath.Join(parent, "vue")
	dir := filepath.Join(vue, "packages", "compiler-dom", "src", "transforms")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "vHtml.ts"),
		[]byte("export const transformVHtml = () => {}\n"), 0o644)
	// decoy app-looking file must not trigger SPA checklist on a core layout
	_ = os.MkdirAll(filepath.Join(vue, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(vue, "src", "Greeter.vue"),
		[]byte("<template><h1>x</h1></template>\n"), 0o644)

	got := ScanConfigHardening(vue, 12)
	for _, f := range got {
		if strings.HasPrefix(f.Rule, "spa-") {
			t.Fatalf("SPA checklist must skip vue framework core, got %+v", got)
		}
	}
}

// TestListSpaSourceFiles_SkipsEvalProjects: host trees that nest .eval-projects
// must not surface foreign .vue/.svelte paths (codehelper self-scan honesty).
func TestListSpaSourceFiles_SkipsEvalProjects(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/host\n"), 0o644)
	nested := filepath.Join(dir, ".eval-projects", "vue", "packages-private", "sfc-playground", "src")
	_ = os.MkdirAll(nested, 0o755)
	_ = os.WriteFile(filepath.Join(nested, "App.vue"),
		[]byte("<template><div v-html=\"x\"></div></template>\n"), 0o644)
	beds := filepath.Join(dir, ".testbeds", "active", "svelte", "src")
	_ = os.MkdirAll(beds, 0o755)
	_ = os.WriteFile(filepath.Join(beds, "Page.svelte"),
		[]byte("<script></script>{@html html}\n"), 0o644)

	if got := listSpaSourceFiles(dir, ".vue", 10); len(got) != 0 {
		t.Fatalf("expected no .vue under eval beds, got %v", got)
	}
	if got := listSpaSourceFiles(dir, ".svelte", 10); len(got) != 0 {
		t.Fatalf("expected no .svelte under testbeds, got %v", got)
	}
	if isFrontendSpaApp(dir) {
		t.Fatal("Go host with only nested eval SPA files must not be isFrontendSpaApp")
	}
	for _, f := range ScanConfigHardening(dir, 12) {
		if strings.HasPrefix(f.Rule, "spa-") {
			t.Fatalf("config hardening must not emit spa-* for nested eval beds, got %+v", f)
		}
	}
}

func TestSignalRank_SpaBelowAppAuth(t *testing.T) {
	auth := signalRank(ContextFinding{Rule: "app-auth-middleware", Evidence: "bearer"})
	spa := signalRank(ContextFinding{Rule: "spa-xss-vhtml", Evidence: "v-html"})
	if !(auth > spa) {
		t.Fatalf("app-auth signalRank=%d must beat spa=%d", auth, spa)
	}
}
