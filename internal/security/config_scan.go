package security

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// configHardeningTargets are grounded config / bootstrap files to inspect when
// code sinks are scarce (skeletons) or when framing an audit checklist.
var configHardeningTargets = []string{
	".env.example", ".env", "config/app.php", "config/session.php",
	"config/cors.php", "config/auth.php", "src/main.ts", "src/main.js", "main.ts", "main.go",
	"Program.cs", "redis.conf", "docker-compose.yml", "compose.yaml",
	"next.config.js", "next.config.mjs", "next.config.ts",
	"settings.py", "config/settings.py", "config/initializers/session_store.rb",
	"config/environments/production.rb", "application.properties",
	"application.yml", "src/main/resources/application.properties",
	"src/main/resources/application.yml", "appsettings.json", "appsettings.Development.json",
}

// configHardeningRules match deploy/footgun defaults with file:line evidence.
var configHardeningRules = []struct {
	rule     string
	severity string
	pattern  string
	message  string
}{
	{"config-debug-enabled", "medium", "app_debug=true", "Debug mode enabled in example/env — disable in production."},
	{"config-debug-enabled", "medium", "debug = true", "Debug flag true — confirm not shipped to production."},
	{"config-debug-enabled", "medium", "debug=true", "Debug flag true — confirm not shipped to production."},
	{"config-debug-enabled", "medium", "debug: true", "Debug flag true — confirm not shipped to production."},
	{"config-session-insecure", "medium", "session_encrypt=false", "Session encryption off in example — enable for sensitive apps."},
	{"config-session-insecure", "low", "session_secure_cookie=false", "Secure cookie flag off — require HTTPS cookies in production."},
	{"config-session-insecure", "low", "'secure' => false", "Session secure cookie false — require HTTPS in production."},
	{"config-cors-open", "medium", "origin: '*'", "CORS origin wildcard — tighten to known frontends."},
	{"config-cors-open", "medium", "allow_origins: ['*']", "CORS allow_origins wildcard — tighten allowlist."},
	{"config-cors-open", "medium", `"cors": {"origin": true}`, "CORS reflects any origin — tighten for credentialed APIs."},
	{"config-cors-open", "medium", "allowed_origins' => ['*']", "CORS allowed_origins wildcard — tighten allowlist."},
	{"config-cors-open", "medium", "'allowed_origins' => ['*']", "CORS allowed_origins wildcard — tighten allowlist."},
	{"config-auth-gap", "medium", "protected-mode no", "Redis protected-mode disabled — dangerous on public interfaces."},
	{"config-auth-gap", "low", "# requirepass", "Redis requirepass commented out — enable ACL/requirepass before exposing."},
	{"config-auth-gap", "medium", "permitall()", "Spring permitAll — confirm intentional public access."},
	{"config-auth-gap", "medium", "management.endpoints.web.exposure.include=*", "Actuator endpoints exposed (*) — lock down management endpoints before production."},
	{"config-auth-gap", "medium", "management.endpoints.web.exposure.include: '*'", "Actuator endpoints exposed (*) — lock down management endpoints before production."},
	{"config-auth-gap", "low", "spring.jpa.open-in-view=true", "Open-in-view enabled — can hide N+1 and widen transactional surface; prefer false + explicit fetch."},
	{"config-missing-validation", "low", "nestfactory.create", "Nest bootstrap without ValidationPipe/helmet in same file — add global validation + security headers."},
	{"config-missing-validation", "low", "app.listen(", "HTTP listen without nearby security middleware (helmet/csrf/rate-limit) — verify stack defaults."},
	{"config-bind-all", "medium", "bind 0.0.0.0", "Service binds all interfaces — pair with auth and firewall."},
	{"config-bind-all", "medium", "host: '0.0.0.0'", "Service binds all interfaces — pair with auth and firewall."},
	{"config-bind-all", "medium", "host: \"0.0.0.0\"", "Service binds all interfaces — pair with auth and firewall."},
}

