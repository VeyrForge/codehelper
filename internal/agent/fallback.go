package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func asStringArray(value any) []string {
	arr, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func contextPackItemsFromRaw(parsed map[string]any) []map[string]any {
	if parsed == nil {
		return nil
	}
	root, ok := parsed["context_pack"].(map[string]any)
	if !ok {
		return nil
	}
	cp, ok := root["context_pack"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(cp))
	for _, v := range cp {
		if rec, isObj := v.(map[string]any); isObj {
			out = append(out, rec)
		}
	}
	return out
}

func topDir(pathLike string) string {
	rel := strings.TrimLeft(strings.ReplaceAll(pathLike, `\`, "/"), "/")
	if rel == "" {
		return ""
	}
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
}

type fallbackCaller struct {
	tools ToolCaller
}

func (f fallbackCaller) call(ctx context.Context, name string, args map[string]any) map[string]any {
	raw, err := f.tools.Call(ctx, name, args)
	if err != nil {
		return nil
	}
	if !mcpToolOutputSucceeded(raw) {
		return nil
	}
	return parseJSONObjectCandidate(raw)
}

// buildDeterministicOverviewFallback recovers a grounded project overview
// from MCP evidence when the model produced malformed output.
func buildDeterministicOverviewFallback(ctx context.Context, tools ToolCaller, qCore, model string, log func(string)) string {
	f := fallbackCaller{tools: tools}

	pc := f.call(ctx, "project_context", map[string]any{})
	repo := ""
	if pc != nil {
		repo, _ = pc["repo"].(string)
	}

	cpArgs := map[string]any{
		"query":                strings.TrimSpace(qCore),
		"intent":               "explore",
		"include_context_pack": true,
		"limit":                28,
	}
	if cpArgs["query"] == "" {
		cpArgs["query"] = "project overview architecture"
	}
	if repo != "" {
		cpArgs["repo"] = repo
	}
	cp := f.call(ctx, "query", cpArgs)

	q1 := map[string]any{"query": "mcp register tools", "intent": "explore"}
	q2 := map[string]any{"query": "vscode extension chat agent", "intent": "explore"}
	if repo != "" {
		q1["repo"] = repo
		q2["repo"] = repo
	}
	qres1 := f.call(ctx, "query", q1)
	qres2 := f.call(ctx, "query", q2)

	var ls map[string]any
	if tools.WorkspaceToolsAvailable() {
		lsArgs := map[string]any{"path": "."}
		if repo != "" {
			lsArgs["repo"] = repo
		}
		ls = f.call(ctx, "list_workspace_directory", lsArgs)
	}

	var keyEntrypoints, likelyEntrypoints, topDirsFromProject, topFilesFromProject []string
	if pc != nil {
		keyEntrypoints = asStringArray(pc["key_entrypoints"])
		likelyEntrypoints = asStringArray(pc["likely_entrypoint_files"])
		topDirsFromProject = asStringArray(pc["top_level_directories"])
		topFilesFromProject = asStringArray(pc["top_level_files"])
	}

	cpItems := contextPackItemsFromRaw(cp)
	if len(cpItems) > 14 {
		cpItems = cpItems[:14]
	}
	cpPathCounts := map[string]int{}
	for _, row := range cpItems {
		p, _ := row["path"].(string)
		if d := topDir(p); d != "" {
			cpPathCounts[d]++
		}
	}
	hotDirs := make([]string, 0, len(cpPathCounts))
	for d := range cpPathCounts {
		hotDirs = append(hotDirs, d)
	}
	sort.Slice(hotDirs, func(i, j int) bool {
		if cpPathCounts[hotDirs[i]] != cpPathCounts[hotDirs[j]] {
			return cpPathCounts[hotDirs[i]] > cpPathCounts[hotDirs[j]]
		}
		return hotDirs[i] < hotDirs[j]
	})
	if len(hotDirs) > 8 {
		hotDirs = hotDirs[:8]
	}

	var lsDirs, lsFiles []string
	if ls != nil {
		if entries, isArr := ls["entries"].([]any); isArr {
			for _, v := range entries {
				e, isObj := v.(map[string]any)
				if !isObj {
					continue
				}
				name, _ := e["name"].(string)
				if name == "" {
					continue
				}
				if isDir, _ := e["is_dir"].(bool); isDir {
					if len(lsDirs) < 12 {
						lsDirs = append(lsDirs, name)
					}
				} else if len(lsFiles) < 12 {
					lsFiles = append(lsFiles, name)
				}
			}
		}
	}

	codeQuote := func(items []string, cap int) string {
		if len(items) > cap {
			items = items[:cap]
		}
		quoted := make([]string, 0, len(items))
		for _, s := range items {
			quoted = append(quoted, "`"+s+"`")
		}
		return strings.Join(quoted, ", ")
	}
	uniq := func(arrs ...[]string) []string {
		seen := map[string]bool{}
		var out []string
		for _, arr := range arrs {
			for _, s := range arr {
				if s == "" || seen[s] {
					continue
				}
				seen[s] = true
				out = append(out, s)
			}
		}
		return out
	}

	var lines []string
	lines = append(lines, "## Project Overview", "")
	lines = append(lines, "- Recovered via deterministic MCP evidence because the model produced malformed tool-call output.")
	if pc != nil {
		get := func(k string) string {
			if s, isStr := pc[k].(string); isStr && s != "" {
				return s
			}
			return "(unknown)"
		}
		lines = append(lines,
			fmt.Sprintf("- Repository: `%s`", get("repo")),
			fmt.Sprintf("- Root: `%s`", get("repo_root")),
			fmt.Sprintf("- Project type: `%s`", get("project_type")),
			fmt.Sprintf("- Index status: `%s`", get("index_status")),
		)
	}
	lines = append(lines, "", "### Layout")
	dirs := uniq(topDirsFromProject, lsDirs)
	files := uniq(topFilesFromProject, lsFiles)
	if len(dirs) > 0 {
		lines = append(lines, "- Top directories: "+codeQuote(dirs, 14))
	}
	if len(files) > 0 {
		lines = append(lines, "- Top files: "+codeQuote(files, 14))
	}
	if eps := uniq(keyEntrypoints, likelyEntrypoints); len(eps) > 0 {
		lines = append(lines, "- Entrypoints/manifests: "+codeQuote(eps, 14))
	}
	lines = append(lines, "", "### Core Subsystems (from indexed context)")
	if len(hotDirs) > 0 {
		lines = append(lines, "- Most referenced areas: "+codeQuote(hotDirs, 8))
	}
	if len(cpItems) > 0 {
		for _, row := range cpItems {
			p, _ := row["path"].(string)
			if p == "" {
				p = "(path)"
			}
			sym, _ := row["symbol"].(string)
			kind, _ := row["kind"].(string)
			reason, _ := row["reason"].(string)
			label := sym
			if kind != "" {
				label = strings.TrimSpace(label + " (" + kind + ")")
			}
			why := ""
			if reason != "" {
				why = " - " + reason
			}
			entry := "- `" + p + "`"
			if label != "" {
				entry += " -> " + label
			}
			lines = append(lines, entry+why)
		}
	} else {
		lines = append(lines, "- Context pack returned no symbol rows; check index freshness and query seeds.")
	}

	hitCount := func(res map[string]any) int {
		if res == nil {
			return 0
		}
		if hits, isArr := res["hits"].([]any); isArr {
			return len(hits)
		}
		return 0
	}
	lines = append(lines, "", "### Retrieval Health")
	lines = append(lines, fmt.Sprintf("- Query coverage: `mcp register tools` => %d hits, `vscode extension chat agent` => %d hits.", hitCount(qres1), hitCount(qres2)))
	if pc != nil {
		if fres, isObj := pc["freshness"].(map[string]any); isObj {
			if stale, _ := fres["stale"].(bool); stale {
				lines = append(lines, "- Index appears stale; run `codehelper analyze` before trusting architectural detail.")
			}
			if ar, isObj := fres["action_required"].(map[string]any); isObj {
				if msg, isStr := ar["message"].(string); isStr && strings.TrimSpace(msg) != "" {
					lines = append(lines, "- Index action required: "+msg)
				}
			}
		}
	}

	lines = append(lines, "", "### Notes")
	lines = append(lines, "- This overview is grounded in MCP index and workspace evidence, not generic template text.")
	if isLikelyQwenModel(model) {
		lines = append(lines, "- Qwen + vLLM hint: enable parser/template flags for reliable tool calls (`--enable-auto-tool-choice --tool-call-parser hermes` for Qwen2.5, and verify chat template/tool-role support).")
	}
	lines = append(lines, "- Ask a follow-up like `explain internal/mcpsvc/register.go` or `map vscode-extension/src/* flow` for a deeper walkthrough.")
	log("[LLM] used deterministic project-overview fallback")
	return strings.Join(lines, "\n")
}

// buildPrefetchedBroadAskEvidence pre-runs lightweight MCP calls before the
// first model round to improve overview quality on weaker tool-calling models.
func buildPrefetchedBroadAskEvidence(ctx context.Context, tools ToolCaller, qCore, model string, log func(string)) string {
	f := fallbackCaller{tools: tools}

	pc := f.call(ctx, "project_context", map[string]any{})
	repo := ""
	if pc != nil {
		repo, _ = pc["repo"].(string)
	}
	cpArgs := map[string]any{
		"query":                strings.TrimSpace(qCore),
		"intent":               "explore",
		"include_context_pack": true,
		"limit":                18,
	}
	if cpArgs["query"] == "" {
		cpArgs["query"] = "project overview architecture"
	}
	if repo != "" {
		cpArgs["repo"] = repo
	}
	cp := f.call(ctx, "query", cpArgs)
	items := contextPackItemsFromRaw(cp)
	if len(items) > 8 {
		items = items[:8]
	}
	if pc == nil && len(items) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "Host prefetched MCP evidence for this broad project question:")
	if pc != nil {
		repoName := "(unknown)"
		if s, isStr := pc["repo"].(string); isStr && s != "" {
			repoName = s
		}
		projectType := "(unknown)"
		if s, isStr := pc["project_type"].(string); isStr && s != "" {
			projectType = s
		}
		lines = append(lines, "- repo: "+repoName, "- project_type: "+projectType)
		entrypoints := asStringArray(pc["key_entrypoints"])
		if len(entrypoints) > 6 {
			entrypoints = entrypoints[:6]
		}
		if len(entrypoints) > 0 {
			lines = append(lines, "- key_entrypoints: "+strings.Join(entrypoints, ", "))
		}
	}
	if len(items) > 0 {
		var hits []string
		for _, it := range items {
			p, _ := it["path"].(string)
			s, _ := it["symbol"].(string)
			h := p
			if s != "" {
				h = p + "#" + s
			}
			if h != "" {
				hits = append(hits, h)
			}
		}
		if len(hits) > 8 {
			hits = hits[:8]
		}
		if len(hits) > 0 {
			lines = append(lines, "- ranked_hits: "+strings.Join(hits, "; "))
		}
	}
	if isLikelyQwenModel(model) {
		lines = append(lines, "- qwen_tool_calling_note: if tool calls degrade into plain JSON text, recover by parsing embedded tool JSON and continue.")
	}
	log("[LLM] injected prefetched broad-ask evidence")
	return strings.Join(lines, "\n")
}
