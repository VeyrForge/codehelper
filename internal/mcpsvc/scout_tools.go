package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- test_impact -----------------------------------------------------------

type testImpactResponse struct {
	Seeds     []string     `json:"seeds"`               // changed/target symbols analyzed
	TestFiles []string     `json:"test_files"`          // distinct test files to run
	Tests     []compactSym `json:"tests"`               // test symbols that transitively reach the change
	Truncated int          `json:"truncated,omitempty"` // tests dropped past the cap
	Safety    string       `json:"safety"`
	Freshness string       `json:"freshness,omitempty"`
	Note      string       `json:"note,omitempty"`
}

// maxTestImpact bounds the per-test list. You run test FILES (test_files), not
// individual functions, so a long per-test list is mostly context bloat — the
// file list and a count are the actionable parts. Kept small to limit how much
// stays in the agent's context window across a session.
const maxTestImpact = 25

// testImpactHandler answers "which tests should I run for this change?" by walking
// the reverse call-graph closure from each changed/target symbol and collecting
// the test functions that reach it. This is a SAFE (over-approximating) selection:
// it never silently drops a test that could be affected, but may include a few
// extra — the documented correctness contract for static test-impact selection.
func testImpactHandler(reg *registry.Registry) server.ToolHandlerFunc {
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

		depth := int(mcp.ParseInt64(req, "depth", 0))
		if depth <= 0 {
			depth = 6 // tests are often several hops above a leaf symbol
		}

		// Seeds: an explicit target symbol, else the symbols changed vs base_ref.
		var seeds []string
		if t := argString(args, "target"); t != "" {
			seeds = []string{t}
		} else {
			base := argString(args, "base_ref")
			if base == "" {
				base = "HEAD~1"
			}
			ids, derr := detect.ChangedSymbols(ctx, repo.RootPath, repo.Name, base, st)
			if derr != nil {
				return mcp.NewToolResultError(derr.Error()), nil
			}
			seeds = ids
		}

		tests := map[string]types.ImpactNode{} // symbolID -> node (dedup)
		for _, seed := range seeds {
			res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, seed, depth, "upstream")
			if aerr != nil || res == nil {
				continue // a seed may not resolve (e.g. deleted symbol); skip it
			}
			for _, n := range res.Nodes {
				if n.Depth == 0 {
					continue // the seed itself
				}
				if review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
					tests[n.SymbolID] = n
				}
			}
		}

		out := testImpactResponse{
			Seeds:  seeds,
			Safety: "SAFE over-approximation: every test that transitively reaches a changed symbol via the call graph is included (may over-select; never silently misses). Run the full suite periodically and after dependency/build changes.",
		}
		fresh := freshness.Inspect(repo.RootPath)
		if fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — re-run analyze for accurate test selection"
		}
		if len(seeds) == 0 {
			out.Note = "no changed symbols detected for the diff; nothing to select"
			return mustToolResultFormatted(out, resolveFormat(args))
		}

		fileSet := map[string]struct{}{}
		nodes := make([]types.ImpactNode, 0, len(tests))
		for _, n := range tests {
			nodes = append(nodes, n)
		}
		// Stable, useful ordering: shallowest (closest to the change) first.
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].Depth != nodes[j].Depth {
				return nodes[i].Depth < nodes[j].Depth
			}
			return nodes[i].Path < nodes[j].Path
		})
		for _, n := range nodes {
			fileSet[n.Path] = struct{}{}
			if len(out.Tests) >= maxTestImpact {
				out.Truncated++
				continue
			}
			out.Tests = append(out.Tests, compactSym{
				Name: n.Name, Kind: n.Kind, Loc: locOf(n.Path, n.SymbolID),
			})
		}
		for f := range fileSet {
			out.TestFiles = append(out.TestFiles, f)
		}
		sort.Strings(out.TestFiles)
		if len(out.Tests) == 0 {
			out.Note = "no tests reach the changed symbols via the indexed call graph. Either coverage is missing or the change is in untested code — add tests, or the index may lack edges (analyze --force)."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func isTestSymbolKind(kind string) bool {
	return kind == "function" || kind == "method"
}

// locOf builds a path:line reference from a symbol ID when possible.
func locOf(path, symID string) string {
	if l := symIDLoc(symID); l != "" {
		return l
	}
	return path
}

// ---- scout -----------------------------------------------------------------

type reuseCandidate struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Loc       string  `json:"loc"`
	Recv      string  `json:"recv,omitempty"`
	Signature string  `json:"signature,omitempty"`
	Callers   int     `json:"callers"` // how load-bearing / proven this symbol is
	Score     float64 `json:"score"`
}

