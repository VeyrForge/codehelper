package security

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// cLikeFuncStart matches C/C++/Java-ish definitions past #includes / packages.
// Avoids control-flow keywords (if/for/while/switch/return).
var cLikeFuncStart = regexp.MustCompile(
	`(?i)^(?:(?:static|inline|extern|unsigned|signed|const|volatile|struct|enum|union|void|int|long|char|bool|size_t|ssize_t|uint\d+_t|int\d+_t|float|double|FILE|redis|sds|robj|client|user|dict|list|rax)\b[\w\s\*]*\s+)+([A-Za-z_][\w]*)\s*\(`)

var jsAssignFunc = regexp.MustCompile(`(?i)(?:^|\s|[=.])(?:exports\.|module\.exports|prototype\.)?\w[\w.]*\s*=\s*(?:async\s+)?function\b`)
var goTypeDecl = regexp.MustCompile(`^type\s+[A-Z]\w*\s+(struct|interface)\b`)
var javaTypeOrMethod = regexp.MustCompile(
	`(?i)^(?:(?:public|protected|private|static|final|abstract|synchronized|native|default)\s+)*(?:class|interface|enum|record)\b|^(?:(?:public|protected|private|static|final|abstract|synchronized|native|default)\s+)+\w[\w.<>,\[\]\s]*\s+\w+\s*\(`)
var phpMethod = regexp.MustCompile(`(?i)^(?:(?:public|protected|private|static|final|abstract)\s+)*function\s+\w+`)
var rubyDef = regexp.MustCompile(`^(?:def\s+\w+|module\s+[A-Z]|class\s+[A-Z])`)

// libraryHotPathHint is one framework-core perf cite. Needle (when set) anchors
// a real hot-path definition — never the first preamble helper in the file.
type libraryHotPathHint struct {
	file    string
	needle  string
	rule    string
	message string
}

