package orchestrator

import (
	"strings"
)

// Intent is the classified task type.
type Intent string

const (
	IntentBugfix   Intent = "bugfix"
	IntentFeature  Intent = "feature"
	IntentRefactor Intent = "refactor"
	IntentExplain  Intent = "explain"
	IntentReview   Intent = "review"
	IntentDeadCode Intent = "dead_code"
)

// Workflow names deterministic investigation chains.
type Workflow string

const (
	WorkflowBugfixTriage   Workflow = "bugfix_triage"
	WorkflowFeatureScope   Workflow = "feature_scope"
	WorkflowRefactorImpact Workflow = "refactor_impact"
	WorkflowExplainCode    Workflow = "explain_code"
	WorkflowReviewGate     Workflow = "review_gate"
	WorkflowDeadCodeScan   Workflow = "dead_code_scan"
)

// Plan is the validated routing output from classification.
type Plan struct {
	Intent     Intent   `json:"intent"`
	Workflow   Workflow `json:"workflow"`
	Confidence float64  `json:"confidence"`
	Entities   []string `json:"entities"`
	Queries    []string `json:"queries"`
	Avoid      []string `json:"avoid,omitempty"`
}

// Constraints carry rerun/feedback scope adjustments.
type Constraints struct {
	PreferredEntities []string `json:"preferred_entities,omitempty"`
	AvoidEntities     []string `json:"avoid_entities,omitempty"`
	Instruction       string   `json:"instruction,omitempty"`
	PreviousRunID     string   `json:"previous_run_id,omitempty"`
}

var bugfixTerms = []string{"bug", "broken", "fix", "error", "fail", "crash", "regression", "not working", "doesn't work", "issue"}
var featureTerms = []string{"add", "implement", "build", "create", "feature", "new", "support"}
var refactorTerms = []string{"refactor", "rename", "extract", "reorganize", "cleanup", "migrate"}
var explainTerms = []string{"how does", "how do", "explain", "what is", "what does", "understand", "walk through"}
var reviewTerms = []string{"review", "audit", "security", "check diff", "ready to merge"}
var deadCodeTerms = []string{"dead code", "unreferenced", "unused symbol", "never called", "orphan", "unreachable", "not used"}

// ClassifyTask deterministically routes a natural-language task to a workflow.
func ClassifyTask(task string, constraints Constraints, memoryRules []string) Plan {
	lt := strings.ToLower(strings.TrimSpace(task))
	intent := IntentFeature
	conf := 0.65

	score := func(terms []string) int {
		n := 0
		for _, t := range terms {
			if strings.Contains(lt, t) {
				n++
			}
		}
		return n
	}
	scores := map[Intent]int{
		IntentBugfix:   score(bugfixTerms),
		IntentFeature:  score(featureTerms),
		IntentRefactor: score(refactorTerms),
		IntentExplain:  score(explainTerms),
		IntentReview:   score(reviewTerms),
		IntentDeadCode: score(deadCodeTerms),
	}
	best := IntentFeature
	bestN := 0
	for k, v := range scores {
		if v > bestN {
			best, bestN = k, v
		}
	}
	if bestN > 0 {
		intent = best
		conf = 0.72 + float64(bestN)*0.03
		if conf > 0.92 {
			conf = 0.92
		}
		// "add error handling" / validation work is feature scope, not bugfix triage.
		if intent == IntentBugfix && scores[IntentFeature] == bestN && scores[IntentFeature] > 0 {
			intent = IntentFeature
		}
		if strings.Contains(lt, "error handling") || strings.Contains(lt, "validation") {
			if scores[IntentFeature] > 0 {
				intent = IntentFeature
			}
		}
	}

	entities := extractEntities(lt, constraints.PreferredEntities)
	avoid := append([]string(nil), constraints.AvoidEntities...)
	queries := buildQueries(task, entities, avoid, memoryRules)

	wf := workflowForIntent(intent)
	return Plan{
		Intent:     intent,
		Workflow:   wf,
		Confidence: conf,
		Entities:   entities,
		Queries:    queries,
		Avoid:      avoid,
	}
}

func workflowForIntent(intent Intent) Workflow {
	switch intent {
	case IntentBugfix:
		return WorkflowBugfixTriage
	case IntentRefactor:
		return WorkflowRefactorImpact
	case IntentExplain:
		return WorkflowExplainCode
	case IntentReview:
		return WorkflowReviewGate
	case IntentDeadCode:
		return WorkflowDeadCodeScan
	default:
		return WorkflowFeatureScope
	}
}

