package security

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Dataflow-lite: cheap intra-function heuristics that raise true app TPs
// (fail-open auth, request→sink injection) without an FP flood from library
// internals. Not full SSA/taint — bounded windows + identifier tracking.

var (
	reFuncStart = regexp.MustCompile(`(?i)^\s*(?:(?:export|async|public|private|protected|static|override|func|fn|def|function|method|proc)\b|[a-zA-Z_][\w.]*\s*\([^;]*\)\s*\{)`)
	// Empty-string RHS: "" or '' (backticks checked separately in helpers).
	reAuthFieldNonEmpty = regexp.MustCompile(`(?i)\bif\s+[!(\s]*(?:\w+\.)*(?:token|apitoken|api_token|authtoken|auth_token|bearer|apikey|api_key|secret|password|requireauth|require_auth)\s*(?:!=|!==|<>)\s*(?:""|''|nil|null|none|undefined)`)
	reAuthFieldLenOK    = regexp.MustCompile(`(?i)\bif\s+[!(\s]*len\s*\(\s*(?:\w+\.)*(?:token|apitoken|api_token|bearer|secret)\s*\)\s*(?:>|>=)\s*0`)
	reAuthVerify        = regexp.MustCompile(`(?i)authorization|bearer\s|constanttimecompare|comparehash|hmac\.|jwt\.|validateToken|authenticate|checkAuth|verifyToken|subtle\.`)
	reHandlerContinue   = regexp.MustCompile(`(?i)ServeHTTP\s*\(|\.Next\s*\(|next\s*\(|handler\.Serve|c\.Next\s*\(|call_next\s*\(|chain\.|middleware\.Next`)
	reFailOpenEmptyAllow = regexp.MustCompile(`(?i)\bif\s+[!(\s]*(?:\w+\.)*(?:token|apitoken|api_token|authtoken|auth_token|bearer|apikey|api_key|secret)\s*(?:==|===)\s*(?:""|''|nil|null|none|undefined).{0,120}(?:return\s+next|next\s*\(|ServeHTTP|allow|pass\b|continue\b)`)
	// JS/TS: if (!token) return next() / if (!apiKey) { next(); return }
	reFailOpenMissingCred = regexp.MustCompile(`(?i)\bif\s*\(\s*!\s*(?:\w+\.)*(?:token|apitoken|api_token|authtoken|auth_token|bearer|apikey|api_key|secret|authorization)\s*\)\s*(?:\{[^}]{0,80}\bnext\s*\(|return\s+next\s*\()`)
	reOpenRedirectSink    = regexp.MustCompile(`(?i)(?:\b(?:redirect|Redirect|HttpResponseRedirect|res\.redirect|Response\.Redirect|header\s*\(\s*[\"']Location)\s*\()`)
	reRequestSource       = regexp.MustCompile(`(?i)(?:\b(?:req|request|r|c|ctx|httpContext)\.(?:(?:URL|Body|Form|Query|Param|Params|Header|Cookie|Cookies|PostForm|MultipartForm|args|GET|POST|data|json|query|params|headers|cookies|input|values)\b)|(?:req|request)\.(?:body|query|params|headers)|request\.(?:args|GET|POST|form)|@Request(?:Body|Param|Query)|HttpServletRequest|getParameter\s*\()`)
	reSQLSink           = regexp.MustCompile(`(?i)(?:\b(?:query|exec|execute|raw|rawquery|rawsql|queryraw|executeraw)\s*\(|sql\.raw|db\.query|db\.exec|cursor\.execute|connection\.execute|session\.execute|\$queryraw|\$executeraw|select\s+.+\+|insert\s+.+\+|update\s+.+\+|delete\s+.+\+|\$\{)`)
	reCmdSink           = regexp.MustCompile(`(?i)(?:\b(?:exec|system|popen|spawn|execsync|child_process|os\.system|subprocess\.|command\s*\(|shellexecute)\s*\()`)
	reEvalSink          = regexp.MustCompile(`(?i)(?:\beval\s*\(|new\s+function\s*\()`)
	reXSSSink           = regexp.MustCompile(`(?i)(?:dangerouslysetinnerhtml|v-html\s*=|\{@html\s|mark_safe\s*\(|\|\s*safe\s*\}|\|\s*raw\s*\}|innerhtml\s*=)`)
)

