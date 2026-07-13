package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/orchestrator"
	"github.com/VeyrForge/codehelper/internal/registry"
)

// Anchor holds project-specific symbols discovered for task generation.
type Anchor struct {
	ProjectType string
	Summary     string
	Symbols     []string
}

func discoverAnchor(ctx context.Context, inv *orchestrator.MeteredInvoker, repo registry.Entry) Anchor {
	a := Anchor{}
	raw, err := inv.Call(ctx, "project_context", map[string]any{"verbosity": "short", "format": "json"})
	if err == nil {
		var pc map[string]any
		if json.Unmarshal([]byte(raw), &pc) == nil {
			a.ProjectType, _ = pc["project_type"].(string)
			a.Summary, _ = pc["summary"].(string)
		}
	}
	queries := anchorQueries(repo.Name, a.ProjectType, raw)
	for _, q := range queries {
		if strings.TrimSpace(q) == "" {
			continue
		}
		qraw, err := inv.Call(ctx, "query", map[string]any{"query": q, "top_k": 8, "format": "json"})
		if err != nil {
			continue
		}
		if syms := symbolsFromQuery(qraw); len(syms) > 0 {
			a.Symbols = syms
			break
		}
	}
	return a
}

func anchorQueries(repoName, projectType, projectContextRaw string) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(existing, s) {
				return
			}
		}
		out = append(out, s)
	}
	add(repoName)
	if projectType != "" {
		add(projectType + " handler")
		add(projectType + " main")
	}
	add("main entry run")
	add("handler serve")
	var pc map[string]any
	if json.Unmarshal([]byte(projectContextRaw), &pc) == nil {
		if eps, ok := pc["key_entrypoints"].([]any); ok {
			for _, ep := range eps {
				if s, ok := ep.(string); ok {
					stem := strings.TrimSuffix(filepath.Base(s), filepath.Ext(s))
					add(stem)
				}
			}
		}
		if lang, _ := pc["primary_language"].(string); lang != "" {
			add(lang + " entrypoint")
		}
	}
	return out
}

