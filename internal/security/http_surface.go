package security

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// HTTPSurface classifies whether a checkout is a plausible place to add an
// HTTP /health|/ready route (framework cores + apps) vs a non-HTTP core
// (datastores, UI compilers, pure C libs).
type HTTPSurface struct {
	Capable bool   `json:"capable"`
	Kind    string `json:"kind,omitempty"`   // http_framework | http_app | non_http | unknown
	Reason  string `json:"reason,omitempty"` // short human label for abstain / notes
}

// DetectHTTPSurface uses layout + manifests (not folder basename alone) so
// renamed flask/django/fastapi/axum checkouts still count as HTTP-capable,
// while redis/vue/svelte-style cores stay non-HTTP.
func DetectHTTPSurface(root string) HTTPSurface {
	root = filepath.Clean(root)
	if s := httpSurfaceFromNonHTTPLayout(root); s.Kind != "" {
		return s
	}
	if s := httpSurfaceFromHTTPLayout(root); s.Kind != "" {
		return s
	}
	if s := httpSurfaceFromManifests(root); s.Kind != "" {
		return s
	}
	if s := httpSurfaceFromAppMarkers(root); s.Kind != "" {
		return s
	}
	return HTTPSurface{Capable: false, Kind: "unknown", Reason: "no HTTP framework/app markers detected"}
}

// ExposesHTTP reports a positive HTTP surface (framework or app). Unknown
// libraries return false so health abstain stays conservative.
func ExposesHTTP(root string) bool {
	return DetectHTTPSurface(root).Capable
}

// IsNonHTTPCore is true for datastores / UI compilers / pure C cores that must
// keep the honest health ABSTAIN.
func IsNonHTTPCore(root string) bool {
	s := DetectHTTPSurface(root)
	return s.Kind == "non_http"
}

func httpSurfaceFromNonHTTPLayout(root string) HTTPSurface {
	// Redis-like: many .c under src/ + server.c, no HTTP app layer.
	if hasFile(root, "src/server.c") {
		cCount := countExtFiles(filepath.Join(root, "src"), ".c", 200)
		if cCount >= 15 && !hasDir(root, "app") && !hasDir(root, "apps") &&
			!hasFile(root, "gin.go") && !hasFile(root, "manage.py") {
			return HTTPSurface{Capable: false, Kind: "non_http",
				Reason: "C datastore/server core (src/server.c) — no HTTP /health surface"}
		}
	}
	// Vue compiler/runtime monorepo — UI framework, not an HTTP server.
	if hasFile(root, "packages/runtime-core/src/renderer.ts") ||
		hasFile(root, "packages/compiler-core/src/index.ts") {
		return HTTPSurface{Capable: false, Kind: "non_http",
			Reason: "Vue UI compiler/runtime — no HTTP /health surface"}
	}
	// Svelte compiler package — not an HTTP server.
	if hasFile(root, "packages/svelte/src/compiler/index.js") ||
		hasFile(root, "packages/svelte/src/compiler/index.ts") {
		return HTTPSurface{Capable: false, Kind: "non_http",
			Reason: "Svelte compiler package — no HTTP /health surface"}
	}
	return HTTPSurface{}
}

func httpSurfaceFromHTTPLayout(root string) HTTPSurface {
	probes := []struct {
		rel    string
		reason string
	}{
		{"src/flask/app.py", "Flask package layout (src/flask/app.py)"},
		{"flask/app.py", "Flask package layout (flask/app.py)"},
		{"fastapi/routing.py", "FastAPI package layout"},
		{"fastapi/applications.py", "FastAPI package layout"},
		{"starlette/applications.py", "Starlette ASGI layout"},
		{"starlette/routing.py", "Starlette routing layout"},
		{"django/urls/resolvers.py", "Django package layout"},
		{"django/db/models/query.py", "Django package layout"},
		{"lib/router/index.js", "Express-like router layout"},
		{"lib/application.js", "Express-like application layout"},
		{"gin.go", "Gin framework layout"},
		{"routergroup.go", "Gin router group layout"},
		{"axum/src/routing/mod.rs", "Axum routing crate layout"},
		{"actionpack/lib/action_dispatch/routing/route_set.rb", "Rails ActionPack routing"},
		{"src/main/java/org/springframework/boot/SpringApplication.java", "Spring Boot"},
		{"src/Controller", "Symfony-style Controller dir"},
		{"config/routes.yaml", "Symfony routes.yaml"},
		{"config/routes/annotations.yaml", "Symfony annotated routes"},
	}
	for _, p := range probes {
		if p.rel == "src/Controller" {
			if hasDir(root, "src/Controller") {
				return HTTPSurface{Capable: true, Kind: "http_app", Reason: p.reason}
			}
			continue
		}
		if !hasFile(root, p.rel) {
			continue
		}
		if p.rel == "lib/router/index.js" && (hasDir(root, "routes") || hasDir(root, "app") || hasDir(root, "apps")) {
			return HTTPSurface{Capable: true, Kind: "http_app", Reason: "Express-like app with routes/"}
		}
		return HTTPSurface{Capable: true, Kind: "http_framework", Reason: p.reason}
	}
	if hasDir(root, "src/Controller") || hasDir(root, "src/Controller/Admin") {
		return HTTPSurface{Capable: true, Kind: "http_app", Reason: "Symfony-style src/Controller"}
	}
	return HTTPSurface{}
}

