package security

import (
	"path/filepath"
	"regexp"
	"strings"
)

type ContextFinding struct {
	Tool               string `json:"tool"`
	Severity           string `json:"severity"`
	Rule               string `json:"rule"`
	File               string `json:"file,omitempty"`
	Line               int    `json:"line,omitempty"`
	Symbol             string `json:"symbol,omitempty"`
	Evidence           string `json:"evidence,omitempty"`
	DistanceFromChange int    `json:"distance_from_change"`
}

func BuildSecurityContext(changedFiles []string, issues []SarifIssue) []ContextFinding {
	set := map[string]struct{}{}
	for _, f := range changedFiles {
		set[filepath.ToSlash(f)] = struct{}{}
	}
	out := make([]ContextFinding, 0, len(issues))
	for _, i := range issues {
		file := filepath.ToSlash(i.File)
		dist := 2
		if _, ok := set[file]; ok {
			dist = 0
		} else {
			for f := range set {
				if strings.Contains(file, filepath.Base(f)) {
					dist = 1
					break
				}
			}
		}
		out = append(out, ContextFinding{
			Tool: i.Tool, Severity: i.Severity, Rule: i.Rule, File: file, DistanceFromChange: dist,
		})
	}
	return out
}

// AddedDiffLine represents one added line scanned from a unified diff.
type AddedDiffLine struct {
	File    string
	Line    int
	Content string
}

// builtinSecurityRules detect well-known security smells in newly added code.
// They never replace SAST but they make security_context useful when no SARIF
// file is provided.
var builtinSecurityRules = []struct {
	rule     string
	severity string
	pattern  *regexp.Regexp
	message  string
}{
	{
		rule:     "hardcoded-secret",
		severity: "high",
		pattern:  regexp.MustCompile(`(?i)(api[_-]?key|secret|password|token|sk_live_|sk_test_)\s*:?=\s*["'][^"']{8,}["']`),
		message:  "Possible hard-coded credential; move it to an environment variable or secret store.",
	},
	{
		rule:     "sql-string-concat",
		severity: "high",
		pattern:  regexp.MustCompile(`(?i)((select|insert|update|delete)\b[^;\n]{0,120}(['"]\s*\+|['"]\s*\.|f['"][^'"]*\{)|['"]\s*\+\s*(req|request|params|input|query)\.|\$\{[^}]+\}.*(select|insert|update|delete)|(select|insert|update|delete).*\$\{)`),
		message:  "SQL built from string concatenation/interpolation; use parameterized queries.",
	},
	{
		rule:     "eval-usage",
		severity: "high",
		pattern:  regexp.MustCompile(`(?i)\beval\s*\(|new\s+Function\s*\(`),
		message:  "eval / new Function on dynamic input is unsafe; remove or sandbox it.",
	},
	{
		rule:     "shell-exec-injection",
		severity: "high",
		pattern:  regexp.MustCompile(`(?i)\b(exec|spawn|system|shell_exec|popen)\s*\([^)]*\$?\w+\s*\.`),
		message:  "External command built from variable input; use argv form or escape rigorously.",
	},
	{
		rule:     "csrf-disabled",
		severity: "medium",
		pattern:  regexp.MustCompile(`(?i)(csrf[_-]?protection|VerifyCsrfToken)\s*[:=]?\s*(false|off|disabled)`),
		message:  "CSRF protection appears to be disabled for a state-changing endpoint.",
	},
	{
		rule:     "open-redirect",
		severity: "medium",
		pattern:  regexp.MustCompile(`(?i)(redirect|location)\s*\(\s*\$?\w*(request|input|query|param)`),
		message:  "Redirect target taken directly from user input; validate against an allowlist.",
	},
	{
		rule:     "blade-unescaped-output",
		severity: "medium",
		pattern:  regexp.MustCompile(`\{!!\s*\$\w+`),
		message:  "Unescaped Blade output ({!! !!}) of dynamic data; prefer {{ }} unless trust is explicit.",
	},
	{
		rule:     "missing-nonce-check",
		severity: "medium",
		pattern:  regexp.MustCompile(`(?i)(wp_ajax_|admin-ajax\.php).*\{[^}]*$`),
		message:  "Possible WordPress AJAX handler change; ensure check_ajax_referer / wp_verify_nonce is present.",
	},
}

// ScanDiffForSecuritySmells classifies added lines against the builtin rules.
func ScanDiffForSecuritySmells(lines []AddedDiffLine) []ContextFinding {
	out := make([]ContextFinding, 0)
	for _, line := range lines {
		content := strings.TrimSpace(line.Content)
		if content == "" || strings.HasPrefix(content, "//") || strings.HasPrefix(content, "#") {
			continue
		}
		for _, r := range builtinSecurityRules {
			if r.pattern.MatchString(content) {
				out = append(out, ContextFinding{
					Tool:               "codehelper-builtin",
					Severity:           r.severity,
					Rule:               r.rule,
					File:               line.File,
					Line:               line.Line,
					Evidence:           truncate(content, 200),
					DistanceFromChange: 0,
				})
			}
		}
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