func extractEntities(task string, preferred []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, p := range preferred {
		add(p)
	}
	for _, tok := range strings.FieldsFunc(task, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ';' || r == ':' || r == '(' || r == ')'
	}) {
		tok = strings.Trim(tok, "`'\"")
		if len(tok) >= 3 && !isStop(tok) {
			add(tok)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func isStop(w string) bool {
	switch strings.ToLower(w) {
	case "the", "and", "for", "with", "this", "that", "after", "before", "when", "from", "into", "does", "not", "but", "are", "was", "has", "have", "use", "using":
		return true
	}
	return false
}

func buildQueries(task string, entities, avoid, memoryRules []string) []string {
	q := strings.TrimSpace(task)
	if len(entities) > 0 {
		q = strings.Join(entities, " ")
	}
	for _, a := range avoid {
		q = strings.ReplaceAll(q, a, "")
	}
	q = strings.TrimSpace(q)
	out := []string{q}
	if len(entities) >= 2 {
		out = append(out, strings.Join(entities[:2], " "))
	}
	for _, rule := range memoryRules {
		if strings.Contains(strings.ToLower(rule), "prioritize") || strings.Contains(strings.ToLower(rule), "inspect") {
			out = append(out, rule)
		}
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// WorkflowSteps returns the allowed tools for a workflow in order.
func WorkflowSteps(wf Workflow) []workflowStep {
	switch wf {
	case WorkflowBugfixTriage:
		return []workflowStep{
			{Tool: "project_context", Why: "Detect project type and commands", Args: map[string]any{"verbosity": "short"}},
			{Tool: "query", Why: "Find symbols related to the bug", Args: map[string]any{"top_k": 6}},
			{Tool: "context", Why: "Inspect the top matching symbol", Args: map[string]any{"body": "none"}},
			{Tool: "impact", Why: "Assess blast radius of the likely fix area", Args: map[string]any{"direction": "upstream", "depth": 2}},
			{Tool: "test_impact", Why: "Find tests covering the area", Args: map[string]any{}},
		}
	case WorkflowFeatureScope:
		return []workflowStep{
			{Tool: "kickoff", Why: "Orient, reuse, steps, and verification in one call", Args: map[string]any{
				"role": "feature", "sections": "orient,reuse,steps,decisions,verify",
			}},
			{Tool: "scout", Why: "Cross-check ranked reuse candidates near the task anchor", Args: map[string]any{"top_k": 6}},
		}
	case WorkflowRefactorImpact:
		return []workflowStep{
			{Tool: "query", Why: "Locate refactor target symbols", Args: map[string]any{"top_k": 6}},
			{Tool: "context", Why: "Understand the target symbol", Args: map[string]any{"body": "none"}},
			{Tool: "impact", Why: "Map dependents before refactoring", Args: map[string]any{"direction": "upstream", "depth": 3}},
			{Tool: "test_impact", Why: "Tests to run after refactor", Args: map[string]any{}},
		}
	case WorkflowExplainCode:
		return []workflowStep{
			{Tool: "query", Why: "Find the code to explain", Args: map[string]any{"top_k": 5}},
			{Tool: "context", Why: "Symbol graph + brief source excerpt", Args: map[string]any{"body": "brief"}},
		}
	case WorkflowReviewGate:
		return []workflowStep{
			{Tool: "detect_changes", Why: "Map changed symbols", Args: map[string]any{}},
			{Tool: "review_diff", Why: "Audit the working tree diff", Args: map[string]any{}},
			{Tool: "diagnostics", Why: "Build/lint status", Args: map[string]any{}},
		}
	case WorkflowDeadCodeScan:
		return []workflowStep{
			{Tool: "project_context", Why: "Detect project type and layout", Args: map[string]any{"verbosity": "short"}},
			{Tool: "dead_code", Why: "List unreferenced symbols", Args: map[string]any{"limit": 20}},
			{Tool: "scout", Why: "Rank candidates near the task anchor", Args: map[string]any{"limit": 8}},
			{Tool: "query", Why: "Cross-check dead symbols against search", Args: map[string]any{"top_k": 5}},
			{Tool: "context", Why: "Inspect top candidate if found", Args: map[string]any{"body": "brief"}},
		}
	default:
		return WorkflowSteps(WorkflowFeatureScope)
	}
}

type workflowStep struct {
	Tool string
	Why  string
	Args map[string]any
}
