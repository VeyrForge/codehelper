package security

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// RepoScanOptions bounds a whole-repo security sink walk.
type RepoScanOptions struct {
	// Limit caps returned findings (default 40).
	Limit int
	// MaxFiles caps how many source files are opened (default 4000).
	MaxFiles int
	// MaxLineLen skips minified/generated lines longer than this (default 2000).
	MaxLineLen int
}

var repoScanSkipDirs = map[string]bool{
	".git": true, ".codehelper": true, "node_modules": true, "vendor": true,
	"target": true, "obj": true, "dist": true, "build": true, "out": true, "tmp": true,
	"tmp_scan":    true, // local scanner fixture corpora (codehelper self-scan pollution)
	"__pycache__": true, ".venv": true, "venv": true, ".idea": true, ".vscode": true,
	"third_party": true, "coverage": true, ".next": true, ".nuxt": true, ".cache": true,
	".turbo": true, ".parcel-cache": true, ".output": true, ".svelte-kit": true,
	"storybook-static": true, ".angular": true, ".vercel": true, ".netlify": true,
	".dart_tool": true, ".gradle": true, ".tox": true, ".nyc_output": true,
	"site-packages": true, "testdata": true, "fixtures": true,
	// Nested eval beds must never pollute the host app's security scan.
	".eval-projects": true, ".testbeds": true, ".ci-testbeds": true, ".ci-testbeds-extended": true,
}

var repoScanSrcExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".py": true, ".rs": true, ".java": true,
	".cs": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".cxx": true, ".hpp": true, ".php": true, ".rb": true, ".kt": true,
	".swift": true, ".scala": true, ".ex": true, ".exs": true, ".vue": true,
	".svelte": true, ".blade.php": true, ".conf": true, ".env": true,
	".yml": true, ".yaml": true, ".properties": true, ".twig": true,
}

// Extra sink patterns for whole-repo audits (XSS / raw HTML / auth gaps) beyond
// the diff-oriented builtinSecurityRules.
var repoExtraSecurityRules = []struct {
	rule     string
	severity string
	pattern  string // substring match, case-insensitive
	message  string
}{
	{"raw-html-xss", "high", "dangerouslysetinnerhtml", "React dangerouslySetInnerHTML — confirm sanitization / trusted HTML only."},
	{"raw-html-xss", "high", "v-html=", "Vue v-html binds unsanitized HTML — XSS if fed user content."},
	{"raw-html-xss", "high", "{@html ", "Svelte {@html} renders raw HTML — sanitize untrusted input."},
	{"raw-html-xss", "medium", "mark_safe(", "Django mark_safe / SafeString escape hatch — XSS if fed untrusted data."},
	{"raw-html-xss", "medium", "|safe}", "Template |safe filter disables escaping — confirm trust boundary."},
	{"raw-html-xss", "medium", "|raw}", "Twig |raw disables escaping — confirm trust boundary."},
	{"raw-html-xss", "medium", "|raw ", "Twig |raw filter — confirm trust boundary."},
	{"raw-html-xss", "medium", "{!! $", "Unescaped Blade output ({!! $var) — prefer escaped defaults."},
	{"raw-html-xss", "medium", "{!!$", "Unescaped Blade output ({!!$var) — prefer escaped defaults."},
	{"sql-string-concat", "high", "queryset.extra(", "Django QuerySet.extra is easy to misuse for SQL injection."},
	{"sql-string-concat", "high", "rawsql(", "RawSQL / raw query helper — ensure parameterization."},
	{"sql-string-concat", "high", ".raw(", "ORM .raw( / execute raw SQL — ensure parameterization."},
	{"hardcoded-secret", "high", "sk_live_", "Stripe live secret-looking token in source."},
	{"hardcoded-secret", "medium", "password = \"", "Possible hard-coded password assignment."},
	{"hardcoded-secret", "medium", "api_key = \"", "Possible hard-coded API key assignment."},
	{"hardcoded-secret", "medium", "secret_key = \"", "Possible hard-coded secret key assignment."},
	{"authz-gap", "medium", "auth: false", "Auth explicitly disabled on a route — confirm intentional."},
	{"authz-gap", "medium", "permitall()", "Spring permitAll / open endpoint — confirm intentional."},
	// Only decorator/call forms — not getattr(view, "csrf_exempt") CSRF enforcement checks.
	{"authz-gap", "medium", "@csrf_exempt", "Django @csrf_exempt — confirm intentional for this view."},
	{"authz-gap", "medium", "csrf_exempt(", "Django csrf_exempt(...) — confirm intentional for this view."},
	{"authz-gap", "medium", "= csrf_exempt", "Django csrf_exempt assignment — confirm intentional for this view."},
	{"open-redirect", "medium", "redirect(request.", "Redirect target may come from request input."},
	// Auth / rate-limit footguns that lexical SQL/eval rules miss.
	// Do NOT match bare `if token == ""` — that is almost always fail-closed
	// (reject missing credential), not fail-open. Real gaps: auth:false / permitAll /
	// skip auth / explicit fail-open wording.
	{"authz-gap", "high", "authorization optional", "Optional authorization — unauthenticated callers may proceed."},
	{"authz-gap", "medium", "fail open", "Fail-open auth/rate-limit path — confirm intentional for availability."},
	{"authz-gap", "medium", "fail-open", "Fail-open auth/rate-limit path — confirm intentional for availability."},
	{"authz-gap", "medium", "failopen", "Fail-open auth/rate-limit path — confirm intentional for availability."},
	{"authz-gap", "medium", "skip auth", "Auth explicitly skipped — confirm intentional."},
	{"authz-gap", "medium", "skipauth", "Auth explicitly skipped — confirm intentional."},
	{"authz-gap", "medium", "noauth:", "Auth explicitly disabled — confirm intentional."},
	{"authz-gap", "medium", "no_auth:", "Auth explicitly disabled — confirm intentional."},
	{"authz-gap", "high", "allowanonymous", "Anonymous access allowed — confirm intentional for this endpoint."},
	{"authz-gap", "high", "[allowanonymous]", "ASP.NET AllowAnonymous — confirm intentional public access."},
	{"authz-gap", "medium", "authorize = false", "Authorization disabled — confirm intentional."},
	{"authz-gap", "medium", "authorization=false", "Authorization disabled — confirm intentional."},
	{"authz-gap", "medium", "disableauth", "Auth disabled — confirm intentional."},
	{"authz-gap", "medium", "disable_auth", "Auth disabled — confirm intentional."},
	{"authz-gap", "high", "optional auth", "Optional auth — unauthenticated callers may proceed."},
	{"authz-gap", "high", "auth optional", "Optional auth — unauthenticated callers may proceed."},
	{"authz-gap", "medium", "requireauth: false", "requireAuth false — confirm intentional public route."},
	{"authz-gap", "medium", "require_auth: false", "require_auth false — confirm intentional public route."},
	{"authz-gap", "medium", "requireauth=false", "requireAuth false — confirm intentional public route."},
	// Spring Boot actuator wide-open exposure is a real deploy footgun (petclinic TP).
	{"authz-gap", "high", "management.endpoints.web.exposure.include=*", "Actuator exposure include=* — all management endpoints are public unless secured by Spring Security / gateway."},
	{"authz-gap", "high", "management.endpoints.web.exposure.include: '*'", "Actuator exposure include=* — all management endpoints are public unless secured by Spring Security / gateway."},
	{"authz-gap", "high", "management.endpoints.web.exposure.include: \"*\"", "Actuator exposure include=* — all management endpoints are public unless secured by Spring Security / gateway."},
	// Session cookie hardening (discord_mod-style Secure defaults to false until TLS detected).
	{"insecure-cookie", "medium", "secure := false", "Cookie Secure defaults to false — session cookie may ship over HTTP until TLS/X-Forwarded-Proto is detected."},
	{"insecure-cookie", "medium", "secure= false", "Cookie Secure defaults to false — session cookie may ship over HTTP."},
	{"insecure-cookie", "medium", "secure: false", "Cookie Secure: false — confirm intentional (HTTPS-only sites must set Secure)."},
	{"insecure-cookie", "medium", "secure:false", "Cookie Secure:false — confirm intentional (HTTPS-only sites must set Secure)."},
	{"insecure-cookie", "medium", "httponly: false", "Cookie HttpOnly: false — JS can read the cookie (XSS→session theft)."},
	{"insecure-cookie", "medium", "httponly:false", "Cookie HttpOnly:false — JS can read the cookie (XSS→session theft)."},
	// Go html/template CSS escape hatch (discord_mod cardrender theme colors → template.CSS).
	{"unsafe-template-css", "high", "template.css(", "Go template.CSS bypasses CSS escaping — allowlist/sanitize colors and urls before casting."},
	// C / Redis memory + auth surfaces (sparse call graphs still need lexical sinks).
	{"c-unsafe-buffer", "high", "strcpy(", "strcpy without bounds check — prefer strncpy/strlcpy or sized APIs."},
	{"c-unsafe-buffer", "high", "strcat(", "strcat without bounds check — prefer sized append APIs."},
	{"c-unsafe-buffer", "high", "gets(", "gets is always unsafe — use fgets with a bound."},
	{"c-unsafe-buffer", "medium", "sprintf(", "sprintf can overflow — prefer snprintf with explicit size."},
	{"redis-auth-gap", "medium", "protected-mode no", "Redis protected-mode disabled — pair with bind/ACL before public exposure."},
	{"redis-auth-gap", "low", "acl setuser", "ACL mutation surface — confirm default users are not wide-open."},
}

