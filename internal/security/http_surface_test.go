package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectHTTPSurface_FrameworksAndNonHTTP(t *testing.T) {
	flask := t.TempDir()
	_ = os.MkdirAll(filepath.Join(flask, "src", "flask"), 0o755)
	_ = os.WriteFile(filepath.Join(flask, "src", "flask", "app.py"), []byte("class Flask:\n    pass\n"), 0o644)
	s := DetectHTTPSurface(flask)
	if !s.Capable || s.Kind != "http_framework" {
		t.Fatalf("flask layout: %+v", s)
	}

	// Renamed checkout + pyproject only.
	py := t.TempDir()
	_ = os.WriteFile(filepath.Join(py, "pyproject.toml"), []byte("[project]\nname = \"fastapi\"\nversion = \"0.1\"\n"), 0o644)
	s = DetectHTTPSurface(py)
	if !s.Capable {
		t.Fatalf("fastapi pyproject: %+v", s)
	}

	axum := t.TempDir()
	_ = os.MkdirAll(filepath.Join(axum, "axum", "src", "routing"), 0o755)
	_ = os.WriteFile(filepath.Join(axum, "axum", "src", "routing", "mod.rs"), []byte("pub struct Router;\n"), 0o644)
	_ = os.WriteFile(filepath.Join(axum, "Cargo.toml"), []byte("[workspace]\nmembers = [\"axum\", \"axum-*\"]\n"), 0o644)
	s = DetectHTTPSurface(axum)
	if !s.Capable {
		t.Fatalf("axum layout/cargo: %+v", s)
	}

	req := t.TempDir()
	_ = os.WriteFile(filepath.Join(req, "requirements.txt"), []byte("flask==3.0.0\nrequests==2.0\n"), 0o644)
	s = DetectHTTPSurface(req)
	if !s.Capable {
		t.Fatalf("requirements.txt flask: %+v", s)
	}

	redis := t.TempDir()
	src := filepath.Join(redis, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "server.c"), []byte("int main(){}\n"), 0o644)
	for i := 0; i < 18; i++ {
		_ = os.WriteFile(filepath.Join(src, fmt.Sprintf("file%d.c", i)), []byte("int x;\n"), 0o644)
	}
	s = DetectHTTPSurface(redis)
	if s.Capable || s.Kind != "non_http" {
		t.Fatalf("redis-like: %+v", s)
	}
	if !strings.Contains(s.Reason, "C datastore") {
		t.Fatalf("redis reason: %q", s.Reason)
	}

	vue := t.TempDir()
	_ = os.MkdirAll(filepath.Join(vue, "packages", "runtime-core", "src"), 0o755)
	_ = os.WriteFile(filepath.Join(vue, "packages", "runtime-core", "src", "renderer.ts"), []byte("export {}\n"), 0o644)
	s = DetectHTTPSurface(vue)
	if s.Capable || s.Kind != "non_http" {
		t.Fatalf("vue: %+v", s)
	}

	svelte := t.TempDir()
	_ = os.MkdirAll(filepath.Join(svelte, "packages", "svelte", "src", "compiler"), 0o755)
	_ = os.WriteFile(filepath.Join(svelte, "packages", "svelte", "src", "compiler", "index.js"), []byte("export {}\n"), 0o644)
	s = DetectHTTPSurface(svelte)
	if s.Capable || s.Kind != "non_http" {
		t.Fatalf("svelte: %+v", s)
	}

	gin := t.TempDir()
	_ = os.WriteFile(filepath.Join(gin, "gin.go"), []byte("package gin\n"), 0o644)
	_ = os.WriteFile(filepath.Join(gin, "go.mod"), []byte("module github.com/gin-gonic/gin\n\ngo 1.22\n"), 0o644)
	s = DetectHTTPSurface(gin)
	if !s.Capable {
		t.Fatalf("gin: %+v", s)
	}

	starlette := t.TempDir()
	_ = os.MkdirAll(filepath.Join(starlette, "starlette"), 0o755)
	_ = os.WriteFile(filepath.Join(starlette, "starlette", "applications.py"), []byte("class Starlette:\n    pass\n"), 0o644)
	s = DetectHTTPSurface(starlette)
	if !s.Capable {
		t.Fatalf("starlette: %+v", s)
	}

	nest := t.TempDir()
	_ = os.WriteFile(filepath.Join(nest, "nest-cli.json"), []byte(`{"collection":"@nestjs/schematics"}`), 0o644)
	s = DetectHTTPSurface(nest)
	if !s.Capable || s.Kind != "http_app" {
		t.Fatalf("nestjs nest-cli: %+v", s)
	}
}
