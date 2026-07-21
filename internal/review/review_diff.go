package review

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/security"
)

type DiffRequest struct {
	RepoRoot           string
	RepoName           string
	Base               string
	SeverityFloor      Severity
	IncludeTests       bool
	IncludeSecurity    bool
	IncludePerformance bool
	IncludeContracts   bool
}

func ReviewDiff(ctx context.Context, st *graph.Store, req DiffRequest) (*ReviewResult, error) {
	_ = st
	if strings.TrimSpace(req.Base) == "" {
		req.Base = "HEAD~1"
	}
	files, err := gitutil.DiffAgainst(req.RepoRoot, req.Base)
	if err != nil && req.Base == "HEAD~1" {
		// Shallow clones (--depth 1) have no parent commit; fall back to working
		// tree vs HEAD so review_diff still works without forcing agents to guess.
		files, err = gitutil.DiffAgainst(req.RepoRoot, "HEAD")
		if err == nil {
			req.Base = "HEAD"
		}
	}
	if err != nil {
		return nil, err
	}
	findings := make([]Finding, 0, 16)
	required := make([]string, 0, 8)
	for _, f := range files {
		p := strings.ToLower(filepath.ToSlash(f))
		if req.IncludeContracts && (strings.Contains(p, "api") || strings.Contains(p, "public")) {
			findings = append(findings, Finding{
				Severity: SeverityHigh, Category: "contract", File: f,
				Message:      "Public-facing path changed; verify backward compatibility.",
				SuggestedFix: "Run contract_guard and add a compatibility test.",
			})
			required = append(required, "Run contract_guard for changed API/public symbols.")
		}
		if req.IncludeTests && !IsTestPath(f) && IsCodeSourceFile(f) && !HasSiblingTestFile(req.RepoRoot, f) {
			required = append(required, "Add/update regression tests for "+f)
		}
		if req.IncludePerformance && (strings.Contains(p, "handler") || strings.Contains(p, "controller")) {
			findings = append(findings, Finding{
				Severity: SeverityMedium, Category: "performance", File: f,
				Message: "Request-path file changed; check for N+1 and unbounded loops.",
			})
		}
		if req.IncludeSecurity && (strings.Contains(p, "auth") || strings.Contains(p, "login") || strings.Contains(p, "security")) {
			findings = append(findings, Finding{
				Severity: SeverityHigh, Category: "security", File: f,
				Message:      "Security-sensitive file changed; confirm authz boundaries and scan for injection/secrets.",
				SuggestedFix: "Review added lines for SQL concat, eval, and hard-coded credentials.",
			})
		}
	}

	// Line-level high-signal rules on the unified diff (not a full SAST product).
	diffText, derr := gitutil.UnifiedDiff(req.RepoRoot, req.Base)
	if derr == nil && strings.TrimSpace(diffText) != "" {
		diffFiles := ParseUnifiedDiff(diffText)
		if req.IncludeSecurity {
			findings = append(findings, securityFindingsFromDiff(diffFiles)...)
		}
		if req.IncludePerformance {
			if pg, perr := PerfGuard(ctx, req.RepoRoot, req.Base); perr == nil && pg != nil {
				findings = append(findings, pg.PerfRisks...)
			}
		}
	}

	findings = dedupeFindings(findings)
	findings = filterBySeverity(findings, req.SeverityFloor)
	risk := RiskScore(findings)
	summary := buildReviewSummary(files, findings, risk)
	required = dedupe(required)
	if hasSecurityRule(findings) {
		required = append(required, "Address security findings (secrets/injection/eval) before merge.")
		required = dedupe(required)
	}
	return &ReviewResult{
		Summary:         summary,
		Risk:            risk,
		Findings:        findings,
		RequiredActions: required,
	}, nil
}