// ScanFileDataflowLite returns fail-open auth and request→sink findings for one
// source file. Cap remaining findings; callers EnrichAndRank afterwards.
func ScanFileDataflowLite(abs, rel string, remaining int) []ContextFinding {
	if remaining <= 0 || abs == "" {
		return nil
	}
	lowerRel := strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	if isRepoScanNoisePath(rel) || isLibraryInternalPath(lowerRel) {
		return nil
	}
	raw, err := os.ReadFile(abs)
	if err != nil || len(raw) == 0 {
		return nil
	}
	// Bound memory: skip huge generated files.
	if len(raw) > 512*1024 {
		return nil
	}
	lines := splitKeepLines(string(raw))
	var out []ContextFinding
	out = append(out, detectFailOpenAuth(rel, lines, remaining)...)
	if rem := remaining - len(out); rem > 0 {
		out = append(out, detectRequestToSinkTaint(rel, lines, rem)...)
	}
	return out
}

// ScanRepoDataflowLite walks the same source set as the lexical repo scan and
// appends dataflow-lite findings. Used as a second pass after substring rules.
func ScanRepoDataflowLite(root string, opts RepoScanOptions) []ContextFinding {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 40
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 4000
	}
	root = filepath.Clean(root)
	out := make([]ContextFinding, 0, opts.Limit/2)
	files := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= opts.Limit {
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
		if files >= opts.MaxFiles {
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
		files++
		hits := ScanFileDataflowLite(path, rel, opts.Limit-len(out))
		out = append(out, hits...)
		return nil
	})
	return out
}

func splitKeepLines(s string) []string {
	sc := bufio.NewScanner(strings.NewReader(s))
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// detectFailOpenAuth finds middleware that only enforces auth when a config
// token/secret is non-empty — empty config silently allows all callers.
func detectFailOpenAuth(rel string, lines []string, remaining int) []ContextFinding {
	var out []ContextFinding
	funcs := splitFunctionRanges(lines)
	for _, fr := range funcs {
		if len(out) >= remaining {
			break
		}
		body := lines[fr.start:fr.end]
		joined := strings.Join(body, "\n")
		lower := strings.ToLower(joined)
		// Pattern A: if token != "" { verify… } … ServeHTTP/next (outside gate)
		if (reAuthFieldNonEmpty.MatchString(joined) || reAuthFieldLenOK.MatchString(joined)) &&
			reAuthVerify.MatchString(joined) && reHandlerContinue.MatchString(joined) {
			// Fail-closed usually returns 401/Unauthorized inside an else or when empty.
			if looksFailClosedAuth(lower) {
				continue
			}
			line := fr.start + 1
			for i, l := range body {
				if reAuthFieldNonEmpty.MatchString(l) || reAuthFieldLenOK.MatchString(l) {
					line = fr.start + i + 1
					break
				}
			}
			ev := truncate(strings.TrimSpace(body[line-fr.start-1]), 180)
			out = append(out, ContextFinding{
				Tool: "codehelper-dataflow-lite", Severity: "high", Rule: "authz-fail-open",
				File: rel, Line: line, Evidence: ev,
				Kind: "sink_candidate", Confidence: "high", Exploitability: "likely",
				Hint: "Empty auth config/token skips verification and still calls next/ServeHTTP — require a token (or reject) in production.",
			})
			continue
		}
		// Pattern B: if token == "" { return next() / allow }
		if reFailOpenEmptyAllow.MatchString(joined) && !looksFailClosedAuth(lower) {
			line := fr.start + 1
			for i, l := range body {
				if reFailOpenEmptyAllow.MatchString(l) || (strings.Contains(strings.ToLower(l), `== ""`) &&
					(strings.Contains(strings.ToLower(l), "token") || strings.Contains(strings.ToLower(l), "bearer"))) {
					line = fr.start + i + 1
					break
				}
			}
			ev := truncate(strings.TrimSpace(lines[line-1]), 180)
			out = append(out, ContextFinding{
				Tool: "codehelper-dataflow-lite", Severity: "high", Rule: "authz-fail-open",
				File: rel, Line: line, Evidence: ev,
				Kind: "sink_candidate", Confidence: "high", Exploitability: "likely",
				Hint: "Empty credential explicitly allows the request — fail-open auth. Reject missing credentials instead.",
			})
			continue
		}
		// Pattern C: if (!token) return next() — JS/TS middleware fail-open
		if reFailOpenMissingCred.MatchString(joined) && !looksFailClosedAuth(lower) {
			line := fr.start + 1
			for i, l := range body {
				if reFailOpenMissingCred.MatchString(l) ||
					(strings.Contains(l, "!") && strings.Contains(strings.ToLower(l), "token") &&
						strings.Contains(strings.ToLower(l), "next")) {
					line = fr.start + i + 1
					break
				}
			}
			ev := truncate(strings.TrimSpace(lines[line-1]), 180)
			out = append(out, ContextFinding{
				Tool: "codehelper-dataflow-lite", Severity: "high", Rule: "authz-fail-open",
				File: rel, Line: line, Evidence: ev,
				Kind: "sink_candidate", Confidence: "high", Exploitability: "likely",
				Hint: "Missing credential skips auth and calls next() — fail-open. Reject unauthenticated callers.",
			})
		}
	}
	return out
}

func looksFailClosedAuth(lower string) bool {
	// Returns 401/Unauthorized when empty, or requires auth unconditionally.
	closed := []string{
		"statusunauthorized", "http.unauthorized", "401", "unauthorized",
		"return err", "return error", "abort(", "forbid", "forbidden",
		"writejsonerror", "http.errormessage", "requireauth", "mustauthenticate",
	}
	// If empty-token branch returns an error/401, it is fail-closed.
	if strings.Contains(lower, `== ""`) || strings.Contains(lower, `== ''`) {
		for _, c := range closed {
			if strings.Contains(lower, c) {
				return true
			}
		}
	}
	// Explicit "must have token" without optional skip.
	if strings.Contains(lower, "token is required") || strings.Contains(lower, "missing bearer") {
		return true
	}
	return false
}

type funcRange struct{ start, end int } // end exclusive

func splitFunctionRanges(lines []string) []funcRange {
	var ranges []funcRange
	start := -1
	depth := 0
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if start < 0 {
			if reFuncStart.MatchString(line) || looksLikeFuncHeader(trim) {
				start = i
				depth = strings.Count(line, "{") - strings.Count(line, "}")
				if depth <= 0 && strings.HasSuffix(trim, "{") {
					depth = 1
				}
				// Python/Ruby: no braces — take next ~40 lines as body window.
				if depth <= 0 && (strings.HasSuffix(trim, ":") || strings.HasPrefix(trim, "def ")) {
					end := i + 1
					for end < len(lines) && end < i+45 {
						nt := strings.TrimSpace(lines[end])
						if nt != "" && !strings.HasPrefix(nt, "#") && !strings.HasPrefix(nt, "\"\"\"") &&
							indentLen(lines[end]) <= indentLen(line) && end > i+1 {
							break
						}
						end++
					}
					ranges = append(ranges, funcRange{start: i, end: end})
					start = -1
				}
			}
			continue
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			ranges = append(ranges, funcRange{start: start, end: i + 1})
			start = -1
			depth = 0
		}
	}
	if start >= 0 {
		end := start + 80
		if end > len(lines) {
			end = len(lines)
		}
		ranges = append(ranges, funcRange{start: start, end: end})
	}
	// Always include a whole-file window for short middleware files.
	if len(ranges) == 0 && len(lines) > 0 {
		end := len(lines)
		if end > 120 {
			end = 120
		}
		ranges = append(ranges, funcRange{start: 0, end: end})
	}
	return ranges
}

