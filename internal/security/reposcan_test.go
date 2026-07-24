package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRepoForSecuritySmells_FindsSinks(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("app/views.py", "from django.utils.safestring import mark_safe\ndef render(u):\n    return mark_safe(u.html)\n")
	mustWrite("app/db.py", "q = \"SELECT * FROM users WHERE id=\" + user_id\n")
	mustWrite("styles/app.css", ".secrets-line { color: red; }\n#search-owner-form { display: block; }\n")
	mustWrite("di/inject.ts", "export function resolveInjections(target: any) { return target; }\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 20})
	if len(got) == 0 {
		t.Fatal("expected at least one sink finding")
	}
	var sawSQL, sawXSS bool
	for _, f := range got {
		switch f.Rule {
		case "sql-string-concat":
			sawSQL = true
			if f.File != "app/db.py" {
				t.Errorf("sql finding file=%q want app/db.py", f.File)
			}
			if f.Line <= 0 {
				t.Errorf("sql finding missing line")
			}
		case "raw-html-xss":
			sawXSS = true
		}
		if strings.Contains(f.File, "styles/") || strings.HasSuffix(f.File, ".css") {
			t.Errorf("CSS path must not appear as security finding: %+v", f)
		}
		if strings.Contains(f.Evidence, "resolveInjections") {
			t.Errorf("DI resolveInjections must not be a security finding: %+v", f)
		}
	}
	if !sawSQL {
		t.Error("expected sql-string-concat finding")
	}
	if !sawXSS {
		t.Error("expected raw-html-xss finding from mark_safe")
	}
}

func TestScanRepoForSecuritySmells_SkipsExamplesAndNonCSPrintf(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("examples/demo.js", "q = \"SELECT * FROM users WHERE id=\" + id\n")
	mustWrite("app/ok.php", "sprintf($fmt, $a);\n")
	mustWrite("src/buf.c", "strcpy(dst, src);\n")
	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 20})
	for _, f := range got {
		if strings.Contains(f.File, "examples/") {
			t.Fatalf("examples must be skipped: %+v", f)
		}
		if f.File == "app/ok.php" && f.Rule == "c-unsafe-buffer" {
			t.Fatalf("sprintf in PHP must not be c-unsafe-buffer: %+v", f)
		}
	}
	var sawC bool
	for _, f := range got {
		if f.File == "src/buf.c" && f.Rule == "c-unsafe-buffer" {
			sawC = true
		}
	}
	if !sawC {
		t.Fatal("expected strcpy finding in C file")
	}
}

func TestLooksLikeSQL_RejectsImports(t *testing.T) {
	if looksLikeSQL(strings.ToLower("import VersionSelect from './VersionSelect.vue'")) {
		t.Fatal("ES import must not look like SQL")
	}
	if looksLikeSQL(strings.ToLower("import { init_select, select_option } from './bindings/select.js';")) {
		t.Fatal("select.js binding import must not look like SQL")
	}
	if looksLikeSQL(strings.ToLower("'option,output,progress,select,textarea,details,dialog,menu,' +")) {
		t.Fatal("HTML tag catalog must not look like SQL")
	}
	if looksLikeSQL(strings.ToLower(`res.send('Viewed ' + count + ' times.')`)) {
		t.Fatal("express res.send concat must not look like SQL")
	}
	if looksLikeSQL(strings.ToLower("const msg = `Updated ${n} attributes`")) {
		t.Fatal("UI template string must not look like SQL")
	}
	if !looksLikeSQL(strings.ToLower(`q = "SELECT * FROM users WHERE id=" + id`)) {
		t.Fatal("real SQL should match")
	}
	if !looksLikeSQL(strings.ToLower(`sql="SELECT id, name FROM users LIMIT 20"`)) {
		t.Fatal("SELECT … FROM example should still look like SQL (filtered elsewhere)")
	}
}

func TestScanRepoRejectsFgets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "src")
	_ = os.MkdirAll(p, 0o755)
	_ = os.WriteFile(filepath.Join(p, "io.c"), []byte("while(fgets(buf,sizeof(buf),fp) != NULL) {}\n"), 0o644)
	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 10})
	for _, f := range got {
		if f.Rule == "c-unsafe-buffer" {
			t.Fatalf("fgets must not be flagged: %+v", f)
		}
	}
}

