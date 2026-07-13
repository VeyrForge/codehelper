package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/memory"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// plan is an architect-mode planner. It does the pre-work a senior engineer does
// BEFORE writing code — check what already exists, weigh the blast radius, frame
// the decisions, and lay out steps — grounded in the actual index, so the LLM
// spends its reasoning on the change, not on rediscovering the codebase. It is
// deliberately ONE role-parameterized tool (not five) to avoid tool overload.
type planResponse struct {
	Task            string           `json:"task"`
	Role            string           `json:"role"`
	AlreadyExists   string           `json:"already_exists"`
	ReuseCandidates []reuseCandidate `json:"reuse_candidates,omitempty"`
	ImpactOfTop     *scoutImpact     `json:"impact_of_top,omitempty"`
	DuplicationRisk []string         `json:"duplication_risk,omitempty"`
	Placement       []string         `json:"placement,omitempty"`
	DecisionPoints  []string         `json:"decision_points"`
	PriorDecisions  []string         `json:"prior_decisions,omitempty"`
	Considerations  []string         `json:"considerations"`
	Steps           []string         `json:"steps"`
	Verification    []string         `json:"verification,omitempty"`
	Freshness       string           `json:"freshness,omitempty"`
	Note            string           `json:"note"`
}

func planHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		task := strings.TrimSpace(argString(args, "task"))
		if task == "" {
			return mcp.NewToolResultError("task is required — describe what you want to build/change/investigate in natural language."), nil
		}
		role := strings.ToLower(strings.TrimSpace(argString(args, "role")))
		if role == "" {
			role = "feature"
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

		// Reuse-first: rank existing symbols by relevance + centrality, exactly like
		// scout, so the most load-bearing match that already does this surfaces.
		// Rank by the SUBJECT, not the imperative verb: "add caching to docs" should
		// surface caching/docs, not every symbol named Add. Fall back to the full task
		// when stripping verbs/stopwords leaves nothing.
		queryStr, tokens := task, strings.Fields(strings.ToLower(task))
		if subj := taskSubjectTokens(task); len(subj) > 0 {
			queryStr, tokens = strings.Join(subj, " "), subj
		}
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, queryStr, 6, retrieval.MCPQueryOptions(
			repo.RootPath, "", tokens, nil,
		))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := planResponse{Task: task, Role: role}
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

		var top *reuseCandidate
		if len(out.ReuseCandidates) > 0 {
			top = &out.ReuseCandidates[0]
		}
		// Frame the candidates as a lexical GUESS to verify, never an assertion. The
		// top hit can be a spurious word match (a perf helper matching "hot", say), so
		// "Likely YES — X" misleads; the LLM judges which (if any) fits from the
		// candidate names/signatures + context.
		if top == nil {
			out.AlreadyExists = "No close match — likely new functionality. Check imports/conventions via project_context before writing."
		} else {
			names := make([]string, 0, 3)
			for i := range out.ReuseCandidates {
				if i >= 3 {
					break
				}
				names = append(names, "`"+out.ReuseCandidates[i].Name+"`")
			}
			out.AlreadyExists = fmt.Sprintf("Closest existing code (ranked, may not be relevant): %s. Confirm with `context` which — if any — actually fits before adding new.", strings.Join(names, ", "))
		}

		// Gather real signals from the closest match's blast radius: risk tier,
		// dependent count, how many tests cover it, and how many packages it spans.
		// These drive the task-specific decision_points instead of a static list.
		sig := taskSignals{Top: top}
		if len(hits) > 0 {
			t := hits[0].Symbol
			if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, t.ID, 4, "upstream"); aerr == nil && res != nil {
				tests := 0
				for _, n := range res.Nodes {
					if n.Depth > 0 && review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
						tests++
					}
				}
				sig.RiskTier = res.RiskTier
				sig.Dependents = len(res.Nodes) - 1
				sig.TestsOnTop = tests
				sig.PkgsSpanned = distinctPkgs(res.Nodes)
				out.ImpactOfTop = &scoutImpact{Target: t.Name, RiskTier: res.RiskTier, Dependents: sig.Dependents, Tests: tests, Confidence: callGraphConfidence(ctx, st, repo.Name)}
			}
		}
		// Domains: match the task text AND the candidate paths so a hit in
		// internal/auth triggers the auth question even if the task didn't say "auth".
		candPaths := make([]string, 0, len(out.ReuseCandidates))
		for _, c := range out.ReuseCandidates {
			candPaths = append(candPaths, c.Loc)
		}
		sig.Domains = detectDomains(task, candPaths)

		out.DuplicationRisk = deriveDuplication(task, out.ReuseCandidates)
		out.Placement = derivePlacement(sig)
		out.PriorDecisions = relevantPriorDecisions(repo.RootPath, task)
		out.DecisionPoints = deriveDecisionPoints(role, sig)
		out.Considerations = deriveConsiderations(role, sig)
		out.Steps = planSteps(role, top)

		if pr, perr := profile.Read(repo.RootPath); perr == nil && pr != nil {
			out.Verification = uniqueTrimmedStrings(pr.LintCommands, pr.TestCommands)
		}
		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — re-run analyze for accurate reuse/impact"
		}
		out.Note = "Architect scaffolding: it gathers what exists and frames the decisions; the reasoning is yours. Resolve the decision_points (ask the user when they're genuine choices), then follow the steps — using context/change_kit on chosen symbols and impact/test_impact before editing."
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// relevantPriorDecisions surfaces ADR-style decisions recorded via agent_memory
// that match this task, so the planner recalls "why we did it this way" from
// earlier sessions instead of silently reversing a considered choice. Best-effort
// and bounded; a missing/empty memory file yields nothing.
func relevantPriorDecisions(repoRoot, task string) []string {
	hits, err := memory.Open(repoRoot).Search(task, 3)
	if err != nil {
		return nil
	}
	var out []string
	for _, h := range hits {
		if h.Type == "decision" {
			out = append(out, h.Summary)
		}
	}
	return out
}

