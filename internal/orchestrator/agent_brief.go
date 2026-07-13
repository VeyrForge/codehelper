package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	maxBriefSymbols = 6
	maxBriefFiles   = 8
	maxBriefTrace   = 8
)

// RunUsage splits internal MCP work from local-LLM work and the cloud-facing payload.
type RunUsage struct {
	MCP               UsageTotals `json:"mcp"`
	LocalLLM          UsageTotals `json:"local_llm"`
	AgentFacingTokens int         `json:"agent_facing_tokens"`
}

// BuildAgentBrief produces a deterministic, token-lean brief for cloud agents.
func BuildAgentBrief(task string, plan Plan, pack ContextPack, trace []CompactTrace, c Constraints, tier TaskTier) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Investigation brief\n\n")
	fmt.Fprintf(&b, "Task: %s\n", truncate(task, 240))
	fmt.Fprintf(&b, "Intent: %s · Workflow: %s · Tier: %s · Confidence: %.2f\n", plan.Intent, plan.Workflow, tier, plan.Confidence)
	if c.Instruction != "" {
		fmt.Fprintf(&b, "Scope: %s\n", truncate(c.Instruction, 160))
	}
	if pack.OrientLine != "" {
		fmt.Fprintf(&b, "\n%s\n", truncate(pack.OrientLine, 320))
	}
	if plan.Intent == IntentFeature && (pack.OrientLine != "" || len(pack.Symbols) > 0) {
		n := len(pack.Symbols)
		if n == 0 {
			n = 1
		}
		fmt.Fprintf(&b, "\nReuse: kickoff/scout ranked %d extension candidate(s) — prefer extending symbols below\n", n)
	}
	if syms := capStrings(pack.Symbols, maxBriefSymbols); len(syms) > 0 {
		fmt.Fprintf(&b, "\nSymbols: %s\n", strings.Join(formatBackticks(syms), ", "))
	}
	if locs := capStrings(pack.Locations, maxBriefFiles); len(locs) > 0 {
		fmt.Fprintf(&b, "Locations: %s\n", strings.Join(locs, ", "))
	}
	if len(pack.SourceExcerpts) > 0 {
		fmt.Fprintf(&b, "\nSource excerpt:\n```\n%s\n```\n", truncate(pack.SourceExcerpts[0], 480))
	}
	if files := capStrings(pack.Files, maxBriefFiles); len(files) > 0 {
		fmt.Fprintf(&b, "Files: %s\n", strings.Join(files, ", "))
	}
	if len(pack.Risks) > 0 {
		fmt.Fprintf(&b, "Risk: %s\n", strings.Join(capStrings(pack.Risks, 4), "; "))
	}
	if len(pack.Verification) > 0 {
		fmt.Fprintf(&b, "Verify: %s\n", strings.Join(capStrings(pack.Verification, 5), "; "))
	}
	if len(pack.Steps) > 0 {
		fmt.Fprintf(&b, "\nSteps:\n")
		for i, s := range capStrings(pack.Steps, 5) {
			fmt.Fprintf(&b, "%d. %s\n", i+1, truncate(s, 120))
		}
	}
	if len(pack.Decisions) > 0 {
		fmt.Fprintf(&b, "\nDecisions:\n")
		for _, d := range capStrings(pack.Decisions, 4) {
			fmt.Fprintf(&b, "- %s\n", truncate(d, 120))
		}
	}
	if len(pack.MissesPossible) > 0 {
		fmt.Fprintf(&b, "\nGaps: %s\n", strings.Join(capStrings(pack.MissesPossible, 3), "; "))
	}
	if len(trace) > 0 {
		fmt.Fprintf(&b, "\nTrace:\n")
		for _, t := range trace {
			if len(trace) > maxBriefTrace && t.Step > maxBriefTrace {
				break
			}
			fmt.Fprintf(&b, "- %d %s: %s\n", t.Step, t.Tool, truncate(t.Result, 100))
		}
	}
	fmt.Fprintf(&b, "\nNext: use `context`/`impact` on a symbol above for source; `run_trace` for full args.\n")
	return strings.TrimSpace(b.String())
}

// AgentPayload returns the MCP response shape. Default omits heavy fields so cloud
// agents pay for a brief + compact trace, not duplicate markdown packs.
func (r *Result) AgentPayload(full bool) any {
	if r == nil {
		return nil
	}
	if full {
		return r
	}
	out := map[string]any{
		"run_id":             r.RunID,
		"status":             r.Status,
		"workflow":           r.Workflow,
		"intent":             r.Intent,
		"confidence":         r.Confidence,
		"agent_brief":        r.AgentBrief,
		"tool_trace_compact": r.ToolTraceCompact,
		"usage":              r.Usage,
		"feedback_prompt":    r.FeedbackPrompt,
		"rerun_suggestions":  r.RerunSuggestions,
		"note":               "Full answer_markdown and context_pack omitted — pass detail=full, or call run_trace / explain_run.",
	}
	if r.PreviousWrongNote != "" {
		out["previous_wrong_note"] = r.PreviousWrongNote
	}
	if r.ChangedFromPrev != "" {
		out["changed_from_previous"] = r.ChangedFromPrev
	}
	return out
}

// AgentFacingTokens estimates tokens for the default slim orchestrate payload.
func AgentFacingTokens(res *Result) int {
	if res == nil {
		return 0
	}
	b, err := json.Marshal(res.AgentPayload(false))
	if err != nil {
		return estimateTokens(len(res.AgentBrief))
	}
	return estimateTokens(len(b))
}

func capStrings(in []string, n int) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
		if len(out) >= n {
			break
		}
	}
	return out
}

func formatBackticks(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = "`" + s + "`"
	}
	return out
}
