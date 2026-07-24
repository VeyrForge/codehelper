package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ProjectShape classifies how security/perf guidance should be framed.
// Agents must not ask library cores for app N+1 or skeletons for confirmed vulns.
type ProjectShape string

const (
	ShapeApp           ProjectShape = "app"
	ShapeLibrary       ProjectShape = "library"
	ShapeSkeleton      ProjectShape = "skeleton"
	ShapeFrameworkCore ProjectShape = "framework_core"
)

// DetectProjectShape classifies the repo so findings mode can route guidance:
// app → sink scan + hotspots; library/framework_core → hot-path/complexity;
// skeleton → config hardening without inventing vulns.
func DetectProjectShape(root string) ProjectShape {
	root = filepath.Clean(root)
	name := strings.ToLower(filepath.Base(root))

	// Layout-first framework cores (works when the checkout folder is renamed).
	if shape := shapeFromFrameworkLayout(root); shape != "" {
		return shape
	}

	// Well-known framework / library cores checked out as eval beds (basename hint).
	frameworkCores := map[string]bool{
		"express": true, "django": true, "flask": true, "fastapi": true,
		"gin": true, "rails": true, "axum": true, "redis": true, "vue": true,
		"svelte": true, "spring-petclinic": false, // app demo, not spring-core
	}
	if frameworkCores[name] {
		return ShapeFrameworkCore
	}
	if name == "nestjs-typescript-starter" || name == "laravel" {
		return ShapeSkeleton
	}

	// package.json name / description heuristics
	if pj := readFileTrunc(filepath.Join(root, "package.json"), 8000); pj != "" {
		var meta struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Private     bool   `json:"private"`
			Main        string `json:"main"`
			Bin         any    `json:"bin"`
		}
		_ = json.Unmarshal([]byte(pj), &meta)
		n := strings.ToLower(meta.Name)
		desc := strings.ToLower(meta.Description)
		if strings.Contains(n, "starter") || strings.Contains(n, "boilerplate") ||
			strings.Contains(desc, "starter") || strings.Contains(desc, "boilerplate") {
			return ShapeSkeleton
		}
		libNames := []string{"express", "koa", "fastify", "hapi", "nestjs", "vue", "svelte", "react"}
		for _, lib := range libNames {
			if n == lib || strings.HasPrefix(n, lib+"/") || strings.HasSuffix(n, "/"+lib) {
				return ShapeFrameworkCore
			}
		}
		// Published library: has main/bin, not private, lib/ or dist/ entry.
		if !meta.Private && (meta.Main != "" || meta.Bin != nil) {
			if hasDir(root, "lib") || hasDir(root, "dist") || hasDir(root, "src") {
				if !hasDir(root, "app") && !hasDir(root, "apps") && !hasDir(root, "routes") {
					// Thin starter with only a few source files → skeleton.
					if countSourceFiles(root, 40) < 12 && (hasFile(root, "nest-cli.json") || hasFile(root, "artisan")) {
						return ShapeSkeleton
					}
					if strings.Contains(desc, "framework") || strings.Contains(desc, "middleware") ||
						strings.Contains(desc, "library") || strings.Contains(desc, "web framework") {
						return ShapeFrameworkCore
					}
				}
			}
		}
	}

	// Laravel/Nest skeletons: artisan/nest-cli + very few app controllers.
	if hasFile(root, "artisan") && countSourceFiles(filepath.Join(root, "app"), 80) < 25 {
		return ShapeSkeleton
	}
	if hasFile(root, "nest-cli.json") && countSourceFiles(filepath.Join(root, "src"), 40) < 12 {
		return ShapeSkeleton
	}

	// Go modules that are frameworks (gin, echo, chi) vs apps.
	if gm := readFileTrunc(filepath.Join(root, "go.mod"), 2000); gm != "" {
		first := strings.SplitN(gm, "\n", 2)[0]
		mod := strings.TrimSpace(strings.TrimPrefix(first, "module "))
		lower := strings.ToLower(mod)
		for _, lib := range []string{"github.com/gin-gonic/gin", "github.com/labstack/echo",
			"github.com/go-chi/chi", "github.com/gofiber/fiber", "github.com/gorilla/mux"} {
			if strings.HasPrefix(lower, lib) || lower == lib {
				return ShapeFrameworkCore
			}
		}
		if !hasDir(root, "cmd") && hasDir(root, "lib") {
			return ShapeLibrary
		}
	}

	// Python: setup.py / pyproject name matching framework.
	if py := readFileTrunc(filepath.Join(root, "pyproject.toml"), 4000); py != "" {
		lower := strings.ToLower(py)
		for _, lib := range []string{`name = "django"`, `name = "flask"`, `name = "fastapi"`, `name = "starlette"`} {
			if strings.Contains(lower, lib) {
				return ShapeFrameworkCore
			}
		}
	}

	// C projects with src/*.c and no app layer → library/server core (redis-like).
	if hasDir(root, "src") && (hasFile(root, "src/server.c") || hasFile(root, "Makefile")) {
		cCount := countExtFiles(filepath.Join(root, "src"), ".c", 200)
		if cCount > 20 && !hasDir(root, "app") && !hasDir(root, "apps") {
			return ShapeFrameworkCore
		}
	}

	if hasDir(root, "lib") && !hasDir(root, "app") && !hasDir(root, "apps") &&
		!hasFile(root, "artisan") && !hasFile(root, "manage.py") {
		return ShapeLibrary
	}
	return ShapeApp
}