func TestScanRepoRejectsDBExecAndScriptEvalMode(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("backend/cmd/migrate/main.go", "package main\n_, err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (`, schemaTable))\n")
	mustWrite("src/eval.c", "rctx.flags |= SCRIPT_EVAL_MODE; /* mark the current run as EVAL (as opposed to FCALL) */\n")
	mustWrite("src/fmacros.h", "char *strcpy(char *restrict dest, const char *src) __attribute__((__nothrow__));\n")
	mustWrite("internal/parser/astquery.go", "s.qc.Exec(s.q, tree.RootNode())\n")
	mustWrite("internal/mcpsvc/findings_mode.go", "\"mark_safe dangerouslySetInnerHTML v-html xss\",\n")
	mustWrite("internal/mcpsvc/ops_tools.go", "return mcp.NewToolResultError(\"db_query needs both connection and sql — e.g. connection=\\\"analytics\\\" sql=\\\"SELECT id, name FROM users LIMIT 20\\\".\")\n")
	mustWrite("internal/security/context.go", "pattern:  regexp.MustCompile(`(?i)(api[_-]?key|secret|password|token|sk_live_|sk_test_)\\s*:?=\\s*[\"'][^\"']{8,}[\"']`),\n")
	mustWrite("src/networking_help.c", "\"it with the '--protected-mode no' option. \"\n")
	mustWrite("sentinel.conf", "protected-mode no\n")
	mustWrite("app/real_sql.py", "q = \"SELECT * FROM users WHERE id=\" + user_id\n")
	mustWrite("app/real_xss.py", "return mark_safe(user_html)\n")
	mustWrite("src/buf.c", "strcpy(dst, src);\n")
	mustWrite("serve/mod.rs", "tokio::spawn(async move { serve(listener).await });\n")
	mustWrite("auth/csrf.rb", "CSRF_TOKEN = \"action_controller.csrf_token\"\n")
	mustWrite("admin/options.py", "return HttpResponseRedirect(request.get_full_path())\n")
	mustWrite("actions/msg.ts", "const msg = `Updated ${count} attributes selected`\n")
	mustWrite("examples/cookie-sessions/index.js", "res.send('Viewed ' + count + ' times.');\n")
	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		if strings.Contains(f.File, "migrate") && f.Rule == "shell-exec-injection" {
			t.Fatalf("migrate db.Exec must not be shell-exec: %+v", f)
		}
		if f.File == "src/eval.c" && f.Rule == "eval-usage" {
			t.Fatalf("SCRIPT_EVAL_MODE / comment EVAL must not be eval-usage: %+v", f)
		}
		if f.File == "src/fmacros.h" {
			t.Fatalf("header prototype must not be c-unsafe-buffer: %+v", f)
		}
		if strings.Contains(f.File, "astquery") && f.Rule == "shell-exec-injection" {
			t.Fatalf("tree-sitter Exec must not be shell-exec: %+v", f)
		}
		if strings.Contains(f.File, "findings_mode") {
			t.Fatalf("query-pack string must not be a sink: %+v", f)
		}
		if strings.Contains(f.File, "ops_tools") {
			t.Fatalf("error-message SQL example must not be sql-string-concat: %+v", f)
		}
		if strings.Contains(f.File, "context.go") && f.Rule == "hardcoded-secret" {
			t.Fatalf("regex pattern definition must not be hardcoded-secret: %+v", f)
		}
		if strings.Contains(f.File, "networking_help") {
			t.Fatalf("help text protected-mode must not be redis-auth-gap: %+v", f)
		}
		if strings.Contains(f.File, "serve/mod") && f.Rule == "shell-exec-injection" {
			t.Fatalf("tokio::spawn must not be shell-exec: %+v", f)
		}
		if strings.Contains(f.File, "csrf.rb") && f.Rule == "hardcoded-secret" {
			t.Fatalf("CSRF_TOKEN key name must not be hardcoded-secret: %+v", f)
		}
		if strings.Contains(f.File, "admin/options") && f.Rule == "open-redirect" {
			t.Fatalf("get_full_path redirect must not be open-redirect: %+v", f)
		}
		if strings.Contains(f.File, "actions/msg") && f.Rule == "sql-string-concat" {
			t.Fatalf("UI template string must not be sql-string-concat: %+v", f)
		}
		if strings.Contains(f.File, "examples/") {
			t.Fatalf("examples must be skipped: %+v", f)
		}
	}
	var sawSQL, sawXSS, sawC, sawRedis bool
	for _, f := range got {
		if f.Rule == "sql-string-concat" && f.File == "app/real_sql.py" {
			sawSQL = true
		}
		if f.Rule == "raw-html-xss" && f.File == "app/real_xss.py" {
			sawXSS = true
		}
		if f.File == "src/buf.c" && f.Rule == "c-unsafe-buffer" {
			sawC = true
		}
		if f.File == "sentinel.conf" && f.Rule == "redis-auth-gap" {
			sawRedis = true
		}
	}
	if !sawSQL {
		t.Fatal("expected real SQL concat to remain")
	}
	if !sawXSS {
		t.Fatal("expected real mark_safe to remain")
	}
	if !sawC {
		t.Fatal("expected real strcpy call to remain")
	}
	if !sawRedis {
		t.Fatal("expected real sentinel protected-mode no to remain")
	}
}