func looksLikeFuncHeader(trim string) bool {
	if trim == "" {
		return false
	}
	lower := strings.ToLower(trim)
	if strings.HasPrefix(lower, "func ") || strings.HasPrefix(lower, "def ") ||
		strings.HasPrefix(lower, "fn ") || strings.HasPrefix(lower, "async function") ||
		strings.HasPrefix(lower, "function ") || strings.HasPrefix(lower, "export function") ||
		strings.HasPrefix(lower, "export async function") {
		return true
	}
	// Go method: func (s *Server) auth(
	if strings.HasPrefix(lower, "func (") && strings.Contains(lower, ")") {
		return true
	}
	return false
}

func indentLen(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
		} else if r == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}

// detectRequestToSinkTaint flags SQL/cmd/eval/XSS sinks that also mention a
// request-derived identifier in the same function (intra-procedural lite).
func detectRequestToSinkTaint(rel string, lines []string, remaining int) []ContextFinding {
	var out []ContextFinding
	funcs := splitFunctionRanges(lines)
	for _, fr := range funcs {
		if len(out) >= remaining {
			break
		}
		body := lines[fr.start:fr.end]
		joined := strings.Join(body, "\n")
		if !reRequestSource.MatchString(joined) {
			continue
		}
		// Collect simple identifiers assigned from request sources.
		tainted := map[string]bool{}
		for _, l := range body {
			ids := extractTaintedIDs(l)
			for _, id := range ids {
				tainted[id] = true
			}
		}
		// Also treat common request field accessors as tainted tokens.
		for _, tok := range []string{"id", "q", "query", "search", "name", "user", "email",
			"path", "file", "cmd", "sql", "raw", "input", "body", "param", "slug"} {
			if strings.Contains(strings.ToLower(joined), "."+tok) ||
				strings.Contains(strings.ToLower(joined), `["`+tok+`"]`) ||
				strings.Contains(strings.ToLower(joined), `['`+tok+`']`) {
				tainted[tok] = true
			}
		}
		if len(tainted) == 0 {
			continue
		}
		for i, l := range body {
			if len(out) >= remaining {
				break
			}
			lower := strings.ToLower(l)
			rule, sev := "", ""
			switch {
			case reSQLSink.MatchString(l) && looksLikeSQL(lower) && !looksLikeParameterizedSQL(lower) &&
				!looksLikeBoundSQL(lower) && !looksLikeORMParameterizedTaggedSQL(lower) &&
				!isSecurityScanFalseFriend(rel, l, "sql-string-concat") &&
				!isMetaSinkString(lower, "sql-string-concat"):
				rule, sev = "injection-taint", "high"
			case reCmdSink.MatchString(l) && !strings.Contains(lower, "shellwords") &&
				!isSecurityScanFalseFriend(rel, l, "shell-exec-injection"):
				rule, sev = "injection-taint", "high"
			case reEvalSink.MatchString(l) && !isFrameworkCompilerEval(strings.ToLower(rel), lower) &&
				!isSecurityScanFalseFriend(rel, l, "eval-usage"):
				rule, sev = "injection-taint", "high"
			case reXSSSink.MatchString(l) && !isLowProvenanceHTML(rel, lower, joined) &&
				!isSecurityScanFalseFriend(rel, l, "raw-html-xss"):
				rule, sev = "injection-taint", "medium"
			case reOpenRedirectSink.MatchString(l) && lineUsesTainted(lower, tainted) &&
				!isSecurityScanFalseFriend(rel, l, "open-redirect"):
				rule, sev = "open-redirect-taint", "medium"
			default:
				continue
			}
			if !lineUsesTainted(lower, tainted) && !reRequestSource.MatchString(l) {
				continue
			}
			lineNo := fr.start + i + 1
			out = append(out, ContextFinding{
				Tool: "codehelper-dataflow-lite", Severity: sev, Rule: rule,
				File: rel, Line: lineNo, Evidence: truncate(strings.TrimSpace(l), 200),
				Kind: "sink_candidate", Confidence: "high", Exploitability: "likely",
				Hint: "Request-derived data reaches this sink in the same function — confirm sanitization/parameterization before dismissing.",
			})
		}
	}
	return out
}