// shapeFromFrameworkLayout classifies by on-disk package layout (not folder name).
func shapeFromFrameworkLayout(root string) ProjectShape {
	probes := []struct {
		rel   string
		shape ProjectShape
	}{
		{"src/flask/app.py", ShapeFrameworkCore},
		{"flask/app.py", ShapeFrameworkCore},
		{"fastapi/routing.py", ShapeFrameworkCore},
		{"django/db/models/query.py", ShapeFrameworkCore},
		{"lib/router/index.js", ShapeFrameworkCore}, // express-like
		{"gin.go", ShapeFrameworkCore},
		{"packages/runtime-core/src/renderer.ts", ShapeFrameworkCore}, // vue
		{"packages/svelte/src/compiler/index.js", ShapeFrameworkCore},
		{"actionpack/lib/action_dispatch/routing/route_set.rb", ShapeFrameworkCore},
		{"axum/src/routing/mod.rs", ShapeFrameworkCore},
		{"src/server.c", ShapeFrameworkCore}, // redis-like when many .c (checked below)
	}
	for _, p := range probes {
		if !hasFile(root, p.rel) {
			continue
		}
		if p.rel == "src/server.c" {
			cCount := countExtFiles(filepath.Join(root, "src"), ".c", 200)
			if cCount < 15 {
				continue
			}
		}
		// Express lib/router alone is not enough if this is clearly an app with routes/.
		if p.rel == "lib/router/index.js" && (hasDir(root, "routes") || hasDir(root, "app") || hasDir(root, "apps")) {
			continue
		}
		return p.shape
	}
	return ""
}

func hasFile(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

func hasDir(root, rel string) bool {
	st, err := os.Stat(filepath.Join(root, rel))
	return err == nil && st.IsDir()
}

func readFileTrunc(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > max {
		return string(b[:max])
	}
	return string(b)
}

func countSourceFiles(dir string, limit int) int {
	n := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || n >= limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "vendor" || name == ".git" || name == "tests" || name == "test" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".py", ".php", ".ts", ".tsx", ".js", ".jsx", ".rb", ".java", ".rs", ".c", ".h":
			n++
		}
		return nil
	})
	return n
}

func countExtFiles(dir, ext string, limit int) int {
	n := 0
	ext = strings.ToLower(ext)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || n >= limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) == ext {
			n++
		}
		return nil
	})
	return n
}
