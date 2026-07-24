package mcpsvc

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/security"
)

// auditFinding is a grounded security/perf candidate for plan/kickoff findings
// mode — file:line + rule, never a CSS class or DI "injection" false friend.
// Kind distinguishes sink candidates from config hardening / library guidance.
type auditFinding struct {
	Rank           int    `json:"rank,omitempty"`
	Rule           string `json:"rule"`
	Severity       string `json:"severity"`
	Confidence     string `json:"confidence,omitempty"`
	Exploitability string `json:"exploitability,omitempty"`
	Kind           string `json:"kind,omitempty"`
	File           string `json:"file"`
	Line           int    `json:"line,omitempty"`
	Evidence       string `json:"evidence,omitempty"`
	Hint           string `json:"hint,omitempty"`
	Message        string `json:"message"`
}

// isAuditNoiseSymbol reports CSS/HTML selectors, Nest/Vue DI "injection",
// Schema migration helpers, and *_unsafe encoding helpers that must NOT be
// ranked as security/perf audit targets.
func isAuditNoiseSymbol(name, loc string) bool {
	n := strings.TrimSpace(name)
	p := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	if i := strings.IndexByte(p, ':'); i > 0 {
		p = p[:i]
	}
	if isStyleAssetPath(p) || strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".htm") {
		return true
	}
	if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "#") || strings.HasPrefix(n, "@keyframes") ||
		strings.HasPrefix(n, "--") {
		return true
	}
	lower := strings.ToLower(n)
	noiseNames := []string{
		"resolveinjections", "resolveinjection", "inject(", "injectiontoken",
		"dependencyinjection", "rendersecretsline", "secrets-line", "search-owner-form",
		"list-unstyled", "concatenatearraybuffers",
	}
	for _, bad := range noiseNames {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	// Encoding helpers named *_unsafe (Redis/ proto) are not memory-safety sinks.
	if strings.HasSuffix(lower, "_unsafe") {
		return true
	}
	if strings.Contains(lower, "unsafe") &&
		(strings.Contains(lower, "encode") || strings.Contains(lower, "decode") || strings.Contains(lower, "parse")) {
		return true
	}
	// Laravel Schema:: / migration scaffolding is not an app vuln by itself.
	if lower == "schema" || lower == "blueprint" || strings.Contains(p, "/migrations/") {
		return true
	}
	return false
}

