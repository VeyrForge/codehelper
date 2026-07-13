package review

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

type MigrationGuardResult struct {
	DestructiveRisks []Finding `json:"destructive_risks"`
}

// migrationSignals match real destructive DDL statements in added lines.
var migrationSignals = []struct {
	pattern  *regexp.Regexp
	severity Severity
	message  string
}{
	{
		pattern:  regexp.MustCompile(`(?i)\bdrop\s+table\b`),
		severity: SeverityCritical,
		message:  "DROP TABLE detected; ensure a deprecation window and backup before applying.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\btruncate\s+(table\s+)?[\w."` + "`" + `]+`),
		severity: SeverityCritical,
		message:  "TRUNCATE will erase all rows; confirm this is intended and reversible.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\balter\s+table\b[^;]*\bdrop\s+(column|constraint|index)\b`),
		severity: SeverityHigh,
		message:  "ALTER TABLE ... DROP is destructive; stage as deprecation + later removal.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\b(dropcolumn|drop_column|->dropColumn)\b`),
		severity: SeverityHigh,
		message:  "Schema builder drops a column; verify deprecation window and rollback path.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\bdrop\s+(unique|primary|foreign\s+key|constraint|index)\b`),
		severity: SeverityHigh,
		message:  "Dropping constraint/index changes data integrity; confirm query plan and invariants.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\balter\s+table\b[^;]*\bset\s+not\s+null\b`),
		severity: SeverityHigh,
		message:  "Adding NOT NULL on an existing column without a default/backfill will fail in production.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\bdelete\s+from\s+[\w."` + "`" + `]+\s*;?\s*$`),
		severity: SeverityHigh,
		message:  "Unconditional DELETE FROM removes all rows; add a WHERE clause or guard.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\brename\s+(column|table)\b|->renameColumn`),
		severity: SeverityMedium,
		message:  "Rename detected; coordinate with code references and update ORM models in the same change.",
	},
}

func MigrationGuard(ctx context.Context, repoRoot, baseRef string) (*MigrationGuardResult, error) {
	_ = ctx
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD~1"
	}
	diff, err := gitutil.UnifiedDiff(repoRoot, baseRef)
	if err != nil {
		return &MigrationGuardResult{DestructiveRisks: []Finding{}}, nil
	}
	files := ParseUnifiedDiff(diff)
	findings := make([]Finding, 0)
	for _, f := range files {
		if !looksLikeMigrationOrSQL(f.Path) {
			continue
		}
		for _, line := range f.Added {
			content := strings.TrimSpace(line.Content)
			if content == "" || strings.HasPrefix(content, "--") || strings.HasPrefix(content, "//") {
				continue
			}
			for _, sig := range migrationSignals {
				if sig.pattern.MatchString(content) {
					findings = append(findings, Finding{
						Severity: sig.severity,
						Category: "migration",
						File:     f.Path,
						Line:     line.LineNo,
						Message:  sig.message,
						Evidence: []string{trimEvidence(content)},
					})
					break
				}
			}
		}
	}
	return &MigrationGuardResult{DestructiveRisks: findings}, nil
}

// IsMigrationOrSQLFile reports whether a path looks like a real DB migration
// file (SQL or a migrations directory). It deliberately excludes application
// source code so regex patterns inside .go/.ts/etc. don't self-match.
func IsMigrationOrSQLFile(path string) bool {
	return looksLikeMigrationOrSQL(path)
}

func looksLikeMigrationOrSQL(path string) bool {
	if path == "" {
		return false
	}
	p := strings.ToLower(filepath.ToSlash(path))
	// Application source code can never be a migration; protect ourselves
	// from regex strings inside .go/.ts/.js/.py files that match these
	// destructive-DDL patterns. Real migrations are SQL files, files in a
	// dedicated migrations folder, or framework migration classes (PHP).
	if strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx") ||
		strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".jsx") || strings.HasSuffix(p, ".mjs") ||
		strings.HasSuffix(p, ".py") || strings.HasSuffix(p, ".rb") || strings.HasSuffix(p, ".rs") ||
		strings.HasSuffix(p, ".java") || strings.HasSuffix(p, ".kt") {
		// allow framework migrations even if implemented in code
		return strings.Contains(p, "/migrations/") || strings.Contains(p, "/migrate/") ||
			strings.Contains(p, "/db/migrations") || strings.Contains(p, "/database/migrations")
	}
	if strings.HasSuffix(p, ".sql") {
		return true
	}
	if strings.Contains(p, "/migrations/") || strings.Contains(p, "/migrate/") {
		return true
	}
	return false
}

func trimEvidence(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