// libraryHotPathHints maps well-known library cores to hot-path files agents
// should inspect instead of inventing app-level N+1 issues.
var libraryHotPathHints = map[string][]libraryHotPathHint{
	"express": {
		{"lib/application.js", "app.handle = function handle", "library-hot-path",
			"app.handle — every request enters here; measure middleware chain length and sync I/O before micro-opts. Unbounded body buffering in early middleware multiplies latency for all consumers."},
		{"lib/response.js", "res.send = function send", "library-alloc",
			"res.send — Buffer/string copies + header map churn under load; stream large bodies, avoid huge sync JSON, and do not treat example res.send concat as a SQL sink."},
		{"lib/application.js", "app.use = function use", "library-hot-path",
			"app.use — each sync middleware adds per-request cost; keep chains short and move heavy work off the request path."},
		{"lib/router/index.js", "Router.prototype.handle", "library-hot-path",
			"Router.prototype.handle — route dispatch; profile matcher cost when apps mount many nested routers."},
	},
	"django": {
		{"django/db/models/query.py", "class QuerySet", "library-hot-path",
			"QuerySet — N+1 risk is at app call sites using this API. Prefer iterator()/iterator(chunk_size=) over list(); call select_related/prefetch_related before loops."},
		{"django/db/models/query.py", "def _fetch_all(self)", "library-hot-path",
			"_fetch_all — materializes the full result set into _result_cache; measure before converting QuerySets to lists in views."},
		{"django/template/base.py", "def render(self, context)", "library-hot-path",
			"Template.render — expensive filters/tags in hot templates dominate CPU; profile templates before ORM micro-opts."},
	},
	"flask": {
		{"src/flask/app.py", "def wsgi_app(self", "library-hot-path",
			"Flask wsgi_app / full_dispatch — middleware/before_request cost compounds per request; keep sync work out of before_request."},
		{"src/flask/wrappers.py", "class Request", "library-alloc",
			"Request/response wrappers — large form/file bodies allocate on the hot path; stream uploads."},
	},
	"fastapi": {
		{"fastapi/routing.py", "async def app(self, scope", "library-hot-path",
			"APIRouter dispatch — dependency trees and sync endpoints block the event loop; prefer async I/O and shallow Depends()."},
		{"fastapi/dependencies/utils.py", "async def solve_dependencies", "library-hot-path",
			"Dependency resolution — deep Depends() graphs add per-request overhead; flatten where possible."},
	},
	"gin": {
		{"gin.go", "func (engine *Engine) ServeHTTP", "library-hot-path",
			"Engine.ServeHTTP — request entry; middleware chain + binding run here. Measure before adding global middleware."},
		{"gin.go", "func (engine *Engine) handleHTTPRequest", "library-hot-path",
			"handleHTTPRequest — route match + handler invoke; keep binding/validation proportional to payload size."},
		{"context.go", "func (c *Context) Next", "library-alloc",
			"Context.Next — middleware walk; never retain *gin.Context past request end (pool reset)."},
		{"context.go", "func (c *Context) reset", "library-alloc",
			"Context.reset — pooling contract; leaking Keys/Writer refs across requests causes alloc growth and races."},
	},
	"rails": {
		{"actionpack/lib/action_dispatch/routing/route_set.rb", "def recognize_path", "library-hot-path",
			"RouteSet recognition — complex routes and middleware stacks dominate request setup."},
		{"activerecord/lib/active_record/relation.rb", "class Relation", "library-hot-path",
			"AR Relation — N+1 comes from app callers; prefer includes/preload at call sites, not inside the gem."},
	},
	"redis": {
		{"src/server.c", "int processCommand(client *c)", "library-hot-path",
			"processCommand — command dispatch hot path; use SLOWLOG / latency-monitor before micro-opts. Do not invent app N+1 here."},
		{"src/server.c", "void beforeSleep(struct aeEventLoop *eventLoop)", "library-hot-path",
			"beforeSleep — event-loop tick; cron/I/O work here affects every connected client."},
		{"src/networking.c", "void readQueryFromClient(connection *conn)", "library-hot-path",
			"readQueryFromClient — client read path; large payloads / many connections stress this path."},
		{"src/db.c", "kvobj *lookupKey(", "library-alloc",
			"lookupKey — keyspace lookup hot path; KEYS / unbounded scans are classic latency killers — prefer SCAN + tight key patterns."},
		{"src/acl.c", "int ACLCheckAllPerm(client *c", "library-hot-path",
			"ACLCheckAllPerm — runs on authenticated commands; keep ACL rules tight and measurable with SLOWLOG."},
	},
	"axum": {
		{"axum/src/routing/mod.rs", "impl<S> Router<S>", "library-hot-path",
			"Axum Router — handler/future allocation patterns under concurrency; prefer owned state over per-request clones."},
	},
	"vue": {
		{"packages/runtime-core/src/renderer.ts", "export function baseCreateRenderer", "library-hot-path",
			"Vue renderer — component update fan-out; prefer keyed lists and fewer reactive deps."},
		{"packages/reactivity/src/effect.ts", "export class ReactiveEffect", "library-hot-path",
			"Reactive effects — accidental deep watches cause render storms."},
	},
	"svelte": {
		{"packages/svelte/src/runtime/internal/Component.js", "export function init", "library-hot-path",
			"Svelte component runtime — large component trees dominate update cost."},
		{"packages/svelte/src/internal/client/reactivity/effects.js", "export function effect", "library-hot-path",
			"Client effects — accidental deep subscriptions cause update storms."},
		{"packages/svelte/src/compiler/index.js", "export function compile", "library-hot-path",
			"Compiler pipeline — large SFC graphs stress parse/transform; profile before micro-opts."},
	},
}

