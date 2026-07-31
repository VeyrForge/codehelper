package review

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

type ArchitectureLintResult struct {
	Violations []Finding `json:"violations"`
}

// archRule maps a file-role to forbidden import substrings that signal a layer
// violation. The rule matches a substring of the changed file's path against
// `fileRole` and then any added import line against `forbiddenIn`.
var archRules = []struct {
	fileRole    string
	importHints []*regexp.Regexp
	forbiddenIn []string
	message     string
	severity    Severity
}{
	{
		fileRole: "controller",
		importHints: []*regexp.Regexp{
			regexp.MustCompile(`(?i)^\s*(use|import|from)\s+.*\b(Eloquent|database/sql|mysqli_|->prepare|\\PDO|Doctrine\\DBAL)\b`),
			regexp.MustCompile(`(?i)^\s*\\?DB::`),
		},
		message:  "Controller imports the data layer directly; route DB access through a repository/service.",
		severity: SeverityMedium,
	},
	{
		fileRole: "view",
		importHints: []*regexp.Regexp{
			regexp.MustCompile(`(?i)^\s*(use|import|from)\s+.*\b(Controller|App\\Http\\Controllers)\b`),
			regexp.MustCompile(`(?i)^\s*(use|import|from)\s+.*\b(Eloquent|database/sql|->prepare|\\PDO)\b`),
		},
		message:  "View/template imports controller or DB layer; keep presentation layer free of side effects.",
		severity: SeverityMedium,
	},
	{
		fileRole: "model",
		importHints: []*regexp.Regexp{
			regexp.MustCompile(`(?i)^\s*(use|import|from)\s+.*\b(Controller|HttpKernel|Request|Response)\b`),
		},
		message:  "Model imports HTTP/controller layer; models should not depend on transport concerns.",
		severity: SeverityMedium,
	},
	{
		fileRole: "internal/",
		importHints: []*regexp.Regexp{
			regexp.MustCompile(`^\s*import\s+"[^"]*/internal/[^/]+/[^"]+`),
		},
		forbiddenIn: []string{},
		message:     "Cross-package import of another internal/ subpackage; check that the boundary is intentional.",
		severity:    SeverityLow,
	},
}

func ArchitectureLint(ctx context.Context, repoRoot, baseRef string) (*ArchitectureLintResult, error) {
	_ = ctx
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD~1"
	}
	diff, err := gitutil.UnifiedDiff(repoRoot, baseRef)
	if err != nil {
		return &ArchitectureLintResult{Violations: []Finding{}}, nil
	}
	files := ParseUnifiedDiff(diff)
	findings := make([]Finding, 0)
	for _, f := range files {
		role := classifyArchRole(f.Path)
		if role == "" {
			continue
		}
		for _, rule := range archRules {
			if rule.fileRole != role {
				continue
			}
			for _, line := range f.Added {
				content := line.Content
				for _, hint := range rule.importHints {
					if hint.MatchString(content) {
						findings = append(findings, Finding{
							Severity: rule.severity,
							Category: "architecture",
							File:     f.Path,
							Line:     line.LineNo,
							Message:  rule.message,
							Evidence: []string{trimEvidence(strings.TrimSpace(content))},
						})
						break
					}
				}
			}
		}
	}
	return &ArchitectureLintResult{Violations: findings}, nil
}

func classifyArchRole(path string) string {
	if path == "" {
		return ""
	}
	p := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.Contains(p, "/controllers/") || strings.HasSuffix(p, "controller.php") || strings.HasSuffix(p, "controller.ts") || strings.HasSuffix(p, "controller.js"):
		return "controller"
	case strings.Contains(p, "/views/") || strings.Contains(p, "/templates/") || strings.HasSuffix(p, ".blade.php") || strings.HasSuffix(p, ".tpl") || strings.HasSuffix(p, ".twig"):
		return "view"
	case strings.Contains(p, "/models/") || strings.HasSuffix(p, "_model.php") || strings.HasSuffix(p, "Model.php"):
		return "model"
	}
	if strings.Contains(p, "/internal/") {
		return "internal/"
	}
	return ""
}