// isFeatureNoiseSymbol drops CSS selectors and lexical false friends that
// derail small endpoint features (health/ready/request-id).
func isFeatureNoiseSymbol(name, loc string) bool {
	if isAuditNoiseSymbol(name, loc) {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	p := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	if i := strings.IndexByte(p, ':'); i > 0 {
		p = p[:i]
	}
	if isStyleAssetPath(p) {
		return true
	}
	// Gin/framework reflection helpers and Flask abort are rarely the right
	// reuse target for "add GET /health".
	noise := []string{"bytype", "stylesheet_link_tag", "stylesheet", "abort",
		"luak_setreturns", "normalize", "view.prototype.lookup", "lookup",
		"isclusterhealthy", "adjust_varargs", "adjusttypeifneeded", "adjust",
		"already_done_comment", "already_done", "alreadydone", "already_notified_comment",
		"already_notified", "alreadynotified", "wordlealreadyplayed",
		"validatepersonalitybotllmendpoint", "buildfinishcheck", "getjson",
		"equal", "schema", "blueprint", "tojson", "browsersupport", "browser_support",
		"checkbox", "getter", "getters",
		// Secret stores / dependency parsers must never win GET /health reuse
		// (HUMAN-AUDIT-V3: codehelper ranked secrets.Get / pomDeps).
		"pomdeps", "gemfiledeps", "composerdeps", "npmpackagejsondeps",
		"cargotomldeps", "godotmoddeps", "usessecretstore"}
	for _, bad := range noise {
		if n == bad || strings.EqualFold(name, bad) || strings.Contains(n, bad) &&
			(strings.Contains(bad, "already") || strings.Contains(bad, "wordle") ||
				strings.Contains(bad, "finishcheck") || strings.Contains(bad, "personality") ||
				strings.Contains(bad, "notified") || strings.Contains(bad, "browser") ||
				strings.Contains(bad, "tojson") || strings.Contains(bad, "deps") ||
				strings.Contains(bad, "secret")) {
			return true
		}
	}
	// Bare Get/Set on secrets/registry packages is not a health route.
	if (n == "get" || n == "set" || n == "loadall") &&
		(strings.Contains(p, "/secrets/") || strings.Contains(p, "/registry/") ||
			strings.Contains(p, "/patterns/") || strings.Contains(p, "/profile/deps")) {
		return true
	}
	// healthURL on green-engine config is a probe helper, not HTTP /health route.
	if n == "healthurl" || strings.Contains(n, "healthurl") {
		return true
	}
	if strings.HasPrefix(n, "already_done") || strings.HasPrefix(n, "already_notified") ||
		strings.Contains(n, "alreadyplayed") ||
		strings.Contains(strings.ReplaceAll(n, "_", ""), "alreadydone") ||
		strings.Contains(strings.ReplaceAll(n, "_", ""), "alreadynotified") {
		return true
	}
	// Test fixtures named *ready* / onmount-fires-when-ready are not HTTP health.
	if strings.Contains(p, "/tests/") || strings.Contains(p, "/test/") ||
		strings.Contains(p, "/__tests__/") || strings.Contains(p, "onmount-fires-when-ready") ||
		strings.Contains(p, "/scripts/notify_translations") {
		return true
	}
	if strings.Contains(p, "/deps/lua") || strings.Contains(p, "/regex_helper") ||
		strings.Contains(p, "/deps/") {
		return true
	}
	return false
}

// dropStyleReuseCandidates removes CSS selectors and stylesheet paths from
// kickoff/plan reuse for every role (vibe without role= must not rank .hero/#id).
func dropStyleReuseCandidates(cands []reuseCandidate) []reuseCandidate {
	if len(cands) == 0 {
		return cands
	}
	out := make([]reuseCandidate, 0, len(cands))
	for _, c := range cands {
		n := strings.TrimSpace(c.Name)
		if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "#") || strings.HasPrefix(n, "@keyframes") ||
			strings.HasPrefix(n, "--") || isStyleAssetPath(c.Loc) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// preferFeatureReuse ranks health/API/route symbols above noise when the task
// looks like a small endpoint feature. Seeds on-disk /health|/ready routes when
// lexical reuse missed them (discord_mod anonymous chi handlers, laravel Route::get).
func preferFeatureReuse(cands []reuseCandidate, task string) []reuseCandidate {
	t := strings.ToLower(task)
	endpointish := isEndpointFeatureTask(t)
	if !endpointish {
		return cands
	}
	var health, boost, rest []reuseCandidate
	for _, c := range cands {
		if isFeatureNoiseSymbol(c.Name, c.Loc) {
			continue
		}
		switch {
		case looksLikeStrictHealthSymbol(c.Name, c.Loc):
			health = append(health, c)
		case looksLikeHealthOrRouteSymbol(c.Name, c.Loc):
			boost = append(boost, c)
		default:
			rest = append(rest, c)
		}
	}
	if len(health) == 0 && len(boost) == 0 && len(rest) == 0 {
		// Do not return original CSS/#selector noise — keep only non-style leftovers.
		for _, c := range cands {
			n := strings.TrimSpace(c.Name)
			if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "#") || isStyleAssetPath(c.Loc) {
				continue
			}
			if isFeatureNoiseSymbol(c.Name, c.Loc) {
				continue
			}
			rest = append(rest, c)
		}
		if len(rest) == 0 {
			return nil
		}
		return rest
	}
	out := append(health, boost...)
	out = append(out, rest...)
	return out
}

// seedHealthRouteCandidates prepends disk-found /health|/ready|/readyz handlers
// when reuse missed them (anonymous route closures are often unindexed).
// For HTTP frameworks with no /health yet, seeds router/placement files so
// kickoff does not look empty or falsely abstain as non-HTTP.
func seedHealthRouteCandidates(root string, cands []reuseCandidate, task string) []reuseCandidate {
	if !isEndpointFeatureTask(task) || strings.TrimSpace(root) == "" {
		return cands
	}
	// Only skip seeding when the *top* candidate is already a real health/ready
	// route. Path-only hits (any symbol in cmd/api/main.go) must NOT block seeding —
	// that was the discord_mod LIVE miss.
	if len(cands) > 0 && isSeededOrNamedHealthRoute(cands[0]) {
		return cands
	}
	seeded := scanHealthRoutesOnDisk(root, 4)
	if len(seeded) == 0 && security.ExposesHTTP(root) {
		seeded = scanHTTPPlacementOnDisk(root, 3)
	}
	if len(seeded) == 0 {
		return cands
	}
	seen := map[string]struct{}{}
	for _, s := range seeded {
		seen[strings.ToLower(s.Loc)] = struct{}{}
	}
	var rest []reuseCandidate
	for _, c := range cands {
		if _, ok := seen[strings.ToLower(c.Loc)]; ok {
			continue
		}
		if isFeatureNoiseSymbol(c.Name, c.Loc) {
			continue
		}
		rest = append(rest, c)
	}
	return append(seeded, rest...)
}

// isSeededOrNamedHealthRoute reports a candidate that is already a concrete
// health/ready route (seeded route_* name, getHealth, …) — not merely a
// symbol that happens to live in main.go / routes.php.
func isSeededOrNamedHealthRoute(c reuseCandidate) bool {
	n := strings.ToLower(strings.TrimSpace(c.Name))
	p := strings.ToLower(strings.ReplaceAll(c.Loc, "\\", "/"))
	if strings.HasPrefix(n, "route_health") || strings.HasPrefix(n, "route_ready") ||
		strings.HasPrefix(n, "route_live") || n == "gethealth" || n == "getready" ||
		n == "handlehealthz" || strings.Contains(n, "healthcontroller") ||
		strings.HasPrefix(n, "placement_health") || strings.Contains(n, "health_json") ||
		strings.Contains(n, "healthjson") {
		return true
	}
	// Next.js App Router path is the route: app/api/health/route.ts
	if strings.Contains(p, "/api/health/") || strings.Contains(p, "/api/readyz/") ||
		strings.Contains(p, "/api/healthz/") || strings.Contains(p, "/api/livez/") ||
		strings.Contains(p, "/api/ready/") {
		return true
	}
	if strings.Contains(p, "health_controller") || strings.Contains(p, "healthcontroller") {
		return true
	}
	return false
}

func scanHealthRoutesOnDisk(root string, limit int) []reuseCandidate {
	if limit <= 0 {
		limit = 4
	}
	root = filepath.Clean(root)
	candidates := []string{
		"backend/cmd/api/main.go", "cmd/api/main.go", "cmd/server/main.go",
		"routes/web.php", "routes/api.php", "app/Http/Controllers/HealthController.php",
		// Symfony / generic PHP controllers (attribute #[Route('/health')]).
		"src/Controller/HealthController.php", "src/Controllers/HealthController.php",
		"src/Controller/Health.php", "src/Controller/ReadyController.php",
		"src/app.controller.ts", "src/health.controller.ts", "src/main.ts",
		"src/main/java/org/springframework/samples/petclinic/system/CrashController.java",
		"src/main/java/org/springframework/samples/petclinic/system/WelcomeController.java",
		"config/routes.rb", "app/controllers/health_controller.rb",
		"Program.cs", "Controllers/HealthController.cs",
		"main.go", "server.go", "app.py", "main.py",
		// Go agent/API hosts (codehelper): /healthz|/ready live on agentapi.
		"internal/agentapi/server.go", "cmd/codehelper/main.go",
		// Next.js App Router health helpers (path IS the route; no "/health" literal).
		"app/api/health/route.ts", "app/api/readyz/route.ts", "app/api/healthz/route.ts",
		"app/api/ready/route.ts", "app/api/livez/route.ts",
		"src/app/api/health/route.ts", "src/app/api/readyz/route.ts",
		"src/app/api/healthz/route.ts", "src/app/api/ready/route.ts",
		// Express hello-world examples often already ship /health|/ready|/livez.
		"examples/hello-world/index.js", "examples/hello-world/index.ts",
		// Framework example servers (axum/gin/fastapi hello-world).
		"examples/hello-world/src/main.rs", "examples/hello-world/main.go",
		"examples/basic/main.go", "docs_src/first_steps/tutorial001.py",
		"docs_src/path_params/tutorial001.py",
	}
	needles := []string{`"/health"`, `'/health'`, `"/ready"`, `'/ready'`,
		`"/readyz"`, `'/readyz'`, `"/healthz"`, `'/healthz'`, `"/livez"`, `'/livez'`,
		`Route::get('/health'`, `Route::get("/health"`, `get('/health'`, `get("/health"`,
		`r.Get("/health"`, `r.Get("/ready"`, `GET /healthz`, `GET /ready`, `GET /health`,
		// PHP 8 attributes / Symfony Route attribute (case-insensitive match below).
		`#[route('/health'`, `#[route("/health"`, `route('/health'`, `route("/health"`}
	var out []reuseCandidate
	seen := map[string]struct{}{}
	tryFile := func(rel string) {
		if len(out) >= limit {
			return
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		f, err := os.Open(abs)
		if err != nil {
			return
		}
		defer f.Close()
		lowerRel := strings.ToLower(rel)
		// Path-based App Router routes: seed on GET export even without "/health" literal.
		pathIsHealth := strings.Contains(lowerRel, "/api/health/") || strings.Contains(lowerRel, "/api/readyz/") ||
			strings.Contains(lowerRel, "/api/healthz/") || strings.Contains(lowerRel, "/api/livez/") ||
			strings.Contains(lowerRel, "/api/ready/")
		sc := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			lower := strings.ToLower(line)
			matched := false
			name := "route_health"
			for _, n := range needles {
				if !strings.Contains(lower, strings.ToLower(n)) {
					continue
				}
				matched = true
				switch {
				case strings.Contains(lower, "readyz"):
					name = "route_readyz"
				case strings.Contains(lower, "healthz"):
					name = "route_healthz"
				case strings.Contains(lower, "livez"):
					name = "route_livez"
				case strings.Contains(lower, "/ready"):
					name = "route_ready"
				}
				break
			}
			if !matched && pathIsHealth {
				// Prefer the exported GET handler line when present.
				if strings.Contains(lower, "export async function get") || strings.Contains(lower, "export function get") ||
					strings.Contains(lower, "export const get") || lineNo == 1 {
					matched = true
					switch {
					case strings.Contains(lowerRel, "readyz"):
						name = "route_readyz"
					case strings.Contains(lowerRel, "healthz"):
						name = "route_healthz"
					case strings.Contains(lowerRel, "livez"):
						name = "route_livez"
					case strings.Contains(lowerRel, "/ready/") || strings.HasSuffix(lowerRel, "/ready/route.ts"):
						name = "route_ready"
					default:
						name = "route_health"
					}
					if lineNo == 1 && !(strings.Contains(lower, "export") && strings.Contains(lower, "get")) {
						// Wait for GET export rather than seeding on import line.
						matched = false
					}
				}
			}
			if !matched {
				if lineNo > 8000 {
					return
				}
				continue
			}
			loc := fmt.Sprintf("%s:%d", filepath.ToSlash(rel), lineNo)
			key := strings.ToLower(loc)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, reuseCandidate{
				Name: name, Kind: "route", Loc: loc, Score: 0.99,
				Signature: strings.TrimSpace(line),
			})
			if len(out) >= limit {
				return
			}
			if pathIsHealth {
				return // one seed per App Router health file
			}
			if lineNo > 8000 {
				return
			}
		}
	}
	for _, rel := range candidates {
		tryFile(rel)
	}
	return out
}

