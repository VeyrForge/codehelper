package mcpsvc

import (
	"context"
	"sort"
	"strconv"

	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- since -----------------------------------------------------------------
//
// `since` is the write-side, post-edit companion to `scout`: one call that
// answers "I changed things since <base_ref> — what did I touch, what does it
// affect, and what must I re-run?" It fuses detect_changes (changed symbols) +
// impact (downstream blast radius) + test_impact (reverse-closure test
// selection) so the agent doesn't spend three round-trips (and three graph
// opens) assembling the same picture before running the suite.

type sinceResponse struct {
	BaseRef          string       `json:"base_ref"`
	ChangedCount     int          `json:"changed_count"`
	ChangedSymbolIDs []string     `json:"changed_symbol_ids,omitempty"`
	RiskTier         string       `json:"risk_tier"`
	Dependents       int          `json:"dependents"`
	MustUpdate       []compactSym `json:"must_update,omitempty"`
	MustUpdateMore   int          `json:"must_update_more,omitempty"`
	TestFiles        []string     `json:"test_files,omitempty"`
	TestCount        int          `json:"test_count"`
	UntrackedFiles   []string     `json:"untracked_source_files,omitempty"`
	Freshness        string       `json:"freshness,omitempty"`
	Note             string       `json:"note,omitempty"`
	NextStep         string       `json:"next_step,omitempty"`
}

const (
	// maxSinceSeeds bounds how many changed symbols we run blast-radius/test
	// analysis over. A huge diff would otherwise fan out into thousands of graph
	// walks; past this the risk/test picture is already saturated.
	maxSinceSeeds = 60
	// maxSinceMustUpdate caps the must-update list returned (the rest are counted).
	maxSinceMustUpdate = 12
)

func sinceHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		base := argString(args, "base_ref")
		if base == "" {
			base = "HEAD~1"
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		testDepth := int(mcp.ParseInt64(req, "test_depth", 0))
		if testDepth <= 0 {
			testDepth = 6 // tests sit several hops above a changed leaf
		}
		impactDepth := int(mcp.ParseInt64(req, "impact_depth", 0))
		if impactDepth <= 0 {
			impactDepth = 2
		}

		seeds, derr := detect.ChangedSymbols(ctx, repo.RootPath, repo.Name, base, st)
		if derr != nil {
			return mcp.NewToolResultError(derr.Error()), nil
		}

		out := sinceResponse{
			BaseRef:          base,
			ChangedCount:     len(seeds),
			ChangedSymbolIDs: seeds,
			RiskTier:         "low",
		}
		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — re-run analyze for accurate impact/test selection"
		}
		// New untracked source files are invisible to git diff AND the index, so the
		// agent must be told they exist — same honesty contract as detect_changes.
		if untracked, uerr := gitutil.UntrackedFiles(repo.RootPath); uerr == nil {
			out.UntrackedFiles = filterSourceFiles(untracked)
		}

		if len(seeds) == 0 {
			out.Note = "no changed symbols vs " + base + " (uncommitted edits to tracked files are included). If you added new files, they may be untracked/unindexed — see untracked_source_files."
			out.NextStep = "nothing changed to analyze; if you expected changes, check base_ref or run `codehelper analyze`."
			return mustToolResultFormatted(out, resolveFormat(args))
		}

		analyzed := seeds
		if len(analyzed) > maxSinceSeeds {
			analyzed = analyzed[:maxSinceSeeds]
			out.Note = "diff is large; blast-radius and test selection cover the first " + strconv.Itoa(maxSinceSeeds) + " changed symbols."
		}

		dependents := map[string]struct{}{}         // distinct downstream symbols
		mustUpdate := map[string]types.ImpactNode{} // dedup by symbol id
		testNodes := map[string]types.ImpactNode{}  // dedup test symbols
		for _, seed := range analyzed {
			// Downstream: who breaks if this change is wrong.
			if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, seed, impactDepth, "downstream"); aerr == nil && res != nil {
				out.RiskTier = worseRisk(out.RiskTier, res.RiskTier)
				for _, n := range res.Nodes {
					if n.Depth == 0 {
						continue
					}
					dependents[n.SymbolID] = struct{}{}
				}
				for _, n := range res.MustUpdateCandidates {
					mustUpdate[n.SymbolID] = n
				}
			}
			// Upstream: the tests that transitively reach this change.
			if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, seed, testDepth, "upstream"); aerr == nil && res != nil {
				for _, n := range res.Nodes {
					if n.Depth == 0 {
						continue
					}
					if review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
						testNodes[n.SymbolID] = n
					}
				}
			}
		}
		out.Dependents = len(dependents)

		// Must-update: shallowest first (closest to the change = most likely to need edits).
		muNodes := make([]types.ImpactNode, 0, len(mustUpdate))
		for _, n := range mustUpdate {
			muNodes = append(muNodes, n)
		}
		sort.Slice(muNodes, func(i, j int) bool {
			if muNodes[i].Depth != muNodes[j].Depth {
				return muNodes[i].Depth < muNodes[j].Depth
			}
			return muNodes[i].Path < muNodes[j].Path
		})
		for _, n := range muNodes {
			if len(out.MustUpdate) >= maxSinceMustUpdate {
				out.MustUpdateMore++
				continue
			}
			out.MustUpdate = append(out.MustUpdate, compactSym{Name: n.Name, Kind: n.Kind, Loc: locOf(n.Path, n.SymbolID)})
		}

		// Test files: distinct paths, sorted. You run FILES, so the file list is the
		// actionable output; test_count is the number of covering test symbols.
		fileSet := map[string]struct{}{}
		for _, n := range testNodes {
			fileSet[n.Path] = struct{}{}
		}
		for f := range fileSet {
			out.TestFiles = append(out.TestFiles, f)
		}
		sort.Strings(out.TestFiles)
		out.TestCount = len(testNodes)

		out.NextStep = sinceNextStep(out)
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// worseRisk returns the higher of two risk tiers (low < medium < high).
func worseRisk(a, b string) string {
	if riskRank(b) > riskRank(a) {
		return b
	}
	return a
}

func riskRank(tier string) int {
	switch tier {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// sinceNextStep points the agent at the cheapest correct follow-up given what the
// change actually touched, instead of a static string.
func sinceNextStep(out sinceResponse) string {
	if len(out.TestFiles) == 0 {
		return "no covering tests reach these changes — consider adding one, then run the suite. Use `change_kit` on a changed symbol before further edits."
	}
	step := "run the " + strconv.Itoa(len(out.TestFiles)) + " test file(s) in test_files"
	if out.RiskTier == "high" {
		step += "; risk_tier is HIGH — review must_update call sites with `change_kit` before shipping"
	} else if len(out.MustUpdate) > 0 {
		step += "; check must_update call sites stayed consistent with your change"
	}
	return step + "."
}