type scoutImpact struct {
	Target     string `json:"target"`
	RiskTier   string `json:"risk_tier"`
	Dependents int    `json:"dependents"`
	Tests      int    `json:"tests_covering"`
	Confidence string `json:"confidence,omitempty"`
}

// callGraphConfidence labels how much to trust call-graph-derived signals
// (risk_tier, dependents, tests_covering). Static, heuristic resolution captures
// most edges in statically-dispatched code (Go, Rust) but few in dynamic
// frameworks (Laravel facades/DI, Rails, dynamic Python), where dependents/tests
// are UNDER-counted. We measure resolved call-edge density (call edges ÷ symbols)
// and warn when it's low so a "0 tests / low risk" isn't read as ground truth.
func callGraphConfidence(ctx context.Context, st *graph.Store, repoID string) string {
	symbols, _, _, err := st.Counts(ctx, repoID)
	if err != nil || symbols == 0 {
		return ""
	}
	deg, derr := st.InDegrees(ctx, repoID, "calls")
	if derr != nil {
		return ""
	}
	callEdges := 0
	for _, d := range deg {
		callEdges += d
	}
	density := float64(callEdges) / float64(symbols)
	if density < 0.5 {
		return fmt.Sprintf("LOW — sparse call graph (%d call edges / %d symbols = %.2f/sym); likely a dynamic framework or a parser without call resolution (PHP/Ruby/C/C++). risk_tier & tests_covering are UNDER-counted. DIRECTIVE: do NOT treat a 0 as 'no callers/tests' — confirm by running the test suite and grepping the symbol name before assuming it is safe to change.", callEdges, symbols, density)
	}
	return ""
}

// usageExample is a real call site of the top reuse candidate so the agent sees
// HOW to use it, not just that it exists — turning "reuse this" into copyable code.
type usageExample struct {
	Caller string `json:"caller"` // the symbol that calls the candidate
	Loc    string `json:"loc"`    // path:line of the actual call
	Code   string `json:"code"`   // the source line of the call, trimmed
}

type scoutResponse struct {
	Task            string           `json:"task"`
	ReuseCandidates []reuseCandidate `json:"reuse_candidates"`
	ImpactOfTop     *scoutImpact     `json:"impact_of_top,omitempty"`
	UsageOfTop      *usageExample    `json:"usage_of_top,omitempty"`
	Freshness       string           `json:"freshness,omitempty"`
	CollisionNote   string           `json:"collision_note,omitempty"`
	Note            string           `json:"note"`
}