// ScanConfigHardening returns grounded config/bootstrap checklist items.
// These are Kind=config_hardening — never claim them as confirmed CVEs.
func ScanConfigHardening(root string, limit int) []ContextFinding {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	if limit <= 0 {
		limit = 12
	}
	root = filepath.Clean(root)
	var out []ContextFinding
	seen := map[string]struct{}{}

	for _, rel := range configHardeningTargets {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		hits := scanConfigFile(abs, rel, limit-len(out))
		for _, h := range hits {
			key := h.Rule + "|" + h.File + "|" + itoa(h.Line)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, h)
			if len(out) >= limit {
				break
			}
		}
	}

	// Nest bootstrap: a bare `NestFactory.create` + `listen` with none of the
	// standard hardening additions is a real, well-documented Nest security
	// checklist gap (https://docs.nestjs.com/security/*). Emit one row PER
	// missing addition (ValidationPipe, helmet, rate limiting) instead of a
	// single combined line — each is independently actionable and grounded
	// in the same bootstrap file, not an invented app CVE.
	if len(out) < limit {
		for _, rel := range []string{"src/main.ts", "src/main.js", "main.ts"} {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			body := readFileTrunc(abs, 4000)
			if body == "" {
				continue
			}
			lower := strings.ToLower(body)
			if !strings.Contains(lower, "nestfactory.create") {
				continue
			}
			// Drop any prior config-missing-validation rows for this file (rule
			// previously matched both NestFactory.create and app.listen).
			filtered := out[:0]
			for _, h := range out {
				if strings.EqualFold(h.Rule, "config-missing-validation") &&
					filepath.ToSlash(h.File) == filepath.ToSlash(rel) {
					continue
				}
				filtered = append(filtered, h)
			}
			out = filtered
			line := firstLineContaining(body, "NestFactory.create")
			evidence := truncate(strings.TrimSpace(lineAt(body, line)), 200)
			// Rate limiting is normally wired in app.module.ts (ThrottlerModule),
			// not main.ts — check both files before flagging it absent.
			moduleLower := lower
			for _, modRel := range []string{"src/app.module.ts", "app.module.ts"} {
				if mb := readFileTrunc(filepath.Join(root, filepath.FromSlash(modRel)), 4000); mb != "" {
					moduleLower += strings.ToLower(mb)
				}
			}
			nestChecklist := []struct {
				needle   string
				rule     string
				severity string
				hint     string
				body     string
			}{
				{"validationpipe", "nest-missing-validation-pipe", "medium",
					"No global ValidationPipe — DTOs accept unvalidated/extra fields (mass-assignment-style payloads). Add app.useGlobalPipes(new ValidationPipe({whitelist:true})).",
					lower},
				{"helmet", "nest-missing-helmet", "low",
					"No helmet() — responses miss standard security headers (CSP/HSTS/X-Frame-Options). Add app.use(helmet()) before listen().",
					lower},
				{"throttlermodule", "nest-missing-rate-limit", "low",
					"No ThrottlerModule/@nestjs/throttler — public endpoints have no request-rate guard. Add ThrottlerModule + ThrottlerGuard for brute-force/DoS protection.",
					moduleLower},
			}
			for _, c := range nestChecklist {
				if strings.Contains(c.body, c.needle) {
					continue
				}
				out = append(out, ContextFinding{
					Tool: "codehelper-config-scan", Severity: c.severity, Rule: c.rule,
					File: rel, Line: line, Evidence: evidence,
					Kind: "config_hardening", Confidence: "medium", Exploitability: "config-only",
					Hint: c.hint,
				})
				if len(out) >= limit {
					break
				}
			}
			break
		}
	}
	// Laravel skeleton: empty APP_KEY= in .env.example is a high-value hardening item.
	if len(out) < limit {
		for _, rel := range []string{".env.example", ".env"} {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			body := readFileTrunc(abs, 8000)
			if body == "" {
				continue
			}
			for i, ln := range strings.Split(body, "\n") {
				trim := strings.TrimSpace(ln)
				lower := strings.ToLower(trim)
				// Only empty APP_KEY= (not APP_KEY=base64:…).
				if lower == "app_key=" || lower == "app_key=\"\"" || lower == "app_key=''" {
					out = append(out, ContextFinding{
						Tool: "codehelper-config-scan", Severity: "medium", Rule: "config-auth-gap",
						File: rel, Line: i + 1, Evidence: truncate(trim, 200),
						Kind: "config_hardening", Confidence: "high", Exploitability: "config-only",
						Hint: "Skeleton: generate APP_KEY (`php artisan key:generate`) before any deploy.",
					})
					break
				}
			}
			if len(out) >= limit {
				break
			}
		}
	}
	// Laravel: config/session.php 'secure' => env('SESSION_SECURE_COOKIE') with
	// no explicit boolean default sends session cookies over plain HTTP unless
	// the env var is set — a standard Laravel production hardening item, real
	// on every fresh Laravel app (not just this skeleton).
	if len(out) < limit {
		rel := "config/session.php"
		abs := filepath.Join(root, filepath.FromSlash(rel))
		body := readFileTrunc(abs, 8000)
		for i, ln := range strings.Split(body, "\n") {
			lower := strings.ToLower(ln)
			if !strings.Contains(lower, "'secure'") && !strings.Contains(lower, "\"secure\"") {
				continue
			}
			if !strings.Contains(lower, "env(") {
				continue
			}
			if strings.Contains(lower, ", true)") || strings.Contains(lower, ",true)") ||
				strings.Contains(lower, ", false)") || strings.Contains(lower, ",false)") {
				break // explicit default present — not the footgun
			}
			out = append(out, ContextFinding{
				Tool: "codehelper-config-scan", Severity: "medium", Rule: "config-session-insecure",
				File: rel, Line: i + 1, Evidence: truncate(strings.TrimSpace(ln), 200),
				Kind: "config_hardening", Confidence: "high", Exploitability: "config-only",
				Hint: "Fail-open Secure-cookie default (null): session cookie can ride cleartext HTTP until SESSION_SECURE_COOKIE=true — not a CVE; set true in production HTTPS.",
			})
			break
		}
	}
	// Laravel: 'encrypt' => env('SESSION_ENCRYPT', false) stores session payloads
	// in plaintext at the default — cite config/session.php (not .env.example) so
	// enrich does not demote it as an example-only checklist row.
	if len(out) < limit {
		rel := "config/session.php"
		abs := filepath.Join(root, filepath.FromSlash(rel))
		body := readFileTrunc(abs, 8000)
		for i, ln := range strings.Split(body, "\n") {
			lower := strings.ToLower(strings.TrimSpace(ln))
			if !(strings.Contains(lower, "'encrypt'") || strings.Contains(lower, "\"encrypt\"")) {
				continue
			}
			if !strings.Contains(lower, "session_encrypt") && !strings.Contains(lower, "env(") {
				continue
			}
			if !strings.Contains(lower, "false") {
				continue
			}
			out = append(out, ContextFinding{
				Tool: "codehelper-config-scan", Severity: "medium", Rule: "config-session-insecure",
				File: rel, Line: i + 1, Evidence: truncate(strings.TrimSpace(ln), 200),
				Kind: "config_hardening", Confidence: "high", Exploitability: "config-only",
				Hint: "Fail-open plaintext session store: SESSION_ENCRYPT defaults false — session payloads stay unencrypted at rest unless enabled; not a CVE, enable for sensitive apps.",
			})
			break
		}
	}
	// Laravel: config/app.php 'key' => env('APP_KEY') with no fallback — cite the
	// real config line (survives .env.example severity demotion).
	if len(out) < limit {
		rel := "config/app.php"
		abs := filepath.Join(root, filepath.FromSlash(rel))
		body := readFileTrunc(abs, 8000)
		for i, ln := range strings.Split(body, "\n") {
			lower := strings.ToLower(strings.TrimSpace(ln))
			if !strings.Contains(lower, "'key'") && !strings.Contains(lower, "\"key\"") {
				continue
			}
			if !strings.Contains(lower, "env('app_key')") && !strings.Contains(lower, "env(\"app_key\")") {
				continue
			}
			// Skip previous_keys / keyed arrays.
			if strings.Contains(lower, "previous") {
				continue
			}
			out = append(out, ContextFinding{
				Tool: "codehelper-config-scan", Severity: "medium", Rule: "config-auth-gap",
				File: rel, Line: i + 1, Evidence: truncate(strings.TrimSpace(ln), 200),
				Kind: "config_hardening", Confidence: "high", Exploitability: "config-only",
				Hint: "Empty APP_KEY footgun (not a CVE): cookies/encryption use an empty key until `php artisan key:generate` — generate before any deploy.",
			})
			break
		}
	}
	// Laravel: Eloquent models with $guarded = [] unguard every column — a
	// real mass-assignment footgun (create()/fill() then accept any field,
	// including is_admin/role-style columns) that shows up in real apps far
	// more often than the skeleton's own User model.
	if len(out) < limit {
		out = append(out, scanLaravelMassAssignment(root, limit-len(out))...)
	}
	// Vue/Svelte SPA apps: Nest-style split FRAMEWORK FOOTGUN checklist
	// (XSS v-html/{@html}, open redirect, secret-in-frontend, CSRF posture)
	// grounded on real .vue/.svelte file:line — skipped for packages/* cores
	// and for non-SPA hosts (Go MCP apps must not inherit nested eval-bed SPAs).
	if len(out) < limit && isFrontendSpaApp(root) {
		out = append(out, scanVueSpaChecklist(root, limit-len(out))...)
	}
	if len(out) < limit && isFrontendSpaApp(root) {
		out = append(out, scanSvelteSpaChecklist(root, limit-len(out))...)
	}
	return EnrichAndRankFindings(out)
}