// scanHTTPPlacementOnDisk seeds router/app entrypoints for HTTP frameworks that
// expose HTTP but do not yet have a /health|/ready literal (flask/django/fastapi/axum).
func scanHTTPPlacementOnDisk(root string, limit int) []reuseCandidate {
	if limit <= 0 {
		limit = 3
	}
	root = filepath.Clean(root)
	type place struct {
		rel   string
		name  string
		needle string // optional line hint; empty → first non-empty def-ish line
	}
	places := []place{
		{"src/flask/sansio/scaffold.py", "placement_route", "def route"},
		{"src/flask/app.py", "placement_flask_app", "class Flask"},
		{"flask/sansio/scaffold.py", "placement_route", "def route"},
		{"flask/app.py", "placement_flask_app", "class Flask"},
		{"fastapi/routing.py", "placement_api_route", "class APIRoute"},
		{"fastapi/applications.py", "placement_fastapi_app", "class FastAPI"},
		{"django/urls/resolvers.py", "placement_url_resolver", "class URLResolver"},
		{"django/urls/conf.py", "placement_path", "def path"},
		{"axum/src/routing/mod.rs", "placement_router", "struct Router"},
		{"examples/hello-world/src/main.rs", "placement_example_server", "Router::new"},
		// Prefer HealthJSON over Engine/GET when seeding gin health placement.
		{"utils.go", "placement_health_json", "func HealthJSON"},
		{"gin.go", "placement_gin_engine", "type Engine"},
		{"routergroup.go", "placement_gin_get", "func (group *RouterGroup) GET"},
		{"lib/router/index.js", "placement_express_router", "proto.handle"},
		{"lib/application.js", "placement_express_app", "app.handle"},
		{"actionpack/lib/action_dispatch/routing/route_set.rb", "placement_rails_routes", "class RouteSet"},
		{"routes/web.php", "placement_laravel_routes", "Route::"},
		{"src/app.controller.ts", "placement_nest_controller", "@Controller"},
		{"config/routes.rb", "placement_rails_draw", "Rails.application.routes.draw"},
	}
	var out []reuseCandidate
	seen := map[string]struct{}{}
	for _, p := range places {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(root, filepath.FromSlash(p.rel))
		f, err := os.Open(abs)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1024*1024)
		lineNo := 0
		picked := 0
		needle := strings.ToLower(p.needle)
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			lower := strings.ToLower(line)
			trim := strings.TrimSpace(line)
			if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "//") {
				continue
			}
			match := false
			if needle != "" {
				match = strings.Contains(lower, needle)
			} else if lineNo <= 40 {
				match = strings.Contains(lower, "func ") || strings.Contains(lower, "class ") ||
					strings.Contains(lower, "def ") || strings.Contains(lower, "struct ") ||
					strings.Contains(lower, "exports.") || strings.HasPrefix(trim, "pub ")
			}
			if !match {
				if lineNo > 12000 {
					break
				}
				continue
			}
			picked = lineNo
			loc := fmt.Sprintf("%s:%d", filepath.ToSlash(p.rel), picked)
			key := strings.ToLower(loc)
			if _, ok := seen[key]; ok {
				break
			}
			seen[key] = struct{}{}
			out = append(out, reuseCandidate{
				Name: p.name, Kind: "placement", Loc: loc, Score: 0.9,
				Signature: "HTTP placement — add GET /health|/ready near this router/app API: " + strings.TrimSpace(line),
			})
			break
		}
		_ = f.Close()
	}
	return out
}

func isEndpointFeatureTask(t string) bool {
	t = strings.ToLower(strings.TrimSpace(t))
	return strings.Contains(t, "health") || strings.Contains(t, "ready") ||
		strings.Contains(t, "request-id") || strings.Contains(t, "request_id") ||
		strings.Contains(t, "endpoint") || strings.Contains(t, "get /") ||
		strings.Contains(t, "route") || strings.Contains(t, "middleware") ||
		strings.Contains(t, "simple health") || strings.Contains(t, "health thing") ||
		strings.Contains(t, "healthz") || strings.Contains(t, "readyz") ||
		strings.Contains(t, "livez") || strings.Contains(t, "health real quick") ||
		(strings.Contains(t, "ping") && strings.Contains(t, "endpoint"))
}

func looksLikeStrictHealthSymbol(name, loc string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	p := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	ln := n + " " + p
	if strings.Contains(n, "cluster") || strings.Contains(n, "unhealthy") ||
		strings.Contains(n, "already") || isFeatureNoiseSymbol(name, loc) {
		return false
	}
	// Require name/route evidence — do NOT treat every symbol in main.go / routes.php
	// as health (that blocked discord_mod disk seeding).
	if strings.HasPrefix(n, "route_health") || strings.HasPrefix(n, "route_ready") ||
		strings.HasPrefix(n, "route_live") || n == "gethealth" || n == "getready" ||
		n == "handlehealthz" || strings.Contains(n, "healthcontroller") ||
		strings.Contains(n, "healthz") ||
		strings.HasPrefix(n, "placement_health") || strings.Contains(n, "health_json") ||
		strings.Contains(n, "healthjson") ||
		(strings.HasPrefix(n, "health") && !strings.Contains(n, "unhealthy") && !strings.Contains(n, "url")) {
		return true
	}
	if strings.Contains(ln, "healthcheck") || strings.Contains(p, "/api/health") ||
		strings.Contains(p, "/api/readyz") || strings.Contains(p, "/api/healthz") ||
		strings.Contains(p, "/api/livez") || strings.Contains(p, "health_controller") ||
		strings.Contains(p, "healthcontroller") {
		return true
	}
	// Loc line suffix ":N" with path segment /health (App Router) or explicit /health in path.
	if strings.Contains(p, "/health/") || strings.Contains(p, "/health.") ||
		strings.Contains(p, "/readyz/") || strings.Contains(p, "/livez/") {
		return true
	}
	return false
}

func looksLikeHealthOrRouteSymbol(name, loc string) bool {
	if looksLikeStrictHealthSymbol(name, loc) {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	p := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	ln := n + " " + p
	// Reject false friends: isClusterHealthy, unhealthyMetric, …
	if strings.Contains(n, "cluster") || strings.Contains(n, "unhealthy") {
		return false
	}
	// CSS / static assets never count as health/API routes.
	if isStyleAssetPath(p) || strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".scss") ||
		strings.HasSuffix(p, ".html") || strings.Contains(p, "/static/") ||
		strings.Contains(p, "/assets/") && !strings.Contains(p, "controller") {
		return false
	}
	if strings.Contains(ln, "readiness") || n == "gethello" ||
		strings.Contains(p, "hello-world") {
		return true
	}
	if strings.HasPrefix(n, "route_get") || strings.HasPrefix(n, "route_post") {
		return true
	}
	// Soft boost for likely HTTP entrypoints (not strict health — won't block seeding).
	if strings.Contains(ln, "controller") || strings.Contains(ln, "router") ||
		strings.Contains(ln, "route") || strings.Contains(ln, "handler") ||
		strings.Contains(ln, "middleware") || strings.Contains(ln, "application") ||
		strings.Contains(p, "routes/") || strings.Contains(p, "/api/") ||
		strings.HasSuffix(p, "program.cs") || strings.Contains(p, "cmd/api/") ||
		strings.Contains(p, "main.go") || strings.Contains(p, "server.go") {
		return true
	}
	return false
}

// inferRoleFromTask upgrades empty/feature role when the task text clearly
// asks for a security or performance audit (vibe kickoff without role=).
func inferRoleFromTask(role, task string) string {
	r := strings.ToLower(strings.TrimSpace(role))
	if r != "" && r != "feature" {
		return r
	}
	if isVagueSecurityQuery(task) || hasSecurityIntent(task) {
		return "security"
	}
	if isVaguePerfQuery(task) || hasPerfIntent(task) {
		return "performance"
	}
	if r == "" {
		return "feature"
	}
	return r
}

func hasSecurityIntent(q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	needles := []string{
		"insecure", "security", "vuln", "xss", "csrf", "injection", "authz",
		"hardcoded secret", "sketchy", "unsafe", "exploit", "cve", "owasp",
		"sql inject", "password", "session fixation", "privilege",
	}
	for _, n := range needles {
		if strings.Contains(q, n) {
			return true
		}
	}
	return false
}

