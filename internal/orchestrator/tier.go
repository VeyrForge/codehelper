package orchestrator

import "strings"

// TaskTier controls how many MCP tools orchestrate runs: fast = lean, deep = full chain.
type TaskTier string

const (
	TierFast     TaskTier = "fast"
	TierStandard TaskTier = "standard"
	TierDeep     TaskTier = "deep"
)

const minReuseToSkipScout = 3

// ClassifyTier picks fast/standard/deep from plan confidence and task shape.
// Fast = fewest tools + tokens; deep = full blast-radius chains when the task is broad or uncertain.
func ClassifyTier(plan Plan, task string) TaskTier {
	task = strings.TrimSpace(task)
	lt := strings.ToLower(task)
	nEnt := len(plan.Entities)
	short := len(task) < 110

	if plan.Confidence < 0.72 || nEnt > 6 {
		return TierDeep
	}
	for _, w := range []string{"entire codebase", "whole repo", "all packages", "across the project", "major refactor"} {
		if strings.Contains(lt, w) {
			return TierDeep
		}
	}
	if plan.Intent == IntentReview || plan.Intent == IntentDeadCode || plan.Intent == IntentPerf || plan.Intent == IntentSecurity {
		if plan.Confidence >= 0.85 && short {
			return TierStandard
		}
		return TierDeep
	}
	if plan.Confidence >= 0.85 && short && nEnt <= 2 {
		switch plan.Intent {
		case IntentExplain, IntentFeature, IntentBugfix, IntentRefactor:
			return TierFast
		}
	}
	if plan.Intent == IntentExplain && plan.Confidence >= 0.72 {
		return TierFast
	}
	if plan.Intent == IntentFeature && plan.Confidence >= 0.88 && nEnt <= 3 {
		return TierFast
	}
	return TierStandard
}

// WorkflowStepsForTier returns the tool chain for a workflow at the given depth tier.
func WorkflowStepsForTier(wf Workflow, tier TaskTier) []workflowStep {
	if tier == TierDeep {
		return WorkflowSteps(wf)
	}
	switch wf {
	case WorkflowExplainCode:
		return []workflowStep{
			{Tool: "query", Why: "Find the code to explain", Args: map[string]any{"top_k": 5}},
			{Tool: "context", Why: "Symbol graph + brief source excerpt", Args: map[string]any{"body": "brief"}},
		}
	case WorkflowFeatureScope:
		if tier == TierFast {
			return []workflowStep{
				{Tool: "kickoff", Why: "Orient + reuse + verify (lean)", Args: map[string]any{
					"role": "feature", "sections": "orient,reuse,verify",
				}},
			}
		}
		return []workflowStep{
			{Tool: "kickoff", Why: "Orient, reuse, steps, and verification", Args: map[string]any{
				"role": "feature", "sections": "orient,reuse,steps,decisions,verify",
			}},
			{Tool: "scout", Why: "Cross-check reuse when kickoff found few candidates", Args: map[string]any{"top_k": 6}},
		}
	case WorkflowBugfixTriage:
		if tier == TierFast {
			return []workflowStep{
				{Tool: "query", Why: "Find symbols related to the bug", Args: map[string]any{"top_k": 5}},
				{Tool: "context", Why: "Inspect the top matching symbol", Args: map[string]any{"body": "none"}},
			}
		}
		return []workflowStep{
			{Tool: "query", Why: "Find symbols related to the bug", Args: map[string]any{"top_k": 6}},
			{Tool: "context", Why: "Inspect the top matching symbol", Args: map[string]any{"body": "none"}},
			{Tool: "test_impact", Why: "Find tests covering the area", Args: map[string]any{}},
		}
	case WorkflowRefactorImpact:
		if tier == TierFast {
			return []workflowStep{
				{Tool: "query", Why: "Locate refactor target symbols", Args: map[string]any{"top_k": 5}},
				{Tool: "context", Why: "Understand the target symbol", Args: map[string]any{"body": "none"}},
			}
		}
		return []workflowStep{
			{Tool: "query", Why: "Locate refactor target symbols", Args: map[string]any{"top_k": 6}},
			{Tool: "context", Why: "Understand the target symbol", Args: map[string]any{"body": "none"}},
			{Tool: "impact", Why: "Map dependents before refactoring", Args: map[string]any{"direction": "upstream", "depth": 2}},
		}
	case WorkflowDeadCodeScan:
		if tier == TierFast {
			return []workflowStep{
				{Tool: "dead_code", Why: "List unreferenced symbols", Args: map[string]any{"top_k": 15}},
				{Tool: "context", Why: "Inspect top candidate", Args: map[string]any{"body": "brief"}},
			}
		}
		return []workflowStep{
			{Tool: "dead_code", Why: "List unreferenced symbols", Args: map[string]any{"top_k": 20}},
			{Tool: "query", Why: "Cross-check dead symbols against search", Args: map[string]any{"top_k": 5}},
			{Tool: "context", Why: "Inspect top candidate if found", Args: map[string]any{"body": "brief"}},
		}
	case WorkflowPerfAudit:
		if tier == TierFast {
			return []workflowStep{
				{Tool: "hotspots", Why: "Rank files by churn × centrality", Args: map[string]any{"top_k": 8}},
				{Tool: "impact", Why: "Blast radius of a hot-path symbol", Args: map[string]any{"direction": "upstream", "depth": 2}},
			}
		}
		return WorkflowSteps(wf)
	case WorkflowSecurityReview:
		return WorkflowSteps(wf)
	case WorkflowReviewGate:
		return WorkflowSteps(wf)
	default:
		return WorkflowSteps(wf)
	}
}

// shouldSkipStep returns true when a prior step already satisfied this one (saves latency/tokens).
func shouldSkipStep(tool string, pack ContextPack) bool {
	if tool == "scout" && len(pack.Symbols) >= minReuseToSkipScout {
		return true
	}
	return false
}

// shouldCompressBrief returns false for fast-tier runs with an already-compact brief.
func shouldCompressBrief(tier TaskTier, briefLen int) bool {
	if tier == TierFast && briefLen < 2000 {
		return false
	}
	if tier == TierStandard && briefLen < 1600 {
		return false
	}
	return true
}
