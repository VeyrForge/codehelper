package agent

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// workspaceToolNames matches tools that take repo-relative paths on disk.
var workspaceToolNames = map[string]bool{
	"read_workspace_file":        true,
	"write_workspace_file":       true,
	"apply_patch_workspace_file": true,
	"revert_workspace_edit":      true,
	"list_workspace_directory":   true,
}

func stringish(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

// stripSurroundingQuotes removes repeated JSON-style quotes LLMs sometimes
// emit around repo names.
func stripSurroundingQuotes(s string) string {
	r := strings.TrimSpace(s)
	for len(r) >= 2 {
		a, b := r[0], r[len(r)-1]
		if (a == '"' && b == '"') || (a == '\'' && b == '\'') {
			r = strings.TrimSpace(r[1 : len(r)-1])
			continue
		}
		break
	}
	return r
}

// instructionalRepoGarbage detects placeholder repo args copied from docs.
func instructionalRepoGarbage(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return true
	}
	switch t {
	case "<registry_name_from_project_context>", "<repository_name>", "<repo>",
		"repository_name", "registry_name_from_project_context", "repo_name",
		"your_repo", "your-repo", "example_repo", "the_repository_name",
		"my-repo-name", "my_repo_name":
		return true
	}
	if strings.Contains(t, "registry_name") || strings.Contains(t, "project_context") {
		return true
	}
	if len(t) >= 2 && t[0] == '<' && t[len(t)-1] == '>' {
		return true
	}
	return false
}

func pickAllowedKeys(args map[string]any, keys []string) map[string]any {
	o := map[string]any{}
	for _, k := range keys {
		if v, ok := args[k]; ok && v != nil {
			o[k] = v
		}
	}
	return o
}

// sanitizeWorkspaceToolArgs drops keys LLMs sometimes echo from prior errors.
func sanitizeWorkspaceToolArgs(name string, args map[string]any) map[string]any {
	switch name {
	case "list_workspace_directory":
		return pickAllowedKeys(args, []string{"path", "repo", "max_entries"})
	case "read_workspace_file":
		return pickAllowedKeys(args, []string{"path", "repo", "max_bytes"})
	case "write_workspace_file":
		return pickAllowedKeys(args, []string{"path", "content", "repo", "create_directories", "allow_truncate"})
	case "apply_patch_workspace_file":
		return pickAllowedKeys(args, []string{"path", "repo", "hunks", "dry_run"})
	case "revert_workspace_edit":
		return pickAllowedKeys(args, []string{"revert_token"})
	default:
		out := map[string]any{}
		for k, v := range args {
			out[k] = v
		}
		return out
	}
}

var repoRootPathRe = regexp.MustCompile(`(?i)^repo\s+root$`)
var slashRepoPrefixRe = regexp.MustCompile(`(?i)^/repo(/|$)`)
var intRangeRe = regexp.MustCompile(`^(\d+)\s*-\s*(\d+)$`)

