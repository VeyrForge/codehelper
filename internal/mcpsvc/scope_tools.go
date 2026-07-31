package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// scope is the requirement-elicitation tool for a VAGUE idea. Where kickoff/plan
// assume a half-formed task, scope handles "I want users to pay for stuff" from
// someone who has the idea but doesn't know what to specify — and crucially isn't
// thinking about security, scale, or failure modes. It applies the classic
// Why/What/How elicitation frame and surfaces ONLY the questions that change
// architecture, security, or data shape (per requirements-elicitation practice),
// grounded in the project's detected domains and existing building blocks.
type scopeResponse struct {
	Idea                   string           `json:"idea"`
	Restated               scopeFrame       `json:"restated"`
	ClarifyingQuestions    []string         `json:"clarifying_questions"`
	DecisionsToMake        []string         `json:"decisions_to_make"`
	UnstatedNonFunctionals []string         `json:"unstated_nonfunctionals"`
	ExistingBuildingBlocks []reuseCandidate `json:"existing_building_blocks,omitempty"`
	SuggestedMVP           []string         `json:"suggested_mvp"`
	OutOfScope             []string         `json:"out_of_scope"`
	Note                   string           `json:"note"`
}

type scopeFrame struct {
	Why  string `json:"why"`
	What string `json:"what"`
	How  string `json:"how"`
}

func scopeHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		idea := strings.TrimSpace(argString(args, "idea"))
		if idea == "" {
			idea = strings.TrimSpace(argString(args, "task"))
		}
		if idea == "" {
			return mcp.NewToolResultError("idea is required — describe in plain words what you want to build, even vaguely (e.g. 'let people pay for stuff')."), nil
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

		// Existing building blocks (lenient: vague ideas have few salient nouns).
		query, tokens := idea, strings.Fields(strings.ToLower(idea))
		if subj := taskSubjectTokens(idea); len(subj) > 0 {
			query, tokens = strings.Join(subj, " "), subj
		}
		hits, _ := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, query, 4, retrieval.MCPQueryOptions(
			repo.RootPath, "", tokens, nil,
		))
		var blocks []reuseCandidate
		var blockNames, blockPaths []string
		for _, h := range hits {
			blocks = append(blocks, reuseCandidate{
				Name: h.Symbol.Name, Kind: string(h.Symbol.Kind),
				Loc:     fmt.Sprintf("%s:%d", h.Symbol.Path, h.Symbol.LineStart),
				Callers: callerCountOf(ctx, st, repo.Name, h.Symbol.ID),
			})
			blockNames = append(blockNames, h.Symbol.Name)
			blockPaths = append(blockPaths, h.Symbol.Path)
		}
		domains := detectDomains(idea, blockPaths)

		out := scopeResponse{Idea: idea, ExistingBuildingBlocks: blocks}

		// Why/What/How — the elicitation frame, honest (prompts, not assertions).
		reuseHint := "this is likely new — no close existing code matched"
		if len(blockNames) > 0 {
			reuseHint = fmt.Sprintf("possibly build on existing `%s`", strings.Join(firstN(blockNames, 3), "`, `"))
		}
		domainHint := "no sensitive domain detected"
		if len(domains) > 0 {
			domainHint = "touches " + strings.Join(domainNames(domains), ", ")
		}
		out.Restated = scopeFrame{
			Why:  "Restate in one sentence what problem this solves for the end user — confirm: \"" + idea + "\".",
			What: "The observable end state: what does the user see or do when this works? Define \"done\" concretely before building.",
			How:  fmt.Sprintf("Grounded guess: %s; %s.", reuseHint, domainHint),
		}

		// Clarifying questions — only the ones that change architecture/security/data.
		out.ClarifyingQuestions = append(out.ClarifyingQuestions,
			"WHO uses this — anonymous visitors, logged-in users, or admins only? (changes auth + data model)",
			"What does success look like in one concrete sentence? (the acceptance test)",
			"What DATA does it create/read, and where does it live (new table, existing model, external service)?",
		)
		for _, d := range domains {
			out.ClarifyingQuestions = append(out.ClarifyingQuestions, d.question)
		}
		if len(blockNames) > 0 {
			out.ClarifyingQuestions = append(out.ClarifyingQuestions,
				fmt.Sprintf("`%s` already exists — extend it, or is this genuinely separate?", blockNames[0]))
		}

		// Decisions the newcomer must make (the things they didn't know to decide).
		out.DecisionsToMake = []string{
			"Data shape: the exact fields/columns and their types.",
			"Scale: roughly how many users/records/requests — tens, thousands, or millions? (decides whether to optimize now)",
			"Failure behavior: what should happen when it errors, times out, or input is invalid?",
		}
		if hasDomain(domains, "auth") {
			out.DecisionsToMake = append(out.DecisionsToMake, "Authorization boundary: exactly who may perform this, enforced server-side.")
		}

		// Unstated non-functionals — what beginners skip but production needs.
		out.UnstatedNonFunctionals = []string{
			"Input validation at every boundary (don't trust client data).",
			"What happens on partial failure / retries (idempotency)?",
			"Who can access it, and is that enforced on the server (not just hidden in the UI)?",
		}
		for _, d := range domains {
			out.UnstatedNonFunctionals = append(out.UnstatedNonFunctionals, d.consider)
		}
		out.UnstatedNonFunctionals = dedupeStrings(out.UnstatedNonFunctionals)

		out.SuggestedMVP = []string{
			"Smallest end-to-end slice that a user can actually use (one happy path).",
			"Reuse existing building blocks above instead of new infrastructure.",
			"Hard-code/defer the fancy parts (config, edge cases, scale) until the core works.",
		}
		out.OutOfScope = []string{
			"Performance tuning before it works and is measured.",
			"Edge cases and admin tooling beyond the core happy path.",
			"Anything not needed to demonstrate the one-sentence \"done\".",
		}
		out.Note = "Requirement-elicitation scaffolding for a vague idea (Why/What/How). Answer the clarifying_questions and resolve decisions_to_make WITH the user before building — these are the choices that change architecture/security/data. Then run `kickoff` (or `plan`) on the now-concrete task."
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func firstN(ss []string, n int) []string {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

func domainNames(rs []domainRule) []string {
	var o []string
	for _, r := range rs {
		o = append(o, r.name)
	}
	return o
}

func hasDomain(rs []domainRule, name string) bool {
	for _, r := range rs {
		if r.name == name {
			return true
		}
	}
	return false
}

// RegisterScopeTools registers the vague-idea elicitation tool.
func RegisterScopeTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("scope",
		mcp.WithDescription("Turn a VAGUE idea into a buildable spec — for when you (or the user) have an idea but don't know what to specify and aren't thinking about security/scale/failure. Returns a Why/What/How restatement, the clarifying_questions that actually change architecture/security/data, the decisions_to_make (data shape, scale, auth boundary), the unstated_nonfunctionals beginners skip, existing building blocks to reuse, and a suggested MVP vs out-of-scope. Use BEFORE kickoff/plan when the request is fuzzy; answer the questions with the user, then run kickoff on the concrete task."),
		mcp.WithString("idea", mcp.Required(), mcp.Description("The idea in plain words, however vague (e.g. 'let people pay for stuff on my site')")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("scope", scopeHandler(reg)))
}