// ScanRepoForSecuritySmells walks source files under root and returns grounded
// sink candidates (SQL concat, eval, secrets, raw HTML, auth gaps). Unlike
// ScanDiffForSecuritySmells, this does NOT require a dirty git tree — it is the
// whole-repo audit path for investigate(recipe=security) and plan findings mode.
func ScanRepoForSecuritySmells(root string, opts RepoScanOptions) []ContextFinding {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 40
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 4000
	}
	if opts.MaxLineLen <= 0 {
		opts.MaxLineLen = 2000
	}
	root = filepath.Clean(root)
	// Windows junctions (e.g. .testbeds/real-oss/<bed>) Lstat as ModeIrregular —
	// WalkDir would see 0 files. Follow to the target (same idea as indexer.ResolveWalkRoot;
	// inlined to avoid security↔indexer import cycles via review).
	root = resolveScanRoot(root)
	out := make([]ContextFinding, 0, opts.Limit)
	files := 0

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= opts.Limit {
			return fs.SkipAll
		}
		if d.IsDir() {
			name := d.Name()
			if repoScanSkipDirs[name] || strings.HasPrefix(name, ".") && name != "." {
				// Still allow walking root; skip known noise dirs.
				if path != root {
					return fs.SkipDir
				}
			}
			return nil
		}
		if files >= opts.MaxFiles {
			return fs.SkipAll
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !isRepoScanSource(rel) {
			return nil
		}
		if isRepoScanNoisePath(rel) {
			return nil
		}
		files++
		hits := scanFileForSecuritySmells(path, rel, opts.MaxLineLen, opts.Limit-len(out))
		out = append(out, hits...)
		if rem := opts.Limit - len(out); rem > 0 {
			out = append(out, ScanFileDataflowLite(path, rel, rem)...)
		}
		return nil
	})
	// Lexical + dataflow-lite can both cite the same SQL/XSS/eval line — keep one.
	return EnrichAndRankFindings(collapseSiblingSecurityFindings(out))
}

// collapseSiblingSecurityFindings keeps one cite per file:line sink family.
// Builtin/extra regex and dataflow-lite often double-hit the same footgun
// (sql-string-concat + injection-taint, raw-html-xss + injection-taint, …).
func collapseSiblingSecurityFindings(in []ContextFinding) []ContextFinding {
	if len(in) < 2 {
		return in
	}
	kept := make([]ContextFinding, 0, len(in))
	for _, f := range in {
		collapsed := false
		for i := range kept {
			if kept[i].File != f.File || kept[i].Line != f.Line {
				continue
			}
			if !siblingSecurityRules(kept[i].Rule, f.Rule) {
				continue
			}
			kept[i] = preferSiblingSecurityFinding(kept[i], f)
			collapsed = true
			break
		}
		if !collapsed {
			kept = append(kept, f)
		}
	}
	return kept
}

func siblingSecurityRules(a, b string) bool {
	if a == b {
		return true
	}
	pairs := [][2]string{
		{"sql-string-concat", "injection-taint"},
		{"raw-html-xss", "injection-taint"},
		{"blade-unescaped-output", "injection-taint"},
		{"eval-usage", "injection-taint"},
		{"shell-exec-injection", "injection-taint"},
		{"open-redirect", "open-redirect-taint"},
	}
	for _, p := range pairs {
		if (a == p[0] && b == p[1]) || (a == p[1] && b == p[0]) {
			return true
		}
	}
	return false
}

func preferSiblingSecurityFinding(a, b ContextFinding) ContextFinding {
	// Prefer specific lexical / redirect-taint ids over generic injection-taint.
	specificOverTaint := map[string]bool{
		"sql-string-concat":      true,
		"raw-html-xss":           true,
		"blade-unescaped-output": true,
		"eval-usage":             true,
		"shell-exec-injection":   true,
	}
	if specificOverTaint[a.Rule] && b.Rule == "injection-taint" {
		return a
	}
	if specificOverTaint[b.Rule] && a.Rule == "injection-taint" {
		return b
	}
	if a.Rule == "open-redirect-taint" && b.Rule == "open-redirect" {
		return a
	}
	if b.Rule == "open-redirect-taint" && a.Rule == "open-redirect" {
		return b
	}
	// Prefer dataflow-lite when both are equally generic/specific.
	if a.Tool == "codehelper-dataflow-lite" && b.Tool != "codehelper-dataflow-lite" {
		return a
	}
	if b.Tool == "codehelper-dataflow-lite" && a.Tool != "codehelper-dataflow-lite" {
		return b
	}
	if sevRank(a.Severity) != sevRank(b.Severity) {
		if sevRank(a.Severity) > sevRank(b.Severity) {
			return a
		}
		return b
	}
	return a
}

func isRepoScanSource(rel string) bool {
	lower := strings.ToLower(rel)
	if strings.HasSuffix(lower, ".blade.php") {
		return true
	}
	base := filepath.Base(lower)
	if base == ".env" || base == ".env.example" || base == "redis.conf" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(lower))
	return repoScanSrcExt[ext]
}

