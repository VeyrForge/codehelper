package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstLineContainingFile_PrefersClassOverImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_key.py")
	src := "" +
		"from typing import Annotated\n" +
		"\n" +
		"from fastapi.openapi.models import APIKey, APIKeyIn\n" +
		"from fastapi.security.base import SecurityBase\n" +
		"\n" +
		"class APIKeyBase(SecurityBase):\n" +
		"    model: APIKey\n" +
		"\n" +
		"class APIKeyQuery(APIKeyBase):\n" +
		"    pass\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	line := firstLineContainingFile(path, "APIKey")
	if line != 6 && line != 9 {
		t.Fatalf("expected class definition line (6 or 9), got %d (import is 3)", line)
	}
	if line2 := firstLineContainingFile(path, "class APIKeyQuery"); line2 != 9 {
		t.Fatalf("expected class APIKeyQuery at 9, got %d", line2)
	}
}

func TestLibrarySecurityGuidance_FastAPICitesDefinition(t *testing.T) {
	parent := t.TempDir()
	named := filepath.Join(parent, "fastapi")
	sec := filepath.Join(named, "fastapi", "security")
	if err := os.MkdirAll(sec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sec, "api_key.py"), []byte(""+
		"from fastapi.openapi.models import APIKey, APIKeyIn\n"+
		"\n"+
		"class APIKeyQuery:\n"+
		"    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sec, "oauth2.py"), []byte(""+
		"from fastapi.openapi.models import OAuth2 as OAuth2Model\n"+
		"\n"+
		"class OAuth2PasswordBearer:\n"+
		"    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := LibrarySecurityGuidance(named, ShapeFrameworkCore, 4)
	if len(findings) == 0 {
		t.Fatal("expected library security findings")
	}
	var sawAPIKey bool
	for _, f := range findings {
		if strings.Contains(f.File, "api_key") {
			sawAPIKey = true
			if f.Line < 3 {
				t.Fatalf("api_key cite should be class def not import, got line %d evidence=%q", f.Line, f.Evidence)
			}
		}
		if strings.Contains(f.File, "oauth2") && f.Line < 3 {
			t.Fatalf("oauth2 cite should be class def not import, got line %d", f.Line)
		}
	}
	if !sawAPIKey {
		t.Fatalf("expected api_key finding, got %+v", findings)
	}
}

func TestFirstLineContainingFile_SkipsCommentProse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.js")
	src := "" +
		"/**\n" +
		" * Return request header.\n" +
		" * Aliased as `req.header()`.\n" +
		" */\n" +
		"\n" +
		"req.get =\n" +
		"req.header = function header(name) {\n" +
		"  return this.headers[name];\n" +
		"};\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if line := firstLineContainingFile(path, "header"); line != 7 {
		t.Fatalf("expected req.header definition line 7, got %d (JSDoc must not win)", line)
	}
	if line := firstLineContainingFile(path, "req.header = function header"); line != 7 {
		t.Fatalf("expected exact needle at 7, got %d", line)
	}
}

func TestLibrarySecurityGuidance_ExpressAndDjangoCiteCode(t *testing.T) {
	parent := t.TempDir()

	express := filepath.Join(parent, "express")
	lib := filepath.Join(express, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "request.js"), []byte(""+
		"/**\n * Return request header.\n */\n"+
		"req.header = function header(name) {\n  return name;\n};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "response.js"), []byte(""+
		"var escapeHtml = require('escape-html');\n"+
		"res.send = function send(body) { return body; };\n"+
		"res.redirect = function redirect(url) { return url; };\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "application.js"), []byte(""+
		"this.set('trust proxy', false);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LibrarySecurityGuidance(express, ShapeFrameworkCore, 6)
	var inputLine int
	for _, f := range got {
		if f.Rule == "library-input" {
			inputLine = f.Line
		}
	}
	if inputLine != 4 {
		t.Fatalf("express library-input must cite req.header def (line 4), got %d findings=%+v", inputLine, got)
	}

	django := filepath.Join(parent, "django")
	csrfDir := filepath.Join(django, "django", "middleware")
	if err := os.MkdirAll(csrfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(django, "django", "utils"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(django, "django", "views", "decorators"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(django, "django", "db", "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(csrfDir, "csrf.py"), []byte(""+
		"import logging\n"+
		"logger = logging.getLogger(\"django.security.csrf\")\n"+
		"\n"+
		"class CsrfViewMiddleware(MiddlewareMixin):\n"+
		"    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(django, "django", "utils", "safestring.py"), []byte("def mark_safe(s):\n    return s\n"), 0o644)
	_ = os.WriteFile(filepath.Join(django, "django", "views", "decorators", "csrf.py"), []byte("def csrf_exempt(view_func):\n    return view_func\n"), 0o644)
	_ = os.WriteFile(filepath.Join(django, "django", "db", "models", "expressions.py"), []byte("class RawSQL(Expression):\n    pass\n"), 0o644)

	dgot := LibrarySecurityGuidance(django, ShapeFrameworkCore, 6)
	var csrfLine int
	for _, f := range dgot {
		if f.Rule == "library-csrf" && strings.Contains(f.File, "middleware/csrf") {
			csrfLine = f.Line
		}
	}
	if csrfLine != 4 {
		t.Fatalf("django CSRF must cite class CsrfViewMiddleware (line 4), got %d findings=%+v", csrfLine, dgot)
	}
}
