package security

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// librarySecurityHints maps framework cores to real trust-boundary files agents
// should inspect — not inventing app CVEs in library source.
var librarySecurityHints = map[string][]struct {
	file    string
	needle  string // optional substring to anchor a real line
	rule    string
	message string
}{
	"express": {
		{"lib/response.js", "redirect", "library-redirect", "res.redirect — confirm apps allowlist Location targets; framework passes through."},
		{"lib/utils.js", "escape", "library-escape", "Escape helpers — apps must use these for untrusted HTML; raw send is caller responsibility."},
		{"lib/request.js", "header", "library-input", "Request header/query accessors — untrusted input surfaces for app middleware."},
	},
	"django": {
		{"django/utils/safestring.py", "mark_safe", "library-xss-api", "mark_safe API — intentional escape hatch; XSS only if callers pass untrusted HTML."},
		{"django/middleware/csrf.py", "csrf", "library-csrf", "CSRF middleware — apps must not disable without an equivalent check."},
		{"django/db/models/expressions.py", "RawSQL", "library-sql-api", "RawSQL / Extra — parameterization is caller responsibility."},
	},
	"flask": {
		{"src/flask/helpers.py", "redirect", "library-redirect", "flask.redirect — validate external targets at call sites."},
		{"src/flask/app.py", "secret_key", "library-session", "SECRET_KEY / session — apps must set a strong secret outside source."},
	},
	"fastapi": {
		{"fastapi/security/api_key.py", "class APIKeyQuery", "library-auth-api", "APIKeyQuery/Header — query-string API keys are a documented footgun; prefer headers."},
		{"fastapi/security/oauth2.py", "class OAuth2PasswordBearer", "library-auth-api", "OAuth2 password flow — token handling is app responsibility."},
	},
	"gin": {
		{"gin.go", "TrustedProxies", "library-proxy", "TrustedProxies defaults — never trust all proxies on public deployments."},
		{"context.go", "ClientIP", "library-proxy", "ClientIP depends on trusted proxy config — spoofable if mis-set."},
	},
	"rails": {
		{"actionpack/lib/action_controller/metal/request_forgery_protection.rb", "CSRF", "library-csrf", "CSRF protection — key constants are not secrets; verify protect_from_forgery stays on."},
		{"actionview/lib/action_view/helpers/output_safety_helper.rb", "raw", "library-xss-api", "raw / html_safe — trusted HTML only."},
	},
	"redis": {
		{"src/acl.c", "ACLAuthenticateUser", "library-auth", "ACLAuthenticateUser — primary auth gate; pair with protected-mode / bind."},
		{"src/networking.c", "protected_mode", "library-auth", "protected_mode + NOPASS path — classic exposure when misconfigured."},
		{"src/server.c", "requirepass", "library-auth", "Auth configuration surfaces — confirm ACL users are not default-open."},
	},
	"axum": {
		{"axum-core/src/extract/default_body_limit.rs", "DefaultBodyLimit", "library-dos", "Default body limit — bypass notes matter for DoS posture."},
		{"axum/src/extract/rejection.rs", "Rejection", "library-input", "Extractor rejections — apps must not leak internals in error bodies."},
	},
	"vue": {
		{"packages/runtime-dom/src/directives/vHtml.ts", "innerHTML", "library-xss-api", "v-html / innerHTML path — sanitize untrusted content at app call sites."},
		{"packages/shared/src/escapeHtml.ts", "escapeHtml", "library-escape", "escapeHtml — prefer this over raw HTML binding."},
	},
	"svelte": {
		{"packages/svelte/src/compiler/phases/3-transform/client/visitors/HtmlTag.js", "export function HtmlTag", "library-xss-api", "{@html} compile path (HtmlTag) — sanitize untrusted HTML before binding."},
	},
	"symfony-demo": {
		{"templates/blog/post_show.html.twig", "sanitize_html", "app-xss", "Twig markdown→HTML pipeline — confirm sanitize_html stays on for untrusted content."},
	},
}

// LibrarySecurityGuidance returns grounded library trust-boundary files when
// sink scans are empty on framework cores — honest footguns, not invented CVEs.
func LibrarySecurityGuidance(root string, shape ProjectShape, limit int) []ContextFinding {
	if shape != ShapeLibrary && shape != ShapeFrameworkCore {
		return nil
	}
	if limit <= 0 {
		limit = 6
	}
	root = filepath.Clean(root)
	base := strings.ToLower(filepath.Base(root))
	hints := librarySecurityHints[base]
	if len(hints) == 0 {
		hints = librarySecurityHintsFromLayout(root)
	}
	var out []ContextFinding
	for _, h := range hints {
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
			Tool: "codehelper-library-security", Severity: "medium", Rule: h.rule,
			File: h.file, Line: line, Evidence: h.message,
			Kind: "library_guidance", Confidence: "medium", Exploitability: "framework-api",
			Hint: "[framework-core] Trust-boundary footgun — not an app CVE by itself; verify callers and deploy defaults.",
		})
	}
	return EnrichAndRankFindings(out)
}

// librarySecurityHintsFromLayout picks framework security hints by on-disk layout
// so renamed checkouts still surface CSRF/auth/XSS APIs with file:line.
func librarySecurityHintsFromLayout(root string) []struct {
	file    string
	needle  string
	rule    string
	message string
} {
	checks := []struct {
		probe string
		key   string
	}{
		{"src/flask/app.py", "flask"},
		{"flask/app.py", "flask"},
		{"fastapi/security/api_key.py", "fastapi"},
		{"django/middleware/csrf.py", "django"},
		{"lib/response.js", "express"},
		{"gin.go", "gin"},
		{"src/acl.c", "redis"},
		{"packages/runtime-dom/src/directives/vHtml.ts", "vue"},
		{"packages/svelte/src/compiler/phases/3-transform/client/visitors/HtmlTag.js", "svelte"},
		{"actionpack/lib/action_controller/metal/request_forgery_protection.rb", "rails"},
		{"axum-core/src/extract/default_body_limit.rs", "axum"},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.probe))); err == nil {
			if h := librarySecurityHints[c.key]; len(h) > 0 {
				return h
			}
		}
	}
	return nil
}

func firstLineContainingFile(abs, needle string) int {
	f, err := os.Open(abs)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	n := strings.ToLower(needle)
	lineNo := 0
	fallback := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		lower := strings.ToLower(raw)
		if !strings.Contains(lower, n) {
			if lineNo > 4000 {
				break
			}
			continue
		}
		trim := strings.TrimSpace(lower)
		// Prefer definition lines over import/from re-exports (fastapi api_key.py:4).
		if strings.HasPrefix(trim, "import ") || strings.HasPrefix(trim, "from ") ||
			strings.HasPrefix(trim, "using ") || strings.HasPrefix(trim, "#include") {
			if fallback == 0 {
				fallback = lineNo
			}
			continue
		}
		if strings.HasPrefix(trim, "class ") || strings.HasPrefix(trim, "def ") ||
			strings.HasPrefix(trim, "func ") || strings.HasPrefix(trim, "fn ") ||
			strings.HasPrefix(trim, "pub fn ") || strings.HasPrefix(trim, "export ") ||
			strings.HasPrefix(trim, "public ") || strings.HasPrefix(trim, "type ") {
			return lineNo
		}
		// Non-import match still beats an import-only hit.
		return lineNo
	}
	return fallback
}