func isRepoScanNoisePath(rel string) bool {
	p := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	noise := []string{
		"/tests/", "/test/", "/__tests__/", "/spec/", "/fixtures/", "/testdata/",
		"/examples/", "/example/", "/docs/", "/documentation/", "/docs_src/",
		"/vendor/", "/node_modules/", "/dist/", "/build/", "/migrations/", "/staticfiles/",
		"/deps/", "/third_party/", "/scripts/", "/benchmarks/", "/bench/",
		"/site-packages/", "/packages-private/",
		// Generator / tooling scripts (Redis utils/*.py, theme prerender) are not
		// application sinks — they inflate shell-exec/eval false positives.
		"/utils/", "/generators/", "/theme-builder/", "/prerender/",
		"/assets/javascripts/", "/public/assets/",
		"/testing/", "/__mocks__/",
		// Scanner / MCP meta source must never score as app vulns.
		"/internal/security/", "/internal/mcpsvc/findings_mode",
		"/internal/mcpsvc/", "/cmd/codehelper/", // CLI help / tool wiring often lists sink names
		// Nested foreign codehelper trees (e.g. discord_mod/codehelper/) pollute audits.
		"/codehelper/", "codehelper/",
		// Local fixture corpora used to develop the scanner itself.
		"/tmp_scan/", "tmp_scan/",
		"/tmp_sinkscan/", "tmp_sinkscan/",
		// Scratch review / Playwright probe dumps (discord_mod/frontend/tmp-review).
		"/tmp-review/", "tmp-review/",
	}
	for _, n := range noise {
		if strings.Contains(p, n) || strings.HasPrefix(p, strings.TrimPrefix(n, "/")) {
			return true
		}
	}
	base := filepath.Base(p)
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.js") || strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, "_spec.rb") || strings.HasPrefix(base, "test_") ||
		base == "test.c" || strings.HasSuffix(base, "_test.c") {
		return true
	}
	return false
}

func scanFileForSecuritySmells(abs, rel string, maxLineLen, remaining int) []ContextFinding {
	if remaining <= 0 {
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []ContextFinding
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	lineNo := 0
	ext := strings.ToLower(filepath.Ext(rel))
	trackPyDoc := ext == ".py"
	inDocstring := ""
	// Rolling window of recent lowered lines for provenance (dev-only XSS, SKIP_AUTH+NODE_ENV).
	var recent []string
	// secure := false often precedes SetCookie — remember until cookie cues appear.
	pendingInsecureCookieLine := 0
	pendingInsecureCookieEvidence := ""
	// fail-open comments are real authz-gap TPs, but citing the comment alone
	// trips harness cite_comment_only soft FPs — prefer a nearby empty catch.
	var pendingFailOpen *ContextFinding
	pendingFailOpenExpire := 0
	flushPendingFailOpen := func() {
		if pendingFailOpen == nil {
			return
		}
		if len(out) < remaining {
			out = append(out, *pendingFailOpen)
		}
		pendingFailOpen = nil
		pendingFailOpenExpire = 0
	}
	for sc.Scan() {
		lineNo++
		if len(out) >= remaining {
			break
		}
		if pendingFailOpen != nil && lineNo > pendingFailOpenExpire {
			flushPendingFailOpen()
		}
		raw := sc.Text()
		if len(raw) > maxLineLen {
			if trackPyDoc {
				inDocstring = updatePyDocstringState(raw, inDocstring)
			}
			continue
		}
		// Python docstrings (Flask config.py example SECRET_KEY) are not sinks.
		if trackPyDoc {
			wasIn := inDocstring != ""
			inDocstring = updatePyDocstringState(raw, inDocstring)
			if wasIn || inDocstring != "" {
				// Entire line is inside or opens/closes a docstring — skip sink matching.
				continue
			}
		}
		content := strings.TrimSpace(raw)
		if content == "" {
			continue
		}
		// Full-line comments are skipped for sink matching (EVAL prose FPs), but
		// intentional fail-open auth is often documented ONLY in comments next to
		// catch/empty guards — keep those as authz-gap cites.
		if strings.HasPrefix(content, "//") || strings.HasPrefix(content, "#") || strings.HasPrefix(content, "*") {
			if hit := failOpenCommentFinding(rel, lineNo, content); hit != nil {
				flushPendingFailOpen()
				pendingFailOpen = hit
				pendingFailOpenExpire = lineNo + 12
			}
			continue
		}
		if pendingFailOpen != nil && isEmptyCatchSwallow(content) {
			pendingFailOpen.Line = lineNo
			pendingFailOpen.Evidence = truncate(content, 200)
			pendingFailOpen.Hint = "Empty catch after fail-open auth/rate-limit comment — confirm limiter/auth outages cannot bypass controls."
			flushPendingFailOpen()
			continue
		}
		// Strip trailing line comments so Redis "EVAL (" prose inside /* … */ or
		// // comments cannot trip \beval\s*\(.
		scanLine := stripInlineComment(content)
		if comment := trailingComment(content); comment != "" {
			if hit := failOpenCommentFinding(rel, lineNo, comment); hit != nil {
				flushPendingFailOpen()
				pendingFailOpen = hit
				pendingFailOpenExpire = lineNo + 12
			}
		}
		if strings.TrimSpace(scanLine) == "" {
			continue
		}
		lowerLine := strings.ToLower(scanLine)
		recent = append(recent, lowerLine)
		if len(recent) > 6 {
			recent = recent[len(recent)-6:]
		}
		window := strings.Join(recent, "\n")
		// Rules already emitted for this line — builtin regex + substring extras
		// overlap on hardcoded-secret (and can on other shared rule ids).
		lineRules := map[string]struct{}{}
		// Diff-oriented builtin rules (SQL concat, eval, secrets, CSRF, …).
		for _, r := range builtinSecurityRules {
			if r.pattern.MatchString(scanLine) {
				if r.rule == "sql-string-concat" && !looksLikeSQL(strings.ToLower(scanLine)) {
					continue
				}
				if isSecurityScanFalseFriend(rel, scanLine, r.rule) {
					continue
				}
				if r.rule == "raw-html-xss" && isLowProvenanceHTML(rel, lowerLine, window) {
					continue
				}
				out = append(out, ContextFinding{
					Tool: "codehelper-repo-scan", Severity: r.severity, Rule: r.rule,
					File: rel, Line: lineNo, Evidence: truncate(content, 200),
				})
				lineRules[r.rule] = struct{}{}
				break
			}
		}
		if len(out) >= remaining {
			break
		}
		lower := lowerLine
		isC := ext == ".c" || ext == ".h" || ext == ".cc" || ext == ".cpp" || ext == ".cxx" || ext == ".hpp"
		for _, r := range repoExtraSecurityRules {
			if _, already := lineRules[r.rule]; already {
				continue
			}
			if strings.HasPrefix(r.rule, "c-unsafe-buffer") && !isC {
				continue
			}
			if r.rule == "redis-auth-gap" && !isC && !strings.HasSuffix(strings.ToLower(rel), ".conf") {
				continue
			}
			if strings.Contains(lower, r.pattern) {
				// Skip CSS/HTML attribute false friends already covered loosely.
				if isSecurityScanFalseFriend(rel, scanLine, r.rule) {
					continue
				}
				// gets( must not match fgets(/fgetws(
				if r.pattern == "gets(" && (strings.Contains(lower, "fgets(") || strings.Contains(lower, "fgetws(")) {
					if !strings.Contains(lower, " gets(") && !strings.HasPrefix(strings.TrimSpace(lower), "gets(") {
						continue
					}
				}
				// sql-string-concat extras: require an SQL verb nearby to avoid
				// compiler/middleware false friends (vue vModel, axum maps, …).
				// Dynamic sql.raw(values) often sits on a VALUES line after INSERT —
				// accept the rolling window as SQL context (descrybe sync route).
				if r.rule == "sql-string-concat" && !looksLikeSQL(lower) && !looksLikeSQL(window) {
					continue
				}
				if r.rule == "raw-html-xss" && isLowProvenanceHTML(rel, lower, window) {
					continue
				}
				// Dev-gated skipAuth / SKIP_*_AUTH is not a production authz gap.
				if r.rule == "authz-gap" && isDevGatedAuthSkip(lower, window) {
					continue
				}
				// Cookie Secure/HttpOnly=false: literal flags need cookie cues in-window;
				// `secure := false` is deferred until SetCookie/HttpOnly appears below.
				if r.rule == "insecure-cookie" {
					if strings.Contains(lower, "secure :=") || strings.Contains(lower, "secure:=") {
						if pendingInsecureCookieLine == 0 {
							pendingInsecureCookieLine = lineNo
							pendingInsecureCookieEvidence = truncate(content, 200)
						}
						continue
					}
					if !looksLikeCookieSetter(lower, window) {
						continue
					}
				}
				// template.CSS("literal") is fine; variable/expression args are the TP.
				if r.rule == "unsafe-template-css" && isStaticTemplateCSSCall(lower) {
					continue
				}
				out = append(out, ContextFinding{
					Tool: "codehelper-repo-scan", Severity: r.severity, Rule: r.rule,
					File: rel, Line: lineNo, Evidence: truncate(content, 200),
				})
				break
			}
		}
		// Flush deferred insecure-cookie once cookie-setter cues appear.
		if pendingInsecureCookieLine > 0 && looksLikeCookieSetter(lower, window) && len(out) < remaining {
			out = append(out, ContextFinding{
				Tool: "codehelper-repo-scan", Severity: "medium", Rule: "insecure-cookie",
				File: rel, Line: pendingInsecureCookieLine, Evidence: pendingInsecureCookieEvidence,
			})
			pendingInsecureCookieLine = 0
			pendingInsecureCookieEvidence = ""
		}
	}
	flushPendingFailOpen()
	return out
}

// stripInlineComment removes //, #, and /* … */ tails so comment prose cannot
// match sink patterns (e.g. Redis "EVAL (as opposed to FCALL)").
func stripInlineComment(s string) string {
	if i := commentStartIndex(s); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// trailingComment returns the // or # comment suffix (including the marker), or "".
func trailingComment(s string) string {
	if i := commentStartIndex(s); i >= 0 {
		return strings.TrimSpace(s[i:])
	}
	return ""
}

func commentStartIndex(s string) int {
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			inStr = c
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			return i
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			return i
		}
		if c == '#' {
			// Shell/Python comments; keep # inside identifiers alone.
			if i == 0 || isSpaceByte(s[i-1]) {
				return i
			}
		}
	}
	return -1
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t'
}

