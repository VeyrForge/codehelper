// Response-review harness.
//
// Unlike tools_smoke_test.go, which only asserts that each tool returns a
// non-error result, this test captures every tool's response body and writes
// it to .codehelper/_smoke/review-<timestamp>/<tool>.txt so a human can read
// each one and decide whether the content is actually useful.
//
// Run with:
//
//	go test ./internal/mcpsvc -run TestReviewToolResponses -v
//
// The test always passes (we want the dump regardless of content quality);
// problems are flagged via t.Logf so they show up in -v output and can be
// scanned without re-running.
package mcpsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type reviewCase struct {
	tool   string
	args   map[string]any
	wantOK bool
	// keysAny: any one of these keys appearing at the top of the JSON body is
	// enough; useful when a tool has alternate response shapes.
	keysAny []string
	// keysAll: every key must appear at the top of the JSON body.
	keysAll []string
	notes   string
}

func TestReviewToolResponses(t *testing.T) {
	reg, repo := liveRegistryWithIndexedRepo(t)
	_, handlers := hookedRegister(reg)

	dumpDir := filepath.Join(repo.RootPath, ".codehelper", "_smoke", "review-"+time.Now().Format("20060102T150405"))
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		t.Fatalf("mkdir dump dir: %v", err)
	}
	t.Logf("dumping tool responses under: %s", dumpDir)

	common := func(extra map[string]any) map[string]any {
		out := map[string]any{"repo": repo.Name}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// Stage scratch files for workspace tools so they have something concrete
	// to read/patch. They live under .codehelper/_smoke which is gitignored.
	scratch := filepath.ToSlash(filepath.Join(".codehelper", "_smoke", fmt.Sprintf("review-%d.txt", time.Now().UnixNano())))
	scratchAbs := filepath.Join(repo.RootPath, scratch)
	if err := os.MkdirAll(filepath.Dir(scratchAbs), 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	if err := os.WriteFile(scratchAbs, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	defer os.Remove(scratchAbs)

	cases := []reviewCase{
		{tool: "project_context", args: common(nil), wantOK: true, keysAll: []string{"repo", "repo_root", "freshness", "index_status"}},
		{tool: "query", args: common(map[string]any{"query": "register"}), wantOK: true, keysAll: []string{"hits", "freshness"}},
		{tool: "context", args: common(map[string]any{"name": "RegisterAll"}), wantOK: true, keysAll: []string{"bundle", "freshness"}},
		{tool: "impact", args: common(map[string]any{"target": "RegisterAll", "direction": "downstream", "depth": float64(2)}), wantOK: true, keysAll: []string{"impact", "freshness"}},
		{tool: "detect_changes", args: common(map[string]any{"base_ref": "HEAD~1"}), wantOK: true, keysAll: []string{"changed_symbol_ids", "base_ref"}},
		{tool: "scout", args: common(map[string]any{"task": "register mcp tools"}), wantOK: true, keysAll: []string{"reuse_candidates", "task"}},
		{tool: "test_impact", args: common(map[string]any{"target": "RegisterAll"}), wantOK: true, keysAll: []string{"safety", "seeds"}},
		{tool: "verify", args: map[string]any{"repo_root": repo.RootPath}, wantOK: true},
		{tool: "review_diff", args: common(map[string]any{"base": "HEAD~1", "severity_floor": "low"}), wantOK: true},
		{tool: "read_workspace_file", args: common(map[string]any{"path": scratch, "max_bytes": float64(4096)}), wantOK: true, keysAll: []string{"content", "size_bytes"}},
		{tool: "list_workspace_directory", args: common(map[string]any{"path": "."}), wantOK: true, keysAll: []string{"entries", "total_found"}},
		{tool: "write_workspace_file", args: common(map[string]any{
			"path":               filepath.ToSlash(filepath.Join(".codehelper", "_smoke", "write-target.txt")),
			"content":            "new content for review\n",
			"create_directories": true,
		}), wantOK: true, keysAll: []string{"bytes_written", "diff"}},
		{tool: "apply_patch_workspace_file", args: common(map[string]any{
			"path": scratch,
			"hunks": []any{
				map[string]any{"old_string": "hello\n", "new_string": "hello PATCHED\n"},
			},
		}), wantOK: true, keysAll: []string{"diff", "revert_token", "hunks_applied"}},
		{tool: "finish_check", args: common(map[string]any{"base_ref": "HEAD~1"}), wantOK: true},
		{tool: "agent_memory", args: common(map[string]any{"action": "search", "query": "register", "limit": float64(4)}), wantOK: true, keysAll: []string{"relevant_memory"}},
		{tool: "agent_plan", args: common(map[string]any{
			"request": "Add rate limiting middleware", "approve_todos": true,
		}), wantOK: true, keysAll: []string{"task_id", "task", "recommended_next_tool"}},
	}

	type summary struct {
		Tool       string `json:"tool"`
		OK         bool   `json:"ok"`
		HasContent bool   `json:"has_content"`
		BodyBytes  int    `json:"body_bytes"`
		FirstLine  string `json:"first_line"`
		Issue      string `json:"issue,omitempty"`
	}
	results := make([]summary, 0, len(cases))

	for _, c := range cases {
		c := c
		t.Run(c.tool, func(t *testing.T) {
			res, err := callTool(t, nil, handlers, c.tool, c.args)
			s := summary{Tool: c.tool, OK: err == nil && res != nil && !res.IsError}
			body := resultText(res)
			s.HasContent = strings.TrimSpace(body) != ""
			s.BodyBytes = len(body)
			s.FirstLine = firstLine(body, 240)

			if err != nil {
				s.Issue = "transport error: " + err.Error()
			} else if res == nil {
				s.Issue = "nil result"
			} else if c.wantOK && res.IsError {
				s.Issue = "IsError=true: " + body
			} else if c.wantOK && !s.HasContent {
				s.Issue = "wantOK but body is empty"
			}

			// Schema-shape assertions (best-effort: only run on JSON bodies).
			if s.Issue == "" && (len(c.keysAll) > 0 || len(c.keysAny) > 0) {
				var top map[string]any
				if json.Unmarshal([]byte(body), &top) == nil {
					missing := []string{}
					for _, k := range c.keysAll {
						if _, ok := top[k]; !ok {
							missing = append(missing, k)
						}
					}
					if len(missing) > 0 {
						s.Issue = fmt.Sprintf("missing required keys: %s", strings.Join(missing, ","))
					} else if len(c.keysAny) > 0 {
						anyHit := false
						for _, k := range c.keysAny {
							if _, ok := top[k]; ok {
								anyHit = true
								break
							}
						}
						if !anyHit {
							s.Issue = fmt.Sprintf("none of keys_any present: %s", strings.Join(c.keysAny, ","))
						}
					}
				}
			}
			if s.Issue == "" {
				if issue := semanticResponseIssue(c.tool, body, repo.Name, repo.RootPath); issue != "" {
					s.Issue = issue
				}
			}

			// Persist the body for human review.
			_ = os.WriteFile(filepath.Join(dumpDir, sanitizeFile(c.tool)+".txt"), []byte(body), 0o644)

			results = append(results, s)
			if s.Issue != "" {
				t.Logf("REVIEW [%s] FAIL: %s\nbody (first 400b): %s", c.tool, s.Issue, truncate(body, 400))
			} else {
				t.Logf("REVIEW [%s] OK   bytes=%d first=%q", c.tool, s.BodyBytes, s.FirstLine)
			}
		})
	}

	// Exercise revert_workspace_edit with a fresh write+revert pair.
	t.Run("revert_workspace_edit", func(t *testing.T) {
		rel := filepath.ToSlash(filepath.Join(".codehelper", "_smoke", "revert-target.txt"))
		defer os.Remove(filepath.Join(repo.RootPath, rel))
		_, _ = callTool(t, nil, handlers, "write_workspace_file", map[string]any{
			"repo":               repo.Name,
			"path":               rel,
			"content":            "before\n",
			"create_directories": true,
		})
		patchRes, _ := callTool(t, nil, handlers, "apply_patch_workspace_file", map[string]any{
			"repo": repo.Name,
			"path": rel,
			"hunks": []any{
				map[string]any{"old_string": "before\n", "new_string": "after\n"},
			},
		})
		var pb map[string]any
		_ = json.Unmarshal([]byte(resultText(patchRes)), &pb)
		token, _ := pb["revert_token"].(string)
		if token == "" {
			t.Fatalf("revert_workspace_edit setup did not yield a token")
		}
		rev, rerr := callTool(t, nil, handlers, "revert_workspace_edit", map[string]any{"revert_token": token})
		body := resultText(rev)
		s := summary{Tool: "revert_workspace_edit", OK: rerr == nil && rev != nil && !rev.IsError, HasContent: body != "", BodyBytes: len(body), FirstLine: firstLine(body, 240)}
		_ = os.WriteFile(filepath.Join(dumpDir, "revert_workspace_edit.txt"), []byte(body), 0o644)
		if rev == nil || rev.IsError {
			s.Issue = "revert failed: " + body
			t.Logf("REVIEW [revert_workspace_edit] FAIL: %s", s.Issue)
		} else {
			t.Logf("REVIEW [revert_workspace_edit] OK   bytes=%d first=%q", s.BodyBytes, s.FirstLine)
		}
		results = append(results, s)
	})

	// Summary table for the human reader.
	sort.Slice(results, func(i, j int) bool { return results[i].Tool < results[j].Tool })
	report := strings.Builder{}
	report.WriteString("tool, ok, bytes, issue\n")
	fails := 0
	for _, r := range results {
		if r.Issue != "" {
			fails++
		}
		report.WriteString(fmt.Sprintf("%s, %v, %d, %s\n", r.Tool, r.OK, r.BodyBytes, r.Issue))
	}
	_ = os.WriteFile(filepath.Join(dumpDir, "_summary.csv"), []byte(report.String()), 0o644)
	t.Logf("review summary: %d tool(s) flagged out of %d. Full bodies under %s", fails, len(results), dumpDir)
	if fails > 0 {
		t.Fatalf("response review flagged %d tool(s); see %s", fails, dumpDir)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return strings.TrimSpace(s)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func sanitizeFile(s string) string {
	rep := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return rep.Replace(s)
}

func semanticResponseIssue(tool, body, repoName, repoRoot string) string {
	var v any
	if json.Unmarshal([]byte(body), &v) != nil {
		return ""
	}
	if path := firstNullJSONPath(v, "$"); path != "" {
		return "JSON null value at " + path + " (use [] or omit with a useful note)"
	}
	obj, _ := v.(map[string]any)
	switch tool {
	case "agent_memory":
		arr, ok := obj["relevant_memory"].([]any)
		if !ok {
			break
		}
		if len(arr) == 0 && strings.TrimSpace(asString(obj["note"])) == "" {
			return "empty relevant_memory needs a note"
		}
	case "select_pattern":
		matched, _ := obj["matched"].(bool)
		patternID := strings.TrimSpace(asString(obj["pattern_id"]))
		if matched && patternID == "" {
			return "matched pattern must include pattern_id"
		}
		if !matched && strings.TrimSpace(asString(obj["note"])) == "" {
			return "unmatched pattern response needs a note"
		}
		if _, ok := obj["triggers"].([]any); !ok {
			return "triggers must be a JSON array"
		}
	case "expand_request":
		if asString(obj["feature_type"]) == "general" && strings.Contains(strings.ToLower(body), "authentication") {
			return "auth request was classified as generic"
		}
	case "cypher":
		if _, ok := obj["results"].([]any); !ok {
			return "cypher DSL response must include results array"
		}
	case "context":
		bundle, _ := obj["bundle"].(map[string]any)
		for _, k := range []string{"callers", "callees", "imports"} {
			if _, ok := bundle[k].([]any); !ok {
				return "context bundle " + k + " must be a JSON array"
			}
		}
	case "context_pack":
		pack, _ := obj["budgeted"].(map[string]any)
		if pack == nil {
			break
		}
		for _, bucket := range []string{"must_include", "should_include", "summarize_only", "exclude"} {
			items, ok := pack[bucket].([]any)
			if !ok {
				continue
			}
			seen := map[string]struct{}{}
			for _, it := range items {
				m, _ := it.(map[string]any)
				p := asString(m["path"])
				if p == "" {
					continue
				}
				if _, dup := seen[p]; dup {
					return "budgeted." + bucket + " contains duplicate path " + p
				}
				seen[p] = struct{}{}
			}
		}
	case "project_context":
		if obj == nil {
			return "project_context response must be an object"
		}
		if asString(obj["repo"]) != repoName {
			return "project_context.repo mismatch"
		}
		if asString(obj["repo_root"]) == "" {
			return "project_context missing repo_root"
		}
	}
	return ""
}

func firstNullJSONPath(v any, path string) string {
	switch x := v.(type) {
	case nil:
		return path
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if p := firstNullJSONPath(x[k], path+"."+k); p != "" {
				return p
			}
		}
	case []any:
		for i, item := range x {
			if p := firstNullJSONPath(item, fmt.Sprintf("%s[%d]", path, i)); p != "" {
				return p
			}
		}
	}
	return ""
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
