package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/mark3labs/mcp-go/mcp"
)

// symbolMissGuidance turns a context/impact resolve miss into an agent-usable
// payload (disk matches, healthish → change_kit, similar names) instead of a
// bare "symbol not found" that stalls the next tool call.
func symbolMissGuidance(ctx context.Context, st *graph.Store, root, repoName, tool, name, format string) *mcp.CallToolResult {
	name = strings.TrimSpace(name)
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "context"
	}

	// Bare health/ready vibes: change_kit owns on-disk /health|/ready + placement.
	// context/impact must not pretend the graph has a symbol named "health".
	if isBareHealthishTarget(name) || (isHealthishTarget(name) && !strings.HasPrefix(name, "sym:")) {
		out := withRecoveryFields(map[string]any{
			"found": false,
			"name":  name,
			"note": fmt.Sprintf(
				"%q is a health/ready vibe target — %s will not resolve it reliably from the graph. "+
					"Call `change_kit target=%s` (resolves on-disk /health|/ready or HTTP placement, or ABSTAINs honestly).",
				name, tool, name,
			),
			"what_next":              fmt.Sprintf("change_kit target=%s — then diagnostics after patch", name),
			"recommended_next_tools": []string{"change_kit", "kickoff", "query"},
			"next_queries": []string{
				fmt.Sprintf("change_kit target=%s", name),
				"kickoff role=feature task=\"add health check\" sections=orient,reuse",
				"query: health ready healthz route",
			},
			"freshness": freshness.Inspect(root),
		}, ErrCategoryNotFound, RecoveryTryAlternative, true)
		res, _ := mustToolResultFormatted(out, format)
		return res
	}

	if dm := retrieval.DiskGrepIdentifier(root, name, 5); len(dm) > 0 {
		out := withRecoveryFields(map[string]any{
			"found":        false,
			"name":         name,
			"disk_matches": dm,
			"freshness":    freshness.Inspect(root),
			"note": fmt.Sprintf(
				"%q is not in the symbol index but EXISTS on disk (likely a new/uncommitted file the index hasn't caught up with). "+
					"Open it with `read_workspace_file path=%s`, or run `codehelper analyze --force` to index it. "+
					"If you meant a health/ready route, prefer `change_kit target=health`.",
				name, dm[0].Path,
			),
			"what_next":              fmt.Sprintf("read_workspace_file path=%s — or change_kit if editing a route; refresh index with analyze --force", dm[0].Path),
			"recommended_next_tools": []string{"read_workspace_file", "change_kit", "query"},
			"suggested_fix":         "codehelper analyze --force",
			"next_queries": []string{
				fmt.Sprintf("read_workspace_file path=%s", dm[0].Path),
				"codehelper analyze --force",
				fmt.Sprintf("query: %s", name),
			},
		}, ErrCategoryStaleIndex, RecoveryRefreshIndex, true)
		res, _ := mustToolResultFormatted(out, format)
		return res
	}

	fresh := freshness.Inspect(root)
	hint := fmt.Sprintf(
		"no indexed symbol named %q (and no on-disk match found). Next: call `query` with %q to find the correct name/sym: id, or `ast_query` for a structural match. If you expect it to exist, the index may be stale — run `codehelper analyze --force`.",
		name, name,
	)
	closeNames := []string{}
	if st != nil {
		if sugg := suggestSimilarSymbols(ctx, st, repoName, name, 5); len(sugg) > 0 {
			closeNames = sugg
			hint += " Close indexed names: " + strings.Join(sugg, ", ") + "."
		}
	}
	rec := agentRecovery{
		ErrorCategory: ErrCategoryNotFound,
		IsRetryable:   true,
		RecoveryHint:  RecoveryTryAlternative,
		Message:       hint,
		Expected:      "exact indexed symbol name or sym: id from query",
		Example:       map[string]any{"name": name, "note": "prefer query first, then context name=<exact>"},
		WhatNext:      fmt.Sprintf("query: %s — then context name=<exact hit> or change_kit target=<exact>", name),
		RecommendedNextTools: []string{"query", "ast_query", "kickoff"},
		NextQueries: []string{
			fmt.Sprintf("query: %s", name),
			"ast_query pattern for the construct you expected",
			"codehelper analyze --force  # if the symbol should already exist",
		},
	}
	// Mid-session edits without re-analyze often look like hard misses — prefer
	// REFRESH_INDEX when freshness already says the working tree moved.
	if fresh.Stale {
		rec.ErrorCategory = ErrCategoryStaleIndex
		rec.RecoveryHint = RecoveryRefreshIndex
		rec.WhatNext = "codehelper analyze --force — then retry " + tool + " with the same name/sym: id"
		rec.RecommendedNextTools = []string{"project_context", "query", tool}
		if fresh.SuggestedFix != "" {
			rec.Message += " Freshness: " + fresh.StaleReason + " — " + fresh.SuggestedFix
		} else {
			rec.Message += " Freshness: " + fresh.StaleReason + " — run codehelper analyze --force"
		}
	}
	if len(closeNames) > 0 && rec.RecoveryHint != RecoveryRefreshIndex {
		rec.RecoveryHint = RecoveryDisambiguate
		rec.Example["close_names"] = closeNames
	}
	return toolResultRecoveryError(rec)
}

// isSymbolNotFound reports the resolveSymbolByName hard-miss error form.
func isSymbolNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "symbol not found:") ||
		strings.Contains(msg, "no symbol named") ||
		strings.Contains(msg, "no symbol with id")
}