// taskSubjectTokens drops imperative verbs, stopwords, and vague modifiers so
// plan/scout rank by the SUBJECT of the task ("caching", "docs") rather than the
// action verb ("add"), casual filler ("i wanna…"), or a non-identifying modifier
// ("hot", "fast"). The word set is retrieval.IsCommonWord — the same definition
// the ranker demotes — so query/scout/plan all parse a task identically. Robust
// to informal phrasing and light typos.
func taskSubjectTokens(task string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(task)) {
		w = strings.Trim(w, ".,:;!?\"'()`")
		if len(w) < 2 || retrieval.IsCommonWord(w) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// roleConsiderations returns the expert checklist for a role. Security and
// performance basics are always present because "is it secure / performant" is
// part of every change.
func roleConsiderations(role string) []string {
	base := map[string][]string{
		"architect": {
			"Placement: which package/layer keeps coupling low and the dependency direction inward?",
			"Does this cross a boundary (ui/domain/data)? Don't let inner layers depend on outer.",
			"Is a new abstraction warranted, or would it add indirection without payoff?",
			"What public contract changes, and who depends on it (run impact)?",
		},
		"security": {
			"Validate and bound every external input at the trust boundary.",
			"AuthN/AuthZ: who may invoke this, and is the check enforced server-side?",
			"Avoid injection (SQL/command/template) — use parameterized/escaped APIs.",
			"No secrets in code or logs; grant least privilege for any new capability.",
		},
		"performance": {
			"Hot path or rare? Estimate input size/frequency before optimizing.",
			"Avoid N+1 queries/scans and accidental O(n^2) over large sets.",
			"Bound memory; stream/paginate large results; reuse buffers on hot paths.",
			"Add a benchmark or measurement if this is load-bearing.",
		},
		"refactor": {
			"Behavior must not change — lock it with characterization tests first.",
			"Refactor in small, separately-verifiable steps; keep the diff reviewable.",
			"Update every call site (run impact) and delete now-dead code (dead_code).",
		},
		"feature": {
			"Reuse before adding: extend the closest existing symbol if it fits.",
			"Keep public contracts unless intentionally breaking them.",
			"Add/extend tests for the new behavior, including edge cases.",
		},
	}
	c := base[role]
	if c == nil {
		c = base["feature"]
	}
	out := append([]string{}, c...)
	if role != "security" {
		out = append(out, "Security: validate inputs, enforce authz, avoid injection/secret leakage.")
	}
	if role != "performance" {
		out = append(out, "Performance: avoid N+1 / O(n^2) / unbounded memory on the common path.")
	}
	return out
}

func roleDecisionPoints(role string) []string {
	switch role {
	case "architect":
		return []string{"Which module/layer should own this, and what is the dependency direction?", "Concrete function or an interface/abstraction — is a seam actually needed yet?"}
	case "security":
		return []string{"What is the trust boundary and threat model for this change?", "Does it touch auth, secrets, payments, or user data (treat as high-risk)?"}
	case "performance":
		return []string{"Is this on a hot path? What is the expected input size and call frequency?", "Correctness-first then measure, or is an up-front benchmark required?"}
	case "refactor":
		return []string{"Is current behavior covered by tests, or must characterization tests come first?", "One big refactor, or a sequence of small, individually-verifiable steps?"}
	default:
		return []string{"What is the minimal version that satisfies the request (MVP), and what is explicitly out of scope?"}
	}
}

func planSteps(role string, top *reuseCandidate) []string {
	var steps []string
	if top != nil {
		steps = append(steps, fmt.Sprintf("Confirm reuse: run `context %s` for its code + callers; extend it if it fits the task.", top.Name))
	} else {
		steps = append(steps, "Confirm nothing existing already does this (scout/query) before writing new code.")
	}
	steps = append(steps,
		"Run `impact` (and `test_impact`) on anything you will change to see blast radius + the tests to run.",
		"Implement the smallest change that satisfies the task; keep public contracts unless intentionally breaking them.",
		"Run `diagnostics`, then the verification commands; add/extend tests for the new behavior.",
	)
	switch role {
	case "security":
		steps = append(steps, "Security pass on the diff: input validation, authz, injection, secret handling.")
	case "performance":
		steps = append(steps, "Performance pass: confirm no N+1/O(n^2) on the common path; measure if hot.")
	}
	return steps
}

// RegisterPlanTools registers the architect-mode planner.
func RegisterPlanTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("plan",
		mcp.WithDescription("Architect-mode planner: turn a task into a grounded, step-by-step plan BEFORE writing code. In ONE call it (1) checks whether it already exists (ranked reuse candidates with caller counts), (2) shows the blast radius of the closest match, (3) frames the decisions to make (extend vs add, backward-compat, trust boundary, hot path…) as decision_points to resolve with the user, (4) gives a role-specific considerations checklist (security & performance always included), and (5) lays out implementation steps + verification commands. Set role=architect|security|performance|refactor|feature. Use when asked to design/plan/add/refactor a feature."),
		mcp.WithString("task", mcp.Required(), mcp.Description("What you want to build/change/investigate, in natural language")),
		mcp.WithString("role", mcp.Description("Expert lens: architect | security | performance | refactor | feature (default)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("plan", planHandler(reg)))
}
