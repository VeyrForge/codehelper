package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/hints"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// kickoff is the ONE-CALL task starter. Beginning any feature/fix used to mean
// chaining project_context (orient) -> scout (reuse) -> docs (library API) ->
// plan (decisions/steps) -> profile (verify commands): four-plus round trips
// before writing a line. kickoff returns all of it in a single call, grounded in
// the real index, so the LLM spends its budget on the change, not on rediscovery.
// It is a SUPERSET of plan + scout + the orient half of project_context.
type kickoffResponse struct {
	Task            string           `json:"task"`
	Role            string           `json:"role"`
	Orient          kickoffOrient    `json:"orient"`
	ReuseCandidates []reuseCandidate `json:"reuse_candidates,omitempty"`
	UsageOfTop      *usageExample    `json:"usage_of_top,omitempty"`
	ImpactOfTop     *scoutImpact     `json:"impact_of_top,omitempty"`
	DuplicationRisk []string         `json:"duplication_risk,omitempty"`
	Placement       []string         `json:"placement,omitempty"`
	RelevantDocs    []string         `json:"relevant_docs,omitempty"`
	DecisionPoints  []string         `json:"decision_points"`
	Considerations  []string         `json:"considerations"`
	Gotchas         []string         `json:"gotchas,omitempty"` // stack pitfalls (curated) + learned hints, so the agent avoids known mistakes before writing code
	Steps           []string         `json:"steps"`
	Verification    []string         `json:"verification,omitempty"`
	Freshness       string           `json:"freshness,omitempty"`
	Note            string           `json:"note"`
}

type kickoffOrient struct {
	ProjectType string   `json:"project_type"`
	Languages   []string `json:"languages,omitempty"`
	Frameworks  []string `json:"frameworks,omitempty"`
	KeyDeps     []string `json:"key_dependencies,omitempty"`
	Summary     string   `json:"summary,omitempty"`
}

func kickoffHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		task := strings.TrimSpace(argString(args, "task"))
		if task == "" {
			return mcp.NewToolResultError("task is required — describe what you want to build/change in natural language."), nil
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

		out := kickoffResponse{Task: task, Role: role}

		// --- ORIENT: stack/frameworks/deps/summary (the project_context half) ---
		frameworks, deps, summary := projectBrief(repo.RootPath)
		out.Orient = kickoffOrient{Frameworks: frameworks, KeyDeps: deps, Summary: summary}
		if pp, perr := profile.Generate(repo.RootPath); perr == nil {
			out.Orient.ProjectType = pp.ProjectType
			out.Orient.Languages = pp.Languages
			out.Verification = uniqueTrimmedStrings(pp.LintCommands, pp.TestCommands)
			// Surface stack pitfalls + learned hints right where the agent is about to
			// implement, so known mistakes are avoided before the first edit.
			out.Gotchas = pp.Gotchas
			depNames := make([]string, 0, len(pp.Dependencies))
			for _, d := range pp.Dependencies {
				depNames = append(depNames, d.Name)
			}
			if lh, herr := hints.MatchingFor(pp.Framework, pp.ProjectType, pp.Languages, depNames); herr == nil {
				out.Gotchas = uniqueTrimmedStrings(out.Gotchas, lh)
			}
		}

		// --- FIND: reuse candidates ranked by subject (same path as scout/plan) ---
		query, tokens := task, strings.Fields(strings.ToLower(task))
		if subj := taskSubjectTokens(task); len(subj) > 0 {
			query, tokens = strings.Join(subj, " "), subj
		}
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, query, 5, retrieval.MCPQueryOptions(
			repo.RootPath, "", tokens, nil,
		))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
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

		// --- UNDERSTAND: usage example + blast radius/tests of the closest match ---
		sig := taskSignals{Top: top}
		if len(hits) > 0 {
			t := hits[0].Symbol
			out.UsageOfTop = usageExampleFor(ctx, st, repo, t)
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

		// --- DOCS: which indexed dependencies the task likely needs API docs for ---
		out.RelevantDocs = relevantDocs(task, deps)

		// --- DESIGN: task-specific decisions/placement/duplication/considerations ---
		candPaths := make([]string, 0, len(out.ReuseCandidates))
		for _, c := range out.ReuseCandidates {
			candPaths = append(candPaths, c.Loc)
		}
		sig.Domains = detectDomains(task, candPaths)
		out.DuplicationRisk = deriveDuplication(task, out.ReuseCandidates)
		out.Placement = derivePlacement(sig)
		out.DecisionPoints = deriveDecisionPoints(role, sig)
		out.Considerations = deriveConsiderations(role, sig)
		out.Steps = planSteps(role, top)

		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — re-run analyze for accurate reuse/impact"
		}
		out.Note = "One-shot task starter (orient + reuse + docs + decisions + verify). Resolve the decision_points (ask the user when they're genuine choices), confirm the top reuse_candidate with `context` before extending, fetch any relevant_docs you'll code against, then implement and run the verification commands."

		// Section opt-in: `sections=reuse,decisions` returns only those, for callers
		// that want a cheaper, focused payload. Empty = everything (default).
		if sel := parseSections(argString(args, "sections")); sel != nil {
			if !sel["orient"] {
				out.Orient = kickoffOrient{}
			}
			if !sel["reuse"] {
				out.ReuseCandidates, out.UsageOfTop, out.ImpactOfTop, out.DuplicationRisk = nil, nil, nil, nil
			}
			if !sel["docs"] {
				out.RelevantDocs = nil
			}
			if !sel["decisions"] {
				out.DecisionPoints, out.Considerations, out.Placement = nil, nil, nil
			}
			if !sel["steps"] {
				out.Steps = nil
			}
			if !sel["verify"] {
				out.Verification = nil
			}
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// parseSections parses a comma-separated section allowlist (orient,reuse,docs,
// decisions,steps,verify). Returns nil when empty so the caller returns all.
func parseSections(s string) map[string]bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out[p] = true
		}
	}
	return out
}