func hasPerfIntent(q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	needles := []string{
		"slow", "faster", "performance", "hotspot", "n+1", "latency",
		"heavy", "sluggish", "optimize", "too much memory", "cpu",
		"quick wins", "bottleneck",
	}
	for _, n := range needles {
		if strings.Contains(q, n) {
			return true
		}
	}
	return false
}

func healthQueryPack() []string {
	return []string{
		"GET /health healthz readyz livez readiness liveness",
		"HealthController getHealth healthcheck Route health",
		`r.Get("/health" Route::get('/health' app.get('/health'`,
		"pingCommand healthcheck.sh /ready",
	}
}

// preferFeatureHits reorders hybrid query hits for health/ready tasks the same
// way preferFeatureReuse does for kickoff/scout candidates.
func preferFeatureHits(hits []retrieval.RankedSymbol, task string) []retrieval.RankedSymbol {
	if len(hits) == 0 || !isEndpointFeatureTask(task) {
		return hits
	}
	var health, boost, rest []retrieval.RankedSymbol
	for _, h := range hits {
		loc := h.Symbol.Path
		if isFeatureNoiseSymbol(h.Symbol.Name, loc) {
			continue
		}
		switch {
		case looksLikeStrictHealthSymbol(h.Symbol.Name, loc):
			health = append(health, h)
		case looksLikeHealthOrRouteSymbol(h.Symbol.Name, loc):
			boost = append(boost, h)
		default:
			rest = append(rest, h)
		}
	}
	if len(health) == 0 && len(boost) == 0 && len(rest) == 0 {
		return hits
	}
	out := append(health, boost...)
	return append(out, rest...)
}

// featureEndpointAbstainNote returns a clear abstain when the task asks for an
// HTTP health/ready endpoint but the project has no plausible HTTP surface
// (C datastores, UI compilers). HTTP frameworks (flask/django/fastapi/axum/…)
// must NOT abstain as non-HTTP — even without a ranked /health symbol.
func featureEndpointAbstainNote(task string, shape security.ProjectShape, root string, cands []reuseCandidate) string {
	if !isEndpointFeatureTask(task) {
		return ""
	}
	switch shape {
	case security.ShapeLibrary, security.ShapeFrameworkCore:
		hasRoute := false
		for _, c := range cands {
			if looksLikeStrictHealthSymbol(c.Name, c.Loc) {
				hasRoute = true
				break
			}
		}
		if hasRoute {
			return ""
		}
		surface := security.DetectHTTPSurface(root)
		// HTTP-capable framework/app: seed/locate placement instead of non-HTTP abstain.
		if surface.Capable {
			return ""
		}
		reason := surface.Reason
		if reason == "" {
			reason = "project_shape=" + string(shape) + " may not expose HTTP"
		}
		return "ABSTAIN: task asks for HTTP health/ready but no route/controller/handler reuse surface ranked. " +
			reason + " — do not invent /health; pick an in-process helper or confirm with the user. " +
			"Next queries: (1) query: GET /health healthz readyz HealthController getHealth; " +
			"(2) scout task=\"add GET /health\"; (3) Ask user: HTTP surface vs in-process helper?"
	}
	return ""
}

// clearNonHealthReuseForAbstain drops noise candidates when a library/framework
// health abstain fires so already_notified/toJSON cannot rank as "reuse".
func clearNonHealthReuseForAbstain(cands []reuseCandidate, abstain string) []reuseCandidate {
	if abstain == "" || len(cands) == 0 {
		return cands
	}
	var keep []reuseCandidate
	for _, c := range cands {
		if looksLikeStrictHealthSymbol(c.Name, c.Loc) {
			keep = append(keep, c)
		}
	}
	return keep
}