func symbolsFromQuery(raw string) []string {
	var p map[string]any
	if json.Unmarshal([]byte(raw), &p) != nil {
		return nil
	}
	hits, _ := p["hits"].([]any)
	var out []string
	for _, h := range hits {
		m, _ := h.(map[string]any)
		if n, _ := m["name"].(string); n != "" && isGoodAnchor(n) {
			out = append(out, n)
		}
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func isGoodAnchor(name string) bool {
	if len(name) < 2 || len(name) > 48 {
		return false
	}
	if strings.ContainsAny(name, `[]"'(){}:;`) {
		return false
	}
	// Skip ultra-generic runtime names that appear everywhere.
	switch strings.ToLower(name) {
	case "main", "init", "run", "new", "default", "setup", "test":
		return false
	}
	return true
}

func tasksForProject(a Anchor) []Task {
	sym := pickAnchor(a.Symbols, "main")
	sym2 := pickSecondAnchor(a.Symbols, sym)
	pt := a.ProjectType
	if pt == "" {
		pt = "this project"
	}
	return []Task{
		{
			Name: "explain", Kind: "explain",
			Task:        fmt.Sprintf("how does %s work in this %s codebase", sym, pt),
			MustContain: []string{strings.ToLower(sym)},
		},
		{
			Name: "new_feature", Kind: "feature",
			Task:        fmt.Sprintf("add better error handling and validation around %s", sym),
			MustContain: []string{strings.ToLower(sym)},
		},
		{
			Name: "debug_flow", Kind: "bugfix",
			Task:        fmt.Sprintf("fix bug: %s fails or returns wrong result under edge cases", sym),
			MustContain: []string{strings.ToLower(sym)},
		},
		{
			Name: "refactor_impact", Kind: "refactor",
			Task:        fmt.Sprintf("refactor %s — what breaks and which tests to run", sym2),
			MustContain: []string{strings.ToLower(sym2)},
		},
		{
			Name: "dead_code_probe", Kind: "dead_code",
			Task:        fmt.Sprintf("find dead unreferenced symbols related to %s", sym),
			MustContain: []string{strings.ToLower(sym)},
		},
	}
}

func pickAnchor(symbols []string, fallback string) string {
	for _, s := range symbols {
		if isGoodAnchor(s) {
			return s
		}
	}
	return fallback
}

// pickSecondAnchor returns a distinct symbol for refactor tasks, or reuses first
// when the index returned fewer than two anchors (empty/small repos).
func pickSecondAnchor(symbols []string, first string) string {
	for _, s := range symbols {
		if isGoodAnchor(s) && !strings.EqualFold(s, first) {
			return s
		}
	}
	return first
}

func manualChainForKind(kind string) []string {
	switch kind {
	case "bugfix":
		return []string{"project_context", "query", "context", "impact", "test_impact"}
	case "feature":
		return []string{"kickoff"}
	case "explain":
		return []string{"query", "context"}
	case "refactor":
		return []string{"query", "context", "impact", "test_impact"}
	case "dead_code":
		return []string{"project_context", "dead_code", "scout", "query", "context"}
	default:
		return []string{"query", "context"}
	}
}

func runToolChain(ctx context.Context, inv *orchestrator.MeteredInvoker, h map[string]func(context.Context, map[string]any) (string, error), repo, task string, chain []string, format string) (string, []string) {
	if format == "" {
		format = "toon"
	}
	var parts []string
	var tools []string
	for _, tool := range chain {
		args := map[string]any{"repo": repo, "format": format}
		switch tool {
		case "kickoff":
			args["task"] = task
			args["sections"] = "orient,reuse,steps,verify"
		case "scout", "query":
			args["query"] = task
			if tool == "query" {
				args["top_k"] = 8
			}
			if tool == "scout" {
				args["limit"] = 8
			}
		case "project_context":
			args["verbosity"] = "short"
		case "dead_code":
			args["limit"] = 20
		case "context", "impact", "test_impact":
			qraw, _ := inv.Call(ctx, "query", map[string]any{"repo": repo, "query": task, "top_k": 3, "format": format})
			parts = append(parts, qraw)
			if sym := firstSymbolFromPayload(qraw, format); sym != "" {
				switch tool {
				case "context":
					args["name"] = sym
					args["body"] = "brief"
				default:
					args["target"] = sym
				}
			}
		}
		raw, err := inv.Call(ctx, tool, args)
		tools = append(tools, tool)
		if err != nil {
			parts = append(parts, err.Error())
			continue
		}
		parts = append(parts, raw)
	}
	return strings.Join(parts, "\n"), tools
}

func runBaselineChain(ctx context.Context, inv *orchestrator.MeteredInvoker, h map[string]func(context.Context, map[string]any) (string, error), repo registry.Entry, task string) (string, []string) {
	// Simulates an agent WITHOUT codehelper MCP: shallow listing + reading common entry files.
	var parts []string
	var tools []string
	args := map[string]any{"repo": repo.Name, "path": "."}
	if raw, err := inv.Call(ctx, "list_workspace_directory", args); err == nil {
		parts = append(parts, raw)
		tools = append(tools, "list_workspace_directory")
	}
	for _, rel := range baselineReadCandidates(repo.RootPath) {
		raw, err := inv.Call(ctx, "read_workspace_file", map[string]any{
			"repo": repo.Name, "path": rel, "max_bytes": 65536,
		})
		tools = append(tools, "read_workspace_file:"+rel)
		if err != nil {
			continue
		}
		parts = append(parts, raw)
	}
	// No query/context — agent would grep blindly; we record the task text only.
	parts = append(parts, "task_without_graph:"+task)
	return strings.Join(parts, "\n"), tools
}

func baselineReadCandidates(root string) []string {
	candidates := []string{
		"README.md", "readme.md", "go.mod", "package.json", "Cargo.toml",
		"pyproject.toml", "composer.json", "index.ts", "main.go", "src/main.go",
		"cmd/main.go", "app/main.go",
	}
	var out []string
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(root, c)); err == nil {
			out = append(out, c)
		}
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func firstSymbol(raw string) string {
	return firstSymbolFromPayload(raw, "json")
}

func firstSymbolFromPayload(raw, format string) string {
	if strings.EqualFold(format, "toon") {
		if sym := firstSymbolFromTOON(raw); sym != "" {
			return sym
		}
	}
	var p map[string]any
	if json.Unmarshal([]byte(raw), &p) != nil {
		return ""
	}
	hits, _ := p["hits"].([]any)
	if len(hits) == 0 {
		return ""
	}
	h0, _ := hits[0].(map[string]any)
	if id, _ := h0["id"].(string); id != "" && strings.HasPrefix(id, "sym:") {
		return id
	}
	s, _ := h0["name"].(string)
	return s
}

func firstSymbolFromTOON(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		for _, part := range strings.FieldsFunc(strings.TrimSpace(line), func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			if strings.HasPrefix(part, "sym:") {
				return part
			}
		}
	}
	return ""
}