func TestEnrichDemotesCSRFTokenAndEnvExample(t *testing.T) {
	in := []ContextFinding{
		{Rule: "hardcoded-secret", Severity: "high", File: "request_forgery_protection.rb", Line: 72,
			Evidence: `CSRF_TOKEN = "action_controller.csrf_token"`},
		{Rule: "config-debug-enabled", Severity: "medium", File: ".env.example", Line: 4,
			Kind: "config_hardening", Evidence: "APP_DEBUG=true"},
		{Rule: "open-redirect", Severity: "medium", File: "admin/options.py", Line: 10,
			Evidence: "HttpResponseRedirect(request.get_full_path())"},
		{Rule: "sql-string-concat", Severity: "high", File: "app/db.py", Line: 1,
			Evidence: `q = "SELECT * FROM users WHERE id=" + id`},
	}
	got := EnrichAndRankFindings(in)
	if got[0].Rule != "sql-string-concat" {
		t.Fatalf("expected real SQL first, got %+v", got[0])
	}
	for _, f := range got {
		switch {
		case strings.Contains(f.File, "request_forgery"):
			if f.Confidence != "low" {
				t.Fatalf("CSRF_TOKEN should be low confidence: %+v", f)
			}
		case strings.Contains(f.File, ".env.example"):
			if f.Severity != "low" || f.Confidence != "low" {
				t.Fatalf(".env.example should be low sev/conf: %+v", f)
			}
		case strings.Contains(f.File, "admin/options"):
			if f.Confidence != "low" {
				t.Fatalf("get_full_path open-redirect should be low: %+v", f)
			}
		}
	}
}

func TestScanRepoRejectsVibeCodingFPs(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Exact vibe-coding LIVE FPs (grounded file:line but not vulns).
	mustWrite("cmd/codehelper/bench.go", "fmt.Fprintf(cmd.OutOrStdout(),\n\t\"qrels eval (%d queries): R@1=%.3f R@5=%.3f\\n\",\n\trep.Queries, rep.Recall1, rep.Recall5)\n")
	mustWrite("cmd/codehelper/update.go", "Long: \"Rebuild from source.\\n\" +\n\t\"On Windows, if gcc is missing, `update` downloads portable MinGW into .vendor/winlibs-mingw64.\\n\",\n")
	mustWrite("internal/review/review_diff.go", "msg := \"Add/update regression tests for \"+f\n")
	mustWrite("actions/attributes-actions.ts", "return {\n  status: \"success\",\n  message: `Category assignments updated successfully for ${categoryIds.length} categories`\n};\n")
	mustWrite("actions/bulk.ts", "message: `Description formula assigned to ${updatedCount} categories and synced ${exportIds.length} export IDs`,\n")
	mustWrite("app/real_sql.py", "q = \"SELECT * FROM users WHERE id=\" + user_id\n")
	mustWrite("app/real_eval.js", "eval(userInput)\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 30})
	for _, f := range got {
		switch {
		case strings.Contains(f.File, "bench.go"):
			t.Fatalf("qrels eval prose must not be eval-usage: %+v", f)
		case strings.Contains(f.File, "update.go"):
			t.Fatalf("cobra Long help must not be sql-string-concat: %+v", f)
		case strings.Contains(f.File, "review_diff.go"):
			t.Fatalf("Add/update regression tests prose must not be sql: %+v", f)
		case strings.Contains(f.File, "attributes-actions") || strings.Contains(f.File, "bulk.ts"):
			t.Fatalf("success message template must not be sql-string-concat: %+v", f)
		}
	}
	var sawSQL, sawEval bool
	for _, f := range got {
		if f.File == "app/real_sql.py" && f.Rule == "sql-string-concat" {
			sawSQL = true
		}
		if f.File == "app/real_eval.js" && f.Rule == "eval-usage" {
			sawEval = true
		}
	}
	if !sawSQL {
		t.Fatal("expected real SQL concat to remain")
	}
	if !sawEval {
		t.Fatal("expected real eval(userInput) to remain")
	}
}

func TestScanRepoRejectsQuotedProseSQL(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("app/ui.ts", "return { status: \"success\", message: `Category assignments updated successfully for ${categoryIds.length} categories` };\n")
	mustWrite("app/help.go", "Long: \"On Windows, if gcc is missing, update downloads portable MinGW into .vendor.\\n\",\n")
	mustWrite("app/real_sql.py", "cur.execute(\"SELECT * FROM users WHERE id=\" + uid)\n")
	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 20})
	for _, f := range got {
		if strings.Contains(f.File, "ui.ts") || strings.Contains(f.File, "help.go") {
			t.Fatalf("quoted prose must not be sink: %+v", f)
		}
	}
	saw := false
	for _, f := range got {
		if f.File == "app/real_sql.py" && f.Rule == "sql-string-concat" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected real SQL concat")
	}
}

