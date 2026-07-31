package security

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Lightweight whole-repo N+1 / alloc smell hints for app shapes where the
// call graph already densifies ORM/HTTP edges. Prefer real loop+query cites
// over canned controller guidance when present.

var (
	perfLoopStart = regexp.MustCompile(`(?i)\b(foreach|for|while|do)\s*(\(|\$|\w|:)|\.each\s*(do|\{)|\bforEach\s*\(`)
	// Stack-specific ORM/HTTP calls — avoid bare .get( (Map/dict FPs) and
	// java.util.Objects (never treat case-insensitive ".objects" as Django ORM).
	djangoObjectsCall = regexp.MustCompile(
		`\bobjects\.(get|filter|all|first|create|update|delete|exclude)\s*\(|` +
			`\b[A-Z][a-zA-Z0-9_]*\.objects\.(get|filter|all|first|create|update|delete|exclude)\b`)
	perfInLoopQuery = regexp.MustCompile(`(?i)` +
		`\b(User|Post|Order|Item|Product|Account|Customer|Member|Owner|Pet|Visit)\.(find|find_by|where|all|create|update|destroy)\s*\(|` +
		`\.(find_by_id|find_by|find_each)\s*\(|` +
		`::(find|where|all|first|firstOrFail|with|load|count|paginate|query)\s*\(|` +
		`->(find|first|firstOrFail|where|all|load|with|count|exists|paginate|chunk|fresh)\s*\(|` +
		`\bDB::(table|select|insert|update|delete)\s*\(|` +
		`\b(findOne|findAll|findMany|findUnique|getRepository)\s*\(|` +
		`\b(axios|fetch|requests)\.(get|post|put|delete)\s*\(|` +
		`\bprisma\.\w+\.(findMany|findFirst|findUnique|create)\s*\(|` +
		`\bawait\s+\w+\.(query|exec|execute|findOne|findAll|findMany)\s*\(|` +
		`\bentityManager\.(find|findOne|createQuery)\s*\(|` +
		`\bSession\.(query|get|load)\s*\(|` +
		// Go app stores / sql (not URL.Query): store.GetX / h.Store.IsTeamMember / db.Query
		`\b(?:store|h\.Store)\.(?:Get|Find|List|Load|Fetch|Is|Count|Select|Query)\w*\s*\(|` +
		`\b(?:db|tx|DB|sqlDB|conn)\.(?:Query|QueryRow|Exec)\s*\(|` +
		// Spring Data / JPA repository calls inside loops
		`\b\w+(?:Repository|Repo|Dao)\.(?:find|get|save|delete|count|exists)\w*\s*\(|` +
		`\.(?:findAll|findById|findBy\w+|findOne|getOne)\s*\(`)

	perfAllocHot = regexp.MustCompile(`(?i)` +
		`\bfs\.readFileSync\b|\bfile_get_contents\s*\(.*\.(json|xml|csv|log|ya?ml)|` +
		`\bJSON\.parse\s*\(\s*(?:await\s+)?fs\.|` +
		`\bnew\s+Array\s*\(\s*\d{5,}|\bBuffer\.alloc\s*\(\s*\d{6,}`)

	// Unbounded repository.findAll() on a request path (Spring /vets JSON demo).
	perfUnboundedFindAll = regexp.MustCompile(`(?i)\w+\.findAll\s*\(\s*\)`)

	perfEagerHint = regexp.MustCompile(`(?i)\b(select_related|prefetch_related|includes|preload|eager_load|joinedload|selectinload|EntityGraph|JOIN\s+FETCH)\b`)

	// URL/query-string accessors — never treat as DB/ORM N+1.
	perfURLQueryNoise = regexp.MustCompile(`(?i)\bURL\.Query\s*\(|\.Query\(\)\.Get\s*\(|r\.URL\.Query`)
)

const appPerfLoopWindow = 14

// AppPerfSmells scans application source for loop+query (N+1) and obvious
// sync-alloc hot-path smells. Returns nil for library/framework_core (wrong target).
func AppPerfSmells(root string, shape ProjectShape, limit int) []ContextFinding {
	if shape == ShapeLibrary || shape == ShapeFrameworkCore {
		return nil
	}
	if limit <= 0 {
		limit = 8
	}
	root = resolveScanRoot(filepath.Clean(root))
	out := make([]ContextFinding, 0, limit)
	files := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= limit {
			return fs.SkipAll
		}
		if d.IsDir() {
			name := d.Name()
			if repoScanSkipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				if path != root {
					return fs.SkipDir
				}
			}
			return nil
		}
		if files >= 2500 {
			return fs.SkipAll
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !isRepoScanSource(rel) || isRepoScanNoisePath(rel) {
			return nil
		}
		// Prefer controllers/services/views over pure models for N+1 cites.
		lower := strings.ToLower(rel)
		if strings.Contains(lower, "/migrations/") || strings.Contains(lower, "/entity/") ||
			strings.Contains(lower, "/entities/") || strings.HasSuffix(lower, ".min.js") ||
			strings.Contains(lower, "/cmd/migrate") || strings.Contains(lower, "/cmd/seed") ||
			// Store/DAO implementations are full of Query loops — cite callers instead.
			strings.HasSuffix(lower, "/store.go") || strings.HasSuffix(lower, "repository.java") ||
			strings.HasSuffix(lower, "repository.php") {
			return nil
		}
		files++
		hits := scanFileAppPerfSmells(path, rel, limit-len(out))
		out = append(out, hits...)
		return nil
	})
	return EnrichAndRankFindings(out)
}

