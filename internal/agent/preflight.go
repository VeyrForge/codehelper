package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

// PreflightContext runs orchestrator-enforced context gathering before todo execution.
func PreflightContext(ctx context.Context, tools ToolCaller, root string, task *taskstore.Task, td *taskstore.Todo) (string, error) {
	if tools == nil {
		return "", fmt.Errorf("tools caller required")
	}
	fr := freshness.Inspect(root)
	if fr.Stale {
		return "", fmt.Errorf("index stale: %s — run codehelper analyze", fr.StaleReason)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Preflight context (orchestrator-enforced):\n")
	fmt.Fprintf(&b, "- Index freshness: ok\n")

	query := strings.TrimSpace(task.UserRequest)
	if len(td.Files) > 0 {
		query = strings.Join(td.Files, " ")
	}
	if query != "" {
		out, err := tools.Call(ctx, "query", map[string]any{"query": query, "limit": float64(8)})
		if err == nil && strings.TrimSpace(out) != "" {
			snippet := out
			if len(snippet) > 4000 {
				snippet = snippet[:4000] + "…"
			}
			fmt.Fprintf(&b, "\nquery(%q):\n%s\n", query, snippet)
		}
	}
	if len(td.ReuseSymbols) > 0 {
		name := td.ReuseSymbols[0]
		out, err := tools.Call(ctx, "context", map[string]any{"name": name})
		if err == nil && strings.TrimSpace(out) != "" {
			snippet := out
			if len(snippet) > 3000 {
				snippet = snippet[:3000] + "…"
			}
			fmt.Fprintf(&b, "\ncontext(%q):\n%s\n", name, snippet)
		}
		if len(td.ReuseSymbols) > 0 {
			imp, err := tools.Call(ctx, "impact", map[string]any{"target": name, "depth": float64(2)})
			if err == nil && strings.TrimSpace(imp) != "" {
				snippet := imp
				if len(snippet) > 2000 {
					snippet = snippet[:2000] + "…"
				}
				fmt.Fprintf(&b, "\nimpact(%q):\n%s\n", name, snippet)
			}
		}
	}
	return b.String(), nil
}