// scanLaravelMassAssignment walks app/Models (one directory level) for
// `protected $guarded = [];` — Eloquent's fully-open mass-assignment mode.
func scanLaravelMassAssignment(root string, remaining int) []ContextFinding {
	if remaining <= 0 {
		return nil
	}
	modelsDir := filepath.Join(root, "app", "Models")
	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		return nil
	}
	var out []ContextFinding
	for _, e := range entries {
		if len(out) >= remaining {
			break
		}
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".php") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("app", "Models", e.Name()))
		abs := filepath.Join(modelsDir, e.Name())
		body := readFileTrunc(abs, 8000)
		if body == "" {
			continue
		}
		for i, ln := range strings.Split(body, "\n") {
			trim := strings.TrimSpace(ln)
			lower := strings.ToLower(trim)
			if lower == "protected $guarded = [];" || lower == "protected $guarded=[];" {
				out = append(out, ContextFinding{
					Tool: "codehelper-config-scan", Severity: "high", Rule: "mass-assignment-open",
					File: rel, Line: i + 1, Evidence: truncate(trim, 200),
					Kind: "sink_candidate", Confidence: "high", Exploitability: "possible",
					Hint: "$guarded = [] unguards every column — create()/fill() accept any field (incl. is_admin/role). Use explicit $fillable instead.",
				})
				break
			}
		}
	}
	return out
}

func scanConfigFile(abs, rel string, remaining int) []ContextFinding {
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
	buf := make([]byte, 0, 32*1024)
	sc.Buffer(buf, 512*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if len(out) >= remaining {
			break
		}
		raw := sc.Text()
		if len(raw) > 2000 {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(raw))
		if lower == "" {
			continue
		}
		for _, r := range configHardeningRules {
			if strings.Contains(lower, r.pattern) {
				// Skip redis bind loopback (safe) matching bind-all loosely.
				if r.rule == "config-bind-all" && (strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "localhost")) {
					continue
				}
				out = append(out, ContextFinding{
					Tool: "codehelper-config-scan", Severity: r.severity, Rule: r.rule,
					File: filepath.ToSlash(rel), Line: lineNo, Evidence: truncate(strings.TrimSpace(raw), 200),
					Kind: "config_hardening",
				})
				break
			}
		}
	}
	return out
}

func firstLineContaining(body, substr string) int {
	lowerSub := strings.ToLower(substr)
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if strings.Contains(strings.ToLower(ln), lowerSub) {
			return i + 1
		}
	}
	return 1
}

func lineAt(body string, line int) string {
	lines := strings.Split(body, "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
