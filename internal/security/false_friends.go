package security

import "strings"

// falseFriendProbe is the normalized input for security false-friend demotion.
type falseFriendProbe struct {
	rel      string
	lowerRel string
	content  string
	lower    string
	trim     string
	rule     string
}

// falseFriendRule is one demotion check. Empty appliesTo means the check runs for
// every sink rule; otherwise it only runs when probe.rule is listed.
type falseFriendRule struct {
	id        string
	appliesTo []string
	match     func(p falseFriendProbe) bool
}

func falseFriendApplies(appliesTo []string, rule string) bool {
	if len(appliesTo) == 0 {
		return true
	}
	for _, r := range appliesTo {
		if r == rule {
			return true
		}
	}
	return false
}

// libraryInternalSinkRules are demoted entirely when the path is framework/ORM
// compiler internals (never app-sink true positives).
var libraryInternalSinkRules = []string{
	"sql-string-concat", "eval-usage", "shell-exec-injection", "raw-html-xss",
	"blade-unescaped-output", "open-redirect", "hardcoded-secret", "authz-gap",
	"injection-taint", "authz-fail-open",
}

// securityFalseFriendRules is the ordered demotion table for isSecurityScanFalseFriend.
// Order is part of the contract: first match wins (same as the prior if-ladder).
var securityFalseFriendRules = []falseFriendRule{
	{
		id:        "library-internal-path",
		appliesTo: libraryInternalSinkRules,
		match: func(p falseFriendProbe) bool {
			return isLibraryInternalPath(p.lowerRel)
		},
	},
	{
		id: "stylesheet-path",
		match: func(p falseFriendProbe) bool {
			return strings.HasSuffix(p.lowerRel, ".css") || strings.HasSuffix(p.lowerRel, ".scss") ||
				strings.Contains(p.lowerRel, "/css/") || strings.Contains(p.lowerRel, "/styles/")
		},
	},
	{
		id: "css-selector-or-decorator-prefix",
		match: func(p falseFriendProbe) bool {
			if !(strings.HasPrefix(p.trim, ".") || strings.HasPrefix(p.trim, "#") || strings.HasPrefix(p.trim, "@")) {
				return false
			}
			// Keep @csrf_exempt decorator lines for authz-gap.
			if p.rule == "authz-gap" && strings.HasPrefix(strings.ToLower(p.trim), "@csrf_exempt") {
				return false
			}
			return true
		},
	},
	{
		id:        "raw-html-class-attr-not-html",
		appliesTo: []string{"raw-html-xss"},
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "class=") && !strings.Contains(p.lower, "html")
		},
	},
	{
		id: "di-injection-prose",
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "resolveinjections") || strings.Contains(p.lower, "@inject(") ||
				strings.Contains(p.lower, "dependency injection")
		},
	},
	{
		id:        "sql-http-ui-concat",
		appliesTo: []string{"sql-string-concat"},
		match: func(p falseFriendProbe) bool {
			return isHTTPOrUIConcat(p.lower)
		},
	},
	{
		id:        "sql-framework-orm-internal",
		appliesTo: []string{"sql-string-concat"},
		match: func(p falseFriendProbe) bool {
			return isFrameworkSQLInternal(p.lowerRel)
		},
	},
	{
		id:        "sql-orm-parameterized-tagged",
		appliesTo: []string{"sql-string-concat"},
		match: func(p falseFriendProbe) bool {
			return looksLikeORMParameterizedTaggedSQL(p.lower)
		},
	},
	{
		id:        "sql-bound-builder",
		appliesTo: []string{"sql-string-concat"},
		match: func(p falseFriendProbe) bool {
			return looksLikeBoundSQL(p.lower)
		},
	},
	{
		id:        "sql-parameterized-placeholders",
		appliesTo: []string{"sql-string-concat"},
		match: func(p falseFriendProbe) bool {
			return looksLikeParameterizedSQL(p.lower)
		},
	},
	{
		id:        "eval-false-friends",
		appliesTo: []string{"eval-usage"},
		match:     matchEvalFalseFriend,
	},
	{
		id:        "hardcoded-secret-false-friends",
		appliesTo: []string{"hardcoded-secret"},
		match:     matchHardcodedSecretFalseFriend,
	},
	{
		id:        "open-redirect-false-friends",
		appliesTo: []string{"open-redirect", "open-redirect-taint"},
		match:     matchOpenRedirectFalseFriend,
	},
	{
		id:        "raw-html-false-friends",
		appliesTo: []string{"raw-html-xss"},
		match:     matchRawHTMLFalseFriend,
	},
	{
		id:        "authz-gap-false-friends",
		appliesTo: []string{"authz-gap"},
		match:     matchAuthzGapFalseFriend,
	},
	{
		id:        "shell-exec-false-friends",
		appliesTo: []string{"shell-exec-injection"},
		match:     matchShellExecFalseFriend,
	},
	{
		id:        "c-unsafe-fgets-not-gets",
		appliesTo: []string{"c-unsafe-buffer"},
		match: func(p falseFriendProbe) bool {
			if !(strings.Contains(p.lower, "fgets(") && !strings.Contains(p.lower, " gets(") &&
				!strings.HasPrefix(strings.TrimSpace(p.lower), "gets(")) {
				return false
			}
			return !strings.Contains(p.lower, "strcpy(") && !strings.Contains(p.lower, "strcat(") &&
				!strings.Contains(p.lower, "sprintf(")
		},
	},
	{
		id:        "c-unsafe-header-prototypes",
		appliesTo: []string{"c-unsafe-buffer"},
		match: func(p falseFriendProbe) bool {
			if !strings.HasSuffix(p.lowerRel, ".h") {
				return false
			}
			return strings.Contains(p.lower, "__attribute__") ||
				strings.Contains(p.lower, "restrict ") ||
				strings.HasPrefix(strings.TrimSpace(p.lower), "char *strcpy") ||
				strings.HasPrefix(strings.TrimSpace(p.lower), "char *strcat") ||
				strings.HasPrefix(strings.TrimSpace(p.lower), "int sprintf")
		},
	},
	{
		id:        "shell-treesitter-exec",
		appliesTo: []string{"shell-exec-injection"},
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "qc.exec") || strings.Contains(p.lower, "s.qc.exec") ||
				strings.Contains(p.lower, "rootnode()") || strings.Contains(p.lower, "tree-sitter") ||
				strings.Contains(p.lower, "querycursor")
		},
	},
	{
		id: "meta-sink-string",
		match: func(p falseFriendProbe) bool {
			return isMetaSinkString(p.lower, p.rule)
		},
	},
	{
		id: "rule-table-or-regex-definition",
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "regexp.mustcompile") || strings.Contains(p.lower, "pattern:") ||
				strings.Contains(p.lower, `{"raw-html`) || strings.Contains(p.lower, `{"sql-string`) ||
				strings.Contains(p.lower, `{"eval-usage`) || strings.Contains(p.lower, `{"hardcoded-secret`) ||
				strings.Contains(p.lower, `{"redis-auth`) || strings.Contains(p.lower, `{"c-unsafe`) ||
				strings.Contains(p.lower, `{"authz-gap`) || strings.Contains(p.lower, `{"config-`) ||
				strings.Contains(p.lower, `rule:     "`) ||
				(strings.Contains(p.lower, `"`+p.rule+`"`) && (strings.Contains(p.lower, "authorize") || strings.Contains(p.lower, "permitall")))
		},
	},
	{
		id: "scanner-meta-under-security",
		match: func(p falseFriendProbe) bool {
			if !(strings.Contains(p.lowerRel, "/security/") || strings.Contains(p.lowerRel, "/mcpsvc/findings_mode")) {
				return false
			}
			return strings.Contains(p.lower, "strings.contains") || strings.Contains(p.lower, "securityquerypack") ||
				strings.Contains(p.lower, "csrf_exempt") || strings.Contains(p.lower, "mark_safe") ||
				strings.Contains(p.lower, "eval(") || strings.Contains(p.lower, "sk_live_") ||
				strings.Contains(p.lower, "permitall") || strings.Contains(p.lower, "protected-mode") ||
				strings.Contains(p.lower, "redirect(request") || strings.Contains(p.lower, `{"open-`) ||
				(strings.HasPrefix(strings.TrimSpace(p.lower), "{") && (strings.Contains(p.lower, `"high"`) || strings.Contains(p.lower, `"medium"`) || strings.Contains(p.lower, `"low"`)))
		},
	},
	{
		id:        "sql-help-error-examples",
		appliesTo: []string{"sql-string-concat"},
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "e.g.") || strings.Contains(p.lower, "newtoolresulterror") ||
				strings.Contains(p.lower, "needs both connection") || strings.Contains(p.lower, "read-only select")
		},
	},
	{
		id:        "redis-auth-docs-prose",
		appliesTo: []string{"redis-auth-gap"},
		match:     matchRedisAuthGapFalseFriend,
	},
	{
		id:        "eval-bundled-assets",
		appliesTo: []string{"eval-usage"},
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lowerRel, "/assets/javascripts/") ||
				strings.HasSuffix(p.lowerRel, ".esm.js") || strings.HasSuffix(p.lowerRel, ".min.js")
		},
	},
	{
		id:        "eval-browser-automation",
		appliesTo: []string{"eval-usage"},
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "rod.eval") || strings.Contains(p.lower, "page.eval") ||
				strings.Contains(p.lower, "cap.eval") || strings.Contains(p.lower, "elementbyjs") ||
				strings.Contains(p.lower, "page.evaluate") || strings.Contains(p.lower, "document.body") ||
				strings.Contains(p.lower, "$eval(") || strings.Contains(p.lower, "$$eval(") ||
				strings.Contains(p.lower, "locator.evaluate") || strings.Contains(p.lower, "frame.evaluate")
		},
	},
	{
		id:        "eval-framework-cli-hooks",
		appliesTo: []string{"eval-usage"},
		match:     matchEvalFrameworkCLIFalseFriend,
	},
	{
		id: "review-tooling-message-strings",
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "unescaped blade") || strings.Contains(p.lower, "of dynamic data") ||
				(strings.Contains(p.lower, "return \"") && (strings.Contains(p.lower, "{!!") || strings.Contains(p.lower, "mark_safe")))
		},
	},
	{
		id:        "shell-safe-argv-system",
		appliesTo: []string{"shell-exec-injection"},
		match: func(p falseFriendProbe) bool {
			return strings.Contains(p.lower, "shellwords") ||
				(strings.Contains(p.lower, `system("`) && strings.Contains(p.lower, `", "`)) ||
				(strings.Contains(p.lower, "system(") && strings.Count(p.lower, `"`) >= 4)
		},
	},
}

