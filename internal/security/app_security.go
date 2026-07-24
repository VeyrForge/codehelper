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
	{"backend/internal/auth/handler.go", "safeRedirectURL", "app-open-redirect", "Redirect allowlist helper — verify all post-login redirects go through it."},
	{"backend/internal/auth/routes.go", "HandleFunc", "app-auth-routes", "Auth route table — confirm login/callback/logout are the only public auth endpoints."},
	{"backend/internal/auth/session_store.go", "SaveSession", "app-session-store", "Session persistence — confirm tokens are not logged and TTL is enforced."},
	{"backend/cmd/api/main.go", "ListenAndServe", "app-listen", "API entry ListenAndServe — pair bind address with auth middleware and TLS termination."},

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
	{"templates/blog/post_show.html.twig", "sanitize_html", "app-html-sanitize", "Blog HTML render — confirm sanitize_html (or equivalent) stays on the pipeline."},

	// Go tooling / MCP hosts (codehelper-like) — product trust boundaries, not
	// meta "audit the scanner" tips (HUMAN-AUDIT-V3: self-repo must not substitute
	// scanner guidance for auth/secrets/path findings).
	{"internal/secrets/secrets.go", "func Set", "app-secret-store", "Secret store Set — confirm secrets never log and paths stay inside the store root."},
	{"internal/secrets/secrets.go", "func Get", "app-secret-get", "Secret store Get — confirm callers do not echo secret values into tool output."},
	{"internal/agentapi/server.go", "func (s *Server) auth", "app-auth-middleware", "Agent API auth middleware — empty Token fail-opens (all requests pass); require a token in production."},
	{"internal/agentapi/server.go", "GET /healthz", "app-healthz", "Agent API /healthz|/ready — confirm health stays public while mutating routes stay behind auth."},
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
	root = filepath.Clean(root)
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
		out = append(out, ContextFinding{
			Tool: "codehelper-app-security", Severity: "medium", Rule: h.rule,
			File: h.file, Line: line, Evidence: h.message,
			Kind: "library_guidance", Confidence: "medium", Exploitability: "possible",
			Hint: "[app-trust-boundary] Grounded audit surface — not a confirmed CVE; verify with context/read before claiming a vuln.",
		})
	}
	return EnrichAndRankFindings(out)
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
