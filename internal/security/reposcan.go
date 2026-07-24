package security

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	"tmp_scan": true, // local scanner fixture corpora (codehelper self-scan pollution)
	"__pycache__": true, ".venv": true, "venv": true, ".idea": true, ".vscode": true,
	"third_party": true, "coverage": true, ".next": true, ".nuxt": true, ".cache": true,
	".turbo": true, ".parcel-cache": true, ".output": true, ".svelte-kit": true,
	"storybook-static": true, ".angular": true, ".vercel": true, ".netlify": true,
	".dart_tool": true, ".gradle": true, ".tox": true, ".nyc_output": true,
	"site-packages": true, "testdata": true, "fixtures": true,
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
	return EnrichAndRankFindings(out)
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
	for sc.Scan() {
		lineNo++
		if len(out) >= remaining {
			break
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
				out = append(out, *hit)
			}
			continue
		}
		// Strip trailing line comments so Redis "EVAL (" prose inside /* … */ or
		// // comments cannot trip \beval\s*\(.
		scanLine := stripInlineComment(content)
		if comment := trailingComment(content); comment != "" {
			if hit := failOpenCommentFinding(rel, lineNo, comment); hit != nil {
				out = append(out, *hit)
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
				break
			}
		}
		if len(out) >= remaining {
			break
		}
		lower := lowerLine
		isC := ext == ".c" || ext == ".h" || ext == ".cc" || ext == ".cpp" || ext == ".cxx" || ext == ".hpp"
		for _, r := range repoExtraSecurityRules {
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
				if r.rule == "sql-string-concat" && !looksLikeSQL(lower) {
					continue
				}
				if r.rule == "raw-html-xss" && isLowProvenanceHTML(rel, lower, window) {
					continue
				}
				// Dev-gated skipAuth / SKIP_*_AUTH is not a production authz gap.
				if r.rule == "authz-gap" && isDevGatedAuthSkip(lower, window) {
					continue
				}
				out = append(out, ContextFinding{
					Tool: "codehelper-repo-scan", Severity: r.severity, Rule: r.rule,
					File: rel, Line: lineNo, Evidence: truncate(content, 200),
				})
				break
			}
		}
	}
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

// updatePyDocstringState tracks whether the scanner is inside a Python
// """ / ''' docstring. Returns the active delimiter (""" or ''') or "".
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

