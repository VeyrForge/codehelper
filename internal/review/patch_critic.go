package review

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/rules"
)

// PatchCriticFinding is a strict finding on a changed line (no praise).
type PatchCriticFinding struct {
	Severity    Severity `json:"severity"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Category    string   `json:"category"`
	Message     string   `json:"message"`
	RequiredFix string   `json:"required_fix"`
}

// PatchCriticResult aggregates patch review.
type PatchCriticResult struct {
	Findings     []PatchCriticFinding `json:"findings"`
	CanClaimDone bool                 `json:"can_claim_done"`
	Summary      string               `json:"summary,omitempty"`
}

// PatchCritic runs pattern checks on added lines only.
func PatchCritic(repoRoot, baseRef string, patterns []rules.RiskPattern) (*PatchCriticResult, error) {
	diff, err := gitutil.UnifiedDiff(repoRoot, baseRef)
	if err != nil {
		return nil, err
	}
	added := parseAddedLines(diff)
	findings := make([]PatchCriticFinding, 0)
	for _, a := range added {
		lineLower := strings.ToLower(a.Text)
		for _, p := range patterns {
			if matchedPattern(lineLower, p) && !hunkSatisfiesRequires(a.HunkText, p) {
				sev := SeverityHigh
				switch strings.ToLower(p.Severity) {
				case "critical":
					sev = SeverityCritical
				case "medium":
					sev = SeverityMedium
				case "low":
					sev = SeverityLow
				}
				findings = append(findings, PatchCriticFinding{
					Severity:    sev,
					File:        a.File,
					Line:        a.Line,
					Category:    "rules_pack",
					Message:     "Matched risk pattern " + p.ID + " on added line.",
					RequiredFix: fixHint(p),
				})
			}
		}
	}
	blocking := false
	for _, f := range findings {
		if f.Severity == SeverityHigh || f.Severity == SeverityCritical {
			blocking = true
		}
	}
	sum := "No blocking findings in changed lines."
	if blocking {
		sum = "Blocking findings present in changed lines."
	} else if len(findings) > 0 {
		sum = "Non-blocking findings in changed lines."
	}
	return &PatchCriticResult{
		Findings:     findings,
		CanClaimDone: !blocking,
		Summary:      sum,
	}, nil
}

func fixHint(p rules.RiskPattern) string {
	if strings.TrimSpace(p.Requires) != "" {
		return "Ensure presence of: " + p.Requires
	}
	if len(p.RequiresAny) > 0 {
		return "Require one of: " + strings.Join(p.RequiresAny, ", ")
	}
	return "Resolve pattern " + p.ID
}

type addedLine struct {
	File     string
	Line     int
	Text     string
	HunkText string
}

func parseAddedLines(diff string) []addedLine {
	lines := strings.Split(diff, "\n")
	var out []addedLine
	curFile := ""
	newLine := 0
	var hunkLines []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "+++ ") {
			curFile = strings.TrimPrefix(ln, "+++ ")
			if idx := strings.Index(curFile, "\t"); idx >= 0 {
				curFile = curFile[:idx]
			}
			curFile = strings.TrimPrefix(strings.TrimSpace(curFile), "b/")
			curFile = filepath.ToSlash(curFile)
			hunkLines = nil
			continue
		}
		if strings.HasPrefix(ln, "@@") {
			ns, ok := parseNewStart(ln)
			if ok {
				newLine = ns
			}
			hunkLines = nil
			continue
		}
		if strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++") {
			txt := strings.TrimPrefix(ln, "+")
			hunkLines = append(hunkLines, ln)
			rec := addedLine{
				File:     curFile,
				Line:     newLine,
				Text:     txt,
				HunkText: strings.Join(hunkLines, "\n"),
			}
			out = append(out, rec)
			newLine++
			continue
		}
		if strings.HasPrefix(ln, " ") {
			newLine++
			hunkLines = append(hunkLines, ln)
			continue
		}
		if strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---") {
			hunkLines = append(hunkLines, ln)
			continue
		}
	}
	return out
}

func parseNewStart(header string) (int, bool) {
	i := strings.Index(header, "+")
	if i < 0 {
		return 0, false
	}
	rest := header[i+1:]
	j := strings.Index(rest, ",")
	if j < 0 {
		j = strings.Index(rest, " ")
	}
	if j <= 0 {
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		return n, err == nil
	}
	n, err := strconv.Atoi(rest[:j])
	return n, err == nil
}

func matchedPattern(lineLower string, p rules.RiskPattern) bool {
	if m := strings.TrimSpace(p.Match); m != "" && strings.Contains(lineLower, strings.ToLower(m)) {
		return true
	}
	for _, m := range p.MatchAny {
		if strings.Contains(lineLower, strings.ToLower(strings.TrimSpace(m))) {
			return true
		}
	}
	return false
}

func hunkSatisfiesRequires(hunk string, p rules.RiskPattern) bool {
	h := strings.ToLower(hunk)
	if req := strings.TrimSpace(p.Requires); req != "" && strings.Contains(h, strings.ToLower(req)) {
		return true
	}
	for _, r := range p.RequiresAny {
		if strings.Contains(h, strings.ToLower(strings.TrimSpace(r))) {
			return true
		}
	}
	return strings.TrimSpace(p.Requires) == "" && len(p.RequiresAny) == 0
}