func httpSurfaceFromManifests(root string) HTTPSurface {
	if py := readFileTrunc(filepath.Join(root, "pyproject.toml"), 6000); py != "" {
		lower := strings.ToLower(py)
		for _, lib := range []string{
			`name = "django"`, `name = "flask"`, `name = "fastapi"`, `name = "starlette"`,
			`name = 'django'`, `name = 'flask'`, `name = 'fastapi'`, `name = 'starlette'`,
		} {
			if strings.Contains(lower, lib) {
				return HTTPSurface{Capable: true, Kind: "http_framework", Reason: "pyproject name is HTTP framework"}
			}
		}
		for _, dep := range []string{"django", "flask", "fastapi", "starlette", "uvicorn", "aiohttp"} {
			if strings.Contains(lower, `"`+dep+`"`) || strings.Contains(lower, `'`+dep+`'`) ||
				strings.Contains(lower, dep+" ") || strings.Contains(lower, dep+"=") {
				// Weak dep mention alone is not enough for framework_core beds;
				// require dependencies table style.
				if strings.Contains(lower, "[project]") || strings.Contains(lower, "[tool.poetry") ||
					strings.Contains(lower, "dependencies") {
					if strings.Contains(lower, dep) && (strings.Contains(lower, "framework") ||
						dep == "flask" || dep == "django" || dep == "fastapi" || dep == "starlette") {
						return HTTPSurface{Capable: true, Kind: "http_app", Reason: "pyproject depends on " + dep}
					}
				}
			}
		}
	}
	if req := readFileTrunc(filepath.Join(root, "requirements.txt"), 4000); req != "" {
		lower := strings.ToLower(req)
		for _, dep := range []string{"flask", "django", "fastapi", "starlette", "uvicorn"} {
			if lineHasDep(lower, dep) {
				return HTTPSurface{Capable: true, Kind: "http_app", Reason: "requirements.txt includes " + dep}
			}
		}
	}
	if gm := readFileTrunc(filepath.Join(root, "go.mod"), 3000); gm != "" {
		lower := strings.ToLower(gm)
		first := strings.SplitN(gm, "\n", 2)[0]
		mod := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(first, "module ")))
		for _, lib := range []string{
			"github.com/gin-gonic/gin", "github.com/labstack/echo", "github.com/go-chi/chi",
			"github.com/gofiber/fiber", "github.com/gorilla/mux",
		} {
			if strings.HasPrefix(mod, lib) || mod == lib || strings.Contains(lower, lib) {
				return HTTPSurface{Capable: true, Kind: "http_framework", Reason: "go.mod HTTP framework (" + lib + ")"}
			}
		}
	}
	if cargo := readFileTrunc(filepath.Join(root, "Cargo.toml"), 6000); cargo != "" {
		lower := strings.ToLower(cargo)
		if strings.Contains(lower, "name = \"axum\"") || strings.Contains(lower, "name = 'axum'") ||
			strings.Contains(lower, "axum/src") || strings.Contains(lower, `members = ["axum"`) ||
			strings.Contains(lower, "members = [\"axum") {
			return HTTPSurface{Capable: true, Kind: "http_framework", Reason: "Cargo.toml axum workspace/crate"}
		}
		if strings.Contains(lower, "axum") && (strings.Contains(lower, "[dependencies]") ||
			strings.Contains(lower, "workspace")) {
			return HTTPSurface{Capable: true, Kind: "http_framework", Reason: "Cargo.toml references axum"}
		}
	}
	// Nested axum crate.
	if hasFile(root, "axum/Cargo.toml") {
		return HTTPSurface{Capable: true, Kind: "http_framework", Reason: "axum/Cargo.toml present"}
	}
	if pj := readFileTrunc(filepath.Join(root, "package.json"), 8000); pj != "" {
		var meta struct {
			Name             string            `json:"name"`
			Description      string            `json:"description"`
			Dependencies     map[string]string `json:"dependencies"`
			DevDependencies  map[string]string `json:"devDependencies"`
			PeerDependencies map[string]string `json:"peerDependencies"`
		}
		_ = json.Unmarshal([]byte(pj), &meta)
		n := strings.ToLower(meta.Name)
		desc := strings.ToLower(meta.Description)
		httpPkgs := []string{"express", "koa", "fastify", "hapi", "@nestjs/core", "nestjs", "connect"}
		for _, lib := range httpPkgs {
			if n == lib || strings.HasPrefix(n, lib+"/") || strings.HasSuffix(n, "/"+lib) ||
				n == "@nestjs/core" || n == "@nestjs/common" {
				return HTTPSurface{Capable: true, Kind: "http_framework", Reason: "package.json name " + n}
			}
		}
		if strings.Contains(desc, "web framework") || strings.Contains(desc, "http framework") ||
			strings.Contains(desc, "nestjs") {
			return HTTPSurface{Capable: true, Kind: "http_framework", Reason: "package.json description HTTP framework"}
		}
		deps := map[string]string{}
		for k, v := range meta.Dependencies {
			deps[strings.ToLower(k)] = v
		}
		for k, v := range meta.DevDependencies {
			deps[strings.ToLower(k)] = v
		}
		for k, v := range meta.PeerDependencies {
			deps[strings.ToLower(k)] = v
		}
		for _, lib := range []string{"express", "koa", "fastify", "@nestjs/core", "hapi", "@hapi/hapi"} {
			if _, ok := deps[lib]; ok {
				return HTTPSurface{Capable: true, Kind: "http_app", Reason: "package.json depends on " + lib}
			}
		}
		// Vue/Svelte package names without layout already caught: treat as non-HTTP
		// only when clearly the compiler package (handled in layout). Plain "vue"
		// name without renderer layout may still be an app — check deps.
		if n == "vue" || n == "svelte" || strings.HasPrefix(n, "@vue/") || strings.HasPrefix(n, "@sveltejs/") {
			if _, hasExpress := deps["express"]; !hasExpress {
				if _, hasNest := deps["@nestjs/core"]; !hasNest {
					return HTTPSurface{Capable: false, Kind: "non_http",
						Reason: "UI package " + n + " — no HTTP server dependency"}
				}
			}
		}
	}
	if comp := readFileTrunc(filepath.Join(root, "composer.json"), 4000); comp != "" {
		lower := strings.ToLower(comp)
		if strings.Contains(lower, `"laravel/framework"`) || strings.Contains(lower, `"symfony/framework-bundle"`) ||
			strings.Contains(lower, `"symfony/http-foundation"`) || strings.Contains(lower, "laravel/laravel") {
			return HTTPSurface{Capable: true, Kind: "http_app", Reason: "composer.json Laravel/Symfony HTTP"}
		}
	}
	if gem := readFileTrunc(filepath.Join(root, "Gemfile"), 3000); gem != "" {
		lower := strings.ToLower(gem)
		if strings.Contains(lower, "rails") || strings.Contains(lower, "sinatra") || strings.Contains(lower, "rack") {
			return HTTPSurface{Capable: true, Kind: "http_app", Reason: "Gemfile Rails/Rack HTTP"}
		}
	}
	if pom := readFileTrunc(filepath.Join(root, "pom.xml"), 6000); pom != "" {
		lower := strings.ToLower(pom)
		if strings.Contains(lower, "spring-boot") || strings.Contains(lower, "springframework") {
			return HTTPSurface{Capable: true, Kind: "http_app", Reason: "pom.xml Spring HTTP"}
		}
	}
	if hasFile(root, "build.gradle") || hasFile(root, "build.gradle.kts") {
		bg := readFileTrunc(filepath.Join(root, "build.gradle"), 4000)
		if bg == "" {
			bg = readFileTrunc(filepath.Join(root, "build.gradle.kts"), 4000)
		}
		lower := strings.ToLower(bg)
		if strings.Contains(lower, "spring-boot") || strings.Contains(lower, "springframework") {
			return HTTPSurface{Capable: true, Kind: "http_app", Reason: "Gradle Spring HTTP"}
		}
	}
	return HTTPSurface{}
}