// relevantDocs returns "lib@ver — call docs" pointers for indexed dependencies
// whose name appears in the task, so the LLM knows WHICH library docs to pull
// (version-correct) without a separate discovery round trip. Deterministic;
// bounded to the top few matches.
func relevantDocs(task string, deps []string) []string {
	subj := taskSubjectTokens(task)
	if len(subj) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, dep := range deps {
		name := dep
		if i := strings.Index(dep, "@"); i > 0 {
			name = dep[:i]
		}
		lower := strings.ToLower(name)
		// match on any path segment of the dependency name (golang.org/x/time -> "time").
		segs := strings.FieldsFunc(lower, func(r rune) bool { return r == '/' || r == '.' || r == '-' })
		for _, s := range subj {
			if len(s) < 4 {
				continue
			}
			for _, seg := range segs {
				if seg == s && !seen[dep] {
					seen[dep] = true
					out = append(out, fmt.Sprintf("%s — call `docs` for version-correct API before coding against it", dep))
				}
			}
		}
		if len(out) >= 4 {
			break
		}
	}
	return out
}

// RegisterKickoffTools registers the one-shot task starter.
func RegisterKickoffTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("kickoff",
		mcp.WithDescription("ONE-CALL task starter: in a single call returns orient (stack/frameworks/deps), reuse_candidates (ranked existing symbols with caller counts + a real usage example), relevant_docs (which libraries to pull API docs for), task-specific decision_points (grounded in the closest match's risk/callers/tests + the security domain the task touches), placement + duplication_risk, a role considerations checklist, steps, and verification commands. Use this FIRST when starting any feature/fix — it replaces chaining project_context + scout + plan. Set role=architect|security|performance|refactor|feature."),
		mcp.WithString("task", mcp.Required(), mcp.Description("What you want to build/change, in natural language")),
		mcp.WithString("role", mcp.Description("Expert lens: architect | security | performance | refactor | feature (default)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("sections", mcp.Description("Optional comma list to return ONLY these sections (cheaper payload): orient,reuse,docs,decisions,steps,verify. Empty = all.")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("kickoff", kickoffHandler(reg)))
}
