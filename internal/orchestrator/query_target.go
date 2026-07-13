package orchestrator

import "strings"

// bestEntityQuery picks the most symbol-like token for graph search.
func bestEntityQuery(entities []string) string {
	for _, e := range entities {
		if looksLikeSymbolName(e) {
			return e
		}
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

func isTestishPath(loc string) bool {
	lt := strings.ToLower(loc)
	return strings.Contains(lt, "_test.") || strings.Contains(lt, "/test") ||
		strings.Contains(lt, "test_") || strings.HasSuffix(lt, "_test.go")
}
