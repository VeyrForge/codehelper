package security

import (
	"sort"
	"strings"
)

// EnrichAndRankFindings upgrades raw sink hits into audit-quality candidates:
// severity rank, confidence, exploitability, and an explicit Kind so agents
// never present config hardening as a confirmed CVE.
func EnrichAndRankFindings(in []ContextFinding) []ContextFinding {
	if len(in) == 0 {
		return in
	}
	out := make([]ContextFinding, len(in))
	copy(out, in)
	for i := range out {
		enrichOne(&out[i])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sevRank(out[i].Severity) != sevRank(out[j].Severity) {
			return sevRank(out[i].Severity) > sevRank(out[j].Severity)
		}
		// sink_candidate before library_guidance / config_hardening so real
		// auth/SQL hits beat checklist footguns at the same severity.
		if kindRank(out[i].Kind) != kindRank(out[j].Kind) {
			return kindRank(out[i].Kind) > kindRank(out[j].Kind)
		}
		// Auth / SQL / fail-open / secrets before /healthz-style guidance.
		if signalRank(out[i]) != signalRank(out[j]) {
			return signalRank(out[i]) > signalRank(out[j])
		}
		if confRank(out[i].Confidence) != confRank(out[j].Confidence) {
			return confRank(out[i].Confidence) > confRank(out[j].Confidence)
		}
		// Prefer real config/app cites over .env.example checklist rows at the
		// same signal (Laravel skeleton: session/APP_KEY in config/* first).
		envExI := strings.Contains(strings.ToLower(out[i].File), ".env.example") ||
			strings.Contains(strings.ToLower(out[i].File), ".env.sample")
		envExJ := strings.Contains(strings.ToLower(out[j].File), ".env.example") ||
			strings.Contains(strings.ToLower(out[j].File), ".env.sample")
		if envExI != envExJ {
			return !envExI
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	for i := range out {
		out[i].Rank = i + 1
		if out[i].Kind == "" {
			out[i].Kind = "sink_candidate"
		}
	}
	return out
}

func enrichOne(f *ContextFinding) {
	if f.Kind == "" {
		f.Kind = "sink_candidate"
	}
	rule := strings.ToLower(f.Rule)
	file := strings.ToLower(strings.ReplaceAll(f.File, "\\", "/"))
	ev := strings.ToLower(f.Evidence)
	switch {
	case strings.HasPrefix(rule, "config-") || f.Kind == "config_hardening":
		f.Kind = "config_hardening"
		if f.Confidence == "" {
			f.Confidence = "medium"
		}
		if f.Exploitability == "" {
			f.Exploitability = "config-only"
		}
		if f.Hint == "" {
			f.Hint = "Hardening checklist item — not a confirmed vuln. Verify deploy defaults before changing production."
		}
		// .env.example / global_settings defaults are checklist-only, never high —
		// except empty APP_KEY= which is a deploy blocker on every Laravel skeleton
		// (HUMAN-AUDIT-V5: .env.example demotion alone capped Sec at 5).
		if strings.Contains(file, ".env.example") || strings.Contains(file, "global_settings") ||
			strings.HasSuffix(file, ".env.sample") {
			if !(rule == "config-auth-gap" && strings.Contains(ev, "app_key=")) {
				f.Severity = "low"
				f.Confidence = "low"
				f.Hint = "Example/default config checklist — not a production vuln by itself."
			}
		}
	case strings.HasPrefix(rule, "library-") || f.Kind == "library_guidance":
		f.Kind = "library_guidance"
		if f.Confidence == "" {
			f.Confidence = "medium"
		}
		if f.Exploitability == "" {
			f.Exploitability = "unknown"
		}
		if f.Hint == "" {
			f.Hint = "Library/framework hot path — profile before optimizing; not an app N+1."
		}
		if rule == "skeleton-perf-guidance" {
			f.Severity = "low"
			f.Confidence = "low"
			if f.Hint == "" || !strings.Contains(strings.ToLower(f.Hint), "not a measured") {
				f.Hint = "[skeleton-not-hotspot] Convention/sample path only — NOT a measured production hotspot."
			}
		}
	case f.Kind == "perf_smell" || rule == "n-plus-one-loop" || rule == "sync-alloc-hot-path":
		f.Kind = "perf_smell"
		if f.Confidence == "" {
			f.Confidence = "medium"
		}
		if f.Exploitability == "" {
			f.Exploitability = "unknown"
		}
		if f.Hint == "" {
			f.Hint = "Perf smell with file:line — confirm with context/callers before rewriting."
		}
	case rule == "sql-string-concat" || rule == "eval-usage" || rule == "shell-exec-injection":
		if f.Confidence == "" {
			f.Confidence = "high"
		}
		if f.Exploitability == "" {
			f.Exploitability = "possible"
		}
		if f.Hint == "" {
			f.Hint = "Confirm untrusted input reaches this sink; then parameterize / remove eval / use argv."
		}
		// Demote residual FPs that still slipped through: migrate DDL, mode flags.
		if strings.Contains(file, "/migrate") || strings.Contains(file, "/migration") ||
			strings.Contains(ev, "script_eval_mode") || strings.Contains(ev, "create table if not exists") ||
			strings.Contains(ev, "db.exec") || strings.Contains(ev, "tokio::spawn") {
			f.Confidence = "low"
			f.Exploitability = "unknown"
			f.Severity = "low"
			f.Hint = "Likely controlled migrate/schema or engine flag — verify before treating as exploitable."
		}
		// Framework ORM/compiler / manage.py shell — library guidance, not web RCE.
		if isFrameworkSQLInternal(file) || isLibraryInternalPath(file) || strings.Contains(file, "/management/commands/") ||
			(rule == "eval-usage" && (strings.Contains(ev, ".eval(") || strings.Contains(ev, "def eval"))) ||
			(rule == "shell-exec-injection" && strings.Contains(file, "/management/")) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "framework-api"
			f.Kind = "library_guidance"
			f.Hint = "Framework/CLI internal — not an application request-path vuln by itself."
		}
		// Drizzle/Prisma/sql tagged templates often parameterize ${} — demote ONLY when
		// the helper says value-bound. Do NOT demote on mere "sql`" / "sql.raw" presence:
		// dynamic table_${id} and sql.raw(values) are real TPs (descrybe).
		if rule == "sql-string-concat" && looksLikeORMParameterizedTaggedSQL(ev) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "unknown"
			f.Kind = "library_guidance"
			f.Hint = "Tagged SQL template — confirm the driver parameterizes interpolations; not classic string-concat SQLi."
		}
		// Help / success / UI templates that slipped past the scanner — demote hard.
		if rule == "sql-string-concat" && (strings.Contains(ev, "successfully") ||
			strings.Contains(ev, "mingw") || strings.Contains(ev, "regression tests") ||
			strings.Contains(ev, "categories") && strings.Contains(ev, "${") ||
			strings.Contains(ev, "message:") && strings.Contains(ev, "${") ||
			strings.Contains(ev, "updated successfully") || strings.Contains(ev, "qrels") ||
			isHTTPOrUIConcat(ev)) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "unknown"
			f.Hint = "Likely help text or UI success template — not SQL injection."
		}
		if rule == "eval-usage" && (strings.Contains(ev, "qrels") || strings.Contains(ev, "ndcg") ||
			strings.Contains(ev, "fmt.") || strings.Contains(ev, "fprintf") ||
			strings.Contains(ev, "eval (%") || strings.Contains(ev, `"eval `)) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "unknown"
			f.Hint = "Likely metrics/help prose mentioning eval — not a dynamic code sink."
		}
		// Framework CLI startup eval(compile(…)) / Rails runner — library guidance, not web RCE.
		if rule == "eval-usage" && (strings.Contains(ev, "eval(compile(") ||
			strings.Contains(file, "/flask/cli.py") ||
			strings.Contains(file, "/cli.py") && strings.Contains(ev, "compile(") ||
			isFrameworkCLIEval(file, ev) || isFrameworkCompilerEval(file, ev)) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "framework-api"
			f.Kind = "library_guidance"
			f.Hint = "Framework CLI/compiler eval — not a web request-path sink."
		}
	case rule == "hardcoded-secret" || rule == "raw-html-xss" || rule == "blade-unescaped-output":
		if f.Confidence == "" {
			f.Confidence = "medium"
		}
		if f.Exploitability == "" {
			f.Exploitability = "possible"
		}
		if f.Hint == "" {
			f.Hint = "Confirm trust boundary (user-controlled data / real secret material) before patching."
		}
		// Framework intentional SafeString / mark_safe helpers → footgun, not app XSS.
		if rule == "raw-html-xss" && (strings.Contains(file, "/django/contrib/") ||
			strings.Contains(file, "/django/forms/") || strings.Contains(file, "/django/utils/") ||
			strings.Contains(file, "/django/template/") || strings.HasPrefix(file, "django/") ||
			strings.Contains(file, "/admin/helpers") || strings.Contains(ev, "mark_safe(") &&
			(strings.Contains(file, "/contrib/") || strings.Contains(file, "/framework/"))) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "framework-api"
			f.Hint = "Framework intentional escape hatch — XSS only if callers pass untrusted HTML; not an app vuln by itself."
			f.Kind = "library_guidance"
		}
		// Twig |trans|raw / Svelte preview+compiler / JSON-LD {@html — demote flood FPs.
		if rule == "raw-html-xss" && (isTwigTransRaw(ev) || isSvelteCompilerHTMLProse(file, ev) ||
			isStructuredDataHTML(ev) || isUIPreviewHTML(file, ev) ||
			isLowProvenanceHTML(file, ev, ev) ||
			strings.Contains(file, "preview") && strings.Contains(ev, "{@html") ||
			strings.Contains(file, "/compiler/") && strings.Contains(ev, "{@html")) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "framework-api"
			f.Kind = "library_guidance"
			f.Hint = "Static i18n |raw, compiler prose, preview/CSS-var HTML, or structured-data HTML — not a high-prec app XSS by itself."
		}
		if rule == "hardcoded-secret" && (strings.Contains(ev, "csrf") || strings.Contains(file, "request_forgery") ||
			strings.Contains(ev, "action_controller.") || isConfigKeyNameRHS(ev) ||
			strings.Contains(ev, "https://") || strings.Contains(ev, "http://") ||
			strings.Contains(ev, "/oauth/") || isPlaceholderSecretLiteral(ev) ||
			isEmptySecretAssignment(ev) || strings.Contains(ev, "=== undefined") ||
			strings.Contains(ev, "== undefined")) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "unknown"
			f.Kind = "library_guidance"
			f.Hint = "Likely a session/CSRF *key name*, config key, OAuth URL, empty default, or docs/example placeholder — ignore unless the RHS is real secret material."
		}
	case rule == "csrf-disabled" || rule == "authz-gap" || rule == "authz-fail-open" || rule == "open-redirect" || rule == "missing-nonce-check" ||
		rule == "insecure-cookie" || rule == "unsafe-template-css":
		if f.Confidence == "" {
			f.Confidence = "medium"
		}
		if f.Exploitability == "" {
			f.Exploitability = "possible"
		}
		if f.Hint == "" {
			f.Hint = "May be intentional for APIs/webhooks — confirm with owners before tightening."
		}
		if rule == "insecure-cookie" {
			f.Kind = "sink_candidate"
			f.Exploitability = "possible"
			if f.Hint == "" || strings.Contains(f.Hint, "intentional") {
				f.Hint = "Insecure cookie flag — confirm Secure/HttpOnly for session cookies on HTTPS deployments."
			}
		}
		if rule == "unsafe-template-css" {
			f.Kind = "sink_candidate"
			f.Confidence = "high"
			f.Exploitability = "possible"
			if f.Severity == "" || f.Severity == "medium" {
				f.Severity = "high"
			}
			f.Hint = "template.CSS casts unsanitized theme/color/url strings — allowlist hex/rgb or escape before casting."
		}
		if rule == "authz-fail-open" {
			f.Confidence = "high"
			f.Exploitability = "likely"
			if f.Severity == "" || f.Severity == "medium" {
				f.Severity = "high"
			}
			if f.Hint == "" || strings.Contains(f.Hint, "intentional") {
				f.Hint = "Fail-open auth: empty token/config skips verification — require credentials in production."
			}
			f.Kind = "sink_candidate"
		}
		if rule == "open-redirect" && (strings.Contains(ev, "get_full_path") || strings.Contains(ev, "fullpath") ||
			strings.Contains(ev, "formdataroutingredirect") || strings.Contains(ev, "does_not_exist_redirect") ||
			strings.Contains(file, "/action_controller/metal/redirecting") ||
			strings.Contains(file, "/actionpack/") && strings.Contains(file, "redirecting")) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "framework-api"
			f.Kind = "library_guidance"
			f.Hint = "Same-path / debug redirect or framework Redirecting API — classic scanner FP; not an app open redirect."
		}
		// csrf_protect wrappers / getattr csrf_exempt checks are enforcement, not gaps.
		// Empty-token guards are fail-closed (reject missing credential), not fail-open.
		// Do NOT demote authz-fail-open (dataflow-lite confirmed empty-config allow).
		if rule == "authz-gap" && (strings.Contains(ev, "csrf_protect") ||
			strings.Contains(ev, "getattr") && strings.Contains(ev, "csrf") ||
			isNonAuthTokenEmptyCheck(ev) || isEmptyTokenGuard(ev) ||
			isDevGatedAuthSkip(ev, ev)) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "unknown"
			f.Kind = "library_guidance"
			f.Hint = "CSRF enforcement check, empty-token fail-closed guard, or DEV-gated skipAuth — not a production authz gap."
		}
	case rule == "injection-taint":
		if f.Confidence == "" {
			f.Confidence = "high"
		}
		if f.Exploitability == "" {
			f.Exploitability = "likely"
		}
		if f.Hint == "" {
			f.Hint = "Request-derived data reaches this sink in-function — parameterize/sanitize before dismissing."
		}
		f.Kind = "sink_candidate"
		// Still demote framework internals that slipped through.
		if isLibraryInternalPath(file) || isFrameworkSQLInternal(file) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "framework-api"
			f.Kind = "library_guidance"
			f.Hint = "Framework/ORM internal — not an application request-path vuln by itself."
		}
		// HTTP response helpers echoing SQL-looking prose are not DB injection.
		if isHTTPOrUIConcat(ev) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Exploitability = "unknown"
			f.Hint = "HTTP response helper — not a SQL/exec sink; ignore unless a DB API is on the same line."
		}
	case rule == "c-unsafe-buffer" || rule == "redis-auth-gap":
		if f.Confidence == "" {
			f.Confidence = "medium"
		}
		if f.Exploitability == "" {
			f.Exploitability = "possible"
		}
		if f.Hint == "" {
			f.Hint = "C/memory or Redis auth surface — confirm bounds checks / ACL before treating as exploitable."
		}
		// Example redis conf defaults are checklist, not confirmed vulns.
		if rule == "redis-auth-gap" && (strings.HasSuffix(file, ".conf") || strings.Contains(file, "sentinel")) {
			f.Confidence = "low"
			f.Severity = "low"
			f.Kind = "config_hardening"
			f.Exploitability = "config-only"
			f.Hint = "Documented example/default — pair with bind/ACL before public exposure; not a confirmed ACL vuln."
		}
	default:
		if f.Confidence == "" {
			f.Confidence = "low"
		}
		if f.Exploitability == "" {
			f.Exploitability = "unknown"
		}
		if f.Hint == "" {
			f.Hint = "Sink candidate only — verify with context/read before calling it a vulnerability."
		}
	}
	if f.Severity == "" {
		f.Severity = "medium"
	}
}

func sevRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func confRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// kindRank prefers confirmed sink candidates over footgun/checklist kinds.
func kindRank(kind string) int {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "sink_candidate":
		return 3
	case "library_guidance":
		return 2
	case "config_hardening":
		return 1
	default:
		return 0
	}
}

// signalRank boosts auth/SQL/injection/secret rules so /healthz guidance and
// generic open-redirect noise do not occupy rank #1 on real apps.
func signalRank(f ContextFinding) int {
	rule := strings.ToLower(strings.TrimSpace(f.Rule))
	ev := strings.ToLower(f.Evidence)
	switch {
	case rule == "authz-fail-open" || rule == "authz-gap" || rule == "sql-string-concat" ||
		rule == "injection-taint" || rule == "hardcoded-secret" || rule == "eval-usage" ||
		rule == "shell-exec-injection" || rule == "csrf-disabled" || rule == "unsafe-template-css":
		return 100
	case rule == "raw-html-xss" || rule == "blade-unescaped-output" ||
		rule == "open-redirect" || rule == "open-redirect-taint" ||
		rule == "c-unsafe-buffer" || rule == "redis-auth-gap" || rule == "missing-nonce-check" ||
		rule == "insecure-cookie":
		return 80
	case strings.HasPrefix(rule, "app-auth") || strings.HasPrefix(rule, "app-session") ||
		strings.HasPrefix(rule, "app-csrf") || strings.HasPrefix(rule, "app-login") ||
		strings.HasPrefix(rule, "app-jwt") || strings.HasPrefix(rule, "app-secret") ||
		strings.HasPrefix(rule, "app-path") || rule == "app-open-redirect" ||
		rule == "app-actuator-exposure" || rule == "app-sanctum" || rule == "app-dev-firewall" ||
		rule == "app-rate-limit-failopen" || rule == "app-html-sanitize" || rule == "app-xss" ||
		rule == "app-cookie-secure-default" || rule == "app-missing-spring-security":
		return 70
	case strings.Contains(ev, "fail-open") || strings.Contains(ev, "fail open") ||
		strings.Contains(ev, "bearer") || strings.Contains(ev, "sql"):
		return 65
	case strings.HasPrefix(rule, "app-health") || rule == "app-ready-route" ||
		rule == "app-listen" || rule == "app-error-surface" || rule == "app-authz-surface":
		return 40
	// SPA checklist rows are honest footguns on real Vue/Svelte apps, but must
	// never outrank auth/secret/path trust boundaries on a Go/Node host that
	// merely nests eval beds (HUMAN Sec honesty — no invented sinks).
	case strings.HasPrefix(rule, "spa-"):
		return 35
	case strings.HasPrefix(rule, "config-") || strings.HasPrefix(rule, "nest-"):
		return 25
	default:
		return 50
	}
}