func scanFileAppPerfSmells(abs, rel string, remaining int) []ContextFinding {
	if remaining <= 0 {
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	var out []ContextFinding
	recentLoop := -appPerfLoopWindow - 1
	lineNo := 0
	for sc.Scan() {
		lineNo++
		content := sc.Text()
		trimmed := strings.TrimSpace(content)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "using ") ||
			strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "namespace ") {
			continue
		}
		if perfURLQueryNoise.MatchString(content) {
			continue
		}
		isQuery := djangoObjectsCall.MatchString(content) || perfInLoopQuery.MatchString(content)
		if perfLoopStart.MatchString(content) {
			recentLoop = lineNo
			// Query on the for-header itself is the collection source (load once), not N+1.
			if isQuery {
				continue
			}
		}
		inLoop := lineNo-recentLoop <= appPerfLoopWindow && recentLoop > 0
		if inLoop && lineNo != recentLoop && isQuery {
			// Skip lines that are themselves eager-load setup (not the N+1).
			if perfEagerHint.MatchString(content) && !strings.Contains(strings.ToLower(content), "for") {
				continue
			}
			// Go store.* / Spring repo.* cites only on request-shaped paths.
			if !looksLikeRequestPath(rel) && (strings.Contains(content, "store.") ||
				strings.Contains(content, "Store.") || strings.Contains(strings.ToLower(content), "repository.")) {
				continue
			}
			msg := "Query / ORM / HTTP call inside a loop — likely N+1. Batch, eager-load (select_related/includes/with), or paginate."
			rule := "n-plus-one-loop"
			sev := "high"
			if perfEagerHint.MatchString(content) {
				msg = "ORM call inside a loop even with eager-load helpers nearby — confirm the association is loaded once, not per iteration."
				sev = "medium"
			}
			out = append(out, ContextFinding{
				Tool: "codehelper-app-perf", Severity: sev, Rule: rule,
				File: rel, Line: lineNo, Evidence: trimPerfEvidence(trimmed),
				Kind: "perf_smell", Confidence: "medium", Exploitability: "unknown",
				Hint: msg + " Next tool: `context` on this symbol, then `hotspots`.",
			})
			if len(out) >= remaining {
				return out
			}
			recentLoop = -appPerfLoopWindow - 1 // one hit per loop window
			continue
		}
		if !inLoop && looksLikeRequestPath(rel) {
			if perfAllocHot.MatchString(content) {
				out = append(out, ContextFinding{
					Tool: "codehelper-app-perf", Severity: "medium", Rule: "sync-alloc-hot-path",
					File: rel, Line: lineNo, Evidence: trimPerfEvidence(trimmed),
					Kind: "perf_smell", Confidence: "medium", Exploitability: "unknown",
					Hint: "[alloc] Sync full-file / large alloc on a request-shaped path — stream or cache. Next tool: `hotspots`.",
				})
				if len(out) >= remaining {
					return out
				}
			} else if perfUnboundedFindAll.MatchString(content) {
				out = append(out, ContextFinding{
					Tool: "codehelper-app-perf", Severity: "medium", Rule: "sync-alloc-hot-path",
					File: rel, Line: lineNo, Evidence: trimPerfEvidence(trimmed),
					Kind: "perf_smell", Confidence: "medium", Exploitability: "unknown",
					Hint: "[alloc] Unbounded findAll() on a request path — paginate (Pageable) or stream. Next tool: `hotspots`.",
				})
				if len(out) >= remaining {
					return out
				}
			}
		}
	}
	return out
}

func looksLikeRequestPath(rel string) bool {
	p := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	for _, k := range []string{
		"/controller", "/handlers/", "/handler.", "/routes/", "/views/", "/api/",
		"/middleware", "/servlet", "/endpoint", "/resource", "/http/", "/discord/",
	} {
		if strings.Contains(p, k) {
			return true
		}
	}
	base := filepath.Base(p)
	return strings.Contains(base, "controller") || strings.Contains(base, "handler") ||
		strings.Contains(base, "route") || strings.Contains(base, "view")
}

func trimPerfEvidence(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}