func securityFindingsFromDiff(files []DiffFile) []Finding {
	var lines []security.AddedDiffLine
	for _, f := range files {
		for _, l := range f.Added {
			lines = append(lines, security.AddedDiffLine{
				File: f.Path, Line: l.LineNo, Content: l.Content,
			})
		}
	}
	smells := security.ScanDiffForSecuritySmells(lines)
	out := make([]Finding, 0, len(smells))
	for _, s := range smells {
		sev := Severity(strings.ToLower(s.Severity))
		if sev == "" {
			sev = SeverityHigh
		}
		msg := securityRuleMessage(s.Rule)
		out = append(out, Finding{
			Severity:     sev,
			Category:     "security",
			File:         s.File,
			Line:         s.Line,
			Message:      msg,
			Evidence:     []string{s.Evidence},
			SuggestedFix: securityRuleFix(s.Rule),
		})
	}
	return out
}

func securityRuleMessage(rule string) string {
	switch rule {
	case "hardcoded-secret":
		return "Possible hard-coded credential in added code."
	case "sql-string-concat":
		return "SQL built via string concatenation/interpolation in added code."
	case "eval-usage":
		return "eval / new Function usage in added code."
	case "shell-exec-injection":
		return "Shell/exec built from variable input in added code."
	case "csrf-disabled":
		return "CSRF protection appears disabled in added code."
	case "open-redirect":
		return "Redirect target taken from user input in added code."
	case "blade-unescaped-output":
		return "Unescaped Blade output ({!! !!}) of dynamic data."
	case "missing-nonce-check":
		return "WordPress AJAX handler change may lack nonce verification."
	default:
		if rule == "" {
			return "Security smell detected in added code."
		}
		return "Security smell (" + rule + ") in added code."
	}
}

func securityRuleFix(rule string) string {
	switch rule {
	case "hardcoded-secret":
		return "Move the value to an environment variable or secret store; never commit credentials."
	case "sql-string-concat":
		return "Use parameterized queries / prepared statements."
	case "eval-usage":
		return "Remove eval/new Function or strictly sandbox the input."
	case "shell-exec-injection":
		return "Pass argv slices (no shell) or validate/escape rigorously."
	default:
		return "Confirm the change is intentional and add a regression test."
	}
}

func hasSecurityRule(findings []Finding) bool {
	for _, f := range findings {
		if f.Category == "security" && (f.Severity == SeverityHigh || f.Severity == SeverityCritical) {
			return true
		}
	}
	return false
}

func dedupeFindings(in []Finding) []Finding {
	seen := map[string]struct{}{}
	out := make([]Finding, 0, len(in))
	for _, f := range in {
		key := fmt.Sprintf("%s|%s|%s|%d|%s", f.Category, f.Severity, f.File, f.Line, f.Message)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, f)
	}
	return out
}

func buildReviewSummary(files []string, findings []Finding, risk string) string {
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Category]++
	}
	if len(findings) == 0 {
		if len(files) == 0 {
			return "No diff against base ref; nothing to review."
		}
		return fmt.Sprintf("%d file(s) changed; no findings at or above the configured severity floor.", len(files))
	}
	parts := make([]string, 0, len(counts))
	for _, cat := range []string{"contract", "security", "performance", "migration", "architecture"} {
		if n, ok := counts[cat]; ok {
			parts = append(parts, fmt.Sprintf("%d %s", n, cat))
		}
	}
	other := 0
	for c, n := range counts {
		switch c {
		case "contract", "security", "performance", "migration", "architecture":
		default:
			other += n
		}
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d other", other))
	}
	return fmt.Sprintf("%s risk over %d file(s): %s.", strings.Title(risk), len(files), strings.Join(parts, ", "))
}

func filterBySeverity(in []Finding, floor Severity) []Finding {
	if floor == "" || floor == SeverityLow {
		return in
	}
	out := make([]Finding, 0, len(in))
	for _, f := range in {
		if severityAtLeast(f.Severity, floor) {
			out = append(out, f)
		}
	}
	return out
}

func severityAtLeast(s, floor Severity) bool {
	r := map[Severity]int{
		SeverityLow: 1, SeverityMedium: 2, SeverityHigh: 3, SeverityCritical: 4,
	}
	return r[s] >= r[floor]
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