func normalizeWorkspacePath(p any) string {
	if p == nil {
		return "."
	}
	s := strings.TrimSpace(stringish(p))
	s = strings.ReplaceAll(s, `\`, "/")

	// Model prose like `repo root` / `/repo/cmd/…`.
	if repoRootPathRe.MatchString(strings.TrimSpace(s)) {
		return "."
	}
	if slashRepoPrefixRe.MatchString(s) {
		s = strings.TrimSpace(slashRepoPrefixRe.ReplaceAllString(s, ""))
		if s == "" {
			return "."
		}
	}

	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	if s == "" || s == "." {
		return "."
	}
	if len(s) > 1 && strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "..") {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "."
	}
	return s
}

func promoteWorkspaceAliases(name string, raw map[string]any) map[string]any {
	o := map[string]any{}
	for k, v := range raw {
		o[k] = v
	}
	switch name {
	case "read_workspace_file", "write_workspace_file", "apply_patch_workspace_file", "list_workspace_directory":
	default:
		return o
	}
	if stringish(o["path"]) == "" {
		for _, k := range []string{"file_path", "filepath", "relative_path", "file", "filename"} {
			if s := stringish(o[k]); s != "" {
				o["path"] = s
				break
			}
		}
	}
	if name == "list_workspace_directory" {
		if _, isStr := o["path"].(string); !isStr || stringish(o["path"]) == "" {
			o["path"] = "."
		} else {
			o["path"] = normalizeWorkspacePath(o["path"])
		}
	} else if _, isStr := o["path"].(string); isStr {
		o["path"] = normalizeWorkspacePath(o["path"])
	}
	return o
}

func coerceIntField(out map[string]any, key string) {
	v, ok := out[key]
	if !ok || v == nil {
		return
	}
	switch t := v.(type) {
	case float64:
		out[key] = math.Floor(t)
	case int:
		// already integral
	case string:
		s := strings.TrimSpace(t)
		if m := intRangeRe.FindStringSubmatch(s); m != nil {
			if n, err := strconv.Atoi(m[2]); err == nil {
				out[key] = float64(max(1, n))
			}
			return
		}
		if n, err := strconv.Atoi(s); err == nil {
			out[key] = float64(max(1, n))
		}
	}
}

// queryFallbackText replaces empty query strings so retrieval still returns
// something useful instead of a hard error.
const queryFallbackText = "codebase architecture main entrypoints"

var queryRepoJunkRe = regexp.MustCompile(`(?i),\s*repo\s*:\s*[^,\s]+`)
var queryIntentJunkRe = regexp.MustCompile(`(?i)\s*,\s*intent\s*:\s*[^,\s]+`)
var repoColonRe = regexp.MustCompile(`(?i)\brepo\s*:`)
var repoPartRe = regexp.MustCompile(`(?i)^repo\s*:`)
var intentPartRe = regexp.MustCompile(`(?i)^intent\s*:`)

func scrubModelQueryGarbage(q string) string {
	s := strings.TrimSpace(q)
	if s == "" {
		return ""
	}
	s = strings.TrimSpace(queryRepoJunkRe.ReplaceAllString(s, ""))
	s = strings.TrimSpace(queryIntentJunkRe.ReplaceAllString(s, ""))
	parts := []string{}
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) >= 2 && repoColonRe.MatchString(s) {
		kept := []string{}
		for _, p := range parts {
			if !repoPartRe.MatchString(p) && !intentPartRe.MatchString(p) {
				kept = append(kept, p)
			}
		}
		s = strings.Join(kept, " ")
	}
	return strings.TrimSpace(s)
}

func coalesceStrings(args map[string]any, keys []string) string {
	for _, k := range keys {
		v, ok := args[k]
		if !ok {
			continue
		}
		s := ""
		if arr, isArr := v.([]any); isArr {
			bits := []string{}
			for _, x := range arr {
				if t := stringish(x); t != "" {
					bits = append(bits, t)
				}
			}
			s = strings.Join(bits, " ")
		} else {
			s = stringish(v)
		}
		if s != "" {
			return s
		}
	}
	return ""
}

// normalizeToolArguments aligns LLM quirks with MCP handlers: copy `q` →
// `query` when `query` is empty, normalize `repo` strings, relative paths
// for workspace reads. Port of the original host-side normalization.
func normalizeToolArguments(name string, args map[string]any) map[string]any {
	patched := promoteWorkspaceAliases(name, args)

	// Coerce `repo` synonyms before sanitization strips unknown keys.
	if stringish(patched["repo"]) == "" {
		for _, k := range []string{"repo_name", "repository_name", "repository", "repositoryName"} {
			if s := stringish(patched[k]); s != "" {
				patched["repo"] = s
			}
			delete(patched, k)
		}
	} else {
		for _, k := range []string{"repo_name", "repository_name", "repository", "repositoryName"} {
			delete(patched, k)
		}
	}

	var out map[string]any
	if workspaceToolNames[name] {
		out = sanitizeWorkspaceToolArgs(name, patched)
	} else {
		out = map[string]any{}
		for k, v := range patched {
			out[k] = v
		}
	}

	if name == "query" {
		coerceIntField(out, "limit")
		coerceIntField(out, "budget_tokens")
	}
	if name == "list_workspace_directory" {
		coerceIntField(out, "max_entries")
	}

	if name == "query" {
		ps := scrubModelQueryGarbage(stringish(out["query"]))
		alt := scrubModelQueryGarbage(stringish(out["q"]))
		fromAliases := scrubModelQueryGarbage(coalesceStrings(out, []string{
			"symbols", "symbol", "search", "terms", "text", "keyword", "keywords",
			"query_terms", "queryTerms", "topic",
		}))
		if ps == "" && alt != "" {
			ps = alt
			delete(out, "q")
		} else if out["query"] == nil {
			delete(out, "query")
			if alt != "" {
				ps = alt
			}
		}
		if ps == "" && fromAliases != "" {
			ps = fromAliases
		}
		if ps != "" {
			out["query"] = ps
		}
		delete(out, "q")
		if q, isStr := out["query"].(string); isStr {
			if scrubModelQueryGarbage(q) == "" {
				delete(out, "query")
			} else {
				out["query"] = scrubModelQueryGarbage(q)
			}
		}
		if stringish(out["query"]) == "" {
			out["query"] = queryFallbackText
		}
		if intent, isStr := out["intent"].(string); isStr && strings.TrimSpace(intent) == "" {
			delete(out, "intent")
		}
		for _, k := range []string{
			"symbols", "symbol", "search", "terms", "text", "keyword", "keywords",
			"query_terms", "queryTerms", "topic", "sort_by", "q",
		} {
			delete(out, k)
		}
	}

	if v, ok := out["repo"]; ok {
		if v == nil {
			delete(out, "repo")
		} else if s, isStr := v.(string); isStr {
			raw := stripSurroundingQuotes(s)
			if instructionalRepoGarbage(raw) {
				delete(out, "repo")
			} else if raw == "" {
				delete(out, "repo")
			} else {
				out["repo"] = raw
			}
		}
	}

	switch name {
	case "read_workspace_file", "write_workspace_file", "apply_patch_workspace_file":
		if p, isStr := out["path"].(string); isStr && strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") {
			out["path"] = strings.TrimLeft(p, "/")
		}
	}

	return out
}