// TestScanRepoRejectsExpressHTTPConcatFPs encodes the LIVE express 20/20 failure:
// res.send / redirect / Error / res.end string concat must NEVER be sql-string-concat,
// even when placed under app/ (not only when examples/ is skipped).
func TestScanRepoRejectsExpressHTTPConcatFPs(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("app/cookie-sessions.js", "res.send('viewed ' + req.session.count + ' times\\n');\n")
	mustWrite("app/pet.js", "res.redirect('/pet/' + req.pet.id);\n")
	mustWrite("app/mw.js", "next(new Error('Failed to load user ' + req.params.id));\n")
	mustWrite("app/router.js", "res.end(req.params.op + 'ing ' + req.params.user);\n")
	mustWrite("app/params.js", "res.send('user ' + req.user.name);\n")
	mustWrite("app/real_sql.js", "q = \"SELECT * FROM users WHERE id=\" + req.params.id;\n")
	mustWrite("app/secret.js", "cookieSession({ secret: 'manny is cool' });\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 30})
	for _, f := range got {
		if f.Rule != "sql-string-concat" {
			continue
		}
		switch {
		case strings.Contains(f.File, "cookie-sessions"), strings.Contains(f.File, "pet.js"),
			strings.Contains(f.File, "mw.js"), strings.Contains(f.File, "router.js"),
			strings.Contains(f.File, "params.js"):
			t.Fatalf("express HTTP concat must not be sql-string-concat: %+v", f)
		}
	}
	sawSQL := false
	for _, f := range got {
		if f.File == "app/real_sql.js" && f.Rule == "sql-string-concat" {
			sawSQL = true
		}
	}
	if !sawSQL {
		t.Fatal("expected real SQL concat with req.params to remain")
	}
}

// TestScanRepoRejectsDescrybeMessageSQL encodes success-template LIVE FPs.
func TestScanRepoRejectsDescrybeMessageSQL(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("actions/attributes-actions.ts", "return {\n  status: \"success\",\n  message: `Category assignments updated successfully for ${categoryIds.length} categories`\n};\n")
	mustWrite("actions/bulk.ts", "message: `Description formula assigned to ${updatedCount} categories and synced ${exportIds.length} export IDs`,\n")
	mustWrite("components/ui/form.tsx", "aria-invalid={!!error}\n")
	mustWrite("app/logs.tsx", "isOpen={!!selectedLog}\n")
	mustWrite("app/sync.ts", "await db.execute(`INSERT INTO current_feed_gtins_${feed.id} (gtin) VALUES (${gtin})`);\n")
	mustWrite("app/inj.py", "q = \"SELECT * FROM users WHERE id=\" + user_id\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 30})
	for _, f := range got {
		if strings.Contains(f.File, "attributes-actions") || strings.Contains(f.File, "bulk.ts") {
			t.Fatalf("success message must not be sink: %+v", f)
		}
		if (strings.Contains(f.File, "form.tsx") || strings.Contains(f.File, "logs.tsx")) &&
			f.Rule == "raw-html-xss" {
			t.Fatalf("JS !!bool must not be Blade raw-html: %+v", f)
		}
	}
	var sawDyn, sawInj bool
	for _, f := range got {
		if f.Rule == "sql-string-concat" && strings.Contains(f.File, "sync.ts") {
			sawDyn = true
		}
		if f.Rule == "sql-string-concat" && strings.Contains(f.File, "inj.py") {
			sawInj = true
		}
	}
	if !sawDyn {
		t.Fatal("expected dynamic table-name SQL to remain")
	}
	if !sawInj {
		t.Fatal("expected classic SQL concat to remain")
	}
}

// TestScanRepoRejectsDjangoFrameworkFPs encodes smartif .eval / csrf getattr / SQL builder FPs.
func TestScanRepoRejectsDjangoFrameworkFPs(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("django/template/smartif.py", "class Literal:\n    def eval(self, context):\n        return self.value\n\ndef resolve(condition, context):\n    return condition.eval(context)\n")
	mustWrite("django/contrib/admin/sites.py", "if not getattr(view, \"csrf_exempt\", False):\n    view = csrf_protect(inner)\n")
	mustWrite("django/db/models/sql/compiler.py", "part_sql = \"SELECT * FROM ({})\".format(part_sql)\n")
	mustWrite("django/core/management/commands/shell.py", "exec(sys.stdin.read(), {**globals(), **self.get_namespace(**options)})\n")
	mustWrite("django/middleware/csrf.py", "if request_csrf_token == \"\":\n    pass\n")
	mustWrite("mysite/views.py", "@csrf_exempt\ndef webhook(request):\n    return HttpResponse(\"ok\")\n")
	mustWrite("mysite/bad.py", "q = \"SELECT * FROM users WHERE id=\" + request.GET['id']\n")
	mustWrite("mysite/rce.py", "eval(user_input)\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		switch {
		case strings.Contains(f.File, "smartif") && f.Rule == "eval-usage":
			t.Fatalf("template AST .eval must not be eval-usage: %+v", f)
		case strings.Contains(f.File, "admin/sites") && f.Rule == "authz-gap":
			t.Fatalf("csrf_protect/getattr csrf_exempt must not be authz-gap: %+v", f)
		case strings.Contains(f.File, "compiler.py") && f.Rule == "sql-string-concat":
			t.Fatalf("django SQL compiler must not be app SQLi: %+v", f)
		case strings.Contains(f.File, "shell.py") && f.Rule == "shell-exec-injection":
			t.Fatalf("manage.py shell exec must not be web RCE: %+v", f)
		case strings.Contains(f.File, "csrf.py") && f.Rule == "authz-gap":
			t.Fatalf("request_csrf_token == \"\" must not be authz-gap: %+v", f)
		}
	}
	var sawExempt, sawSQL, sawEval bool
	for _, f := range got {
		if strings.Contains(f.File, "views.py") && f.Rule == "authz-gap" {
			sawExempt = true
		}
		if strings.Contains(f.File, "bad.py") && f.Rule == "sql-string-concat" {
			sawSQL = true
		}
		if strings.Contains(f.File, "rce.py") && f.Rule == "eval-usage" {
			sawEval = true
		}
	}
	if !sawExempt {
		t.Fatal("expected real @csrf_exempt to remain")
	}
	if !sawSQL {
		t.Fatal("expected app SQL concat to remain")
	}
	if !sawEval {
		t.Fatal("expected bare eval(user_input) to remain")
	}
}

// TestScanRepoRejectsDiscordModFPs encodes db.Exec-as-shell / OAuth-as-secret FPs.
func TestScanRepoRejectsDiscordModFPs(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("backend/cmd/migrate/main.go", "package main\n_, err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id INT)`, schemaTable))\n")
	mustWrite("backend/internal/guildconfig/store.go", "_, err := s.DB.Exec(ctx, q, args...)\n")
	mustWrite("backend/internal/auth/handler.go", "const discordTokenURL = \"https://discord.com/api/oauth2/token\"\n")
	mustWrite("backend/internal/api/handlers.go", "if botToken == \"\" {\n\treturn errMissing\n}\n")
	mustWrite("backend/cmd/bot/main.go", "if token == \"\" {\n\tlog.Fatal(\"DISCORD_BOT_TOKEN is required\")\n}\n")
	mustWrite("backend/internal/discord/channel_send.go", "if token == \"\" {\n\treply(\"Could not create\")\n\treturn\n}\n")
	mustWrite("backend/internal/steam/link_handler.go", "if token == \"\" {\n\thttp.Error(w, \"Missing link token.\", 400)\n\treturn\n}\n")
	mustWrite("frontend/src/routes/dashboard/+page.svelte", "if (config.personality_bot_llm_api_key === undefined) config.personality_bot_llm_api_key = \"\";\n")
	mustWrite("backend/internal/guildknowledge/store.go", "_, err := d.sql.Exec(`UPDATE messages SET deleted=1, content='', collected_at=? WHERE message_id=?`, time.Now().UTC().Format(time.RFC3339), messageID)\n")
	mustWrite("backend/internal/api/warn.go", "warnings = append(warnings, \"user \"+userID+\" warned\")\n")
	mustWrite("backend/internal/guildconfig/analytics.go",
		"err := s.DB.QueryRowContext(ctx, \"SELECT COUNT(*) FROM counting_events WHERE \"+where, args...).Scan(&n)\n")
	mustWrite("app/real_sql.go", "q := \"SELECT * FROM users WHERE id=\" + id\n")
	mustWrite("app/real_secret.go", "api_key = \"sk_live_abcdefghijklmnop\"\n")
	mustWrite("app/real_authz.go", "auth: false\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		switch {
		case strings.Contains(f.File, "migrate") && f.Rule == "shell-exec-injection":
			t.Fatalf("db.Exec migrate must not be shell-exec: %+v", f)
		case strings.Contains(f.File, "store.go") && f.Rule == "shell-exec-injection":
			t.Fatalf("s.DB.Exec must not be shell-exec: %+v", f)
		case strings.Contains(f.File, "guildknowledge") && f.Rule == "sql-string-concat":
			t.Fatalf("parameterized Exec with time.Format must not be sql-concat: %+v", f)
		case strings.Contains(f.File, "handler.go") && f.Rule == "hardcoded-secret":
			t.Fatalf("OAuth URL must not be hardcoded-secret: %+v", f)
		case strings.Contains(f.File, "handlers.go") && f.Rule == "authz-gap":
			t.Fatalf("botToken == \"\" must not be authz-gap: %+v", f)
		case strings.Contains(f.File, "main.go") && f.Rule == "authz-gap":
			t.Fatalf("if token == \"\" fail-closed must not be authz-gap: %+v", f)
		case strings.Contains(f.File, "channel_send") && f.Rule == "authz-gap":
			t.Fatalf("channel_send token == \"\" must not be authz-gap: %+v", f)
		case strings.Contains(f.File, "link_handler") && f.Rule == "authz-gap":
			t.Fatalf("steam link token == \"\" must not be authz-gap: %+v", f)
		case strings.Contains(f.File, "+page.svelte") && f.Rule == "hardcoded-secret":
			t.Fatalf("undefined → \"\" api_key default must not be hardcoded-secret: %+v", f)
		case strings.Contains(f.File, "warn.go") && f.Rule == "sql-string-concat":
			t.Fatalf("warning append must not be sql-string-concat: %+v", f)
		case strings.Contains(f.File, "analytics.go") && f.Rule == "sql-string-concat":
			t.Fatalf("bound WHERE+args builder must not be high sql-concat: %+v", f)
		}
	}
	var sawSQL, sawSecret, sawAuthz bool
	for _, f := range got {
		if strings.Contains(f.File, "real_sql") && f.Rule == "sql-string-concat" {
			sawSQL = true
		}
		if strings.Contains(f.File, "real_secret") && f.Rule == "hardcoded-secret" {
			sawSecret = true
		}
		if strings.Contains(f.File, "real_authz") && f.Rule == "authz-gap" {
			sawAuthz = true
		}
	}
	if !sawSQL {
		t.Fatal("expected real SQL concat to remain")
	}
	if !sawSecret {
		t.Fatal("expected real sk_live_ secret to remain")
	}
	if !sawAuthz {
		t.Fatal("expected real auth: false to remain")
	}
}

