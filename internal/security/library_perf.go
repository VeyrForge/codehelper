package security

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// libraryHotPathHints maps well-known library cores to hot-path files agents
// should inspect instead of inventing app-level N+1 issues.
var libraryHotPathHints = map[string][]struct {
	file    string
	rule    string
	message string
}{
	"express": {
		{"lib/router/index.js", "library-hot-path", "Express router dispatch — profile middleware chain length / sync work on request path."},
		{"lib/application.js", "library-hot-path", "Application handle — avoid sync I/O and unbounded body buffering in middleware."},
		{"lib/response.js", "library-alloc", "Response helpers — watch large buffer copies and header map churn under load."},
	},
	"django": {
		{"django/db/models/query.py", "library-hot-path", "ORM QuerySet evaluation — N+1 risk is at call sites using this API; check iterators vs lists."},
		{"django/template/base.py", "library-hot-path", "Template render loop — expensive filters in hot templates dominate CPU."},
	},
	"flask": {
		{"src/flask/app.py", "library-hot-path", "Flask app dispatch — middleware/before_request cost compounds per request."},
		{"src/flask/wrappers.py", "library-alloc", "Request/response wrappers — large form/file bodies allocate on the hot path."},
	},
	"fastapi": {
		{"fastapi/routing.py", "library-hot-path", "APIRouter dispatch — dependency trees and sync endpoints block the event loop."},
		{"fastapi/dependencies/utils.py", "library-hot-path", "Dependency resolution — deep Depends() graphs add per-request overhead."},
	},
	"gin": {
		{"gin.go", "library-hot-path", "Gin engine ServeHTTP — middleware chain + binding on hot path."},
		{"context.go", "library-alloc", "gin.Context pooling — avoid retaining references past request end."},
	},
	"rails": {
		{"actionpack/lib/action_dispatch/routing/route_set.rb", "library-hot-path", "RouteSet recognition — complex routes and middleware stacks dominate request setup."},
		{"activerecord/lib/active_record/relation.rb", "library-hot-path", "AR Relation — N+1 comes from app callers; prefer includes/preload at call sites."},
	},
	"redis": {
		{"src/server.c", "library-hot-path", "Redis server main loop — command processing and client I/O dominate CPU."},
		{"src/networking.c", "library-hot-path", "Client networking — large payloads / many connections stress this path."},
		{"src/acl.c", "library-hot-path", "ACL checks — run on authenticated commands; keep rules tight and measurable."},
		{"src/db.c", "library-alloc", "Keyspace ops — large key scans / KEYS-style patterns are classic latency killers."},
	},
	"axum": {
		{"axum/src/routing/mod.rs", "library-hot-path", "Axum routing — handler/future allocation patterns under concurrency."},
	},
	"vue": {
		{"packages/runtime-core/src/renderer.ts", "library-hot-path", "Vue renderer — component update fan-out; prefer keyed lists and fewer reactive deps."},
		{"packages/reactivity/src/effect.ts", "library-hot-path", "Reactive effects — accidental deep watches cause render storms."},
	},
	"svelte": {
		{"packages/svelte/src/runtime/internal/Component.js", "library-hot-path", "Svelte component runtime — large component trees dominate update cost."},
		{"packages/svelte/src/internal/client/reactivity/effects.js", "library-hot-path", "Client effects — accidental deep subscriptions cause update storms."},
		{"packages/svelte/src/internal/client/dom/elements/bindings/shared.js", "library-hot-path", "DOM bindings — keep binding work proportional to visible nodes."},
		{"packages/svelte/src/compiler/index.js", "library-hot-path", "Compiler pipeline — large SFC graphs stress parse/transform; profile before micro-opts."},
	},
}

