package orchestrator

import "strings"

// rankedQueryHit is a slim query/investigate hit used for primary-symbol pick.
type rankedQueryHit struct {
	Name string
	ID   string
	Loc  string
}

// bestEntityQuery picks the most symbol-like token for graph search.
// Prefers longer CamelCase / snake tokens over short ones (Run, Get).
func bestEntityQuery(entities []string) string {
	best := ""
	for _, e := range entities {
		if !looksLikeSymbolName(e) {
			continue
		}
		if best == "" || symbolTokenRank(e) > symbolTokenRank(best) {
			best = e
		}
	}
	if best != "" {
		return best
	}
	for _, e := range entities {
		e = strings.TrimSpace(e)
		if len(e) >= 3 && !isStop(strings.ToLower(e)) {
			return e
		}
	}
	return ""
}

func looksLikeSymbolName(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 || isStop(strings.ToLower(s)) {
		return false
	}
	if strings.Contains(s, "_") {
		return true
	}
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return len(s) >= 2
		}
	}
	return false
}

// isStrongSymbolToken is a multi-segment CamelCase or snake name — strong enough
// to short-circuit vague explain routes to investigate/change_kit.
func isStrongSymbolToken(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 4 || isStop(strings.ToLower(s)) || isPathLikeToken(s) {
		return false
	}
	if strings.Contains(s, "_") {
		return len(s) >= 4
	}
	hasUpper, hasLower := false, false
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
		}
		if r >= 'a' && r <= 'z' {
			hasLower = true
		}
	}
	// Require mixed case and enough length so Run/Get/Use stay weak.
	return hasUpper && hasLower && len(s) >= 6
}

func isPathLikeToken(s string) bool {
	lt := strings.ToLower(s)
	if strings.ContainsAny(s, "/\\") {
		return true
	}
	for _, ext := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".php", ".rb", ".rs", ".java", ".cs", ".gd"} {
		if strings.HasSuffix(lt, ext) {
			return true
		}
	}
	return false
}

func symbolTokenRank(s string) int {
	s = strings.TrimSpace(s)
	n := len(s)
	if isStrongSymbolToken(s) {
		return 1000 + n
	}
	if looksLikeSymbolName(s) {
		return 100 + n
	}
	return n
}

func isTestishPath(loc string) bool {
	lt := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	return strings.Contains(lt, "_test.") || strings.Contains(lt, "/test/") ||
		strings.HasPrefix(lt, "test/") || strings.Contains(lt, "test_") ||
		strings.HasSuffix(lt, "_test.go")
}

// isOrchNoisePath demotes fixtures / third_party / testbeds so they lose to
// production defs when picking the orchestrate primary symbol.
func isOrchNoisePath(loc string) bool {
	if loc == "" {
		return false
	}
	p := strings.ToLower(strings.ReplaceAll(loc, "\\", "/"))
	for _, seg := range []string{
		"/third_party/", "/testbeds/", "/.testbeds/", "/.eval-projects/",
		"/testdata/", "/fixtures/", "/fixture/", "/examples/", "/example/",
		"/sample/", "/samples/", "/docs_src/", "/playground/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	for _, prefix := range []string{
		"third_party/", "testbeds/", ".testbeds/", ".eval-projects/",
		"testdata/", "fixtures/", "fixture/", "examples/", "example/",
		"sample/", "samples/", "docs_src/", "playground/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return isTestishPath(loc)
}

func tokenizeTask(task string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(task, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == ':' || r == '(' || r == ')' ||
			r == '[' || r == ']' || r == '{' || r == '}' || r == '`' || r == '"' || r == '\''
	}) {
		tok = strings.Trim(tok, ".`'\"")
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

func pathHintsFromTask(task string) []string {
	var out []string
	for _, tok := range tokenizeTask(task) {
		lt := strings.ToLower(tok)
		if strings.Contains(tok, "/") || strings.Contains(tok, "\\") ||
			strings.HasSuffix(lt, ".go") || strings.HasSuffix(lt, ".ts") ||
			strings.HasSuffix(lt, ".js") || strings.HasSuffix(lt, ".py") ||
			strings.HasSuffix(lt, ".php") || strings.HasSuffix(lt, ".rb") {
			out = append(out, strings.ReplaceAll(tok, "\\", "/"))
		}
	}
	return out
}

// exactSymbolFromTask returns the strongest CamelCase/snake token named in the
// task (or plan entities). Empty when nothing is strong enough to short-circuit.
func exactSymbolFromTask(task string, entities []string) string {
	var best string
	consider := func(c string) {
		c = strings.TrimSpace(c)
		if !isStrongSymbolToken(c) {
			return
		}
		if best == "" || symbolTokenRank(c) > symbolTokenRank(best) {
			best = c
		}
	}
	for _, e := range entities {
		consider(e)
	}
	for _, tok := range tokenizeTask(task) {
		consider(tok)
	}
	return best
}

// shouldShortCircuitInvestigate routes known symbol-shaped tasks away from
// vague explain_code query→context chains toward investigate.
func shouldShortCircuitInvestigate(plan Plan, task string) (symbol string, ok bool) {
	sym := exactSymbolFromTask(task, plan.Entities)
	if sym == "" {
		return "", false
	}
	switch plan.Workflow {
	case WorkflowExplainCode, WorkflowBugfixTriage, WorkflowRefactorImpact:
		return sym, true
	default:
		return "", false
	}
}

func hitIdentity(h rankedQueryHit) string {
	if h.ID != "" && strings.HasPrefix(h.ID, "sym:") {
		return h.ID
	}
	return h.Name
}

func parseRankedQueryHits(hits []any) []rankedQueryHit {
	out := make([]rankedQueryHit, 0, len(hits))
	for _, h := range hits {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		n, _ := m["name"].(string)
		if n == "" {
			continue
		}
		id, _ := m["id"].(string)
		loc, _ := m["loc"].(string)
		out = append(out, rankedQueryHit{Name: n, ID: id, Loc: loc})
	}
	return out
}

// pickPrimaryQueryHit prefers exact CamelCase / path-qualified production hits
// and demotes fixtures, third_party, and testbeds — fixing sibling-first skew
// (e.g. orchestrationDisabledResult beating requireOrchestrationEnabled).
func pickPrimaryQueryHit(task string, entities []string, hits []rankedQueryHit) rankedQueryHit {
	if len(hits) == 0 {
		return rankedQueryHit{}
	}
	wantExact := map[string]struct{}{}
	wantFold := map[string]struct{}{}
	addWant := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || !looksLikeSymbolName(s) {
			return
		}
		wantExact[s] = struct{}{}
		wantFold[strings.ToLower(s)] = struct{}{}
	}
	for _, e := range entities {
		addWant(e)
	}
	for _, tok := range tokenizeTask(task) {
		addWant(tok)
	}
	pathHints := pathHintsFromTask(task)

	bestIdx := 0
	bestScore := int(-1 << 30)
	for i, h := range hits {
		score := 1000 - i // weak: keep hybrid order when ties
		if isOrchNoisePath(h.Loc) {
			score -= 600
		}
		if _, ok := wantExact[h.Name]; ok {
			score += 1200
		} else if _, ok := wantFold[strings.ToLower(h.Name)]; ok {
			score += 900
		}
		locSlash := strings.ReplaceAll(h.Loc, "\\", "/")
		for _, ph := range pathHints {
			if ph != "" && strings.Contains(strings.ToLower(locSlash), strings.ToLower(ph)) {
				score += 400
				break
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	return hits[bestIdx]
}
