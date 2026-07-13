// Package plan builds deterministic structured plans and todos from expand_request.
package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/patterns"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/questiongate"
	"github.com/VeyrForge/codehelper/internal/research"
	"github.com/VeyrForge/codehelper/internal/skills"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

// Input for plan generation.
type Input struct {
	Request     string
	ProjectType string
	ChangedArea string
	RepoRoot    string
	// Quick skips repo graph intelligence (pattern expansion only).
	Quick bool
}

// Output is a plan document plus editable todos.
type Output struct {
	Plan           taskstore.Plan
	Todos          []taskstore.Todo
	DecisionPoints []taskstore.DecisionPoint
}

// Build creates a structured plan and todo list from pattern expansion.
func Build(in Input) (Output, error) {
	req := strings.TrimSpace(in.Request)
	if req == "" {
		return Output{}, fmt.Errorf("request is required")
	}
	root := strings.TrimSpace(in.RepoRoot)
	pt := strings.TrimSpace(in.ProjectType)
	if pt == "" {
		if pr, err := profile.Read(root); err == nil && pr != nil {
			pt = pr.ProjectType
		}
	}
	packs, err := patterns.LoadAll(root)
	if err != nil {
		return Output{}, err
	}
	ex := patterns.ExpandRequest(patterns.ExpandInput{
		Request: req, ProjectType: pt, ChangedArea: in.ChangedArea,
	}, packs)
	var pr *profile.ProjectProfile
	if p, err := profile.Read(root); err == nil {
		pr = p
	}
	fr := freshness.Inspect(root)

	assumptions := []string{}
	if ex.AskUser && strings.TrimSpace(ex.AskReason) != "" {
		assumptions = append(assumptions, "ASSUMPTION: "+ex.AskReason)
	}
	doneCriteria := []string{
		"All todos complete or intentionally skipped",
		"verify ran for changed code",
		"finish_check passes with evidence",
	}

	plan := taskstore.Plan{
		Goal:           req,
		Assumptions:    assumptions,
		ExpandRequest:  ex,
		ProjectProfile: pr,
		Freshness:      &fr,
		DoneCriteria:   doneCriteria,
	}

	if !in.Quick {
		intel := gatherIntel(context.Background(), root, req)
		plan.CurrentUnderstanding = intel.Understanding
		plan.ExistingCodeFound = intel.ExistingCode
		plan.ReuseCandidates = intel.ReuseCandidates
		plan.ImpactTier = intel.ImpactTier
		plan.ImplementationOptions = intel.ImplementationOptions
		plan.RecommendedOption = intel.RecommendedOption
		if fr.Stale {
			plan.Assumptions = append(plan.Assumptions, "ASSUMPTION: index may be stale — run codehelper analyze before execute")
		}
		if !intel.IndexAvailable {
			plan.Assumptions = append(plan.Assumptions, "ASSUMPTION: symbol index missing — plan based on patterns only")
		}
	}

	todos := buildTodos(req, ex, pr)
	if len(plan.ExistingCodeFound) > 0 && len(todos) > 0 {
		var files []string
		for _, c := range plan.ExistingCodeFound {
			if c.Path != "" {
				files = append(files, c.Path)
			}
		}
		todos[0].Files = dedupeStrings(files)
		todos[0].ReuseSymbols = plan.ReuseCandidates
	}
	decisions := buildDecisionPoints(req, ex, pr)
	if research.ShouldResearch(req, pr, ex) {
		plan.ResearchSummary = research.BuildSummary(req, pr)
		decisions = appendResearchDecision(decisions)
	}
	if skillMatches := skills.MatchTask(req, pt); len(skillMatches) > 0 {
		var notes []string
		for _, sk := range skillMatches {
			plan.Assumptions = append(plan.Assumptions, "Skill: "+sk.Title)
			if len(sk.Checklist) > 0 {
				notes = append(notes, sk.Title+": "+strings.Join(sk.Checklist, "; "))
			}
		}
		if len(todos) > 0 && len(notes) > 0 {
			if todos[0].ImplementationNotes != "" {
				todos[0].ImplementationNotes += "\n"
			}
			todos[0].ImplementationNotes += strings.Join(notes, "\n")
		}
		if len(todos) > 1 {
			for _, sk := range skillMatches {
				todos[len(todos)-2].VerifyCommands = append(todos[len(todos)-2].VerifyCommands, sk.Checklist...)
			}
			todos[len(todos)-2].VerifyCommands = dedupeStrings(todos[len(todos)-2].VerifyCommands)
		}
	}
	return Output{Plan: plan, Todos: todos, DecisionPoints: decisions}, nil
}