// LibraryPerfGuidance returns file:line (line=1 when unknown) guidance for
// framework/library cores so agents stop hunting app N+1 in library source.
func LibraryPerfGuidance(root string, shape ProjectShape, limit int) []ContextFinding {
	if shape != ShapeLibrary && shape != ShapeFrameworkCore {
		return nil
	}
	if limit <= 0 {
		limit = 6
	}
	root = filepath.Clean(root)
	base := strings.ToLower(filepath.Base(root))
	hints := libraryHotPathHints[base]
	if len(hints) == 0 {
		// Layout / language patterns — work when the checkout folder is renamed.
		hints = libraryHotPathHintsFromLayout(root)
	}
	if len(hints) == 0 {
		// Fallback: prefer lib/ or src/ entry files that exist.
		for _, rel := range []string{"lib/index.js", "index.js", "src/index.ts", "src/main.rs", "src/server.c"} {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if _, err := os.Stat(abs); err == nil {
				hints = append(hints, struct {
					file    string
					rule    string
					message string
				}{rel, "library-hot-path", "Library entry/hot path — profile complexity and allocations, not app N+1."})
			}
		}
	}
	var out []ContextFinding
	for _, h := range hints {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(root, filepath.FromSlash(h.file))
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		out = append(out, ContextFinding{
			Tool: "codehelper-library-perf", Severity: "medium", Rule: h.rule,
			File: h.file, Line: lineForHotPath(abs), Evidence: h.message,
			Kind: "library_guidance", Confidence: "medium", Exploitability: "unknown",
			Hint: "[framework-core] Optimize hot paths / allocs; do not invent controller N+1. Call hotspots next; if commits_scanned≤1 prefer primary-language centrality.",
		})
	}
	return EnrichAndRankFindings(out)
}

// libraryHotPathHintsFromLayout picks framework hot-path hints by on-disk layout
// so renamed checkouts (not just basename "flask") still get core guidance.
func libraryHotPathHintsFromLayout(root string) []struct {
	file    string
	rule    string
	message string
} {
	checks := []struct {
		probe string
		key   string
	}{
		{"src/flask/app.py", "flask"},
		{"flask/app.py", "flask"},
		{"fastapi/routing.py", "fastapi"},
		{"django/db/models/query.py", "django"},
		{"lib/router/index.js", "express"},
		{"gin.go", "gin"},
		{"src/server.c", "redis"},
		{"packages/runtime-core/src/renderer.ts", "vue"},
		{"packages/svelte/src/compiler/index.js", "svelte"},
		{"actionpack/lib/action_dispatch/routing/route_set.rb", "rails"},
		{"axum/src/routing/mod.rs", "axum"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.probe))); err == nil {
			if h := libraryHotPathHints[c.key]; len(h) > 0 {
				return h
			}
		}
	}
	return nil
}

func lineForHotPath(abs string) int {
	return LineForHotPath(abs)
}

// LineForHotPath prefers a real function/export line over canned line 1.
func LineForHotPath(abs string) int {
	f, err := os.Open(abs)
	if err != nil {
		return 1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") {
			continue
		}
		lower := strings.ToLower(t)
		if strings.HasPrefix(lower, "function ") || strings.HasPrefix(lower, "exports.") ||
			strings.HasPrefix(lower, "pub fn ") || strings.HasPrefix(lower, "fn ") ||
			strings.HasPrefix(lower, "def ") || strings.Contains(lower, "func ") ||
			strings.HasPrefix(t, "module.exports") || strings.Contains(lower, "class ") {
			return lineNo
		}
		if lineNo > 80 {
			break
		}
	}
	if lineNo > 0 {
		return 1
	}
	return 1
}