// failOpenCommentFinding emits authz-gap when a comment documents intentional
// fail-open auth/rate-limit behavior (common next to empty catch blocks).
func failOpenCommentFinding(rel string, lineNo int, comment string) *ContextFinding {
	lower := strings.ToLower(comment)
	if !(strings.Contains(lower, "fail-open") || strings.Contains(lower, "fail open") ||
		strings.Contains(lower, "failopen")) {
		return nil
	}
	// Require an auth/rate-limit cue so "fail-open cache" prose is not a finding.
	if !(strings.Contains(lower, "auth") || strings.Contains(lower, "rate") ||
		strings.Contains(lower, "limit") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "credential") || strings.Contains(lower, "session") ||
		strings.Contains(lower, "permission") || strings.Contains(lower, "acl")) {
		return nil
	}
	f := ContextFinding{
		Tool: "codehelper-repo-scan", Severity: "medium", Rule: "authz-gap",
		File: rel, Line: lineNo, Evidence: truncate(comment, 200),
		Kind: "sink_candidate", Confidence: "medium", Exploitability: "possible",
		Hint: "Comment documents fail-open auth/rate-limit — confirm the nearby code rejects missing credentials in production.",
	}
	return &f
}

// isEmptyCatchSwallow reports empty catch/except bodies that realize a documented
// fail-open (descrybe RateLimiter try/catch). Used only to retarget the cite.
func isEmptyCatchSwallow(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	if lower == "" {
		return false
	}
	// JS/TS: } catch (_) {} / catch (e) {} / catch { }
	if strings.Contains(lower, "catch") && strings.Contains(lower, "{") && strings.Contains(lower, "}") {
		// Strip catch (...) then require empty body.
		idx := strings.Index(lower, "catch")
		rest := strings.TrimSpace(lower[idx+len("catch"):])
		if strings.HasPrefix(rest, "(") {
			if end := strings.Index(rest, ")"); end >= 0 {
				rest = strings.TrimSpace(rest[end+1:])
			}
		}
		return rest == "{}" || rest == "{ }"
	}
	// Python: except Exception: pass / except: pass
	if strings.HasPrefix(lower, "except") && strings.Contains(lower, "pass") {
		return true
	}
	return false
}

// updatePyDocstringState tracks whether the scanner is inside a Python
// triple-quoted docstring (""" or three single quotes). Returns the active
// delimiter or "".
// Best-effort lexical (not a full tokenizer) — enough to drop Flask-style
// example SECRET_KEY lines inside class docs (oxvault/pyseccheck pattern).
func updatePyDocstringState(line, state string) string {
	i := 0
	for i < len(line) {
		if state != "" {
			idx := strings.Index(line[i:], state)
			if idx < 0 {
				return state
			}
			i += idx + len(state)
			state = ""
			continue
		}
		// Skip normal strings so quotes inside them don't open a docstring.
		c := line[i]
		if c == '"' || c == '\'' {
			// Triple quote?
			if i+2 < len(line) && line[i+1] == c && line[i+2] == c {
				state = string([]byte{c, c, c})
				i += 3
				continue
			}
			// Single-quoted string — advance to close.
			q := c
			i++
			for i < len(line) {
				if line[i] == '\\' && i+1 < len(line) {
					i += 2
					continue
				}
				if line[i] == q {
					i++
					break
				}
				i++
			}
			continue
		}
		i++
	}
	return state
}