// scoutHandler pre-assembles the context needed to implement a change: existing
// symbols that already do something similar (reuse candidates, ranked, with caller
// counts so the agent sees what is load-bearing) plus the blast radius and test
// coverage of the single best match. The goal is for the LLM to spend its thinking
// on the change, not on rediscovering what already exists.
func scoutHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		task := argString(args, "task")
		if task == "" {
			task = argString(args, "query")
		}
		if task == "" {
			return mcp.NewToolResultError("task is required (describe what you want to add or fix)"), nil
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

		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = 8
		}
		// Centrality ranking matters most here: scout's whole premise is "prefer
		// the load-bearing reuse candidate", so the most-called existing symbol
		// that matches the task should surface first, not just the closest lexical
		// match. Mirrors the boost applied in the query tool.
		// Rank by the subject, not the imperative verb / casual filler ("i wanna show"),
		// so the reuse candidate matches the noun the user means. Same as plan.
		scoutQuery, scoutTokens := task, strings.Fields(strings.ToLower(task))
		if subj := taskSubjectTokens(task); len(subj) > 0 {
			scoutQuery, scoutTokens = strings.Join(subj, " "), subj
		}
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, scoutQuery, topK, retrieval.MCPQueryOptions(
			repo.RootPath, "", scoutTokens, nil,
		))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		hits, demoted := demoteFixtureHits(hits)

		out := scoutResponse{Task: task, CollisionNote: fixtureCollisionNote(demoted)}
		for _, h := range hits {
			out.ReuseCandidates = append(out.ReuseCandidates, reuseCandidate{
				Name: h.Symbol.Name, Kind: string(h.Symbol.Kind),
				Loc:       fmt.Sprintf("%s:%d", h.Symbol.Path, h.Symbol.LineStart),
				Recv:      h.Symbol.ParentID,
				Signature: h.Symbol.Signature,
				Callers:   callerCountOf(ctx, st, repo.Name, h.Symbol.ID),
				Score:     round3(h.Score),
			})
		}

		// Blast radius + test coverage of the single best reuse candidate, so the
		// agent immediately knows what changing it would touch.
		if len(hits) > 0 {
			top := hits[0].Symbol
			if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, top.ID, 4, "upstream"); aerr == nil && res != nil {
				tests := 0
				for _, n := range res.Nodes {
					if n.Depth > 0 && review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
						tests++
					}
				}
				out.ImpactOfTop = &scoutImpact{
					Target: top.Name, RiskTier: res.RiskTier,
					Dependents: len(res.Nodes) - 1, Tests: tests,
					Confidence: callGraphConfidence(ctx, st, repo.Name),
				}
			}
			// Show a real call site so the agent can copy the calling convention
			// instead of guessing it (or making another round-trip to read code).
			out.UsageOfTop = usageExampleFor(ctx, st, repo, top)
		}

		fresh := freshness.Inspect(repo.RootPath)
		if fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ")"
		}
		if len(out.ReuseCandidates) == 0 {
			out.Note = "no existing symbols match this task — likely new functionality; check imports/conventions before writing."
		} else {
			out.Note = "Prefer extending the high-caller reuse_candidates over writing new code. usage_of_top shows a real call site (copy its calling convention); impact_of_top shows what depends on the closest match and how many tests cover it — run test_impact before changing it."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// callerCountOf returns how many resolved calls target this symbol — a proxy for
// how load-bearing (and therefore reuse-worthy / risky-to-change) it is.
func callerCountOf(ctx context.Context, st *graph.Store, repoID, symID string) int {
	in, err := st.EdgesTo(ctx, repoID, symID, "calls")
	if err != nil {
		return 0
	}
	return len(in)
}

// usageExampleFor finds a real call site of sym and returns the exact source line
// that invokes it, so the agent sees the calling convention. It prefers a
// non-test caller (more representative), reads the caller's body, and locates the
// first line that names the symbol. Best-effort: returns nil if no resolved
// caller exists or the source can't be read.
func usageExampleFor(ctx context.Context, st *graph.Store, repo registry.Entry, sym types.Symbol) *usageExample {
	callers, err := st.CallersOf(ctx, repo.Name, sym.ID)
	if err != nil || len(callers) == 0 {
		return nil
	}
	// Prefer a non-test caller; fall back to the first if all are tests.
	chosen := callers[0]
	for _, c := range callers {
		if !review.IsTestPath(c.Path) {
			chosen = c
			break
		}
	}
	line, lineNo := callSiteLine(repo.RootPath, chosen, sym.Name)
	if line == "" {
		return nil
	}
	return &usageExample{
		Caller: chosen.Name,
		Loc:    fmt.Sprintf("%s:%d", chosen.Path, lineNo),
		Code:   line,
	}
}

// callSiteLine reads caller's source between its start/end lines and returns the
// first line (and its 1-based number) that mentions name — the actual call site.
func callSiteLine(root string, caller types.Symbol, name string) (string, int) {
	b, err := os.ReadFile(filepath.Join(root, caller.Path))
	if err != nil {
		return "", 0
	}
	lines := strings.Split(string(b), "\n")
	start := caller.LineStart
	if start < 1 {
		start = 1
	}
	end := caller.LineEnd
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	for i := start - 1; i < end && i < len(lines); i++ {
		if strings.Contains(lines[i], name) {
			trimmed := strings.TrimSpace(lines[i])
			if len(trimmed) > 200 {
				trimmed = trimmed[:200] + "…"
			}
			return trimmed, i + 1
		}
	}
	return "", 0
}