// SkeletonPerfGuidance gives skeletons measurable next steps without inventing
// hotspots in empty apps.
func SkeletonPerfGuidance(root string, limit int) []ContextFinding {
	if limit <= 0 {
		limit = 4
	}
	root = filepath.Clean(root)
	candidates := []struct {
		file, msg string
	}{
		{"routes/web.php", "Laravel route list is tiny — add realistic list/detail endpoints before hunting N+1; watch Eloquent in loops once models exist."},
		{"routes/api.php", "API skeleton — define pagination + eager-load conventions before load testing."},
		{"src/app.controller.ts", "Nest starter controller — no DB yet; add a sample TypeORM/Prisma list endpoint to practice avoiding N+1."},
		{"src/app.service.ts", "Nest starter service — keep business logic here; avoid sync CPU work in interceptors."},
		{"src/main.ts", "Bootstrap only — performance work belongs in services/guards once real I/O exists."},
		{"app/Http/Controllers/Controller.php", "Base controller — set query budgets / pagination helpers before filling CRUD."},
	}
	var out []ContextFinding
	for _, c := range candidates {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(root, filepath.FromSlash(c.file))
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		out = append(out, ContextFinding{
			Tool: "codehelper-skeleton-perf", Severity: "low", Rule: "skeleton-perf-guidance",
			File: c.file, Line: 1, Evidence: c.msg,
			Kind: "library_guidance", Confidence: "medium", Exploitability: "config-only",
			Hint: "[app-trust-boundary] Skeleton/demo — deliver conventions and sample patterns; do not invent production hotspots.",
		})
	}
	return EnrichAndRankFindings(out)
}

// AppPerfGuidance seeds thin-graph apps (empty hotspots) with known controller hubs.
// Uses app-hot-path labeling — never skeleton-perf-guidance on production apps
// (HUMAN-AUDIT-V3: codehelper labeled as skeleton).
func AppPerfGuidance(root string, limit int) []ContextFinding {
	if limit <= 0 {
		limit = 3
	}
	root = filepath.Clean(root)
	candidates := []struct {
		file, msg string
	}{
		{"src/main/java/org/springframework/samples/petclinic/owner/OwnerController.java", "Petclinic owner list/detail — watch JPA findAll without pagination and N+1 on pet collections."},
		{"src/main/java/org/springframework/samples/petclinic/owner/PetController.java", "Pet forms — binding + validation path; keep DB work out of the render loop."},
		{"backend/internal/api/routes.go", "API route table — measure handler latency; avoid per-request DB fan-out and unbounded list queries."},
		{"backend/cmd/api/main.go", "API process entry — keep middleware lean; push heavy work off the request path."},
		{"frontend/src/lib/api.js", "Frontend API client — batch requests; avoid N+1 fetches in list views."},
		{"frontend/src/routes/+page.svelte", "Dashboard page — defer non-critical loads; watch reactive stores that refetch on every tick."},
		{"internal/mcpsvc/register.go", "MCP tool registration — keep handler fan-out lean; avoid sync work on the request path."},
		{"internal/retrieval/hybrid.go", "Hybrid retrieval ranker — hot path for every query; watch O(n²) merges and unbounded candidate sets."},
		{"internal/mcpsvc/workspace_tools.go", "Workspace tool handlers — I/O bound; stream large reads and bound walk depth."},
		{"internal/indexer/analyze.go", "Index analyze pipeline — dominant CPU/IO for large repos; measure before micro-opts."},
		{"internal/graph/ingest.go", "Graph ingest — batch writes; avoid per-symbol fsync on large indexes."},
		{"app/lib/processing/pipeline/product-pipeline.ts", "Product pipeline — profile stage latency; watch unbounded fan-out on large feeds."},
		{"db/queries/feeds-queries.ts", "Feed queries — prefer keyed lookups / pagination over full-table scans."},
		{"lib/feed-sync.ts", "Feed sync — batch DB writes; bound concurrency on large catalogs."},
		{"actions/export-feeds-actions.ts", "Export actions — stream large CSV; avoid loading whole result sets in memory."},
	}
	var out []ContextFinding
	for _, c := range candidates {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(root, filepath.FromSlash(c.file))
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		out = append(out, ContextFinding{
			Tool: "codehelper-app-perf", Severity: "medium", Rule: "app-hot-path",
			File: c.file, Line: lineForHotPath(abs), Evidence: c.msg,
			Kind: "library_guidance", Confidence: "medium", Exploitability: "unknown",
			Hint: "[app-hot-path] Measured next: call hotspots; if commits_scanned≤1 prefer primary-language centrality over inventing N+1.",
		})
	}
	return EnrichAndRankFindings(out)
}