// isSecurityScanFalseFriend drops CSS selectors / DI "injection" / HTML ids that
// look like security sinks but are not.
func isSecurityScanFalseFriend(rel, content, rule string) bool {
	lowerRel := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	// Hard demote: framework/ORM/compiler internals are never app sink TPs.
	if isLibraryInternalPath(lowerRel) {
		switch rule {
		case "sql-string-concat", "eval-usage", "shell-exec-injection", "raw-html-xss",
			"blade-unescaped-output", "open-redirect", "hardcoded-secret", "authz-gap",
			"injection-taint", "authz-fail-open":
			return true
		}
	}
	if strings.HasSuffix(lowerRel, ".css") || strings.HasSuffix(lowerRel, ".scss") ||
		strings.Contains(lowerRel, "/css/") || strings.Contains(lowerRel, "/styles/") {
		return true
	}
	trim := strings.TrimSpace(content)
	if strings.HasPrefix(trim, ".") || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "@") {
		// Keep @csrf_exempt decorator lines for authz-gap.
		if !(rule == "authz-gap" && strings.HasPrefix(strings.ToLower(trim), "@csrf_exempt")) {
			return true
		}
	}
	lower := strings.ToLower(content)
	if rule == "raw-html-xss" && (strings.Contains(lower, "class=") && !strings.Contains(lower, "html")) {
		return true
	}
	// Vue/Nest DI: resolveInjections / @Inject — not XSS/injection sinks.
	if strings.Contains(lower, "resolveinjections") || strings.Contains(lower, "@inject(") ||
		strings.Contains(lower, "dependency injection") {
		return true
	}
	// HTTP / UI / error-message string concat is never SQL (express LIVE 20/20 FPs).
	if rule == "sql-string-concat" && isHTTPOrUIConcat(lower) {
		return true
	}
	// Framework ORM/compiler internals are not app SQLi.
	if rule == "sql-string-concat" && isFrameworkSQLInternal(lowerRel) {
		return true
	}
	// Drizzle/Prisma/sql tagged templates with value-position ${} are parameterized — not concat SQLi.
	// Keep dynamic table/identifier interpolation (current_feed_gtins_${id}).
	if rule == "sql-string-concat" && looksLikeORMParameterizedTaggedSQL(lower) {
		return true
	}
	// Bound-arg SQL builders (fixed fragments + ? placeholders) are not classic concat SQLi.
	if rule == "sql-string-concat" && looksLikeBoundSQL(lower) {
		return true
	}
	// DB-API / driver placeholders (%s, %(name)s, $1, ?) without string concat are parameterized.
	if rule == "sql-string-concat" && looksLikeParameterizedSQL(lower) {
		return true
	}
	// Redis / scripting mode flags — not JS eval().
	if rule == "eval-usage" {
		if strings.Contains(lower, "script_eval_mode") || strings.Contains(lower, "eval_mode") ||
			strings.Contains(lower, "evalsha") || strings.Contains(lower, "redisop_eval") ||
			(strings.Contains(lower, "flags") && strings.Contains(lower, "eval")) {
			return true
		}
		// Prose / metrics: "qrels eval (", "eval set", print format strings.
		if strings.Contains(lower, "qrels") || strings.Contains(lower, "ndcg") ||
			strings.Contains(lower, "fmt.fprintf") || strings.Contains(lower, "fmt.sprintf") ||
			strings.Contains(lower, "fmt.fprintln") || strings.Contains(lower, "printf(") {
			return true
		}
		// Method definitions / AST .eval( — django smartif/defaulttags (LIVE FPs).
		if strings.Contains(lower, "def eval") || strings.Contains(lower, ".eval(") ||
			strings.Contains(lower, "class eval") {
			return true
		}
		// Vue/Svelte compiler + runtime compile paths intentionally use new Function — not app RCE.
		if isFrameworkCompilerEval(lowerRel, lower) {
			return true
		}
		// Rails/Flask CLI runner/query commands eval under TOPLEVEL_BINDING — operator tool, not web RCE.
		if isFrameworkCLIEval(lowerRel, lower) {
			return true
		}
		// Require a real call site: eval( with no space-only prose, or new Function(.
		if !strings.Contains(lower, "eval(") && !strings.Contains(lower, "new function") {
			return true
		}
		// "qrels eval (" has space before paren — already excluded by eval( check, but
		// also drop when "eval" is clearly a noun in a format string.
		if strings.Contains(lower, "eval (%") || strings.Contains(lower, "eval (%d") ||
			strings.Contains(lower, `"eval `) || strings.Contains(lower, `'eval `) {
			return true
		}
	}
	// Redis module arg token names / constant key names that look like secrets.
	if rule == "hardcoded-secret" {
		if strings.Contains(lower, ".name =") || strings.Contains(lower, "arg_type") ||
			strings.Contains(lower, "pure_token") || strings.Contains(lower, "withscores") ||
			strings.Contains(lower, "withattribs") || strings.Contains(lower, "email_host_password") ||
			isEmptySecretAssignment(lower) ||
			strings.Contains(lower, "csrf_token") || strings.Contains(lower, "csrf-token") ||
			strings.Contains(lower, "session_token") || strings.Contains(lower, "authenticity_token") ||
			strings.Contains(lower, "action_controller.") || strings.Contains(lower, "request_forgery") ||
			// OAuth/token *endpoint URLs* are not credentials.
			strings.Contains(lower, "https://") || strings.Contains(lower, "http://") ||
			strings.Contains(lower, "/oauth/") || strings.Contains(lower, "/token") && strings.Contains(lower, "url") ||
			// Config *key names* (dotted identifiers), not secret material.
			isConfigKeyNameRHS(lower) ||
			// Docs/examples: "development key", changeme, your-secret-here, …
			isPlaceholderSecretLiteral(lower) ||
			// undefined → "" defaulting (frontend config init) is not a leaked secret.
			strings.Contains(lower, "=== undefined") || strings.Contains(lower, "== undefined") ||
			strings.Contains(lower, "??=") && (strings.Contains(lower, `""`) || strings.Contains(lower, `''`)) {
			return true
		}
	}
	// Same-path / debug redirects are classic open-redirect scanner FPs.
	if rule == "open-redirect" {
		if strings.Contains(lower, "get_full_path") || strings.Contains(lower, "getfullpath") ||
			strings.Contains(lower, "formdataroutingredirect") ||
			strings.Contains(lower, "request.path") ||
			(strings.Contains(lower, "request.url") && !strings.Contains(lower, "request.urlopen")) ||
			strings.Contains(lower, "fullpath") ||
			strings.Contains(lower, "_get_obj_does_not_exist_redirect") ||
			strings.Contains(lower, "does_not_exist_redirect") {
			return true
		}
		// Rails ActionController::Redirecting framework definition — not an app open redirect.
		if strings.Contains(lowerRel, "/action_controller/metal/redirecting") ||
			strings.Contains(lowerRel, "/actionpack/") && strings.Contains(lowerRel, "redirecting") {
			return true
		}
	}
	// Intentional Django/framework trusted-HTML helpers: keep as candidates only via
	// enrich demotion — still drop pure import/re-export / docstring lines here.
	if rule == "raw-html-xss" {
		if strings.Contains(lower, "from django.utils.safestring import") ||
			strings.Contains(lower, "import mark_safe") && !strings.Contains(lower, "mark_safe(") ||
			strings.Contains(lower, "def mark_safe") || strings.Contains(lower, "class safestring") {
			return true
		}
		// JS boolean `!!foo` must not match Blade `{!! $var`.
		if strings.Contains(lower, "{!!") && !strings.Contains(lower, "{!! $") && !strings.Contains(lower, "{!!$") {
			return true
		}
		// Framework template/filter internals — not app XSS.
		if isFrameworkXSSInternal(lowerRel) {
			return true
		}
		// Svelte compiler print/warnings emit `{@html` as prose — not app XSS sinks.
		if isSvelteCompilerHTMLProse(lowerRel, lower) {
			return true
		}
		// Twig `|trans|raw` on static i18n keys is not user-controlled XSS.
		if isTwigTransRaw(lower) {
			return true
		}
		// JSON-LD / schema {@html with JSON.stringify — structured data, not HTML XSS flood.
		if isStructuredDataHTML(lower) {
			return true
		}
		// Theme/UI preview / sandbox {@html — design-time surfaces, not prod XSS.
		if isUIPreviewHTML(lowerRel, lower) {
			return true
		}
	}
	// CSRF getattr checks / csrf_protect wrappers are not authz gaps.
	if rule == "authz-gap" {
		if strings.Contains(lower, "csrf_protect") || strings.Contains(lower, "getattr") &&
			strings.Contains(lower, "csrf_exempt") {
			return true
		}
		if strings.Contains(lower, `"csrf_exempt"`) || strings.Contains(lower, `'csrf_exempt'`) {
			return true
		}
		// Framework definition of csrf_exempt itself is not an app gap.
		if strings.Contains(lower, "def csrf_exempt") || strings.Contains(lower, "function csrf_exempt") {
			return true
		}
		// Empty-token / missing-credential guards are fail-closed, not authz gaps.
		if isEmptyTokenGuard(lower) || isNonAuthTokenEmptyCheck(lower) {
			return true
		}
	}
	// spawn/exec false friends: DB Exec, tokio::spawn, regex Exec, migrate DDL.
	if rule == "shell-exec-injection" {
		if strings.Contains(lower, "tokio::spawn") || strings.Contains(lower, "pg_conn.exec") ||
			(strings.Contains(lower, ".exec(") && (strings.Contains(lower, "escape") || strings.Contains(lower, "pattern") || strings.Contains(lower, "/"))) ||
			(strings.Contains(lower, "compile(") && strings.Contains(lower, "exec")) ||
			strings.Contains(lower, "site-packages") {
			return true
		}
		// database/sql / GORM / sqlx / Prisma-style Exec — not shell.
		if isDBExecCall(lower) {
			return true
		}
		// Schema migrate helpers: CREATE TABLE via db.Exec(fmt.Sprintf(…)).
		if strings.Contains(lowerRel, "/migrate") || strings.Contains(lowerRel, "/migration") ||
			strings.Contains(lowerRel, "/cmd/migrate") {
			if strings.Contains(lower, "create table") || strings.Contains(lower, "insert into") ||
				strings.Contains(lower, "alter table") || strings.Contains(lower, "db.exec") ||
				strings.Contains(lower, ".exec(") {
				return true
			}
		}
		// Django/Flask manage.py shell / management commands — CLI, not web RCE.
		if strings.Contains(lowerRel, "/management/commands/") || strings.Contains(lowerRel, "/management/__init__") ||
			strings.HasSuffix(lowerRel, "manage.py") || strings.Contains(lowerRel, "/django/core/management/") {
			return true
		}
	}
	// Bounded fgets is not the unsafe gets() API.
	if rule == "c-unsafe-buffer" && strings.Contains(lower, "fgets(") && !strings.Contains(lower, " gets(") &&
		!strings.HasPrefix(strings.TrimSpace(lower), "gets(") {
		if !strings.Contains(lower, "strcpy(") && !strings.Contains(lower, "strcat(") && !strings.Contains(lower, "sprintf(") {
			return true
		}
	}
	// libc prototypes / attribute declarations in headers are not call sites.
	if rule == "c-unsafe-buffer" {
		if strings.HasSuffix(lowerRel, ".h") && (strings.Contains(lower, "__attribute__") ||
			strings.Contains(lower, "restrict ") || strings.HasPrefix(strings.TrimSpace(lower), "char *strcpy") ||
			strings.HasPrefix(strings.TrimSpace(lower), "char *strcat") ||
			strings.HasPrefix(strings.TrimSpace(lower), "int sprintf")) {
			return true
		}
	}
	// tree-sitter / query-compiler Exec — not shell.
	if rule == "shell-exec-injection" {
		if strings.Contains(lower, "qc.exec") || strings.Contains(lower, "s.qc.exec") ||
			strings.Contains(lower, "rootnode()") || strings.Contains(lower, "tree-sitter") ||
			strings.Contains(lower, "querycursor") {
			return true
		}
	}
	// Query-pack / docs / error-message strings that list sink names.
	if isMetaSinkString(lower, rule) {
		return true
	}
	// Rule tables / regex definitions that document sinks (not call sites).
	if strings.Contains(lower, "regexp.mustcompile") || strings.Contains(lower, "pattern:") ||
		strings.Contains(lower, `{"raw-html`) || strings.Contains(lower, `{"sql-string`) ||
		strings.Contains(lower, `{"eval-usage`) || strings.Contains(lower, `{"hardcoded-secret`) ||
		strings.Contains(lower, `{"redis-auth`) || strings.Contains(lower, `{"c-unsafe`) ||
		strings.Contains(lower, `{"authz-gap`) || strings.Contains(lower, `{"config-`) ||
		strings.Contains(lower, `rule:     "`) ||
		(strings.Contains(lower, `"`+rule+`"`) && (strings.Contains(lower, "severity") || strings.Contains(lower, "permitall"))) {
		return true
	}
	// Scanner/meta source: entire rule tables and helpers under security/.
	if strings.Contains(lowerRel, "/security/") || strings.Contains(lowerRel, "/mcpsvc/findings_mode") {
		if strings.Contains(lower, "strings.contains") || strings.Contains(lower, "securityquerypack") ||
			strings.Contains(lower, "csrf_exempt") || strings.Contains(lower, "mark_safe") ||
			strings.Contains(lower, "eval(") || strings.Contains(lower, "sk_live_") ||
			strings.Contains(lower, "permitall") || strings.Contains(lower, "protected-mode") ||
			strings.Contains(lower, "redirect(request") || strings.Contains(lower, `{"open-`) ||
			strings.HasPrefix(strings.TrimSpace(lower), "{") && (strings.Contains(lower, `"high"`) || strings.Contains(lower, `"medium"`) || strings.Contains(lower, `"low"`)) {
			return true
		}
	}
	// Help/error examples: e.g. sql="SELECT …
	if rule == "sql-string-concat" && (strings.Contains(lower, "e.g.") || strings.Contains(lower, "newtoolresulterror") ||
		strings.Contains(lower, "needs both connection") || strings.Contains(lower, "read-only select")) {
		return true
	}
	// Redis docs / error strings mentioning ACL or protected-mode — not the config itself.
	if rule == "redis-auth-gap" {
		if strings.Contains(lower, "config set") || strings.Contains(lower, "option") ||
			strings.Contains(lower, "from the loopback") || strings.Contains(lower, "with the '--protected-mode") ||
			strings.Contains(lower, "addreplyerror") || strings.Contains(lower, "error in acl") ||
			strings.Contains(lower, "not configured to use an acl") ||
			(strings.Count(lower, "'") >= 2 && strings.Contains(lower, "protected-mode no") &&
				!strings.HasPrefix(strings.TrimSpace(lower), "protected-mode")) {
			return true
		}
	}
	// Bundled/minified framework assets — eval(require) polyfills are not app sinks.
	if rule == "eval-usage" && (strings.Contains(lowerRel, "/assets/javascripts/") ||
		strings.HasSuffix(lowerRel, ".esm.js") || strings.HasSuffix(lowerRel, ".min.js")) {
		return true
	}
	// Browser automation (rod/playwright) page.Eval — not application code execution sinks.
	if rule == "eval-usage" && (strings.Contains(lower, "rod.eval") || strings.Contains(lower, "page.eval") ||
		strings.Contains(lower, "cap.eval") || strings.Contains(lower, "elementbyjs") ||
		strings.Contains(lower, "page.evaluate") || strings.Contains(lower, "document.body")) {
		return true
	}
	// Framework CLI startup hooks (Flask cli.py eval(compile(…))) — not web RCE.
	if rule == "eval-usage" {
		if strings.Contains(lower, "ast.literal_eval") {
			return true
		}
		if strings.Contains(lower, "eval(compile(") && (strings.Contains(lowerRel, "/cli.py") ||
			strings.Contains(lowerRel, "/flask/cli") || strings.Contains(lowerRel, "/management/commands/")) {
			return true
		}
		if strings.Contains(lowerRel, "/flask/cli.py") || strings.Contains(lowerRel, "/src/flask/cli.py") {
			return true
		}
	}
	// Review/tooling message strings describing sinks.
	if strings.Contains(lower, "unescaped blade") || strings.Contains(lower, "of dynamic data") ||
		(strings.Contains(lower, "return \"") && (strings.Contains(lower, "{!!") || strings.Contains(lower, "mark_safe"))) {
		return true
	}
	// Safe argv-form system/exec (no shell string concat).
	if rule == "shell-exec-injection" && (strings.Contains(lower, "shellwords") ||
		(strings.Contains(lower, `system("`) && strings.Contains(lower, `", "`)) ||
		(strings.Contains(lower, "system(") && strings.Count(lower, `"`) >= 4)) {
		return true
	}
	return false
}

// isHTTPOrUIConcat reports Express/HTTP/UI string-building that the SQL concat
// regex must never treat as injection (res.send / redirect / Error / messages).
func isHTTPOrUIConcat(lower string) bool {
	httpHints := []string{
		"res.send", "res.redirect", "res.end", "res.write", "res.json", "res.status",
		"res.render", "http.redirect", "http.redir", "ctx.redirect", "c.redirect",
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
// Dynamic identifiers (table_${id}) return false so they stay as candidates.
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
	if isDynamicSQLIdentifierInterp(lower) {
		return false
	}
	// Value-position or no ${ at all inside a tagged call.
	if !strings.Contains(lower, "${") {
		return tagged // sql.raw('fixed') / empty interp — not concat SQLi
	}
	return true
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
// (`if token == ""`, `if bearer == ''`, …). SAST often mislabels these as
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

// isEmptySecretAssignment reports `api_key = ""` / `secret = ''` defaults
// (often with trailing `;`) — not leaked credentials.
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

func isIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '$'
}

// sqlTokenFollower allows SELECT * / SELECT( / SELECT␠ but rejects select.js / select_option.
func sqlTokenFollower(b byte) bool {
	if isIdentByte(b) || b == '.' {
		return false
	}
	return true
}