var reAssignFromReq = regexp.MustCompile(`(?i)\b([a-z_][\w]*)\s*[:=]=?\s*.*(?:req|request|r|c|ctx)\.(?:(?:Query|Param|Params|Form|Body|Header|URL|args|GET|POST|json|query|params|body|headers)\b|FormValue|PathValue|QueryParam)`)

func extractTaintedIDs(line string) []string {
	var ids []string
	for _, m := range reAssignFromReq.FindAllStringSubmatch(line, -1) {
		if len(m) > 1 && m[1] != "" {
			ids = append(ids, strings.ToLower(m[1]))
		}
	}
	return ids
}

func lineUsesTainted(lower string, tainted map[string]bool) bool {
	for id := range tainted {
		if id == "" || len(id) < 2 {
			continue
		}
		if strings.Contains(lower, id) {
			return true
		}
	}
	return false
}

// isLibraryInternalPath reports framework/ORM/compiler internals that must not
// surface as app sink TPs (demote/drop harder than generic noise paths).
func isLibraryInternalPath(lowerRel string) bool {
	if isFrameworkSQLInternal(lowerRel) || isFrameworkXSSInternal(lowerRel) {
		return true
	}
	markers := []string{
		"/node_modules/", "/vendor/", "/site-packages/", "/.venv/",
		"/packages/compiler-", "/packages/runtime-", "/packages/shared/",
		"/packages/svelte/src/compiler/", "/django/db/", "/django/core/",
		"/django/contrib/", "/flask/sansio/", "/starlette/", "/uvicorn/",
		"/gin-gonic/", "/labstack/echo/", "/actionpack/lib/", "/activerecord/lib/",
		"/illuminate/database/", "/symfony/http-foundation/", "/symfony/routing/",
		"/axum-core/", "/axum/src/extract/", "/tokio/", "/hyper/",
		"/spring-framework/", "/springframework/", "/reactor/",
		"/lib/router/", // express core router internals when scanning express itself are OK as guidance —
		"/rails/railties/", "/railties/lib/",
	}
	for _, m := range markers {
		if strings.Contains(lowerRel, m) {
			return true
		}
	}
	// Top-level framework package trees (eval checkouts).
	bases := []string{"django/", "flask/", "fastapi/", "starlette/", "gin/", "axum/",
		"actionpack/", "activerecord/", "illuminate/", "symfony/"}
	for _, b := range bases {
		if strings.HasPrefix(lowerRel, b) {
			return true
		}
	}
	return false
}