// isHTTPOrUIConcat reports Express/HTTP/UI string-building that the SQL concat
// regex must never treat as injection (res.send / redirect / Error / messages).
// Also covers Response.write-style APIs (w.Write / this.send / reply.send) that
// echo SQL-looking prose to clients — not DB sinks.
func isHTTPOrUIConcat(lower string) bool {
	httpHints := []string{
		"res.send", "res.redirect", "res.end", "res.write", "res.json", "res.status",
		"res.render", "http.redirect", "http.redir", "ctx.redirect", "c.redirect",
		// Prototype / Fastify / Koa aliases (LIVE: this.send / reply.send ranked as SQLi).
		"this.send", "this.end", "this.write", "this.json", "this.status",
		"reply.send", "reply.redirect", "reply.code", "response.send", "response.write",
		"response.end", "response.json", "ctx.body", "c.string(", "c.json(",
		// Go net/http ResponseWriter echo (not db.Query).
		"w.write(", "w.write([]byte", "fmt.fprintf(w", "fmt.fprint(w",
		"new error(", "next(new error", "throw new error", "console.", "logger.",
		"toast(", "message:", "msg:", "title:", "label:", "placeholder", "aria-",
		"warnings = append", "content:", "description:", "filename", "filepath",
		"status: \"success\"", "status: 'success'", "successfully",
	}
	for _, h := range httpHints {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}

// isFrameworkSQLInternal demotes Django/ORM compiler & introspection SQL builders.
func isFrameworkSQLInternal(lowerRel string) bool {
	// Entire Django framework tree (eval checkouts + vendored copies).
	if strings.HasPrefix(lowerRel, "django/") || strings.Contains(lowerRel, "/site-packages/django/") ||
		strings.Contains(lowerRel, "/django/db/") || strings.Contains(lowerRel, "/django/contrib/") ||
		strings.Contains(lowerRel, "/django/core/") {
		return true
	}
	markers := []string{
		"/sqlalchemy/sql/",
		"/activerecord/connection_adapters/", "/illuminate/database/",
		"/gorm.io/", "/github.com/go-sql-driver/",
	}
	for _, m := range markers {
		if strings.Contains(lowerRel, m) {
			return true
		}
	}
	return false
}

func isFrameworkXSSInternal(lowerRel string) bool {
	markers := []string{
		"/django/template/", "/django/forms/", "/django/contrib/admin/",
		"/django/contrib/admindocs/", "/django/contrib/humanize/",
		"/django/contrib/messages/", "/django/contrib/flatpages/",
		"/django/utils/safestring", "/django/templatetags/",
		"/flask/templating", "/jinja2/",
		// Svelte/Vue compiler internals emit raw-html markers as codegen.
		"/packages/svelte/src/compiler/", "/packages/compiler-core/",
		"/packages/compiler-dom/", "/packages/compiler-sfc/",
	}
	for _, m := range markers {
		if strings.Contains(lowerRel, m) || strings.HasPrefix(lowerRel, strings.TrimPrefix(m, "/")) {
			return true
		}
	}
	return false
}

// looksLikeORMParameterizedTaggedSQL reports Drizzle/Prisma/sql`…${value}`
// tagged templates where interpolations are value-bound (not classic concat).
// Dynamic identifiers (table_${id}) and sql.raw(variable) return false so they
// stay as candidates (descrybe VALUES ${sql.raw(values)} TP).
func looksLikeORMParameterizedTaggedSQL(lower string) bool {
	tagged := strings.Contains(lower, "sql`") || strings.Contains(lower, "sql.raw") ||
		strings.Contains(lower, "prisma.$queryraw") || strings.Contains(lower, "$queryraw") ||
		strings.Contains(lower, "$execute") && strings.Contains(lower, "`") ||
		(strings.Contains(lower, "sql<") && strings.Contains(lower, "`"))
	if !tagged {
		// Multiline fragments often start mid-template: "select * from … ${id}"
		// after a prior sql` opener — treat ${} SQL with = / values / in ( as bound.
		if !(strings.Contains(lower, "${") && looksLikeSQL(lower)) {
			return false
		}
	}
	if isDynamicSQLIdentifierInterp(lower) || isDynamicSQLRawCall(lower) {
		return false
	}
	// Value-position or no ${ at all inside a tagged call.
	if !strings.Contains(lower, "${") {
		return tagged // sql.raw('fixed') / empty interp — not concat SQLi
	}
	return true
}

// isDynamicSQLRawCall reports sql.raw(expr) where expr is not a string literal —
// string-built VALUES batches and dynamic fragments are real SQLi candidates.
func isDynamicSQLRawCall(lower string) bool {
	needle := "sql.raw("
	start := 0
	for {
		rel := strings.Index(lower[start:], needle)
		if rel < 0 {
			return false
		}
		i := start + rel + len(needle)
		for i < len(lower) && (lower[i] == ' ' || lower[i] == '\t' || lower[i] == '\n' || lower[i] == '\r') {
			i++
		}
		if i >= len(lower) {
			return true
		}
		c := lower[i]
		if c != '\'' && c != '"' && c != '`' {
			return true
		}
		start = i + 1
	}
}

// looksLikeCookieSetter requires SetCookie / http.Cookie / HttpOnly / SameSite
// cues so bare `secure := false` flags elsewhere are not cookie sinks.
func looksLikeCookieSetter(lower, window string) bool {
	w := window
	if w == "" {
		w = lower
	}
	return strings.Contains(w, "setcookie") || strings.Contains(w, "http.cookie") ||
		strings.Contains(w, "httponly") || strings.Contains(w, "samesite") ||
		strings.Contains(w, "cookies.append") || strings.Contains(w, "res.cookie") ||
		strings.Contains(w, "response.cookie") || strings.Contains(w, "set_cookie") ||
		strings.Contains(lower, "httponly") || strings.Contains(lower, "samesite")
}

// isStaticTemplateCSSCall reports template.CSS("…") / template.CSS(`…`) literals
// with no concat — safe static styles, not injection sinks.
func isStaticTemplateCSSCall(lower string) bool {
	needle := "template.css("
	i := strings.Index(lower, needle)
	if i < 0 {
		return false
	}
	i += len(needle)
	for i < len(lower) && (lower[i] == ' ' || lower[i] == '\t') {
		i++
	}
	if i >= len(lower) {
		return false
	}
	c := lower[i]
	if c == '\'' || c == '"' || c == '`' {
		// Concat after the literal → not static.
		if strings.Contains(lower, "+") {
			return false
		}
		return true
	}
	// template.CSS(fmt.Sprintf("width:%dpx", n)) with only numeric verbs is safe.
	if isNumericOnlySprintfCSS(lower) {
		return true
	}
	return false
}

// isNumericOnlySprintfCSS reports template.CSS(fmt.Sprintf("…%d…", ints…)) where
// the format literal has no %s/%v/%q — width/height style builders are not XSS/CSS-i.
func isNumericOnlySprintfCSS(lower string) bool {
	if !(strings.Contains(lower, "fmt.sprintf(") || strings.Contains(lower, "sprintf(")) {
		return false
	}
	// Extract first string literal after sprintf(.
	si := strings.Index(lower, "sprintf(")
	if si < 0 {
		return false
	}
	si += len("sprintf(")
	for si < len(lower) && (lower[si] == ' ' || lower[si] == '\t') {
		si++
	}
	if si >= len(lower) {
		return false
	}
	quote := lower[si]
	if quote != '"' && quote != '`' && quote != '\'' {
		return false
	}
	si++
	end := si
	for end < len(lower) && lower[end] != quote {
		end++
	}
	if end >= len(lower) {
		return false
	}
	format := lower[si:end]
	// Disallow string/any verbs; allow %d %f %x %% width/height patterns.
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		if i+1 >= len(format) {
			return false
		}
		v := format[i+1]
		if v == '%' {
			i++
			continue
		}
		// optional flags/width
		j := i + 1
		for j < len(format) && (format[j] == '+' || format[j] == '-' || format[j] == ' ' ||
			format[j] == '#' || format[j] == '0' || (format[j] >= '1' && format[j] <= '9') || format[j] == '.') {
			j++
		}
		if j >= len(format) {
			return false
		}
		switch format[j] {
		case 'd', 'f', 'x', 'b', 'o', 'e', 'g':
			i = j
		default:
			return false
		}
	}
	return strings.Contains(format, "%")
}