func TestScanRepoForSecuritySmells_EmptyOnClean(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ok.go")
	if err := os.WriteFile(p, []byte("package ok\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 10})
	if len(got) != 0 {
		t.Fatalf("clean file should yield no findings, got %+v", got)
	}
}

// TestScanRepoRejectsFlaskDocstringAndCLIEval encodes LIVE flask FPs:
// docstring SECRET_KEY = 'development key' and cli.py eval(compile(...)).
func TestScanRepoRejectsFlaskDocstringAndCLIEval(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("src/flask/config.py", "class Config(dict):\n    \"\"\"Works like a dict.\n\n        DEBUG = True\n        SECRET_KEY = 'development key'\n        app.config.from_object(__name__)\n\n    Only uppercase keys are added.\n    \"\"\"\n\n    def __init__(self):\n        pass\n")
	mustWrite("src/flask/cli.py", "def load_dotenv():\n    eval(compile(f.read(), startup, \"exec\"), ctx)\n")
	mustWrite("app/settings.py", "SECRET_KEY = 'super-secret-production-value-xyz'\n")
	mustWrite("app/rce.py", "eval(user_input)\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		ev := strings.ToLower(f.Evidence)
		switch {
		case strings.Contains(f.File, "config.py") && f.Rule == "hardcoded-secret" &&
			strings.Contains(ev, "development key"):
			t.Fatalf("docstring SECRET_KEY must not be hardcoded-secret: %+v", f)
		case strings.Contains(f.File, "cli.py") && f.Rule == "eval-usage":
			t.Fatalf("flask cli eval(compile) must not be eval-usage: %+v", f)
		}
	}
	var sawSecret, sawEval bool
	for _, f := range got {
		if strings.Contains(f.File, "settings.py") && f.Rule == "hardcoded-secret" {
			sawSecret = true
		}
		if strings.Contains(f.File, "rce.py") && f.Rule == "eval-usage" {
			sawEval = true
		}
	}
	if !sawSecret {
		t.Fatal("expected real app SECRET_KEY to remain")
	}
	if !sawEval {
		t.Fatal("expected bare eval(user_input) to remain")
	}
}

// TestScanRepoSkipsTmpScanFixtures excludes scanner self-test corpora.
func TestScanRepoSkipsTmpScanFixtures(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("tmp_scan/main.go", "package main\nq := \"SELECT * FROM users WHERE id=\" + id\neval(x)\n")
	mustWrite("app/real.go", "q := \"SELECT * FROM users WHERE id=\" + id\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 20})
	for _, f := range got {
		if strings.Contains(f.File, "tmp_scan") {
			t.Fatalf("tmp_scan fixture must be skipped: %+v", f)
		}
	}
	var saw bool
	for _, f := range got {
		if strings.Contains(f.File, "real.go") && f.Rule == "sql-string-concat" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected real SQL concat outside tmp_scan")
	}
}

func TestUpdatePyDocstringState(t *testing.T) {
	st := ""
	st = updatePyDocstringState(`    """hello`, st)
	if st != `"""` {
		t.Fatalf("expected open, got %q", st)
	}
	st = updatePyDocstringState(`        SECRET_KEY = 'development key'`, st)
	if st != `"""` {
		t.Fatalf("expected still open, got %q", st)
	}
	st = updatePyDocstringState(`    """`, st)
	if st != "" {
		t.Fatalf("expected closed, got %q", st)
	}
}

// TestScanRepoRejectsDrizzleParameterizedSQL keeps dynamic table-name SQL but
// drops value-bound drizzle/prisma sql`…${id}` templates (descrybe LIVE FPs).
func TestScanRepoRejectsDrizzleParameterizedSQL(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("lib/processing/job-manager.ts", "const [jobResult] = await db.execute(sql`\n        SELECT * FROM processing_jobs WHERE id = ${jobId}\n      `) as any[];\n")
	mustWrite("app/api/feeds/custom-fields/route.ts", "await db.execute(sql`SELECT id, company_id FROM xml_feeds WHERE id = ${feedIdStr}`);\n")
	mustWrite("app/api/feeds/sync/route.ts", "await db.execute(sql`INSERT INTO current_feed_gtins_${feed.id} (gtin) VALUES (${gtin})`);\n")
	mustWrite("app/classic.py", "q = \"SELECT * FROM users WHERE id=\" + user_id\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		if f.Rule != "sql-string-concat" {
			continue
		}
		if strings.Contains(f.File, "job-manager") || strings.Contains(f.File, "custom-fields") {
			t.Fatalf("parameterized drizzle sql` must not be sink: %+v", f)
		}
	}
	var sawDyn, sawClassic bool
	for _, f := range got {
		if f.Rule == "sql-string-concat" && strings.Contains(f.File, "sync/route") {
			sawDyn = true
		}
		if f.Rule == "sql-string-concat" && strings.Contains(f.File, "classic.py") {
			sawClassic = true
		}
	}
	if !sawDyn {
		t.Fatal("expected dynamic table-name SQL to remain")
	}
	if !sawClassic {
		t.Fatal("expected classic SQL concat to remain")
	}
}

// TestScanRepoRejectsSvelteHTMLFlood drops compiler/warnings/JSON-LD {@html FPs
// while keeping a real app {@html binding (discord_mod / svelte LIVE).
func TestScanRepoRejectsSvelteHTMLFlood(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("packages/svelte/src/compiler/print/index.js", "context.write('{@html ');\n")
	mustWrite("packages/svelte/src/internal/client/warnings.js", ": 'The value of an `{@html ...}` block changed between server and client renders';\n")
	mustWrite("frontend/src/lib/components/SeoHead.svelte", "{@html `<script type=\"application/ld+json\">${JSON.stringify(schema)}<\\/script>`}\n")
	mustWrite("frontend/src/lib/components/ThemeSurfacePreview.svelte", "<div class=\"preview-rich title\">{@html rankDetail}</div>\n")
	mustWrite("src/Comment.svelte", "{@html userComment}\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		if f.Rule != "raw-html-xss" {
			continue
		}
		switch {
		case strings.Contains(f.File, "compiler/print"):
			t.Fatalf("compiler print {@html must not be sink: %+v", f)
		case strings.Contains(f.File, "warnings.js"):
			t.Fatalf("warning prose {@html must not be sink: %+v", f)
		case strings.Contains(f.File, "SeoHead"):
			t.Fatalf("JSON-LD {@html must not be sink: %+v", f)
		case strings.Contains(f.File, "ThemeSurfacePreview"):
			t.Fatalf("preview {@html must not be sink: %+v", f)
		}
	}
	var sawReal bool
	for _, f := range got {
		if f.Rule != "raw-html-xss" {
			continue
		}
		if strings.Contains(f.File, "Comment.svelte") {
			sawReal = true
		}
	}
	if !sawReal {
		t.Fatal("expected real app {@html userComment to remain")
	}
}

// TestScanRepoRejectsVueCompilerEval drops packages/compiler new Function FPs.
func TestScanRepoRejectsVueCompilerEval(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("packages/compiler-core/src/validateExpression.ts", "new Function(\n  `return (${exp.content})`\n)\n")
	mustWrite("packages/compiler-dom/src/transforms/stringifyStatic.ts", "return new Function(`return (${exp.content})`)()\n")
	mustWrite("packages/vue/src/index.ts", "__GLOBAL__ ? new Function(code)() : new Function('Vue', code)(runtimeDom)\n")
	mustWrite("app/evil.ts", "eval(userInput)\n")
	mustWrite("app/evil2.ts", "const f = new Function(userCode)\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		if f.Rule == "eval-usage" && (strings.Contains(f.File, "packages/compiler") ||
			strings.Contains(f.File, "packages/vue/")) {
			t.Fatalf("vue compiler new Function must not be eval-usage: %+v", f)
		}
	}
	var sawEval, sawFn bool
	for _, f := range got {
		if f.Rule == "eval-usage" && strings.Contains(f.File, "evil.ts") {
			sawEval = true
		}
		if f.Rule == "eval-usage" && strings.Contains(f.File, "evil2.ts") {
			sawFn = true
		}
	}
	if !sawEval {
		t.Fatal("expected app eval(userInput) to remain")
	}
	if !sawFn {
		t.Fatal("expected app new Function(userCode) to remain")
	}
}

