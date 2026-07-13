package mcpsvc

import (
	"context"
	"fmt"
	"sort"

	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// review is the deterministic, write-side complement to `plan`: a one-call audit
// of a diff. It does the mechanical pre-review a senior engineer does — what
// changed, what each change can break, what's untested, what touches the public
// contract — so the LLM (and the human) review the things that actually matter.
type reviewFinding struct {
	Symbol     string `json:"symbol"`
	Loc        string `json:"loc"`
	Kind       string `json:"kind"`
	Exported   bool   `json:"exported,omitempty"`
	RiskTier   string `json:"risk_tier,omitempty"`
	Dependents int    `json:"dependents,omitempty"`
	Tests      int    `json:"tests_covering"`
}

type reviewResponse struct {
	BaseRef          string          `json:"base_ref"`
	ChangedSymbols   int             `json:"changed_symbols"`
	Findings         []reviewFinding `json:"findings,omitempty"`
	PublicAPIChanges []string        `json:"public_api_changes,omitempty"`
	UntestedChanges  []string        `json:"untested_changes,omitempty"`
	HighRisk         []string        `json:"high_risk,omitempty"`
	TestsToRun       []string        `json:"tests_to_run,omitempty"`
	Checklist        []string        `json:"checklist"`
	Verdict          string          `json:"verdict"`
	Freshness        string          `json:"freshness,omitempty"`
	Note             string          `json:"note"`
}

const maxReviewSymbols = 40

func reviewHandler(reg *registry.Registry) server.ToolHandlerFunc {
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

		base := argString(args, "base_ref")
		if base == "" {
			base = "HEAD~1"
		}
		ids, derr := detect.ChangedSymbols(ctx, repo.RootPath, repo.Name, base, st)
		if derr != nil {
			return mcp.NewToolResultError(derr.Error()), nil
		}

		out := reviewResponse{BaseRef: base, ChangedSymbols: len(ids)}
		testFiles := map[string]struct{}{}
		for i, id := range ids {
			if i >= maxReviewSymbols {
				break
			}
			sym, e := st.SymbolByID(ctx, repo.Name, id)
			if e != nil || sym == nil || review.IsTestPath(sym.Path) {
				continue // skip unresolved + the test files themselves
			}
			f := reviewFinding{
				Symbol:   sym.Name,
				Loc:      fmt.Sprintf("%s:%d", sym.Path, sym.LineStart),
				Kind:     string(sym.Kind),
				Exported: isExportedSymbol(*sym),
			}
			if res, ae := mcpimpact.Analyze(ctx, st, repo.Name, id, 2, "downstream"); ae == nil && res != nil {
				f.RiskTier = res.RiskTier
				f.Dependents = len(res.Nodes) - 1
			}
			if res, ae := mcpimpact.Analyze(ctx, st, repo.Name, id, 6, "upstream"); ae == nil && res != nil {
				for _, n := range res.Nodes {
					if n.Depth > 0 && review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
						f.Tests++
						testFiles[n.Path] = struct{}{}
					}
				}
			}
			out.Findings = append(out.Findings, f)
			ref := f.Symbol + " " + f.Loc
			if f.Exported {
				out.PublicAPIChanges = append(out.PublicAPIChanges, ref)
			}
			if f.Tests == 0 {
				out.UntestedChanges = append(out.UntestedChanges, ref)
			}
			if f.RiskTier == "high" {
				out.HighRisk = append(out.HighRisk, ref)
			}
		}
		for tf := range testFiles {
			out.TestsToRun = append(out.TestsToRun, tf)
		}
		sort.Strings(out.TestsToRun)

		out.Checklist = []string{
			"Security: validate inputs, enforce authz, avoid injection/secret leakage on the changed lines.",
			"Performance: no N+1 / O(n^2) / unbounded memory introduced on the common path.",
			"Reuse: does any new symbol duplicate existing code? cross-check with scout before keeping it.",
			"Contracts: public_api_changes must be intentional + documented; check callers with impact.",
		}

		switch {
		case len(ids) == 0:
			out.Verdict = "No changed symbols vs " + base + " (clean, or changes are in non-indexed files)."
		case len(out.HighRisk) > 0 || len(out.UntestedChanges) > 0 || len(out.PublicAPIChanges) > 0:
			out.Verdict = fmt.Sprintf("Review needed: %d changed symbol(s) — %d public-API, %d untested, %d high-risk. Address the flags, then run tests_to_run + diagnostics.",
				len(out.Findings), len(out.PublicAPIChanges), len(out.UntestedChanges), len(out.HighRisk))
		default:
			out.Verdict = fmt.Sprintf("Looks contained: %d changed symbol(s), test-covered, no public-API/high-risk flags. Run tests_to_run + diagnostics to confirm.", len(out.Findings))
		}
		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — re-run analyze for accurate review"
		}
		out.Note = "Deterministic diff audit (no LLM): changed symbols + blast radius + covering tests + flags. Use after editing, before finishing; pair with diagnostics (build/vet) and review_diff (line-level). The write-side complement to plan."
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// RegisterReviewTools registers the deterministic diff-audit tool.
func RegisterReviewTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("review",
		mcp.WithDescription("Deterministic diff AUDIT in one call (no LLM): the symbols changed vs base_ref, each with its blast radius + risk tier + covering-test count, plus flags — public_api_changes (potential breaking), untested_changes (test gaps), high_risk — the tests_to_run, and a security/performance/reuse/contracts checklist. Use AFTER editing, before finishing. The write-side complement to `plan`; pair with `diagnostics` (build/vet) and `review_diff` (line-level)."),
		mcp.WithString("base_ref", mcp.Description("Diff base (default HEAD~1)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("review", reviewHandler(reg)))
}