func httpSurfaceFromAppMarkers(root string) HTTPSurface {
	markers := []struct {
		rel    string
		reason string
	}{
		{"manage.py", "Django manage.py app"},
		{"artisan", "Laravel artisan app"},
		{"nest-cli.json", "NestJS app"},
		{"config/routes.rb", "Rails routes"},
		{"routes/web.php", "Laravel routes/web.php"},
		{"routes/api.php", "Laravel routes/api.php"},
		{"cmd/api/main.go", "Go cmd/api HTTP host"},
		{"backend/cmd/api/main.go", "Go backend/cmd/api HTTP host"},
		{"internal/agentapi/server.go", "Go agentapi HTTP server"},
		{"app/api/health/route.ts", "Next.js App Router health"},
		{"src/app.controller.ts", "Nest controller"},
		{"src/main.ts", "Nest/TS main entry"},
	}
	for _, m := range markers {
		if hasFile(root, m.rel) {
			return HTTPSurface{Capable: true, Kind: "http_app", Reason: m.reason}
		}
	}
	if hasDir(root, "routes") || hasDir(root, "Controllers") || hasDir(root, "controllers") {
		return HTTPSurface{Capable: true, Kind: "http_app", Reason: "routes/controllers directory"}
	}
	return HTTPSurface{}
}

func lineHasDep(lowerReq, dep string) bool {
	for _, line := range strings.Split(lowerReq, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == dep || strings.HasPrefix(line, dep+"=") || strings.HasPrefix(line, dep+"==") ||
			strings.HasPrefix(line, dep+">") || strings.HasPrefix(line, dep+"<") ||
			strings.HasPrefix(line, dep+"~") || strings.HasPrefix(line, dep+"[") {
			return true
		}
	}
	return false
}