// isSecurityScanFalseFriend drops CSS selectors / DI "injection" / HTML ids that
// look like security sinks but are not.
func isSecurityScanFalseFriend(rel, content, rule string) bool {
	lowerRel := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	trim := strings.TrimSpace(content)
	lower := strings.ToLower(content)
	p := falseFriendProbe{
		rel:      rel,
		lowerRel: lowerRel,
		content:  content,
		lower:    lower,
		trim:     trim,
		rule:     rule,
	}
	for _, r := range securityFalseFriendRules {
		if !falseFriendApplies(r.appliesTo, rule) {
			continue
		}
		if r.match(p) {
			return true
		}
	}
	return false
}

func matchEvalFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
	lowerRel := p.lowerRel
	// Playwright page.$eval / $$eval — `$` is not `\w`, so bare-eval regex matches.
	if strings.Contains(lower, "$eval(") || strings.Contains(lower, "$$eval(") ||
		strings.Contains(lower, ".$eval(") || strings.Contains(lower, ".$$eval(") {
		return true
	}
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
	return false
}

func matchHardcodedSecretFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
	return strings.Contains(lower, ".name =") || strings.Contains(lower, "arg_type") ||
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
		strings.Contains(lower, "??=") && (strings.Contains(lower, `""`) || strings.Contains(lower, `''`))
}

func matchOpenRedirectFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
	lowerRel := p.lowerRel
	if strings.Contains(lower, "get_full_path") || strings.Contains(lower, "getfullpath") ||
		strings.Contains(lower, "formdataroutingredirect") ||
		strings.Contains(lower, "request.path") ||
		(strings.Contains(lower, "request.url") && !strings.Contains(lower, "request.urlopen")) ||
		strings.Contains(lower, "fullpath") ||
		strings.Contains(lower, "_get_obj_does_not_exist_redirect") ||
		strings.Contains(lower, "does_not_exist_redirect") {
		return true
	}
	// Next.js / URL API: new URL('/login', req.url) is same-origin relative — not open redirect.
	if strings.Contains(lower, "new url(") &&
		(strings.Contains(lower, "('/") || strings.Contains(lower, "(\"/") ||
			strings.Contains(lower, "(`/")) {
		return true
	}
	// Config-base + fixed path / request path under known frontend origin (discord_mod).
	if (strings.Contains(lower, "frontendurl+") || strings.Contains(lower, "publicurl+")) &&
		!strings.Contains(lower, "redirectparam") && !strings.Contains(lower, "next=") &&
		!strings.Contains(lower, "return_to") && !strings.Contains(lower, "returnto") {
		return true
	}
	// Rails ActionController::Redirecting framework definition — not an app open redirect.
	return strings.Contains(lowerRel, "/action_controller/metal/redirecting") ||
		strings.Contains(lowerRel, "/actionpack/") && strings.Contains(lowerRel, "redirecting")
}

func matchRawHTMLFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
	lowerRel := p.lowerRel
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
	return isUIPreviewHTML(lowerRel, lower)
}

func matchAuthzGapFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
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
	// JSON/response payloads echoing requireAuth/auth flags are not route config
	// (e.g. NextResponse.json({ sql, requireAuth: false })).
	if isAuthFlagInResponsePayload(lower) {
		return true
	}
	// Empty-token / missing-credential guards are fail-closed, not authz gaps.
	return isEmptyTokenGuard(lower) || isNonAuthTokenEmptyCheck(lower)
}

// isAuthFlagInResponsePayload demotes authz-gap when requireAuth/auth:false appears
// inside an HTTP JSON/response helper — not export const route = { auth: false }.
func isAuthFlagInResponsePayload(lower string) bool {
	hasFlag := strings.Contains(lower, "requireauth") || strings.Contains(lower, "require_auth") ||
		strings.Contains(lower, "auth: false") || strings.Contains(lower, "auth:false") ||
		strings.Contains(lower, "authorization=false") || strings.Contains(lower, "authorize = false")
	if !hasFlag {
		return false
	}
	if strings.Contains(lower, "nextresponse.json") || strings.Contains(lower, "response.json") ||
		strings.Contains(lower, "res.json") || strings.Contains(lower, "res.send") ||
		strings.Contains(lower, "ctx.json") || strings.Contains(lower, "c.json(") ||
		strings.Contains(lower, "reply.send") || strings.Contains(lower, "jsonresponse") ||
		strings.Contains(lower, "httpresponse.json") {
		return true
	}
	// return { … requireAuth: false } — keep export const route = { auth: false }.
	trim := strings.TrimSpace(lower)
	return strings.HasPrefix(trim, "return ") && strings.Contains(trim, "{")
}

func matchShellExecFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
	lowerRel := p.lowerRel
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
	return strings.Contains(lowerRel, "/management/commands/") || strings.Contains(lowerRel, "/management/__init__") ||
		strings.HasSuffix(lowerRel, "manage.py") || strings.Contains(lowerRel, "/django/core/management/")
}

func matchRedisAuthGapFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
	return strings.Contains(lower, "config set") || strings.Contains(lower, "option") ||
		strings.Contains(lower, "from the loopback") || strings.Contains(lower, "with the '--protected-mode") ||
		strings.Contains(lower, "addreplyerror") || strings.Contains(lower, "error in acl") ||
		strings.Contains(lower, "not configured to use an acl") ||
		(strings.Count(lower, "'") >= 2 && strings.Contains(lower, "protected-mode no") &&
			!strings.HasPrefix(strings.TrimSpace(lower), "protected-mode"))
}

func matchEvalFrameworkCLIFalseFriend(p falseFriendProbe) bool {
	lower := p.lower
	lowerRel := p.lowerRel
	if strings.Contains(lower, "ast.literal_eval") {
		return true
	}
	if strings.Contains(lower, "eval(compile(") && (strings.Contains(lowerRel, "/cli.py") ||
		strings.Contains(lowerRel, "/flask/cli") || strings.Contains(lowerRel, "/management/commands/")) {
		return true
	}
	return strings.Contains(lowerRel, "/flask/cli.py") || strings.Contains(lowerRel, "/src/flask/cli.py")
}