// TestScanRepoRejectsRailsCLIEval drops railties query/runner CLI eval FPs.
func TestScanRepoRejectsRailsCLIEval(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("railties/lib/rails/commands/query/query_command.rb", "relation = with_readonly_connection { eval(expression, TOPLEVEL_BINDING, \"(query)\") }\n")
	mustWrite("railties/lib/rails/commands/runner/runner_command.rb", "eval(code_or_file, TOPLEVEL_BINDING, __FILE__, __LINE__)\n")
	mustWrite("actionpack/lib/action_controller/metal/redirecting.rb", "redirect_to_location = _compute_redirect_to_location(request, options)\n")
	mustWrite("app/controllers/evil_controller.rb", "eval(params[:code])\n")
	mustWrite("app/controllers/sessions_controller.rb", "redirect(request.params[:return_to])\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		switch {
		case strings.Contains(f.File, "query_command") && f.Rule == "eval-usage":
			t.Fatalf("rails query CLI eval must not be sink: %+v", f)
		case strings.Contains(f.File, "runner_command") && f.Rule == "eval-usage":
			t.Fatalf("rails runner CLI eval must not be sink: %+v", f)
		case strings.Contains(f.File, "redirecting.rb") && f.Rule == "open-redirect":
			t.Fatalf("ActionController::Redirecting must not be open-redirect: %+v", f)
		}
	}
	var sawEval, sawRedir bool
	for _, f := range got {
		if strings.Contains(f.File, "evil_controller") && f.Rule == "eval-usage" {
			sawEval = true
		}
		if strings.Contains(f.File, "sessions_controller") && f.Rule == "open-redirect" {
			sawRedir = true
		}
	}
	if !sawEval {
		t.Fatal("expected app eval(params) to remain")
	}
	if !sawRedir {
		t.Fatal("expected app redirect(request.params) to remain")
	}
}

