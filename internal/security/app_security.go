package security

import (
	"os"
	"path/filepath"
	"strings"
)

// appSecurityFootguns are high-value auth/session/CSRF/actuator surfaces that
// exist in many apps. When sink scans are empty we still return verified
// file:line guidance — not invented CVEs.
var appSecurityFootguns = []struct {
	file    string
	needle  string
	rule    string
	message string
}{
	// Spring / Java apps
	{"src/main/resources/application.properties", "management.endpoints.web.exposure.include=*", "app-actuator-exposure", "Actuator endpoints exposed broadly — lock down management endpoints before production."},
	{"src/main/resources/application.yml", "exposure.include", "app-actuator-exposure", "Actuator exposure config — confirm not '*' in production."},
	{"src/main/java/org/springframework/samples/petclinic/system/CrashController.java", "exception", "app-error-surface", "Error/crash controller — confirm it does not leak stack traces to clients."},
	{"src/main/java/org/springframework/samples/petclinic/owner/OwnerController.java", "findOwner", "app-authz-surface", "Owner list/detail controller — confirm authz + pagination on PII-bearing records."},

	// Discord / Go API + OAuth apps
	{"backend/internal/auth/handler.go", "SessionSecret", "app-session-secret", "OAuth/session handler — SessionSecret must come from env; verify cookie flags and redirect allowlist."},
	{"backend/internal/auth/handler.go", "func safeRedirectURL", "app-open-redirect", "Redirect allowlist helper — verify all post-login redirects go through it."},
	{"backend/internal/auth/handler.go", "secure := false", "app-cookie-secure-default", "Session cookie Secure defaults to false until HTTPS is detected — confirm production always sets Secure (or terminates TLS correctly)."},
	// chi mounts use r.Get/Post — "HandleFunc" misses and fell back to package line 1.
	{"backend/internal/auth/routes.go", "func Routes", "app-auth-routes", "Auth route table — confirm login/callback/logout are the only public auth endpoints."},
	{"backend/internal/auth/session_store.go", "func (s *DBStore) SaveSession", "app-session-store", "Session persistence — confirm tokens are not logged and TTL is enforced."},
	{"backend/cmd/api/main.go", `r.Get("/health"`, "app-health-route", "API GET /health — keep public and slim; mutating /api routes must stay behind session/auth."},
	{"backend/cmd/api/main.go", `r.Get("/ready"`, "app-ready-route", "API GET /ready — readiness probe; confirm it does not leak secrets and pairs with /health."},
	{"backend/cmd/api/main.go", "ListenAndServe", "app-listen", "API entry ListenAndServe — pair bind address with auth middleware and TLS termination."},

	// Next.js / SaaS apps (descrybe-like) — cite RateLimiter code, not the fail-open comment.
	{"lib/api-auth.ts", "RateLimiter.getInstance", "app-rate-limit-failopen", "API key auth rate-limit fail-open — confirm limiter outages cannot bypass abuse controls."},
	{"middleware.ts", "NextResponse.redirect", "app-auth-middleware", "Next.js middleware auth redirect — confirm unauthenticated routes stay gated."},

	// Generic Go / Node / PHP auth surfaces
	{"internal/auth/handler.go", "Session", "app-auth-handler", "Auth handler — verify session/cookie hardening and CSRF on state changes."},
	{"Auth/AuthService.cs", "Login", "app-auth-service", "Auth service — confirm password compare is constant-time and lockout exists."},
	{"app/Http/Middleware/VerifyCsrfToken.php", "except", "app-csrf-except", "CSRF except list — keep empty unless endpoints have an equivalent check."},
	{"config/sanctum.php", "stateful", "app-sanctum", "Sanctum stateful domains — tighten to known frontends."},
	{"src/auth/auth.service.ts", "validateUser", "app-auth-service", "Nest auth service — confirm credentials never log and JWT secret is external."},
	{"src/auth/jwt.strategy.ts", "secretOrKey", "app-jwt-secret", "JWT strategy secretOrKey — must be env-backed, never a literal."},

	// Symfony demo / PHP apps
	{"config/packages/security.yaml", "security: false", "app-dev-firewall", "Dev firewall disables security for profiler/assets — confirm not exposed in production."},
	{"config/packages/security.yaml", "enable_csrf", "app-csrf", "Form login CSRF flag — keep enable_csrf true for state-changing auth."},
	{"src/Controller/SecurityController.php", "function login", "app-login", "Login controller — confirm lockout/rate limits and errors do not leak usernames."},
	{"src/Controller/HealthController.php", "/health", "app-health-route", "App GET /health — keep public/slim for probes; do not put secrets or admin actions here."},
	{"templates/blog/post_show.html.twig", "sanitize_html", "app-html-sanitize", "Blog HTML render — confirm sanitize_html (or equivalent) stays on the pipeline."},

	// Go tooling / MCP hosts (codehelper-like) — product trust boundaries, not
	// meta "audit the scanner" tips (HUMAN-AUDIT-V3: self-repo must not substitute
	// scanner guidance for auth/secrets/path findings). Auth before health so
	// AppSecurityGuidance limit truncation keeps the bearer gate.
	{"internal/agentapi/server.go", "func (s *Server) auth", "app-auth-middleware", "Agent API auth middleware — mutating routes require CODEHELPER_API_TOKEN (fail-closed when empty); confirm chat/tools stay behind bearer auth."},
	{"internal/agentapi/server.go", "GET /healthz", "app-healthz", "Agent API /healthz|/ready — confirm health stays public and slim (no llm_completion_url/model) while mutating routes stay behind auth."},
	{"internal/secrets/secrets.go", "func Set", "app-secret-store", "Secret store Set — confirm secrets never log and paths stay inside the store root."},
	{"internal/secrets/secrets.go", "func Get", "app-secret-get", "Secret store Get — confirm callers do not echo secret values into tool output."},
	{"internal/connections/websites.go", "UsesSecretStore", "app-secret-usage", "Connection layer UsesSecretStore — confirm credentials resolve from the store, not literals."},
	{"internal/mcpsvc/workspace_tools.go", "resolveRepo", "app-path-boundary", "Workspace path resolution — confirm all file tools stay inside the repo root."},
}

