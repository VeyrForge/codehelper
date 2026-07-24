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

// AgentMCPParamKeys mirrors mcpsvc.MCPParamKeys for slim orchestrate payloads
// without importing mcpsvc (avoids a cycle). Keep in sync with toolcatalog.go.
const AgentMCPParamKeys = "context/context_bundle→name · impact/change_kit→target · trace→from+to · query/search_hybrid→query · kickoff/orchestrate→task · investigate→query|recipe|target · rename_symbol→name+to · scope→idea · agent_memory→action"

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
	} else if plan.Workflow == WorkflowSecurityReview && len(pack.Files) == 0 && len(pack.Symbols) == 0 {
		fmt.Fprintf(&b, "\nABSTAIN: no grounded file:line security sinks — do not invent vulns; narrow the module or re-run investigate recipe=security.\n")
	}
	if len(pack.SourceExcerpts) > 0 {
		fmt.Fprintf(&b, "\nSource excerpt:\n```\n%s\n```\n", truncate(pack.SourceExcerpts[0], 480))
	}
	if files := capStrings(pack.Files, maxBriefFiles); len(files) > 0 {
		fmt.Fprintf(&b, "Files: %s\n", strings.Join(files, ", "))
	}
	if plan.Workflow == WorkflowSecurityReview && len(pack.Locations) > 0 {
		fmt.Fprintf(&b, "Security: cite locations above; confirm exploitability before patching. kind=config_hardening is checklist-only.\n")
	}
	if len(pack.Risks) > 0 {
		fmt.Fprintf(&b, "Risk: %s\n", strings.Join(capStrings(pack.Risks, 4), "; "))
	}
	if len(pack.Verification) > 0 {
		fmt.Fprintf(&b, "Verify: %s\n", strings.Join(capStrings(pack.Verification, 5), "; "))
	}
	whatNext, nextQueries := packWhatNextAndQueries(pack)
	if whatNext != "" || len(nextQueries) > 0 || len(pack.Steps) > 0 {
		fmt.Fprintf(&b, "\nWhat to do next:\n")
		n := 0
		if whatNext != "" {
			n++
			fmt.Fprintf(&b, "%d. %s\n", n, truncate(whatNext, 160))
		}
		for _, s := range capStrings(pack.Steps, 5) {
			low := strings.ToLower(strings.TrimSpace(s))
			if low == strings.ToLower(whatNext) || strings.HasPrefix(low, "next:") {
				continue
			}
			n++
			fmt.Fprintf(&b, "%d. %s\n", n, truncate(s, 140))
			if n >= 5 {
				break
			}
		}
		for _, s := range capStrings(nextQueries, 3) {
			n++
			fmt.Fprintf(&b, "%d. %s\n", n, truncate(s, 140))
		}
		if n == 0 {
			for i, s := range capStrings(pack.Steps, 5) {
				fmt.Fprintf(&b, "%d. %s\n", i+1, truncate(s, 140))
			}
		}
	} else if plan.Workflow == WorkflowSecurityReview || plan.Workflow == WorkflowPerfAudit || plan.Intent == IntentFeature {
		fmt.Fprintf(&b, "\nWhat to do next:\n1. Paste a next_queries item from kickoff/investigate, or call `query` with a concrete noun.\n")
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
	fmt.Fprintf(&b, "\nParam keys: %s\n", AgentMCPParamKeys)
	fmt.Fprintf(&b, "Next: follow What to do next; use `context name=…` / `impact target=…` / `change_kit target=…`; `run_trace` for full args.\n")
	return strings.TrimSpace(b.String())
}

// SlimAgentPayload is the default orchestrate / orchestration_rerun response.
// It is a struct (not map[string]any) so JSON→TOON keeps stable field order
// with run_id first for agents parsing the compact text.
type SlimAgentPayload struct {
	RunID                string         `json:"run_id"`
	Status               string         `json:"status"`
	Workflow             Workflow       `json:"workflow"`
	Intent               Intent         `json:"intent"`
	Confidence           float64        `json:"confidence"`
	AgentBrief           string         `json:"agent_brief,omitempty"`
	WhatNext             string         `json:"what_next,omitempty"`
	NextQueries          []string       `json:"next_queries,omitempty"`
	RecommendedNextTools []string       `json:"recommended_next_tools,omitempty"`
	MCPParamKeys         string         `json:"mcp_param_keys,omitempty"`
	ToolTraceCompact     []CompactTrace `json:"tool_trace_compact,omitempty"`
	Usage                RunUsage       `json:"usage,omitempty"`
	FeedbackPrompt       string         `json:"feedback_prompt,omitempty"`
	RerunSuggestions     []string       `json:"rerun_suggestions,omitempty"`
	PreviousWrongNote    string         `json:"previous_wrong_note,omitempty"`
	ChangedFromPrev      string         `json:"changed_from_previous,omitempty"`
	Note                 string         `json:"note,omitempty"`
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
	whatNext, nextQueries := packWhatNextAndQueries(r.ContextPack)
	out := SlimAgentPayload{
		RunID:                r.RunID,
		Status:               r.Status,
		Workflow:             r.Workflow,
		Intent:               r.Intent,
		Confidence:           r.Confidence,
		AgentBrief:           r.AgentBrief,
		WhatNext:             whatNext,
		NextQueries:          nextQueries,
		RecommendedNextTools: orchestrateRecommendedTools(string(r.Intent), string(r.Workflow)),
		MCPParamKeys:         AgentMCPParamKeys,
		ToolTraceCompact:     r.ToolTraceCompact,
		Usage:                r.Usage,
		FeedbackPrompt:       r.FeedbackPrompt,
		RerunSuggestions:     r.RerunSuggestions,
		PreviousWrongNote:    r.PreviousWrongNote,
		ChangedFromPrev:      r.ChangedFromPrev,
		Note:                 "Full answer_markdown and context_pack omitted — pass detail=true, or call run_trace / explain_run. Prefer context name=… and change_kit target=….",
	}
	return out
}

// packWhatNextAndQueries extracts LLM-facing follow-ups from a context pack
// (kickoff what_next + next_queries are folded into Steps as "next: …").
func packWhatNextAndQueries(pack ContextPack) (whatNext string, nextQueries []string) {
	if len(pack.NextQueries) > 0 {
		nextQueries = capStrings(pack.NextQueries, 3)
	}
	for _, s := range pack.Steps {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(s), "next:") {
			q := strings.TrimSpace(s[len("next:"):])
			if q != "" {
				nextQueries = uniqueAppend(nextQueries, q)
			}
			continue
		}
		if whatNext == "" && !strings.HasPrefix(strings.ToLower(s), "steps:") {
			whatNext = s
		}
	}
	if len(nextQueries) > 3 {
		nextQueries = nextQueries[:3]
	}
	return whatNext, nextQueries
}

// orchestrateRecommendedTools steers the agent after a slim orchestrate brief.
func orchestrateRecommendedTools(intent, workflow string) []string {
	i := strings.ToLower(strings.TrimSpace(intent + " " + workflow))
	switch {
	case strings.Contains(i, "security"):
		return []string{"investigate", "context", "impact", "change_kit"}
	case strings.Contains(i, "perf"):
		return []string{"hotspots", "context", "impact", "test_impact"}
	case strings.Contains(i, "feature") || strings.Contains(i, "scope"):
		return []string{"change_kit", "diagnostics", "review_diff", "verify"}
	default:
		return []string{"context", "change_kit", "query", "kickoff"}
	}
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
