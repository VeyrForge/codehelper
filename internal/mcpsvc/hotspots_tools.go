package mcpsvc

import (
	"context"
	"fmt"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/hotspots"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- hotspots --------------------------------------------------------------

type hotspotRow struct {
	File       string  `json:"file"`
	Commits    int     `json:"commits"`
	Centrality int     `json:"centrality"`
	Score      float64 `json:"score"`
}

type hotspotsResponse struct {
	Hotspots  []hotspotRow `json:"hotspots"`
	Window    int          `json:"commits_scanned"`
	Freshness string       `json:"freshness,omitempty"`
	Note      string       `json:"note"`
}

const (
	hotspotsMaxRows       = 20
	hotspotsDefaultWindow = 1500
)

// hotspotsHandler ranks files by architectural risk = git churn × call-graph
// centrality. It fuses two signals the other tools already expose separately —
// how often a file changes (git history) and how load-bearing its symbols are
// (inbound call edges, the same centrality `query`/`scout` rank by) — into the
// "where is refactoring most valuable / where do bugs hurt most" view. Pure
// deterministic ranking (internal/hotspots) over data read from git + the graph;
// no model. Best-effort on git history (shallow clone / non-repo → empty).
func hotspotsHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		window := int(mcp.ParseInt64(req, "commits", 0))
		if window <= 0 {
			window = hotspotsDefaultWindow
		}
		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = hotspotsMaxRows
		}

		// Churn: commits touching each file in the window (git history).
		commits, _ := gitutil.LogNameOnly(repo.RootPath, window)
		churn := hotspots.ChurnFromCommits(commits)

		// Centrality: sum inbound "calls" edges over the symbols each file defines.
		// One whole-repo InDegrees scan + one symbol enumeration, then aggregate by
		// path — O(symbols+edges), not a per-file round-trip.
		indeg, err := st.InDegrees(ctx, repo.Name, "calls")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		syms, err := st.SymbolsByPathPrefix(ctx, repo.Name, "", 1_000_000)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		centrality := map[string]int{}
		for _, s := range syms {
			if review.IsTestPath(s.Path) || review.IsSecondaryNoisePath(s.Path) {
				continue // hotspots target production code, not tests/demos/fixtures
			}
			if d := indeg[s.ID]; d > 0 {
				centrality[s.Path] += d
			}
		}

		ranked := hotspots.Rank(churn, centrality, topK)
		out := hotspotsResponse{Window: len(commits)}
		for _, r := range ranked {
			out.Hotspots = append(out.Hotspots, hotspotRow{
				File: r.File, Commits: r.Commits, Centrality: r.Centrality, Score: round3(r.Score),
			})
		}

		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — centrality reflects the last analyze; re-run for current ranking"
		}
		switch {
		case len(commits) == 0:
			out.Note = "no git history readable (not a git repo, or a shallow clone) — churn is unavailable, so hotspots can't be computed. The centrality half is still in `query`/`scout` ranking."
		case len(out.Hotspots) == 0:
			out.Note = "no file scored on both axes — either nothing churned also has inbound call edges, or the call graph is empty (run `codehelper analyze --force`)."
		default:
			out.Note = fmt.Sprintf("Files ranked by churn × centrality over the last %d commits: changed often AND depended on heavily = highest refactor value / defect risk. Inspect the top rows with `context` or `change_kit` before refactoring; `impact` shows their blast radius.", len(commits))
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}
