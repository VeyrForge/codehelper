package review

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/graph"
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
	if strings.TrimSpace(req.Base) == "" {
		req.Base = "HEAD~1"
	}
	files, err := gitutil.DiffAgainst(req.RepoRoot, req.Base)
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
				Message:      "Security-sensitive file changed; run security_context checks.",
				SuggestedFix: "Import SARIF findings and confirm auth boundaries remain intact.",
			})
		}
	}
	findings = filterBySeverity(findings, req.SeverityFloor)
	risk := RiskScore(findings)
	summary := buildReviewSummary(files, findings, risk)
	required = dedupe(required)
	return &ReviewResult{
		Summary:         summary,
		Risk:            risk,
		Findings:        findings,
		RequiredActions: required,
	}, nil
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
