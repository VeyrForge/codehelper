package review

import (
	"context"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

type PerfGuardResult struct {
	PerfRisks []Finding `json:"perf_risks"`
}

// loopStart matches the opening of a loop in supported languages (PHP, JS/TS,
// Go, Python, Ruby, Rust, Java). We treat each loop as a 12-line window in
// which database/IO operations are suspicious.
var loopStart = regexp.MustCompile(`(?i)\b(foreach|for|while|do)\s*(\(|\$|\w|:)|\bforEach\s*\(`)

// inLoopQuery matches database/ORM/HTTP/IO calls that imply N+1 or unbounded I/O
// when they appear inside a loop window.
var inLoopQuery = regexp.MustCompile(`(?i)->(get|find|first|firstOrFail|where|all|load|with|count|exists|paginate|chunk|fresh)\s*\(|::(find|where|all|first|firstOrFail|with|load|count|paginate)\s*\(|\bDB::(table|select|insert|update|delete)\s*\(|\bquery\s*\(|\bfindOne\s*\(|\bfindAll\s*\(|\bgetRepository\s*\(|\baxios\.(get|post|put|delete)\s*\(|\bfetch\s*\(|\.scan\s*\(|\.bytes\s*\(|requests\.(get|post|put|delete)\s*\(|\bfile_get_contents\s*\(|\bfopen\s*\(`)

// singleLineSignals match patterns visible on a single line.
var singleLineSignals = []struct {
	pattern  *regexp.Regexp
	severity Severity
	category string
	message  string
}{
	{
		pattern:  regexp.MustCompile(`(?i)(get_posts|new\s+WP_Query)\s*\([^)]*'?posts_per_page'?\s*=>\s*-1`),
		severity: SeverityHigh,
		category: "performance",
		message:  "Unbounded WordPress query (posts_per_page = -1); enforce a hard limit.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\b(select|find|where|all|paginate)\s*\(.*\)->(\w+)\s*->`),
		severity: SeverityMedium,
		category: "performance",
		message:  "Chained relationship traversal without eager loading; consider with()/eager-loading.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\btime\.Sleep\s*\(|\bsleep\s*\(\s*\d{2,}|setTimeout\s*\([^,]+,\s*\d{4,}`),
		severity: SeverityMedium,
		category: "performance",
		message:  "Long sleep/timeout on a request path; avoid blocking the caller.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\bfs\.readFileSync\b|\bfile_get_contents\s*\(.*\.(json|xml|csv|log)`),
		severity: SeverityMedium,
		category: "performance",
		message:  "Synchronous full-file read on a hot path; stream or cache instead.",
	},
	{
		pattern:  regexp.MustCompile(`(?i)\bnew\s+RegExp\s*\([^,)]*\+`),
		severity: SeverityLow,
		category: "performance",
		message:  "RegExp built from a string concat at runtime; precompile or escape input.",
	},
}

// loopWindow is how many lines a loop "covers" for in-loop query detection.
const loopWindow = 12

func PerfGuard(ctx context.Context, repoRoot, baseRef string) (*PerfGuardResult, error) {
	_ = ctx
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD~1"
	}
	diff, err := gitutil.UnifiedDiff(repoRoot, baseRef)
	if err != nil {
		return &PerfGuardResult{PerfRisks: []Finding{}}, nil
	}
	files := ParseUnifiedDiff(diff)
	findings := make([]Finding, 0)
	dedup := map[string]struct{}{}

	for _, f := range files {
		// Track the most recent loop start line within the file's added lines.
		recentLoopLine := -loopWindow - 1
		for _, line := range f.Added {
			content := line.Content
			trimmed := strings.TrimSpace(content)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
				continue
			}

			if loopStart.MatchString(content) {
				recentLoopLine = line.LineNo
			}
			if line.LineNo-recentLoopLine <= loopWindow && inLoopQuery.MatchString(content) {
				addFinding(&findings, dedup, Finding{
					Severity: SeverityHigh,
					Category: "performance",
					File:     f.Path,
					Line:     line.LineNo,
					Message:  "Query / HTTP / IO call inside a loop; likely N+1 or unbounded I/O. Batch the call or eager-load.",
					Evidence: []string{trimEvidence(trimmed)},
				})
			}

			for _, sig := range singleLineSignals {
				if sig.pattern.MatchString(content) {
					addFinding(&findings, dedup, Finding{
						Severity: sig.severity,
						Category: sig.category,
						File:     f.Path,
						Line:     line.LineNo,
						Message:  sig.message,
						Evidence: []string{trimEvidence(trimmed)},
					})
				}
			}
		}
	}
	return &PerfGuardResult{PerfRisks: findings}, nil
}

func addFinding(out *[]Finding, seen map[string]struct{}, f Finding) {
	key := f.File + "|" + f.Message
	if f.Line > 0 {
		key += "|" + itoa(f.Line)
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*out = append(*out, f)
}

// itoa avoids importing strconv just for this hot helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
