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

	// Nest/Express bootstrap: if main.ts only creates + listens, emit ONE guidance hit
	// (do not also keep duplicate NestFactory.create + app.listen rows from rules).
	if len(out) < limit {
		for _, rel := range []string{"src/main.ts", "src/main.js", "main.ts"} {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			body := readFileTrunc(abs, 4000)
			if body == "" {
				continue
			}
			lower := strings.ToLower(body)
			if strings.Contains(lower, "nestfactory.create") &&
				!strings.Contains(lower, "validationpipe") &&
				!strings.Contains(lower, "helmet") {
				// Drop prior config-missing-validation rows for this file (rule matches
				// both NestFactory.create and app.listen → duplicate checklist).
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
				out = append(out, ContextFinding{
					Tool: "codehelper-config-scan", Severity: "low", Rule: "config-missing-validation",
					File: rel, Line: line, Evidence: truncate(strings.TrimSpace(lineAt(body, line)), 200),
					Kind: "config_hardening", Confidence: "medium", Exploitability: "config-only",
					Hint: "Skeleton Nest bootstrap — add ValidationPipe, helmet, and CORS allowlist before production.",
				})
				break
			}
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
	return EnrichAndRankFindings(out)
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
