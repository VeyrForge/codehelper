package mcpsvc

import (
	"encoding/json"
	"strings"

	"github.com/VeyrForge/codehelper/internal/retrieval"
)

// hostProductRepoID is the monorepo product name. Nested beds under
// codehelper/.testbeds/<bed> must never surface this in symbol ids when the
// open workspace is the bed itself.
const hostProductRepoID = "codehelper"

const hostBleedSymPrefix = "sym:" + hostProductRepoID + ":"

// shouldScrubHostBleed reports whether MCP payloads for activeRepo must drop
// host-product symbol ids (nested eval beds, not the codehelper product itself).
func shouldScrubHostBleed(activeRepo string) bool {
	r := strings.TrimSpace(activeRepo)
	return r != "" && !strings.EqualFold(r, hostProductRepoID)
}

// dropHostBleedHits removes ranked hits that carry host-product symbol ids when
// the open workspace is a nested bed. Defense in depth for resolveRepo races.
func dropHostBleedHits(hits []retrieval.RankedSymbol, activeRepo string) ([]retrieval.RankedSymbol, int) {
	if !shouldScrubHostBleed(activeRepo) || len(hits) == 0 {
		return hits, 0
	}
	out := make([]retrieval.RankedSymbol, 0, len(hits))
	dropped := 0
	for _, h := range hits {
		if isHostBleedSymbol(h.Symbol.ID, h.Symbol.RepoID) || isHostBleedPath(h.Symbol.Path) {
			dropped++
			continue
		}
		out = append(out, h)
	}
	return out, dropped
}

func isHostBleedSymbol(id, repoID string) bool {
	if strings.EqualFold(strings.TrimSpace(repoID), hostProductRepoID) {
		return true
	}
	return strings.HasPrefix(id, hostBleedSymPrefix)
}

// scrubHostBleedPayload removes host-product symbol ids / wrong repo labels from
// an MCP payload when activeRepo is a nested bed. Uses a JSON round-trip so it
// works for maps and typed response structs alike.
func scrubHostBleedPayload(payload any, activeRepo string) any {
	if !shouldScrubHostBleed(activeRepo) || payload == nil {
		return payload
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return payload
	}
	scrubbed := scrubHostBleedValue(raw, activeRepo)
	out, err := json.Marshal(scrubbed)
	if err != nil {
		return payload
	}
	// Prefer map[string]any so TOON/JSON encoders keep object shape.
	var asMap map[string]any
	if err := json.Unmarshal(out, &asMap); err == nil {
		return asMap
	}
	var asAny any
	if err := json.Unmarshal(out, &asAny); err == nil {
		return asAny
	}
	return payload
}

func scrubHostBleedValue(v any, activeRepo string) any {
	switch x := v.(type) {
	case map[string]any:
		if isHostBleedObject(x) {
			return nil // signal drop to parent array
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			if (k == "repo" || k == "repo_id" || k == "RepoID") && isHostRepoLabel(val) {
				out[k] = activeRepo
				continue
			}
			sv := scrubHostBleedValue(val, activeRepo)
			if sv == nil {
				// Dropped nested object — omit key when it was a single object;
				// arrays handle nil by filtering.
				continue
			}
			out[k] = sv
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, el := range x {
			sv := scrubHostBleedValue(el, activeRepo)
			if sv == nil {
				continue
			}
			if s, ok := sv.(string); ok && isHostBleedString(s) {
				continue
			}
			out = append(out, sv)
		}
		return out
	case string:
		if isHostBleedString(x) {
			return nil // drop: arrays filter nil; maps omit the key
		}
		return x
	default:
		return v
	}
}

func isHostRepoLabel(v any) bool {
	s, ok := v.(string)
	return ok && strings.EqualFold(strings.TrimSpace(s), hostProductRepoID)
}

func isHostBleedString(s string) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, hostBleedSymPrefix) {
		return true
	}
	if isHostBleedPath(s) {
		return true
	}
	// Compact / pretty JSON fragments that leak the host repo label.
	lower := strings.ToLower(s)
	return strings.Contains(lower, `"repo":"codehelper"`) ||
		strings.Contains(lower, `"repo": "codehelper"`) ||
		strings.Contains(lower, `"repo_id":"codehelper"`) ||
		strings.Contains(lower, `"repo_id": "codehelper"`) ||
		strings.Contains(lower, `"name":"codehelper"`) && strings.Contains(lower, `"root_path"`)
}

// isHostBleedPath reports host-product filesystem paths (absolute or multi-segment)
// that must not appear in nested-bed MCP payloads. Nested beds under
// codehelper/.testbeds or .eval-projects are allowed.
func isHostBleedPath(s string) bool {
	n := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "\\", "/"))
	if n == "" {
		return false
	}
	if strings.Contains(n, "/codehelper/.testbeds/") ||
		strings.Contains(n, "/codehelper/.eval-projects/") ||
		strings.Contains(n, "/codehelper/.ci-testbeds") {
		return false
	}
	markers := []string{
		"/codehelper/internal/",
		"/codehelper/cmd/",
		"/codehelper/pkg/",
		"/codehelper/docs/",
		"/codehelper/scripts/",
	}
	for _, m := range markers {
		if strings.Contains(n, m) {
			return true
		}
	}
	// Bare host root absolute path (…/codehelper or …/codehelper/).
	if strings.HasSuffix(n, "/"+hostProductRepoID) || strings.HasSuffix(n, "/"+hostProductRepoID+"/") {
		return true
	}
	return false
}

func isHostBleedObject(m map[string]any) bool {
	for _, k := range []string{"id", "symbol_id", "target", "ID", "SymbolID"} {
		if s, ok := m[k].(string); ok && (strings.Contains(s, hostBleedSymPrefix) || isHostBleedPath(s)) {
			return true
		}
	}
	for _, k := range []string{"path", "loc", "root_path", "RootPath"} {
		if s, ok := m[k].(string); ok && isHostBleedPath(s) {
			return true
		}
		// loc is often "path:line" — strip trailing :digits for path check.
		if s, ok := m[k].(string); ok && k == "loc" {
			if p, _, ok2 := strings.Cut(s, ":"); ok2 && isHostBleedPath(p) {
				return true
			}
		}
	}
	if looksLikeHitOrEntry(m) {
		if isHostRepoLabel(m["repo"]) || isHostRepoLabel(m["repo_id"]) || isHostRepoLabel(m["RepoID"]) {
			return true
		}
		// registry.Entry shape: name=codehelper + root_path
		if isHostRepoLabel(m["name"]) && (m["root_path"] != nil || m["RootPath"] != nil) {
			return true
		}
	}
	if sym, ok := m["symbol"].(map[string]any); ok {
		if s, ok := sym["id"].(string); ok && strings.Contains(s, hostBleedSymPrefix) {
			return true
		}
		if p, ok := sym["path"].(string); ok && isHostBleedPath(p) {
			return true
		}
		if isHostRepoLabel(sym["repo_id"]) || isHostRepoLabel(sym["RepoID"]) {
			return true
		}
	}
	return false
}

func looksLikeHitOrEntry(m map[string]any) bool {
	_, hasName := m["name"]
	_, hasPath := m["path"]
	_, hasKind := m["kind"]
	_, hasID := m["id"]
	_, hasSymID := m["symbol_id"]
	_, hasRoot := m["root_path"]
	if hasRoot {
		return true
	}
	return hasSymID || hasID || (hasName && (hasPath || hasKind))
}