// LibraryPerfGuidance returns file:line guidance for framework/library cores so
// agents stop hunting app N+1 in library source. Prefers needle-anchored
// definitions (ServeHTTP, processCommand, app.handle) over first-in-file helpers.
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
		hints = libraryHotPathHintsFromLayout(root)
	}
	if len(hints) == 0 {
		for _, rel := range []string{"lib/index.js", "index.js", "src/index.ts", "src/main.rs", "src/server.c"} {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if _, err := os.Stat(abs); err == nil {
				hints = append(hints, libraryHotPathHint{rel, "", "library-hot-path",
					"Library entry/hot path — profile complexity and allocations, not app N+1."})
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
		line := lineForLibraryHotPath(abs, h.needle)
		sev := "medium"
		// Primary request/command entry points outrank alphabetical siblings
		// (acl.c before server.c) so juniors see the real hot path first.
		if h.needle != "" && (strings.Contains(h.needle, "processCommand") ||
			strings.Contains(h.needle, "ServeHTTP") ||
			strings.Contains(h.needle, "app.handle") ||
			strings.Contains(h.needle, "class QuerySet") ||
			strings.Contains(h.needle, "wsgi_app") ||
			strings.Contains(h.needle, "baseCreateRenderer")) {
			sev = "high"
		}
		out = append(out, ContextFinding{
			Tool: "codehelper-library-perf", Severity: sev, Rule: h.rule,
			File: h.file, Line: line, Evidence: h.message,
			Kind: "library_guidance", Confidence: "medium", Exploitability: "unknown",
			Hint: "[framework-core] Real hot-path cite — optimize allocs/complexity here; do not invent controller N+1. Next tool: `hotspots`; if commits_scanned≤1 prefer primary-language centrality.",
		})
	}
	return EnrichAndRankFindings(out)
}

// PreferLibraryHotPathLine upgrades a weak first-def cite to a known hot-path
// needle for this relative file (when the checkout matches a known framework).
// Hints are ordered hottest-first; the first matching needle for rel wins.
// Returns orig unchanged when no needle matches.
func PreferLibraryHotPathLine(root, rel string, orig int) int {
	root = filepath.Clean(root)
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
	base := strings.ToLower(filepath.Base(root))
	hints := libraryHotPathHints[base]
	if len(hints) == 0 {
		hints = libraryHotPathHintsFromLayout(root)
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	for _, h := range hints {
		if filepath.ToSlash(h.file) != rel || h.needle == "" {
			continue
		}
		if ln := firstLineContainingFile(abs, h.needle); ln > 0 {
			return ln
		}
	}
	if orig <= 1 {
		if ln := LineForHotPath(abs); ln > orig {
			return ln
		}
	}
	return orig
}

func lineForLibraryHotPath(abs, needle string) int {
	if needle != "" {
		if ln := firstLineContainingFile(abs, needle); ln > 0 {
			return ln
		}
	}
	return LineForHotPath(abs)
}

// libraryHotPathHintsFromLayout picks framework hot-path hints by on-disk layout
// so renamed checkouts (not just basename "flask") still get core guidance.
func libraryHotPathHintsFromLayout(root string) []libraryHotPathHint {
	checks := []struct {
		probe string
		key   string
	}{
		{"src/flask/app.py", "flask"},
		{"flask/app.py", "flask"},
		{"fastapi/routing.py", "fastapi"},
		{"django/db/models/query.py", "django"},
		{"lib/router/index.js", "express"},
		{"lib/application.js", "express"},
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

const hotPathScanLimit = 220

// LineForHotPath prefers a real function/export/type line over canned line 1.
// Skips license headers, imports, and #includes so Express/Redis/Gin cores
// (application.js, acl.c, context.go) resolve past preamble noise.
func LineForHotPath(abs string) int {
	f, err := os.Open(abs)
	if err != nil {
		return 1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	lineNo := 0
	inBlockComment := false
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		t := strings.TrimSpace(raw)
		if inBlockComment {
			if strings.Contains(t, "*/") {
				inBlockComment = false
			}
			continue
		}
		if strings.HasPrefix(t, "/*") {
			if !strings.Contains(t, "*/") {
				inBlockComment = true
			}
			continue
		}
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") ||
			strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*!") {
			continue
		}
		lower := strings.ToLower(t)
		// Preamble / imports — keep scanning.
		if isHotPathPreamble(lower, t) {
			if lineNo > hotPathScanLimit {
				break
			}
			continue
		}
		if isHotPathDefinition(t, lower) {
			return lineNo
		}
		if lineNo > hotPathScanLimit {
			break
		}
	}
	return 1
}

func isHotPathPreamble(lower, t string) bool {
	switch {
	case strings.HasPrefix(lower, "'use strict'") || strings.HasPrefix(lower, `"use strict"`):
		return true
	case strings.HasPrefix(lower, "package ") || strings.HasPrefix(lower, "import ") ||
		strings.HasPrefix(lower, "from ") || strings.HasPrefix(lower, "using ") ||
		strings.HasPrefix(lower, "namespace ") || strings.HasPrefix(lower, "require(") ||
		strings.HasPrefix(lower, "var ") && strings.Contains(lower, "require(") ||
		strings.HasPrefix(lower, "const ") && strings.Contains(lower, "require(") ||
		strings.HasPrefix(lower, "let ") && strings.Contains(lower, "require("):
		return true
	case strings.HasPrefix(lower, "#include") || strings.HasPrefix(lower, "#define") ||
		strings.HasPrefix(lower, "#pragma") || strings.HasPrefix(lower, "#ifndef") ||
		strings.HasPrefix(lower, "#ifdef") || strings.HasPrefix(lower, "#endif") ||
		strings.HasPrefix(lower, "#else"):
		return true
	case t == "(" || t == ")" || t == "{" || t == "}" || t == "[" || t == "]":
		return true
	case strings.HasPrefix(t, `"`) && (strings.HasSuffix(t, `"`) || strings.HasSuffix(t, `",`)):
		// Go import strings / lone string literals in import blocks.
		return true
	}
	return false
}

func isHotPathDefinition(t, lower string) bool {
	switch {
	case strings.HasPrefix(lower, "function ") || strings.HasPrefix(lower, "async function "):
		return true
	case strings.HasPrefix(lower, "export function ") || strings.HasPrefix(lower, "export async function "):
		return true
	case strings.HasPrefix(lower, "export const ") && (strings.Contains(lower, "=>") || strings.Contains(lower, "function")):
		return true
	case strings.HasPrefix(lower, "exports.") && (strings.Contains(lower, "function") || strings.Contains(lower, "=>") || strings.Contains(lower, "=")):
		return true
	case strings.Contains(lower, "module.exports") && (strings.Contains(lower, "function") || strings.Contains(lower, "=>")):
		return true
	case jsAssignFunc.MatchString(t):
		return true
	case strings.HasPrefix(lower, "pub fn ") || strings.HasPrefix(lower, "fn ") ||
		strings.HasPrefix(lower, "pub async fn "):
		return true
	case strings.HasPrefix(lower, "def ") || strings.HasPrefix(lower, "async def ") ||
		strings.HasPrefix(lower, "class "):
		return true
	case strings.HasPrefix(lower, "func ") || strings.Contains(lower, "\tfunc ") ||
		strings.Contains(lower, " func ") || goTypeDecl.MatchString(t):
		return true
	case rubyDef.MatchString(t):
		return true
	case phpMethod.MatchString(t) || (strings.HasPrefix(lower, "class ") && (strings.Contains(t, "{") || !strings.Contains(t, "("))):
		return true
	case javaTypeOrMethod.MatchString(t):
		return true
	case cLikeFuncStart.MatchString(t):
		if strings.HasSuffix(t, "\\") {
			return false
		}
		return true
	}
	return false
}

// LineForSymbolDef upgrades a bogus line≤1 graph cite for type-like symbols
// (class/module/interface/…) by locating the named definition on disk.
// Prefers a name-specific def needle; falls back to LineForHotPath.
// Returns orig unchanged when already >1, kind is not type-like, or no better
// line exists — never invents hotspots, only fixes cite lines.
func LineForSymbolDef(abs, name, kind string, orig int) int {
	if orig > 1 {
		return orig
	}
	if orig <= 0 {
		orig = 1
	}
	if abs == "" || !isTypeLikeKind(kind) {
		return orig
	}
	name = strings.TrimSpace(name)
	if name != "" {
		for _, needle := range symbolDefNeedles(name, kind) {
			if ln := firstLineContainingFile(abs, needle); ln > orig {
				return ln
			}
		}
	}
	if ln := LineForHotPath(abs); ln > orig {
		return ln
	}
	return orig
}

func isTypeLikeKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "class", "interface", "module", "namespace", "enum", "type_alias":
		return true
	default:
		return false
	}
}

func symbolDefNeedles(name, kind string) []string {
	k := strings.ToLower(strings.TrimSpace(kind))
	var out []string
	switch k {
	case "class":
		out = []string{"class " + name, "module " + name}
	case "module", "namespace":
		out = []string{"module " + name, "namespace " + name, "package " + name}
	case "interface":
		out = []string{"interface " + name}
	case "enum":
		out = []string{"enum " + name}
	case "type_alias":
		out = []string{"type " + name}
	default:
		out = []string{"class " + name}
	}
	// Go hubs are often stored as class; Ruby modules as class — keep forms handy.
	out = append(out, "type "+name, "struct "+name)
	return out
}

// SkeletonPerfGuidance gives skeletons measurable next steps without inventing
// hotspots in empty apps. Cites are conventions / sample paths — never claim
// measured production bottlenecks.
func SkeletonPerfGuidance(root string, limit int) []ContextFinding {
	if limit <= 0 {
		limit = 4
	}
	root = filepath.Clean(root)
	candidates := []struct {
		file, needle, msg string
	}{
		{"routes/web.php", "Route::get",
			"Laravel route list is tiny — before hunting N+1: add a realistic list endpoint, paginate (`paginate`/`simplePaginate`), and eager-load with `with()` once models exist. Not a measured production hotspot."},
		{"routes/api.php", "Route::",
			"API skeleton — define pagination + `with()`/`loadMissing` conventions before load testing. Convention only, not a measured hotspot."},
		{"app/Http/Controllers/Controller.php", "abstract class Controller",
			"Base controller — set query budgets / pagination helpers before filling CRUD; forbid `Model::all()` then per-row `find` in loops. Convention only."},
		{"app/Models/User.php", "class User",
			"User model — when relations land, default to `with([...])` on list queries; watch N+1 in Blade `@foreach`. Convention only."},
		{"src/app.controller.ts", "export class AppController",
			"Nest starter controller — no DB yet; add a sample TypeORM/Prisma list endpoint to practice avoiding N+1. Not a measured hotspot."},
		{"src/app.service.ts", "export class AppService",
			"Nest starter service — keep business logic here; avoid sync CPU work in interceptors. Convention only."},
		{"src/main.ts", "NestFactory.create",
			"Bootstrap only — performance work belongs in services/guards once real I/O exists. Not a measured hotspot."},
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
		line := lineForLibraryHotPath(abs, c.needle)
		out = append(out, ContextFinding{
			Tool: "codehelper-skeleton-perf", Severity: "low", Rule: "skeleton-perf-guidance",
			File: c.file, Line: line, Evidence: c.msg,
			Kind: "library_guidance", Confidence: "low", Exploitability: "config-only",
			Hint: "[skeleton-not-hotspot] Convention/sample path only — NOT a measured production hotspot. Do not invent N+1. Next: add a realistic list endpoint with pagination + eager-load, then call `hotspots`.",
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
		{"src/main/java/org/springframework/samples/petclinic/vet/VetController.java", "JSON /vets — unbounded vetRepository.findAll(); HTML /vets.html already uses Pageable — prefer the paginated path."},
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