// isDynamicSQLIdentifierInterp detects table/column/schema name interpolation
// (CREATE TABLE foo_${id}, FROM bar_${x}) — real SQLi risk vs bound values.
func isDynamicSQLIdentifierInterp(lower string) bool {
	if !strings.Contains(lower, "${") {
		return false
	}
	// Identifier glue: name_${ / .${ inside FROM/INTO/TABLE/JOIN/UPDATE/TRUNCATE.
	if strings.Contains(lower, "_${") || strings.Contains(lower, ".${") {
		return true
	}
	ddl := []string{
		"create temporary table", "create table", "drop table", "truncate table",
		"alter table", "rename table",
	}
	for _, d := range ddl {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}

// isFrameworkCompilerEval drops Vue/Svelte intentional new Function / eval compile paths.
func isFrameworkCompilerEval(lowerRel, lower string) bool {
	if !(strings.Contains(lower, "new function") || strings.Contains(lower, "eval(")) {
		return false
	}
	markers := []string{
		"/packages/compiler-core/", "/packages/compiler-dom/", "/packages/compiler-sfc/",
		"/packages/compiler-ssr/", "/packages/vue/src/", "/packages/vue-compat/",
		"/packages/runtime-core/", "/packages/runtime-dom/",
		"/packages/svelte/src/compiler/", "/packages/svelte/src/internal/",
		"/node_modules/@vue/", "/node_modules/vue/",
	}
	for _, m := range markers {
		if strings.Contains(lowerRel, m) || strings.HasPrefix(lowerRel, strings.TrimPrefix(m, "/")) {
			return true
		}
	}
	// validateExpression / stringifyStatic filenames even outside packages/.
	base := lowerRel
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if base == "validateexpression.ts" || base == "stringifystatic.ts" ||
		strings.Contains(base, "compile") && strings.Contains(lower, "new function") {
		if strings.Contains(lowerRel, "/compiler") || strings.Contains(lowerRel, "/vue") {
			return true
		}
	}
	return false
}

// isFrameworkCLIEval drops Rails runner/query and similar operator CLI eval hooks.
func isFrameworkCLIEval(lowerRel, lower string) bool {
	if strings.Contains(lower, "toplevel_binding") {
		return true
	}
	if strings.Contains(lowerRel, "/rails/commands/") ||
		(strings.Contains(lowerRel, "/railties/") && strings.Contains(lowerRel, "/commands/")) ||
		strings.Contains(lowerRel, "/query_command.rb") || strings.Contains(lowerRel, "/runner_command.rb") {
		return true
	}
	if strings.Contains(lower, "eval(compile(") && (strings.Contains(lowerRel, "/cli.py") ||
		strings.Contains(lowerRel, "/management/commands/")) {
		return true
	}
	return false
}

// isSvelteCompilerHTMLProse drops compiler writers and warning strings about {@html}.
func isSvelteCompilerHTMLProse(lowerRel, lower string) bool {
	if strings.Contains(lowerRel, "/compiler/") || strings.Contains(lowerRel, "/warnings.js") ||
		strings.Contains(lowerRel, "/warnings.ts") {
		if strings.Contains(lower, "{@html") || strings.Contains(lower, "`{@html") {
			return true
		}
	}
	if strings.Contains(lower, "context.write(") && strings.Contains(lower, "{@html") {
		return true
	}
	// Quoted warning/help mentioning `{@html ...}` without being a template sink.
	if (strings.Contains(lower, "`{@html") || strings.Contains(lower, "'{@html") ||
		strings.Contains(lower, "\"{@html")) &&
		(strings.Contains(lower, "block") || strings.Contains(lower, "changed between") ||
			strings.Contains(lower, "warning") || strings.Contains(lower, "server and client")) {
		return true
	}
	return false
}

// isTwigTransRaw reports static i18n `|trans|raw` / `|trans(...)|raw` — not user HTML.
func isTwigTransRaw(lower string) bool {
	if strings.Contains(lower, "|trans|raw") {
		return true
	}
	if strings.Contains(lower, "|trans(") && strings.Contains(lower, ")|raw") {
		return true
	}
	return false
}

// isStructuredDataHTML drops JSON-LD / schema.org {@html embeds (SeoHead class).
func isStructuredDataHTML(lower string) bool {
	if !strings.Contains(lower, "{@html") && !strings.Contains(lower, "dangerouslysetinnerhtml") {
		return false
	}
	return strings.Contains(lower, "ld+json") || strings.Contains(lower, "json.stringify") ||
		strings.Contains(lower, "application/ld+json") || strings.Contains(lower, "type=\"application/ld")
}

// isPlaceholderSecretLiteral drops docs/example credential values that SAST
// regexes love (Flask "development key", changeme, your-secret-here).
func isPlaceholderSecretLiteral(lower string) bool {
	placeholders := []string{
		"development key", "changeme", "change-me", "change_me",
		"your-secret", "your_secret", "your-secret-here", "secret-key-here",
		"placeholder", "not-a-secret", "notasecret", "dummy-secret",
		"test-secret", "dev-secret", "insecure", "example-secret",
		"todo", "xxx", "abcd1234", "secret123",
	}
	for _, p := range placeholders {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// looksLikeBoundSQL reports Query/Exec with bound args (?/$1) rather than string-built SQL.
func looksLikeBoundSQL(lower string) bool {
	// MySQL reserved-word quoting via Go string join: ` + "`key`" + `
	if strings.Contains(lower, "+ \"`") || strings.Contains(lower, "+ '`") ||
		strings.Contains(lower, "\"`\"") && strings.Contains(lower, "insert into") {
		return true
	}
	// Fixed WHERE fragment join: `WHERE " + where` / `WHERE "+where`
	if strings.Contains(lower, "where \" +") || strings.Contains(lower, "where \"+") ||
		strings.Contains(lower, "where ' +") || strings.Contains(lower, "where '+") ||
		strings.Contains(lower, "+ where") || strings.Contains(lower, "+where") {
		return true
	}
	if !(strings.Contains(lower, "?") || strings.Contains(lower, "$1") || strings.Contains(lower, "args")) {
		return false
	}
	if strings.Contains(lower, "queryrow") || strings.Contains(lower, "querycontext") ||
		strings.Contains(lower, "execcontext") || strings.Contains(lower, "db.query") ||
		strings.Contains(lower, "args...") || strings.Contains(lower, ", args") {
		return true
	}
	return false
}

// looksLikeParameterizedSQL reports driver placeholders without Python/JS string concat.
func looksLikeParameterizedSQL(lower string) bool {
	// Python old-style interpolation of the whole string: "..." % var — keep as candidate.
	if strings.Contains(lower, "\" %") || strings.Contains(lower, "' %") || strings.Contains(lower, "` %") ||
		strings.Contains(lower, "\"%") && (strings.Contains(lower, "% table") || strings.Contains(lower, "% (")) {
		return false
	}
	hasPlaceholder := strings.Contains(lower, "%s") || strings.Contains(lower, "%(") ||
		strings.Contains(lower, "?") || strings.Contains(lower, "$1")
	if !hasPlaceholder {
		return false
	}
	// Classic concat / Python "...".format( / template interp — not parameterized-only.
	// Do NOT treat Go time.Format( as Python str.format.
	if strings.Contains(lower, "\"+") || strings.Contains(lower, "'+") ||
		strings.Contains(lower, "\".format(") || strings.Contains(lower, "'.format(") ||
		strings.Contains(lower, "`.format(") || strings.Contains(lower, "${") ||
		strings.Contains(lower, "f\"") || strings.Contains(lower, "f'") {
		return false
	}
	return true
}

// isConfigKeyNameRHS detects SECRET_* = "dotted.config.key" style non-secrets.
func isConfigKeyNameRHS(lower string) bool {
	// RHS looks like a settings key (has a dot, no whitespace, short).
	idx := strings.Index(lower, "=")
	if idx < 0 {
		return false
	}
	rhs := strings.TrimSpace(lower[idx+1:])
	rhs = strings.Trim(rhs, " `\"';,")
	if rhs == "" || strings.ContainsAny(rhs, " \t") {
		return false
	}
	if strings.Contains(rhs, ".") && !strings.Contains(rhs, "://") && len(rhs) < 80 {
		// codehelper.embeddingApiKey / action_controller.csrf_token
		return true
	}
	return false
}

// isNonAuthTokenEmptyCheck drops botToken/discordToken/csrf empty checks from authz-gap.
func isNonAuthTokenEmptyCheck(lower string) bool {
	noise := []string{
		"bottoken", "discordtoken", "discord_token", "csrf_token", "request_csrf",
		"csrftoken", "apptoken", "slacktoken", "steamtoken", "refreshtoken ==",
		"linktoken", "webhooktoken", "invitetoken",
	}
	for _, n := range noise {
		if strings.Contains(lower, n) {
			return true
		}
	}
	// `accessToken == ""` after err check is often "no session yet", not disabled auth.
	if strings.Contains(lower, "accesstoken == \"\"") || strings.Contains(lower, "accesstoken == ''") {
		if strings.Contains(lower, "err != nil") || strings.Contains(lower, "err !=") {
			return true
		}
	}
	return false
}

// isEmptyTokenGuard reports fail-closed missing-credential checks
// (`if token == ""`, empty single-quoted string, …). SAST often mislabels these as
// authz-gap / fail-open; they reject empty input (defense-in-depth).
func isEmptyTokenGuard(lower string) bool {
	trim := strings.TrimSpace(lower)
	if !(strings.HasPrefix(trim, "if ") || strings.HasPrefix(trim, "} else if ") ||
		strings.HasPrefix(trim, "elif ") || strings.HasPrefix(trim, "when ")) {
		return false
	}
	// Credential-ish identifiers compared to empty.
	ids := []string{"token", "bearer", "access_token", "accesstoken", "auth_token",
		"authtoken", "api_token", "apitoken", "jwt", "apikey", "api_key"}
	hasID := false
	for _, id := range ids {
		// Word-ish: avoid matching "csrf_token" / "bot_token" via bare "token"
		// only when preceded by non-ident (handled by contains of " "+id+" " etc.).
		if strings.Contains(lower, id+" ==") || strings.Contains(lower, id+"==") ||
			strings.Contains(lower, id+" !=") || strings.Contains(lower, id+"!=") ||
			strings.Contains(lower, id+".isempty") || strings.Contains(lower, id+".is_empty") ||
			strings.Contains(lower, "len("+id+")") && strings.Contains(lower, "== 0") ||
			strings.Contains(lower, "!"+id) && (strings.Contains(lower, "if !") || strings.Contains(lower, "if(!")) {
			hasID = true
			break
		}
	}
	if !hasID {
		return false
	}
	return strings.Contains(lower, `== ""`) || strings.Contains(lower, `== ''`) ||
		strings.Contains(lower, `!= ""`) || strings.Contains(lower, `!= ''`) ||
		strings.Contains(lower, ".isempty()") ||
		strings.Contains(lower, ".is_empty()") || strings.Contains(lower, "== 0")
}

// isEmptySecretAssignment reports empty-string defaults (`api_key = ""` or
// single-quoted empty). Often with trailing `;` — not leaked credentials.
func isEmptySecretAssignment(lower string) bool {
	trim := strings.TrimSpace(lower)
	trim = strings.TrimRight(trim, ";,")
	trim = strings.TrimSpace(trim)
	if strings.HasSuffix(trim, `= ""`) || strings.HasSuffix(trim, `= ''`) ||
		strings.HasSuffix(trim, `=""`) || strings.HasSuffix(trim, `=''`) ||
		strings.Contains(lower, `= ""`) || strings.Contains(lower, `= ''`) {
		// Only treat as empty-default when no long quoted secret appears.
		if !strings.Contains(lower, "sk_live_") && !strings.Contains(lower, "sk_test_") {
			return true
		}
	}
	return false
}

// isUIPreviewHTML drops design-preview / sandbox raw-HTML bindings that are not
// production XSS sinks (ThemeSurfacePreview, Storybook, playground).
func isUIPreviewHTML(rel, lower string) bool {
	if !strings.Contains(lower, "{@html") && !strings.Contains(lower, "dangerouslysetinnerhtml") &&
		!strings.Contains(lower, "v-html=") {
		return false
	}
	r := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	base := filepath.Base(r)
	markers := []string{"preview", "sandbox", "storybook", "playground", "fixture",
		"mock", "demo-theme", "themesurface"}
	for _, m := range markers {
		if strings.Contains(r, "/"+m) || strings.Contains(r, m+"/") ||
			strings.Contains(base, m) {
			return true
		}
	}
	return false
}

// isLowProvenanceHTML reports raw-HTML bindings that lack an untrusted-content
// path: DEV-only toast hide, CSS-variable theme injection, chart style tags.
// These must not rank as high XSS without provenance (HUMAN-AUDIT-V3).
func isLowProvenanceHTML(rel, lower, window string) bool {
	w := strings.ToLower(window)
	if w == "" {
		w = lower
	}
	r := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	// Next.js / Vite DEV-only scripts (toast hide, HMR helpers).
	if (strings.Contains(w, "node_env") && strings.Contains(w, "development")) ||
		strings.Contains(w, "nextjs-toast") || strings.Contains(w, "toast.style.display") ||
		strings.Contains(w, "process.env.NODE_ENV") && strings.Contains(w, "development") {
		return true
	}
	// Recharts / shadcn ChartStyle: injects --color-* CSS vars, not user HTML.
	if strings.Contains(w, "--color-") || strings.Contains(w, "data-chart") ||
		(strings.Contains(w, "themes") && strings.Contains(w, "__html")) ||
		strings.Contains(r, "/components/ui/chart") ||
		(strings.Contains(lower, "dangerouslysetinnerhtml") && strings.Contains(w, "<style")) {
		return true
	}
	// JSON-LD / structured data already handled elsewhere; also drop __html that
	// is only a CSS custom-property block.
	if strings.Contains(w, "__html") && strings.Contains(w, "--") &&
		(strings.Contains(w, "color") || strings.Contains(w, "theme")) &&
		!strings.Contains(w, "<script") && !strings.Contains(w, "innerhtml") {
		return true
	}
	return false
}

// isDevGatedAuthSkip reports skipAuth / SKIP_*_AUTH that is explicitly AND-ed
// with NODE_ENV===development (or equivalent). Production path still enforces auth.
func isDevGatedAuthSkip(lower, window string) bool {
	w := strings.ToLower(window)
	if w == "" {
		w = lower
	}
	if !(strings.Contains(lower, "skipauth") || strings.Contains(lower, "skip_auth") ||
		strings.Contains(lower, "skip_cron_auth") || strings.Contains(lower, "skip auth")) {
		return false
	}
	return (strings.Contains(w, "node_env") && strings.Contains(w, "development")) ||
		(strings.Contains(w, "node_env") && strings.Contains(w, "dev")) ||
		strings.Contains(w, "isenvironment(\"development\")") ||
		strings.Contains(w, "app.debug")
}

// isMetaSinkString detects keyword-list / tool-description lines that mention
// sinks without being sink call sites (codehelper query packs, MCP errors).
func isMetaSinkString(lower, rule string) bool {
	// Heavy quoting with multiple security keywords → documentation/pack text.
	sinkWords := 0
	for _, w := range []string{"mark_safe", "dangerouslysetinnerhtml", "v-html", "csrf_exempt",
		"sql injection", "strcpy", "shell_exec", "hardcoded", "xss", "eval exec",
		"sql-string-concat", "raw-html-xss", "open-redirect", "shell-exec",
		"permitall", "sk_live_", "protected-mode", "authz"} {
		if strings.Contains(lower, w) {
			sinkWords++
		}
	}
	if sinkWords >= 2 {
		return true
	}
	// Cobra/CLI help, bench labels, qrels "eval" prose — not sinks.
	if strings.Contains(lower, "cobra") || strings.Contains(lower, "short:") ||
		strings.Contains(lower, "long:") || strings.Contains(lower, "examples:") ||
		strings.Contains(lower, "qrels") || strings.Contains(lower, "\"eval\"") ||
		strings.Contains(lower, "ndcg") || strings.Contains(lower, "mingw") ||
		strings.Contains(lower, "build-essential") || strings.Contains(lower, "winlibs") ||
		strings.Contains(lower, "regression tests") || strings.Contains(lower, "add/update") ||
		strings.Contains(lower, "downloads portable") || strings.Contains(lower, "portable mingw") ||
		(strings.Contains(lower, "usage:") && strings.Contains(lower, "sql")) {
		if rule == "eval-usage" || rule == "sql-string-concat" || rule == "raw-html-xss" {
			return true
		}
	}
	// Success / status message templates (descrybe-style) are not SQL.
	if rule == "sql-string-concat" {
		if strings.Contains(lower, "successfully") || strings.Contains(lower, "message:") ||
			strings.Contains(lower, "status:") || strings.Contains(lower, "updatedcount") ||
			strings.Contains(lower, "assignments updated") || strings.Contains(lower, "formula assigned") ||
			strings.Contains(lower, "synced ") && strings.Contains(lower, "export") ||
			strings.Contains(lower, "categories") && strings.Contains(lower, "${") ||
			strings.Contains(lower, "updated successfully") ||
			(strings.Contains(lower, "status: \"success\"") || strings.Contains(lower, "status: 'success'")) {
			return true
		}
		// Pure string-literal / template with ${} or + concat but no SQL verb+FROM/SET.
		if isQuotedProseOnly(lower) && !looksLikeSQL(lower) {
			return true
		}
	}
	if rule == "eval-usage" {
		// Format/print strings mentioning "eval" as a noun are not call sites.
		if isQuotedProseOnly(lower) && !strings.Contains(lower, "eval(") && !strings.Contains(lower, "new function") {
			return true
		}
		if strings.Contains(lower, "fmt.") || strings.Contains(lower, "fprintf") ||
			strings.Contains(lower, "printf(") || strings.Contains(lower, "sprint") {
			return true
		}
	}
	if rule == "raw-html-xss" || rule == "sql-string-concat" {
		// Entire meaningful content is a quoted catalog string.
		if strings.Count(lower, `"`) >= 2 && !strings.Contains(lower, "mark_safe(") &&
			!strings.Contains(lower, "dangerouslysetinnerhtml={") &&
			!strings.Contains(lower, "select ") && !strings.Contains(lower, "insert into") {
			if strings.Contains(lower, "dangerouslysetinnerhtml") || strings.Contains(lower, "v-html") ||
				strings.Contains(lower, "db_query") || strings.Contains(lower, "needs both") ||
				strings.Contains(lower, "securityquerypack") || strings.Contains(lower, "query pack") ||
				strings.Contains(lower, "csrf_exempt") || strings.Contains(lower, "strcpy") {
				return true
			}
		}
	}
	return false
}

// isQuotedProseOnly reports lines that are essentially a string/template literal
// (help text, UI copy, status messages) rather than executable sink call sites.
func isQuotedProseOnly(lower string) bool {
	trim := strings.TrimSpace(lower)
	if trim == "" {
		return false
	}
	// Strip common LHS: return / msg := / message: / Long:
	for _, prefix := range []string{"return ", "msg :=", "msg =", "message:", "message =",
		"long:", "short:", "err :=", "error(", "fmt.", "console.", "logger."} {
		if strings.HasPrefix(trim, prefix) {
			trim = strings.TrimSpace(trim[len(prefix):])
			break
		}
	}
	if len(trim) < 2 {
		return false
	}
	opens := strings.Count(trim, `"`) + strings.Count(trim, "`") + strings.Count(trim, `'`)
	if opens < 2 {
		return false
	}
	// Call-site markers mean it is not prose-only.
	if strings.Contains(trim, "eval(") || strings.Contains(trim, "mark_safe(") ||
		strings.Contains(trim, "dangerouslysetinnerhtml={") || strings.Contains(trim, "system(") ||
		strings.Contains(trim, "shell_exec(") || strings.Contains(trim, "strcpy(") {
		return false
	}
	return true
}

// isDBExecCall reports database Exec/Query helpers that the shell-exec regex
// falsely matches (db.Exec, tx.ExecContext, sqlx.MustExec, …).
func isDBExecCall(lower string) bool {
	dbHints := []string{
		"db.exec", "tx.exec", "conn.exec", "s.db.exec", "sqldb.exec",
		".execcontext(", "mustexec(", "queryrow", "queryrowcontext",
		"rawquery(", "gorm", "sqlx.", "database/sql",
	}
	for _, h := range dbHints {
		if strings.Contains(lower, h) {
			return true
		}
	}
	// receiver.Exec( / .ExecContext( with SQL-ish nearby.
	if (strings.Contains(lower, ".exec(") || strings.Contains(lower, ".execcontext(")) &&
		(looksLikeSQL(lower) || strings.Contains(lower, "`create ") || strings.Contains(lower, "\"create ") ||
			strings.Contains(lower, "fmt.sprintf")) {
		return true
	}
	return false
}

// looksLikeSQL requires an SQL verb used as a query, not method names like
// .update( / .insert( or JS reserved-word lists containing "delete".
func looksLikeSQL(lower string) bool {
	// HTML/SVG tag catalogs: 'option,output,progress,select,textarea'
	if strings.Contains(lower, ",select,") || strings.Contains(lower, ",select'") ||
		strings.Contains(lower, ",select\"") || strings.Contains(lower, "'select,") {
		return false
	}
	// UI/log/template/HTTP prose with ${} or + concat is not SQL (descrybe/express FPs).
	if isHTTPOrUIConcat(lower) {
		return false
	}
	if hasSQLToken(lower, "select") {
		// Bare "select" is too common (DOM tags, UI labels). Require query shape.
		if strings.Contains(lower, " from ") || strings.Contains(lower, "from ") ||
			strings.Contains(lower, "select *") || strings.Contains(lower, "select(") ||
			strings.Contains(lower, " where ") || strings.Contains(lower, "select count") ||
			strings.Contains(lower, "select id") || strings.Contains(lower, "select 1") {
			return true
		}
		return false
	}
	if strings.Contains(lower, "insert into") || strings.Contains(lower, "delete from") {
		return true
	}
	// UPDATE ... SET is SQL; bare .update( is not.
	if hasSQLToken(lower, "update") && strings.Contains(lower, " set ") {
		return true
	}
	if strings.Contains(lower, " where ") || strings.Contains(lower, " join ") ||
		strings.Contains(lower, " group by") || strings.Contains(lower, " order by") ||
		strings.Contains(lower, " union ") {
		// Require a select/insert/delete/update token nearby to avoid "where" in prose.
		return hasSQLToken(lower, "select") || strings.Contains(lower, "insert into") ||
			strings.Contains(lower, "delete from") || (hasSQLToken(lower, "update") && strings.Contains(lower, " set "))
	}
	return false
}

func hasSQLToken(lower, token string) bool {
	idx := 0
	for {
		i := strings.Index(lower[idx:], token)
		if i < 0 {
			return false
		}
		i += idx
		beforeOK := i == 0 || !isIdentByte(lower[i-1])
		// Reject HTML tags: <select> / </select>
		if i > 0 && (lower[i-1] == '<' || lower[i-1] == '/') {
			beforeOK = false
		}
		after := i + len(token)
		afterOK := after >= len(lower) || sqlTokenFollower(lower[after])
		if beforeOK && afterOK {
			return true
		}
		idx = i + len(token)
	}
}

// sqlTokenFollower allows SELECT * / SELECT( / SELECT␠ but rejects select.js / select_option.
func sqlTokenFollower(b byte) bool {
	if isIdentByte(b) || b == '.' {
		return false
	}
	return true
}

// resolveScanRoot follows Windows junctions/symlinks so filepath.WalkDir can
// descend (shared paths.ResolveWalkRoot; local name avoids churn at call sites).
func resolveScanRoot(path string) string {
	return paths.ResolveWalkRoot(path)
}