func buildTodos(userRequest string, ex patterns.ExpandOutput, pr *profile.ProjectProfile) []taskstore.Todo {
	var todos []taskstore.Todo

	ctxItems := ex.RequiredContext
	if len(ctxItems) == 0 {
		ctxItems = []string{"related symbols via query", "callers/callees via context", "impact before edits"}
	}
	todos = append(todos, taskstore.Todo{
		ID:              "todo-1",
		Title:           "Inspect project and find reusable code",
		Goal:            "Search existing code before creating anything new",
		Description:     "Use project_context, query, context, and impact for the request scope.",
		RequiredContext: ctxItems,
		SecurityChecks:  ex.RiskChecks,
		Status:          taskstore.TodoPlanned,
	})

	reqs := dedupeStrings(ex.InferredRequirements)
	if len(reqs) == 0 {
		reqs = []string{"Implement: " + userRequest}
	}
	chunkSize := 3
	if len(reqs) <= 4 {
		chunkSize = len(reqs)
	}
	idx := 2
	for i := 0; i < len(reqs); i += chunkSize {
		end := i + chunkSize
		if end > len(reqs) {
			end = len(reqs)
		}
		chunk := reqs[i:end]
		todos = append(todos, taskstore.Todo{
			ID:                  fmt.Sprintf("todo-%d", idx),
			Title:               fmt.Sprintf("Implement step %d", idx-1),
			Goal:                userRequest,
			Description:         strings.Join(chunk, "\n"),
			ImplementationNotes: strings.Join(ex.PerformanceHints, "; "),
			Risks:               ex.RiskChecks,
			SecurityChecks:      ex.RiskChecks,
			PerformanceChecks:   ex.PerformanceHints,
			Status:              taskstore.TodoPlanned,
		})
		idx++
	}

	verify := ex.VerificationSuggestions
	if pr != nil {
		verify = append(verify, pr.TestCommands...)
		verify = append(verify, pr.LintCommands...)
	}
	verify = dedupeStrings(verify)
	todos = append(todos, taskstore.Todo{
		ID:                 fmt.Sprintf("todo-%d", idx),
		Title:              "Verify and review changes",
		Goal:               "Run verification and review diff before marking done",
		Description:        "Run verify (argv), review_diff, and finish_check when prior todos are complete.",
		VerifyCommands:     verify,
		ManualVerification: "Confirm user-visible behavior matches the request",
		Status:             taskstore.TodoPlanned,
	})

	return todos
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func appendResearchDecision(points []taskstore.DecisionPoint) []taskstore.DecisionPoint {
	return append(points, taskstore.DecisionPoint{
		ID:          "research-first",
		Question:    "Official docs research is recommended for this task. Run Research First before approving the plan?",
		Options:     []string{"Research first (recommended)", "Skip research and proceed"},
		Recommended: "Research first (recommended)",
		Pros:        []string{"Reduces outdated API assumptions"},
		Cons:        []string{"Requires network approval when research is enabled"},
	})
}

func buildDecisionPoints(request string, ex patterns.ExpandOutput, prof *profile.ProjectProfile) []taskstore.DecisionPoint {
	var points []taskstore.DecisionPoint
	if ex.AskUser && strings.TrimSpace(ex.AskReason) != "" {
		qg := questiongate.Evaluate(questiongate.Input{
			Task:              request,
			ProposedQuestions: []string{ex.AskReason},
		}, prof)
		if qg.AskUser {
			points = append(points, taskstore.DecisionPoint{
				ID:          "decision-1",
				Question:    ex.AskReason,
				Options:     []string{"Proceed with assumption", "Provide custom answer", "Defer to repo search"},
				Recommended: "Proceed with assumption",
				Pros:        []string{"Unblocks planning quickly"},
				Cons:        []string{"May need revision if assumption is wrong"},
			})
		}
	}
	return points
}