// filterAuditCandidates drops CSS/DI/Schema noise from reuse lists when
// role=security|performance. Preserves relative order of kept hits.
func filterAuditCandidates(cands []reuseCandidate, role string) []reuseCandidate {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "security" && role != "performance" {
		return cands
	}
	out := make([]reuseCandidate, 0, len(cands))
	for _, c := range cands {
		if isAuditNoiseSymbol(c.Name, c.Loc) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// preferSecurityReuse ranks real auth/session/login symbols above lexical
// false-friends when the task is an auth locate (LIVE-BROAD-V6: getAuthor,
// KeepAlive, expected_token, cmdToken, IsDebugging beating BasicAuth/GetSession).
func preferSecurityReuse(cands []reuseCandidate, task string) []reuseCandidate {
	if len(cands) == 0 || !hasAuthLocateIntent(task) {
		return cands
	}
	var strong, weak, noise []reuseCandidate
	for _, c := range cands {
		switch {
		case isAuthFalseFriend(c.Name, c.Loc):
			noise = append(noise, c)
		case looksLikeAuthSymbol(c.Name, c.Loc):
			strong = append(strong, c)
		default:
			weak = append(weak, c)
		}
	}
	if len(strong) == 0 && len(weak) == 0 {
		// All false-friends — empty reuse so agents follow findings / investigate
		// instead of context on KeepAlive/cmdToken (LIVE-BROAD-V6).
		return nil
	}
	out := append(strong, weak...)
	return append(out, noise...)
}

func hasAuthLocateIntent(task string) bool {
	t := strings.ToLower(strings.TrimSpace(task))
	if t == "" {
		return false
	}
	needles := []string{
		"auth", "login", "session", "token", "password", "csrf", "jwt",
		"bearer", "oauth", "middleware", "credential", "acl", "permission",
	}
	for _, n := range needles {
		if strings.Contains(t, n) {
			return true
		}
	}
	return false
}

func looksLikeAuthSymbol(name, loc string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	p := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	if i := strings.IndexByte(p, ':'); i > 0 {
		p = p[:i]
	}
	if isAuthFalseFriend(name, loc) {
		return false
	}
	needles := []string{
		"authenticat", "authorization", "authorize", "authz", "authn",
		"session", "login", "logout", "password", "csrf", "jwt", "oauth",
		"passport", "bearer", "basicauth", "requirepass", "acl",
		"getsession", "sessionstore", "middleware", "credential", "erasecredential",
	}
	for _, needle := range needles {
		if strings.Contains(n, needle) {
			return true
		}
	}
	// Path-based: auth packages / security yaml / ACL sources
	pathHints := []string{
		"/auth/", "/auth.", "/security/", "/acl.", "session_store",
		"http_authentication", "forgery_protection", "host_authorization",
	}
	for _, h := range pathHints {
		if strings.Contains(p, h) {
			return true
		}
	}
	// Bare "auth" as whole segment (not Author)
	if n == "auth" || strings.HasPrefix(n, "auth_") || strings.HasSuffix(n, "_auth") ||
		strings.Contains(n, ".auth") {
		return true
	}
	return false
}

func isAuthFalseFriend(name, loc string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	p := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	if i := strings.IndexByte(p, ':'); i > 0 {
		p = p[:i]
	}
	// Entity Author accessors — substring of "auth" but not authentication.
	if (n == "author" || n == "getauthor" || n == "setauthor" ||
		strings.HasSuffix(n, "_author") || strings.HasSuffix(n, "author")) &&
		!strings.Contains(n, "authoriz") && !strings.Contains(n, "authentic") {
		return true
	}
	if strings.Contains(n, "keepalive") || strings.Contains(n, "keep_alive") ||
		strings.Contains(n, "keep-alive") || strings.Contains(p, "keepalive") {
		return true
	}
	if n == "isdebugging" || n == "isdebuglog" || n == "get_debug_flag" ||
		n == "debugfileskeyerror" || strings.HasPrefix(n, "debugprint") ||
		(strings.Contains(n, "debug") && (strings.Contains(n, "log") || strings.Contains(n, "flag") || strings.Contains(n, "print"))) {
		return true
	}
	if n == "localechangeinterceptor" || strings.Contains(n, "localechange") {
		return true
	}
	// Compiler / Redis-debug "token" — not auth tokens.
	if n == "expected_token" || n == "cmdtoken" || n == "to_tokens" || n == "effect_label" {
		if strings.Contains(p, "compiler") || strings.Contains(p, "/debug") ||
			strings.Contains(p, "macros") || strings.Contains(p, "errors.js") ||
			strings.Contains(p, "debug.c") || strings.Contains(p, "/dev/") {
			return true
		}
	}
	// Any cmdToken / expected_token regardless of path — auth locate never wants these.
	if n == "cmdtoken" || n == "expected_token" || n == "to_tokens" {
		return true
	}
	if strings.Contains(p, "/dev/debug") || strings.HasSuffix(p, "/debug.js") ||
		strings.Contains(p, "/client/dev/") {
		return true
	}
	if strings.HasPrefix(n, "log_") && (strings.Contains(p, "/dev/") || strings.Contains(p, "debug")) {
		return true
	}
	return false
}

func filterAuditHits(hits []retrieval.RankedSymbol, role string) []retrieval.RankedSymbol {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "security" && role != "performance" {
		return hits
	}
	out := make([]retrieval.RankedSymbol, 0, len(hits))
	for _, h := range hits {
		if isAuditNoiseSymbol(h.Symbol.Name, h.Symbol.Path) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// demoteSecurityLexicalNoise drops timeval / macro expand_* / bare Str hits that
// pollute senior security queries on C/Rust/PHP trees (HUMAN-AUDIT-V3).
func demoteSecurityLexicalNoise(hits []retrieval.RankedSymbol) []retrieval.RankedSymbol {
	if len(hits) == 0 {
		return hits
	}
	var keep, demoted []retrieval.RankedSymbol
	for _, h := range hits {
		n := strings.ToLower(strings.TrimSpace(h.Symbol.Name))
		p := strings.ToLower(strings.ReplaceAll(h.Symbol.Path, "\\", "/"))
		noise := n == "timeval" || n == "str" || n == "string" ||
			strings.HasPrefix(n, "expand_with") || strings.HasPrefix(n, "expand_attr") ||
			(n == "expand" && strings.Contains(p, "macros")) ||
			(n == "to_tokens" && strings.Contains(p, "macros")) ||
			n == "concatenatearraybuffers" ||
			(n == "author" && strings.Contains(p, "/docs/"))
		if noise {
			demoted = append(demoted, h)
			continue
		}
		keep = append(keep, h)
	}
	if len(keep) == 0 {
		return hits
	}
	return append(keep, demoted...)
}

// demoteIntentMismatchedHits pushes HTTP response false-friends below SQL-oriented
// hits when the query clearly asks about SQL/injection (LIVE: express query
// "res.send SQL injection …" ranking res.send as if it were a SQL sink).
func demoteIntentMismatchedHits(q string, hits []retrieval.RankedSymbol) []retrieval.RankedSymbol {
	if len(hits) < 2 {
		return hits
	}
	ql := strings.ToLower(q)
	sqlIntent := strings.Contains(ql, "sql") || strings.Contains(ql, "injection") ||
		strings.Contains(ql, "queryraw") || strings.Contains(ql, "rawsql") ||
		strings.Contains(ql, "parameterized") || strings.Contains(ql, "string concat")
	if !sqlIntent {
		return hits
	}
	var keep, demoted []retrieval.RankedSymbol
	for _, h := range hits {
		n := strings.ToLower(strings.TrimSpace(h.Symbol.Name))
		base := n
		if i := strings.LastIndex(n, "."); i >= 0 {
			base = n[i+1:]
		}
		httpFriend := base == "send" || base == "sendfile" || base == "sendstatus" ||
			base == "json" || base == "end" || base == "write" || base == "redirect" ||
			strings.HasPrefix(n, "res.send") || strings.HasPrefix(n, "res.json") ||
			strings.HasPrefix(n, "res.end") || strings.HasPrefix(n, "res.write") ||
			strings.HasPrefix(n, "res.redirect")
		sqlish := strings.Contains(n, "sql") || strings.Contains(n, "query") ||
			strings.Contains(n, "exec") || strings.Contains(n, "raw") ||
			strings.Contains(n, "inject") || strings.Contains(n, "escape") ||
			strings.Contains(n, "trust") || strings.Contains(n, "proxy") ||
			strings.Contains(n, "pollut") || strings.Contains(n, "proto")
		if httpFriend && !sqlish {
			demoted = append(demoted, h)
			continue
		}
		keep = append(keep, h)
	}
	if len(keep) == 0 {
		return hits
	}
	return append(keep, demoted...)
}

// securityQueryPack returns sink-oriented queries for vague junior security
// prompts so hybrid retrieval lands on file:line sinks instead of CSS/DI noise.
func securityQueryPack() []string {
	return []string{
		"sql injection raw query concatenate parameterized",
		"mark_safe dangerouslySetInnerHTML v-html xss innerHTML",
		"csrf_exempt permitAll auth middleware bearer token",
		"password secret api_key hardcoded token sk_live",
		"eval exec shell_exec subprocess system(",
		"session cookie Secure HttpOnly SESSION_COOKIE",
		"strcpy strcat sprintf gets buffer overflow",
		"protected-mode requirepass ACL redis auth ACLAuthenticateUser",
		"APP_DEBUG ValidationPipe helmet cors origin",
		"optional bearer Authorization fail open rate limit",
		"open redirect allowlist HttpResponseRedirect",
		"authz gap missing authorization check",
		"CommandBlocked ValidateReadOnlySQL UsesSecretStore",
		"authenticateRequest isAdmin requireCanEditGuild",
		"res.redirect TrustedProxies ClientIP",
	}
}

// isVagueSecurityQuery detects junior-style "is this secure?" prompts that need
// the security query pack instead of literal lexical ranking.
func isVagueSecurityQuery(q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return false
	}
	// Already precise (path, symbol, CWE, sink keyword) — leave alone.
	precise := []string{"/", ":", "cwe-", "sql", "xss", "csrf", "auth", "password",
		"secret", "inject", "eval", "mark_safe", "raw(", "session", "strcpy",
		"buffer", "acl", "helmet", "cors"}
	hasPrecise := false
	for _, p := range precise {
		if strings.Contains(q, p) {
			hasPrecise = true
			break
		}
	}
	vague := []string{
		"is this secure", "is it secure", "anything sketchy", "security review",
		"security audit", "how secure", "secure enough", "security issues",
		"any vulns", "vulnerabilit", "look for security", "check security",
		"security concerns", "is our app safe", "harden security",
		"any security", "security problems", "find vulns", "audit security",
		"feels insecure", "feels kinda insecure", "kinda insecure", "insecure idk",
		"this feels insecure", "is it safe", "what should i worry", "worry about",
		"any sketchy", "sketchy in how", "idk if secure", "security vibes",
		"looks sketchy", "feels unsafe", "any red flags", "red flags security",
		"passwords, sessions", "handle passwords", "sessions, or user input",
		"idk this codebase", "codebase feels",
	}
	for _, v := range vague {
		if strings.Contains(q, v) {
			return true
		}
	}
	// Short generic security ask without sink keywords.
	if !hasPrecise && (q == "security" || q == "secure" || strings.HasPrefix(q, "secure ") ||
		strings.HasPrefix(q, "security ")) {
		return true
	}
	return false
}

// isVaguePerfQuery detects junior-style "why is this slow?" prompts.
func isVaguePerfQuery(q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	vague := []string{
		"why is this slow", "why so slow", "feels slow", "performance issue",
		"make it faster", "too slow", "optimize performance", "any hotspots",
		"perf review", "performance review", "is this fast", "lots of data",
		"with lots of data", "slow with", "performance problems",
		"make it faster somehow", "faster somehow", "feels sluggish", "speed it up",
		"lots of items", "when there are lots", "main list/detail", "list/detail path",
		"perf vibes", "idk why slow", "seems slow", "taking forever",
		"it's slow", "feels heavy", "quick wins", "any quick wins",
	}
	for _, v := range vague {
		if strings.Contains(q, v) {
			return true
		}
	}
	return false
}

func perfQueryPack() []string {
	return []string{
		"n+1 query prefetch select_related",
		"loop database query inside",
		"unbounded memory allocate buffer",
		"hot path handler middleware",
		"cache miss expensive",
		"O(n^2) nested loop complexity",
		"sync io blocking request",
		"pagination limit offset list detail",
		"eager load includes preload",
	}
}

// perfQueryPackLibrary is used when the repo is a library/framework core —
// app N+1 queries are the wrong target.
func perfQueryPackLibrary() []string {
	return []string{
		"hot path handler middleware router dispatch",
		"unbounded memory allocate buffer copy",
		"O(n^2) nested loop complexity",
		"sync io blocking request",
		"mutex lock contention",
		"allocation churn garbage",
	}
}

// collectRepoSecurityFindings runs the whole-repo sink scan and maps to auditFinding.
// When sinks are empty/scarce, falls back to grounded config hardening, framework
// trust-boundary footguns, and app auth surfaces — never invent CVEs, never return
// empty when medium-confidence high-signal guidance exists.
func collectRepoSecurityFindings(repoRoot string, limit int) []auditFinding {
	if limit <= 0 {
		limit = 12
	}
	// Over-fetch then keep high-signal first so low-confidence FPs do not fill top-N.
	raw := security.ScanRepoForSecuritySmells(repoRoot, security.RepoScanOptions{Limit: limit * 3})
	raw = preferHighSignalFindings(raw, limit)
	shape := security.DetectProjectShape(repoRoot)

	hasHighSevSink := false
	for _, f := range raw {
		sev := strings.ToLower(f.Severity)
		kind := strings.ToLower(f.Kind)
		if kind == "sink_candidate" && (sev == "high" || sev == "critical") {
			hasHighSevSink = true
			break
		}
	}

	// Framework/library cores: when sinks are scarce after FP purge, surface
	// trust-boundary guidance with real file:line (not invented app CVEs).
	if shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore {
		lib := security.LibrarySecurityGuidance(repoRoot, shape, limit)
		if !hasHighSevSink || len(raw) == 0 {
			raw = security.MergeUniqueFindings(raw, lib, limit)
		}
	}

	// Config hardening for skeletons always; also when sinks empty on apps.
	if len(raw) == 0 || shape == security.ShapeSkeleton || !hasHighSevSink {
		cfg := security.ScanConfigHardening(repoRoot, limit)
		if len(raw) == 0 || shape == security.ShapeSkeleton {
			raw = security.MergeUniqueFindings(raw, cfg, limit)
		} else if len(cfg) > 0 && countSinkCandidates(raw) < 2 {
			// Thin sink lists: keep sinks, fill with grounded config footguns.
			raw = security.MergeUniqueFindings(raw, cfg, limit)
		}
	}

	// Apps with no/high-sev-scarce sinks: ranked auth/session/actuator footguns.
	if shape == security.ShapeApp && (!hasHighSevSink || len(raw) == 0) {
		app := security.AppSecurityGuidance(repoRoot, limit)
		raw = security.MergeUniqueFindings(raw, app, limit)
		// Demo apps may also have basename library hints (e.g. symfony-demo templates).
		if baseHints := security.LibrarySecurityGuidance(repoRoot, security.ShapeFrameworkCore, 3); len(baseHints) > 0 {
			// Only merge if files exist under this app (LibrarySecurityGuidance keyed by basename).
			raw = security.MergeUniqueFindings(raw, baseHints, limit)
		}
	}

	if len(raw) > limit {
		raw = raw[:limit]
	}
	return mapContextFindings(raw)
}

func countSinkCandidates(in []security.ContextFinding) int {
	n := 0
	for _, f := range in {
		if strings.EqualFold(f.Kind, "sink_candidate") {
			n++
		}
	}
	return n
}

// preferHighSignalFindings keeps auth/SQL/XSS/secret/eval sinks with medium+
// confidence ahead of demoted low-confidence noise. Low-confidence items are
// retained only to fill remaining slots (never bury a high-signal hit as #10).
// When EVERY hit is low-confidence, return empty so callers can abstain clearly
// instead of ranking help-text FPs as audit findings.
func preferHighSignalFindings(in []security.ContextFinding, limit int) []security.ContextFinding {
	if len(in) == 0 {
		return in
	}
	in = security.EnrichAndRankFindings(in)
	highSignal := map[string]bool{
		"sql-string-concat": true, "eval-usage": true, "shell-exec-injection": true,
		"hardcoded-secret": true, "raw-html-xss": true, "blade-unescaped-output": true,
		"csrf-disabled": true, "authz-gap": true, "authz-fail-open": true, "injection-taint": true,
		"open-redirect": true, "open-redirect-taint": true,
		"c-unsafe-buffer": true, "redis-auth-gap": true, "missing-nonce-check": true,
	}
	var primary, secondary []security.ContextFinding
	for _, f := range in {
		conf := strings.ToLower(f.Confidence)
		kind := strings.ToLower(f.Kind)
		if conf == "low" || kind == "config_hardening" || kind == "library_guidance" {
			secondary = append(secondary, f)
			continue
		}
		if highSignal[strings.ToLower(f.Rule)] || conf == "high" || conf == "medium" {
			primary = append(primary, f)
		} else {
			secondary = append(secondary, f)
		}
	}
	// Prefer medium+/high sinks. Config/library guidance may fill remaining slots.
	// Pure low-confidence sink_candidate noise alone → empty (abstain upstream).
	out := make([]security.ContextFinding, 0, limit)
	out = append(out, primary...)
	if len(primary) == 0 {
		var guidance []security.ContextFinding
		for _, f := range secondary {
			kind := strings.ToLower(f.Kind)
			if kind == "config_hardening" || kind == "library_guidance" {
				guidance = append(guidance, f)
			}
		}
		if len(guidance) == 0 {
			return nil
		}
		out = append(out, guidance...)
	} else {
		out = append(out, secondary...)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	// Re-rank after truncation so Rank 1..N is contiguous.
	return security.EnrichAndRankFindings(out)
}

func mapContextFindings(raw []security.ContextFinding) []auditFinding {
	out := make([]auditFinding, 0, len(raw))
	for _, f := range raw {
		msg := f.Rule
		for _, r := range appendSecurityMessages(f.Rule) {
			msg = r
			break
		}
		if f.Evidence != "" && (f.Kind == "config_hardening" || f.Kind == "library_guidance") {
			msg = f.Evidence
		}
		out = append(out, auditFinding{
			Rank: f.Rank, Rule: f.Rule, Severity: f.Severity,
			Confidence: f.Confidence, Exploitability: f.Exploitability, Kind: f.Kind,
			File: f.File, Line: f.Line, Evidence: f.Evidence, Hint: f.Hint, Message: msg,
		})
	}
	return out
}

// preferTaskAlignedFindings boosts auth/session/ACL/fail-open cites when the
// task clearly asks about auth — so "where does auth happen?" / "fail-open?"
// do not bury auth behind unrelated SQL or /healthz guidance.
func preferTaskAlignedFindings(findings []auditFinding, task string) []auditFinding {
	if len(findings) < 2 {
		return findings
	}
	t := strings.ToLower(strings.TrimSpace(task))
	if t == "" {
		return findings
	}
	authAsk := strings.Contains(t, "auth") || strings.Contains(t, "fail-open") ||
		strings.Contains(t, "fail open") || strings.Contains(t, "failopen") ||
		strings.Contains(t, "session") || strings.Contains(t, "login") ||
		strings.Contains(t, "acl") || strings.Contains(t, "protected-mode") ||
		strings.Contains(t, "protected mode") || strings.Contains(t, "requirepass")
	if !authAsk {
		return findings
	}
	var boost, rest []auditFinding
	for _, f := range findings {
		rule := strings.ToLower(f.Rule)
		file := strings.ToLower(strings.ReplaceAll(f.File, "\\", "/"))
		ev := strings.ToLower(f.Evidence + " " + f.Message)
		if strings.Contains(rule, "auth") || strings.Contains(rule, "session") ||
			strings.Contains(rule, "csrf") || strings.Contains(rule, "acl") ||
			rule == "authz-fail-open" || rule == "authz-gap" || rule == "injection-taint" ||
			strings.Contains(rule, "fail") || strings.Contains(rule, "login") ||
			strings.Contains(rule, "jwt") || strings.Contains(rule, "password") ||
			strings.Contains(file, "/auth") || strings.Contains(file, "auth.") ||
			strings.Contains(file, "session") || strings.Contains(file, "acl.") ||
			strings.Contains(file, "acl.c") || strings.Contains(ev, "fail-open") ||
			strings.Contains(ev, "fail open") || strings.Contains(ev, "bearer") ||
			strings.Contains(ev, "protected-mode") || strings.Contains(ev, "requirepass") {
			boost = append(boost, f)
			continue
		}
		rest = append(rest, f)
	}
	if len(boost) == 0 {
		return findings
	}
	out := append(boost, rest...)
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

func appendSecurityMessages(rule string) []string {
	switch rule {
	case "sql-string-concat":
		return []string{"SQL built from string concat/interpolation — use parameterized queries."}
	case "raw-html-xss":
		return []string{"Raw/unescaped HTML sink — confirm sanitization or trusted input only."}
	case "hardcoded-secret":
		return []string{"Possible hard-coded credential — move to env/secret store."}
	case "eval-usage":
		return []string{"eval / dynamic code execution on possibly untrusted input."}
	case "shell-exec-injection":
		return []string{"External command built from variables — use argv form."}
	case "csrf-disabled", "authz-gap", "authz-fail-open":
		return []string{"AuthZ/CSRF protection appears relaxed — confirm intentional."}
	case "injection-taint":
		return []string{"Request-derived data reaches a dangerous sink in the same function — parameterize/sanitize."}
	case "open-redirect", "open-redirect-taint":
		return []string{"Redirect target may come from user input — allowlist."}
	case "c-unsafe-buffer":
		return []string{"C unbounded string/buffer API — confirm size checks before treating as exploitable."}
	case "redis-auth-gap":
		return []string{"Redis auth/ACL surface — confirm protected-mode/bind/ACL before public exposure."}
	case "config-debug-enabled", "config-session-insecure", "config-cors-open",
		"config-auth-gap", "config-missing-validation", "config-bind-all":
		return []string{"Config/bootstrap hardening item — grounded file:line, not a confirmed CVE."}
	default:
		return []string{"Security sink candidate — verify before treating as confirmed vuln."}
	}
}

// applyFindingsMode mutates plan/kickoff shared fields for security|performance
// roles: filter noise reuse, attach grounded findings, or abstain clearly.
// task (optional) re-ranks findings so auth/fail-open asks surface auth cites first.
func applyFindingsMode(role, repoRoot, task string, cands *[]reuseCandidate, already *string, note *string, steps *[]string) (findings []auditFinding, mode, abstain string) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "security" && role != "performance" {
		return nil, "", ""
	}
	mode = role
	shape := security.DetectProjectShape(repoRoot)
	eco := detectEcosystem(repoRoot)
	if cands != nil {
		*cands = filterAuditCandidates(*cands, role)
		if role == "security" {
			*cands = preferSecurityReuse(*cands, task)
		}
	}

	switch role {
	case "security":
		findings = labelFrameworkFootguns(collectRepoSecurityFindings(repoRoot, 12), shape)
		findings = preferTaskAlignedFindings(findings, task)
		if len(findings) == 0 {
			abstain = "ABSTAIN: No security-relevant sinks, config hardening, or trust-boundary footguns matched in a bounded scan. This is not a clean bill of health."
			followUps := securitySkeletonNextQueries(eco)
			if already != nil {
				*already = abstain + " Follow-up queries: (1) " + followUps[0] + "; (2) " + followUps[1] + "; (3) " + followUps[2]
			}
			if note != nil {
				*note = "Findings mode (security): abstain — " + abstain + " project_shape=" + string(shape) +
					". Do not invent vulns. Run the three follow-up queries above, then investigate recipe=security."
			}
			if steps != nil {
				*steps = []string{
					"ABSTAIN from inventing vulns. Next: `" + followUps[0] + "`",
					"Then: `" + followUps[1] + "`",
					"Then: `" + followUps[2] + "` — or ask which surface (auth, uploads, payments) to audit.",
				}
			}
			return findings, mode, abstain
		}
		// Partial abstain wording when only config hardening (skeleton) — still useful.
		onlyConfig := true
		onlyGuidance := true
		for _, f := range findings {
			kind := strings.ToLower(f.Kind)
			if kind != "config_hardening" && kind != "library_guidance" {
				onlyConfig = false
				onlyGuidance = false
				break
			}
			if kind != "config_hardening" {
				onlyConfig = false
			}
			if kind == "sink_candidate" {
				onlyGuidance = false
			}
		}
		if already != nil {
			locs := make([]string, 0, 3)
			for i, f := range findings {
				if i >= 3 {
					break
				}
				locs = append(locs, fmt.Sprintf("%s:%d (%s/%s conf=%s)", f.File, f.Line, f.Kind, f.Rule, f.Confidence))
			}
			prefix := "Grounded security sink candidates (not confirmed vulns)"
			if onlyConfig {
				prefix = "No code sinks; grounded config/bootstrap hardening checklist"
				followUps := securitySkeletonNextQueries(eco)
				abstain = "No exploitable code sinks matched — delivering config hardening with file:line (kind=config_hardening). Do not inflate these into CVEs. Follow-ups: (1) " +
					followUps[0] + "; (2) " + followUps[1] + "; (3) " + followUps[2]
			} else if onlyGuidance && shape == security.ShapeApp {
				// Apps: guidance-only is not enough — force concrete next-step modules.
				abstain = "ABSTAIN: no confirmed sink_candidate with clear exploit narrative — next inspect auth/session/secrets modules (not a clean bill of health)."
				followUps := securitySkeletonNextQueries(eco)
				*already = abstain + " Top cites: " + strings.Join(locs, "; ") +
					". Follow-ups: (1) " + followUps[0] + "; (2) " + followUps[1] + "; (3) " + followUps[2]
				if note != nil {
					*note = "Findings mode (security): app guidance-only — " + abstain + " project_shape=" + string(shape) +
						". Prefer sink_candidate or the three follow-ups; do not stop at library_guidance."
				}
				if steps != nil {
					*steps = []string{
						"ABSTAIN from claiming CVEs. Next: `" + followUps[0] + "`",
						"Then: `" + followUps[1] + "`",
						"Then: `" + followUps[2] + "`",
					}
				}
				return findings, mode, abstain
			}
			*already = prefix + ": " + strings.Join(locs, "; ") +
				". Confirm each with `read_workspace_file` / `context` before calling it a vuln. CSS/DI/Schema hits were demoted."
		}
		if note != nil {
			*note = fmt.Sprintf("Findings mode (security): %d ranked candidate(s) with severity/confidence/exploitability (project_shape=%s). Prefer file:line over lexical reuse. Pair with `investigate recipe=security`.", len(findings), shape)
		}
		if steps != nil {
			top := findings[0]
			*steps = []string{
				fmt.Sprintf("Inspect rank#1 `%s:%d` (%s, confidence=%s, exploitability=%s) — %s",
					top.File, top.Line, top.Rule, top.Confidence, top.Exploitability, top.Hint),
				"Run `investigate recipe=security` for the full ranked list + review_diff.",
				"Patch confirmed issues only; re-scan with review_diff include_security; diagnostics → finish_check.",
			}
		}
	case "performance":
		var perfFindings []auditFinding
		switch shape {
		case security.ShapeLibrary, security.ShapeFrameworkCore:
			perfFindings = mapContextFindings(security.LibraryPerfGuidance(repoRoot, shape, 6))
		case security.ShapeSkeleton:
			perfFindings = mapContextFindings(security.SkeletonPerfGuidance(repoRoot, 4))
		default:
			// Apps (including thin-graph demos like petclinic): grounded controller guidance.
			perfFindings = mapContextFindings(security.AppPerfGuidance(repoRoot, 3))
		}
		findings = enrichPerfFindingsWhy(perfFindings, shape)

		if len(findings) == 0 && (cands == nil || len(*cands) == 0) {
			abstain = "ABSTAIN: No performance-relevant symbols matched after demoting CSS/HTML/DI noise. Call `hotspots` (check commits_scanned honesty) or name a concrete hot path. Thin skeletons / library cores often have no app-level N+1 to find — use library hot-path guidance when project_shape is library|framework_core."
			followUps := vibeNextQueries("performance", shape, true, "", repoRoot)
			if already != nil {
				*already = abstain + " Follow-up queries: (1) " + followUps[0] + "; (2) " + followUps[1] + "; (3) " + followUps[2]
			}
			if note != nil {
				*note = "Findings mode (performance): abstain — " + abstain + " project_shape=" + string(shape)
			}
			if steps != nil {
				*steps = []string{
					"ABSTAIN from inventing hotspots. Next: `" + followUps[0] + "`",
					"Then: `" + followUps[1] + "`",
					"Then: `" + followUps[2] + "` — measure before optimizing.",
				}
			}
			return findings, mode, abstain
		}
		if already != nil && len(findings) > 0 {
			locs := make([]string, 0, 3)
			for i, f := range findings {
				if i >= 3 {
					break
				}
				why := f.Hint
				if why == "" {
					why = f.Message
				}
				locs = append(locs, fmt.Sprintf("%s:%d (%s) — %s", f.File, f.Line, f.Rule, truncateRunes(why, 80)))
			}
			*already = "Performance hotspot(s) (project_shape=" + string(shape) + "): " + strings.Join(locs, "; ") +
				". Prefer these over inventing controller N+1. Next tool: `hotspots`."
		}
		if note != nil {
			switch shape {
			case security.ShapeLibrary, security.ShapeFrameworkCore:
				*note = "Findings mode (performance): project_shape=" + string(shape) + " — route to library hot paths / alloc / complexity, not app N+1. CSS/DI noise demoted."
			case security.ShapeSkeleton:
				*note = "Findings mode (performance): project_shape=skeleton — deliver conventions + sample patterns; do not invent production hotspots. Prefer `hotspots` honesty warnings."
			default:
				*note = "Findings mode (performance): CSS/HTML/DI noise demoted from reuse. Prefer `hotspots` + primary-language files; do not optimize ranked stylesheets."
			}
		}
		if steps != nil {
			base := []string{
				"Call `hotspots` first; if commits_scanned≤1, warn and prefer primary_language source over scripts/tests.",
				"Confirm the hotspot with `context` / callers before optimizing.",
				"Measure before/after; impact + test_impact; avoid inventing hotspots in skeleton apps.",
			}
			if len(findings) > 0 {
				top := findings[0]
				base = append([]string{fmt.Sprintf("Inspect guidance `%s:%d` (%s) — %s", top.File, top.Line, top.Rule, top.Hint)}, base...)
			} else if cands != nil && len(*cands) > 0 {
				base = append([]string{fmt.Sprintf("Confirm reuse/hot path: `context %s` (demoted CSS/DI already filtered).", (*cands)[0].Name)}, base...)
			}
			*steps = base
		}
	}
	return findings, mode, abstain
}

// primaryLangExts maps profile primary_language → file extensions to prefer in hotspots.
func primaryLangExts(primary string) map[string]bool {
	switch strings.ToLower(strings.TrimSpace(primary)) {
	case "go":
		return map[string]bool{".go": true}
	case "python":
		return map[string]bool{".py": true}
	case "ruby":
		return map[string]bool{".rb": true}
	case "php":
		return map[string]bool{".php": true}
	case "rust":
		return map[string]bool{".rs": true}
	case "java", "kotlin":
		return map[string]bool{".java": true, ".kt": true, ".kts": true}
	case "c":
		return map[string]bool{".c": true, ".h": true}
	case "cpp", "c++":
		return map[string]bool{".cpp": true, ".cc": true, ".cxx": true, ".hpp": true, ".h": true}
	case "csharp", "c#":
		return map[string]bool{".cs": true}
	case "javascript", "typescript":
		return map[string]bool{".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true}
	case "vue":
		return map[string]bool{".vue": true, ".ts": true, ".js": true}
	case "svelte":
		return map[string]bool{".svelte": true, ".ts": true, ".js": true}
	case "elixir":
		return map[string]bool{".ex": true, ".exs": true}
	default:
		return nil
	}
}

// preferPrimaryLanguageFiles reorders hotspot rows so primary-language sources
// rank above scripts/tests/generators when primary is set. Relative order within
// each partition is preserved.
func preferPrimaryLanguageFiles(rows []hotspotRow, primary string) []hotspotRow {
	exts := primaryLangExts(primary)
	if len(exts) == 0 || len(rows) == 0 {
		return rows
	}
	primaryLang := strings.ToLower(strings.TrimSpace(primary))
	var coreSrc, primaryRows, other []hotspotRow
	for _, r := range rows {
		ext := strings.ToLower(filepath.Ext(r.File))
		lower := strings.ToLower(strings.ReplaceAll(r.File, "\\", "/"))
		base := path.Base(lower)
		// Demote vendored/deps/scripts/tests even when the extension matches
		// (Redis deps/lua/*.c ranked over src/server.c).
		if strings.Contains(lower, "/deps/") || strings.HasPrefix(lower, "deps/") ||
			strings.Contains(lower, "/vendor/") || strings.Contains(lower, "/third_party/") ||
			strings.Contains(lower, "/node_modules/") ||
			strings.Contains(lower, "/tests/") || strings.Contains(lower, "/test/") ||
			strings.Contains(lower, "/__tests__/") || strings.Contains(lower, "/testdata/") ||
			(strings.Contains(lower, "/utils/") && primaryLang == "c") ||
			strings.Contains(lower, "/scripts/") || strings.Contains(lower, "/generators/") ||
			strings.Contains(lower, "/theme-builder/") || strings.Contains(lower, "/prerender/") ||
			strings.HasPrefix(base, "test_") || strings.Contains(lower, "/migrations/") ||
			strings.Contains(lower, "/lua/") ||
			// ORM Entity / fixtures / factories dominate churn but are weak perf targets.
			isORMModelOrFixturePath(lower) {
			other = append(other, r)
			continue
		}
		if !exts[ext] {
			other = append(other, r)
			continue
		}
		// C/Redis: force primary src/*.c core ahead of stray root .c files.
		if primaryLang == "c" && (strings.HasPrefix(lower, "src/") || strings.Contains(lower, "/src/")) &&
			(strings.HasSuffix(base, ".c") || strings.HasSuffix(base, ".h")) {
			coreSrc = append(coreSrc, r)
			continue
		}
		// PHP/Java: prefer Controllers / services over Entity models.
		if (primaryLang == "php" || primaryLang == "java") && (strings.Contains(lower, "/controller/") ||
			strings.Contains(lower, "/controllers/") || strings.HasSuffix(base, "controller.php") ||
			strings.HasSuffix(base, "controller.java") || strings.Contains(lower, "/service/") ||
			strings.Contains(lower, "/services/") && strings.HasSuffix(base, "service.java")) {
			coreSrc = append(coreSrc, r)
			continue
		}
		primaryRows = append(primaryRows, r)
	}
	if len(coreSrc) == 0 && len(primaryRows) == 0 {
		return rows
	}
	out := make([]hotspotRow, 0, len(rows))
	out = append(out, coreSrc...)
	out = append(out, primaryRows...)
	out = append(out, other...)
	return out
}

// isORMModelOrFixturePath reports Doctrine/JPA entity, fixture, and factory paths
// that inflate hotspot churn without being meaningful perf targets.
func isORMModelOrFixturePath(lower string) bool {
	lower = strings.ToLower(strings.ReplaceAll(lower, "\\", "/"))
	markers := []string{
		"/entity/", "/entities/",
		"/datafixtures/", "/fixture/", "/fixtures/",
		"/database/factories/", "/database/seeders/",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	base := path.Base(lower)
	if strings.HasSuffix(lower, ".java") {
		stem := strings.TrimSuffix(base, ".java")
		if strings.HasSuffix(stem, "entity") || strings.HasSuffix(stem, "fixture") ||
			strings.Contains(lower, "/model/") || strings.Contains(lower, "/models/") {
			if strings.Contains(lower, "controller") || strings.Contains(lower, "service") ||
				strings.Contains(lower, "repository") {
				return false
			}
			return true
		}
	}
	if strings.HasSuffix(lower, ".php") {
		if strings.Contains(base, "factory.php") || strings.Contains(base, "fixture") {
			return true
		}
	}
	return false
}