// TestScanRepoRejectsTwigTransRaw drops Symfony |trans|raw i18n FPs.
func TestScanRepoRejectsTwigTransRaw(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("templates/blog/about.html.twig", "{{ 'help.app_description'|trans|raw }}\n")
	mustWrite("templates/bundles/TwigBundle/Exception/error.html.twig", "{{ 'http_error.suggestion'|trans({url: path('blog_index')})|raw }}\n")
	mustWrite("templates/blog/post_show.html.twig", "{{ post.content|raw }}\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		if f.Rule == "raw-html-xss" && (strings.Contains(f.File, "about.html") || strings.Contains(f.File, "error.html")) {
			t.Fatalf("|trans|raw must not be sink: %+v", f)
		}
	}
	var sawContent bool
	for _, f := range got {
		if f.Rule == "raw-html-xss" && strings.Contains(f.File, "post_show") {
			sawContent = true
		}
	}
	if !sawContent {
		t.Fatal("expected post.content|raw to remain as candidate")
	}
}


func TestScanRepoRejectsLowProvenanceHTML(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("app/layout.tsx", ""+
		"export default function Root() {\n"+
		"  return (\n"+
		"    <html><head>\n"+
		"      {process.env.NODE_ENV === 'development' && (\n"+
		"        <script dangerouslySetInnerHTML={{\n"+
		"          __html: \"document.querySelector('.nextjs-toast')\"\n"+
		"        }} />\n"+
		"      )}\n"+
		"    </head></html>\n"+
		"  )\n}\n")
	mustWrite("components/ui/chart.tsx", ""+
		"const ChartStyle = ({ id }) => (\n"+
		"  <style dangerouslySetInnerHTML={{\n"+
		"    __html: `[data-chart=${id}] { --color-primary: blue; }`\n"+
		"  }} />\n)\n")
	mustWrite("app/api/cron/job/route.ts", ""+
		"const skipAuth = process.env.SKIP_CRON_AUTH === 'true' && process.env.NODE_ENV === 'development';\n"+
		"if (!skipAuth && secret !== KEY) throw new Error('unauthorized')\n")
	mustWrite("app/Comment.tsx", ""+
		"export function Comment({ html }) {\n"+
		"  return <div dangerouslySetInnerHTML={{ __html: html }} />\n}\n")
	mustWrite("app/public.ts", "export const route = { auth: false }\n")

	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 40})
	for _, f := range got {
		if f.Rule == "raw-html-xss" && (strings.Contains(f.File, "layout.tsx") || strings.Contains(f.File, "chart.tsx")) {
			t.Fatalf("low-provenance HTML must not be high XSS sink: %+v", f)
		}
		if f.Rule == "authz-gap" && strings.Contains(f.File, "cron") {
			t.Fatalf("DEV-gated skipAuth must not be authz-gap: %+v", f)
		}
	}
	var sawRealXSS, sawAuthz bool
	for _, f := range got {
		if f.Rule == "raw-html-xss" && strings.Contains(f.File, "Comment.tsx") {
			sawRealXSS = true
		}
		if f.Rule == "authz-gap" && strings.Contains(f.File, "public.ts") {
			sawAuthz = true
		}
	}
	if !sawRealXSS {
		t.Fatal("expected real user HTML dangerouslySetInnerHTML to remain")
	}
	if !sawAuthz {
		t.Fatal("expected auth: false to remain")
	}
}