// AppSecurityGuidance returns ranked auth/session/config footguns with verified
// file:line when high-sev sinks are absent. Useful for clean-looking apps that
// still have real trust boundaries to audit.
func AppSecurityGuidance(root string, limit int) []ContextFinding {
	if limit <= 0 {
		limit = 6
	}
	root = resolveScanRoot(filepath.Clean(root))
	var out []ContextFinding
	for _, h := range appSecurityFootguns {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(root, filepath.FromSlash(h.file))
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		line := 1
		if h.needle != "" {
			if ln := firstLineContainingFile(abs, h.needle); ln > 0 {
				line = ln
			}
		}
		conf := "medium"
		// Auth / secret / path trust boundaries are the strongest honest footguns
		// on fail-closed apps (codehelper) — high conf so they outrank SPA checklist
		// noise without inventing sink_candidate CVEs (HUMAN Sec ceiling stays ≤7).
		if strings.HasPrefix(h.rule, "app-auth") || strings.HasPrefix(h.rule, "app-secret") ||
			strings.HasPrefix(h.rule, "app-path") || strings.HasPrefix(h.rule, "app-session") ||
			strings.HasPrefix(h.rule, "app-jwt") || h.rule == "app-rate-limit-failopen" ||
			h.rule == "app-cookie-secure-default" || h.rule == "app-missing-spring-security" {
			conf = "high"
		}
		out = append(out, ContextFinding{
			Tool: "codehelper-app-security", Severity: "medium", Rule: h.rule,
			File: h.file, Line: line, Evidence: h.message,
			Kind: "library_guidance", Confidence: conf, Exploitability: "possible",
			Hint: "[app-trust-boundary] Grounded audit surface — not a confirmed CVE; verify with context/read before claiming a vuln.",
		})
	}
	if len(out) < limit {
		out = append(out, scanMissingSpringSecurity(root, limit-len(out))...)
	}
	return EnrichAndRankFindings(out)
}

// scanMissingSpringSecurity flags Spring Boot apps that ship controllers / actuator
// but have no SecurityFilterChain / WebSecurityConfigurer — petclinic-style demos
// expose PII and /actuator without auth by default (honest footgun, not a CVE invent).
func scanMissingSpringSecurity(root string, remaining int) []ContextFinding {
	if remaining <= 0 {
		return nil
	}
	appJava := resolveScanRoot(filepath.Join(root, "src", "main", "java"))
	if _, err := os.Stat(appJava); err != nil {
		return nil
	}
	// Require a Spring Boot application entry so we do not flag unrelated Java trees.
	entryRel := ""
	entryLine := 1
	_ = filepath.WalkDir(appJava, func(path string, d os.DirEntry, err error) error {
		if err != nil || entryRel != "" || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), "application.java") {
			return nil
		}
		body := readFileTrunc(path, 4000)
		if !strings.Contains(body, "@SpringBootApplication") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		entryRel = filepath.ToSlash(rel)
		entryLine = firstLineContaining(body, "@SpringBootApplication")
		return filepath.SkipAll
	})
	if entryRel == "" {
		return nil
	}
	hasSecurity := false
	_ = filepath.WalkDir(appJava, func(path string, d os.DirEntry, err error) error {
		if err != nil || hasSecurity || d.IsDir() {
			return err
		}
		name := strings.ToLower(d.Name())
		if !strings.HasSuffix(name, ".java") {
			return nil
		}
		if strings.Contains(name, "security") {
			hasSecurity = true
			return filepath.SkipAll
		}
		body := strings.ToLower(readFileTrunc(path, 6000))
		if strings.Contains(body, "securityfilterchain") ||
			strings.Contains(body, "websecurityconfigurer") ||
			strings.Contains(body, "enablespringsecurity") ||
			strings.Contains(body, "httpsecurity") {
			hasSecurity = true
			return filepath.SkipAll
		}
		return nil
	})
	if hasSecurity {
		return nil
	}
	return []ContextFinding{{
		Tool: "codehelper-app-security", Severity: "medium", Rule: "app-missing-spring-security",
		File: entryRel, Line: entryLine, Evidence: "Spring Boot app has controllers/actuator but no SecurityFilterChain / *Security* config in src/main/java.",
		Kind: "library_guidance", Confidence: "high", Exploitability: "possible",
		Hint: "[app-trust-boundary] No Spring Security config found — owner/PII routes and actuator are anonymous unless an external gateway enforces auth. Not a CVE; verify deploy posture.",
	}}
}

// MergeUniqueFindings appends extras whose rule|file|line is not already present.
func MergeUniqueFindings(base, extras []ContextFinding, limit int) []ContextFinding {
	if limit <= 0 {
		limit = len(base) + len(extras)
	}
	seen := map[string]struct{}{}
	out := make([]ContextFinding, 0, limit)
	keyOf := func(f ContextFinding) string {
		return strings.ToLower(f.Rule) + "|" + filepath.ToSlash(f.File) + "|" + itoa(f.Line)
	}
	for _, f := range base {
		k := keyOf(f)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, f)
		if len(out) >= limit {
			return EnrichAndRankFindings(out)
		}
	}
	for _, f := range extras {
		k := keyOf(f)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, f)
		if len(out) >= limit {
			break
		}
	}
	return EnrichAndRankFindings(out)
}
