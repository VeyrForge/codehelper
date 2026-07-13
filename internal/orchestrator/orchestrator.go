package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Orchestrator runs guided workflows.
type Orchestrator struct {
	repoRoot   string
	repoName   string
	invoker    ToolInvoker
	meter      *MeteredInvoker
	store      *Store
	chat       LocalChat
	localUsage *UsageTotals
}

// Options configures an orchestrator run.
type Options struct {
	RepoRoot string
	RepoName string
	Invoker  ToolInvoker
	Store    *Store
	Chat     LocalChat // nil → ResolveLocalChat() at construction
}

// New creates an orchestrator for a repo.
func New(opts Options) *Orchestrator {
	chat := opts.Chat
	if chat == nil {
		chat = ResolveLocalChat()
	}
	var localUsage UsageTotals
	if chat != nil {
		chat = wrapMeteringChat(chat, &localUsage)
	}
	o := &Orchestrator{
		repoRoot: opts.RepoRoot,
		repoName: opts.RepoName,
		invoker:  opts.Invoker,
		store:    opts.Store,
		chat:     chat,
	}
	if chat != nil {
		o.localUsage = &localUsage
	}
	if m, ok := opts.Invoker.(*MeteredInvoker); ok {
		o.meter = m
	}
	return o
}

// CompactTrace is the token-lean tool trace in responses.
type CompactTrace struct {
	Step   int    `json:"step"`
	Tool   string `json:"tool"`
	Why    string `json:"why"`
	Result string `json:"result"`
}

// ContextPack collects investigation artifacts.
type ContextPack struct {
	Files          []string `json:"files,omitempty"`
	Locations      []string `json:"locations,omitempty"` // path:line citations for agent briefs
	Symbols        []string `json:"symbols,omitempty"`
	Snippets       []string `json:"snippets,omitempty"`
	Risks          []string `json:"risks,omitempty"`
	Verification   []string `json:"verification,omitempty"`
	MissesPossible []string `json:"misses_possible,omitempty"`
	Steps          []string `json:"steps,omitempty"`
	OrientLine     string   `json:"orient_line,omitempty"`
	Decisions      []string `json:"decisions,omitempty"`
	SourceExcerpts []string `json:"source_excerpts,omitempty"`
}

// Result is the orchestration output package.
type Result struct {
	RunID             string         `json:"run_id"`
	Status            string         `json:"status"`
	Workflow          Workflow       `json:"workflow"`
	Intent            Intent         `json:"intent"`
	Confidence        float64        `json:"confidence"`
	AnswerMarkdown    string         `json:"answer_markdown"`
	ContextPack       ContextPack    `json:"context_pack"`
	ToolTraceCompact  []CompactTrace `json:"tool_trace_compact"`
	RerunSuggestions  []string       `json:"rerun_suggestions"`
	FeedbackPrompt    string         `json:"feedback_prompt"`
	AgentBrief        string         `json:"agent_brief"`
	Usage             RunUsage       `json:"usage,omitempty"`
	PreviousWrongNote string         `json:"previous_wrong_note,omitempty"`
	ChangedFromPrev   string         `json:"changed_from_previous,omitempty"`
}

// Run executes a full orchestration workflow.
func (o *Orchestrator) Run(ctx context.Context, task string, constraints Constraints) (*Result, error) {
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("task is required")
	}
	memRules, _ := o.loadMemoryRules(ctx, task)
	plan := ClassifyTaskHybrid(ctx, o.chat, task, constraints, memRules)
	tier := ClassifyTier(plan, task)

	runID := NewRunID()
	steps := WorkflowStepsForTier(plan.Workflow, tier)
	pack := ContextPack{}
	var compact []CompactTrace
	stepIdx := 0
	var topSymbol string
	var queryText string
	if len(plan.Queries) > 0 {
		queryText = plan.Queries[0]
	}

	for _, step := range steps {
		if shouldSkipStep(step.Tool, pack) {
			continue
		}
		args := copyArgs(step.Args)
		if _, ok := args["format"]; !ok {
			args["format"] = "json"
		}
		switch step.Tool {
		case "query":
			q := queryText
			if bq := bestEntityQuery(plan.Entities); bq != "" && (q == "" || strings.Count(q, " ") > 3) {
				q = bq
			}
			if q == "" {
				q = task
			}
			args["query"] = q
		case "scout":
			args["task"] = task
			if queryText != "" {
				args["query"] = queryText
			}
		case "kickoff":
			args["task"] = task
		case "dead_code":
			// uses default limit from workflow step args
		case "context", "impact", "test_impact":
			if topSymbol == "" {
				continue
			}
			switch step.Tool {
			case "context":
				args["name"] = topSymbol
				if _, ok := args["body"]; !ok {
					args["body"] = "none"
				}
			case "impact", "test_impact":
				args["target"] = topSymbol
			}
		}

		stepIdx++
		start := time.Now()
		raw, err := o.invoker.Call(ctx, step.Tool, args)
		dur := time.Since(start).Milliseconds()
		summary, sym, symbols, files, risks, verify := summarizeToolOutput(step.Tool, raw, err)
		if sym != "" {
			topSymbol = sym
		}
		if step.Tool == "kickoff" && err == nil {
			ks := enrichPackFromKickoff(raw, &pack)
			if ks == "" && len(pack.Symbols) == 0 {
				ks = enrichPackFromKickoffText(raw, &pack)
			}
			if ks != "" && topSymbol == "" {
				topSymbol = ks
			}
		}
		pack.Files = uniqueAppend(pack.Files, files...)
		pack.Locations = uniqueAppend(pack.Locations, extractLocations(step.Tool, raw)...)
		pack.Symbols = uniqueAppend(pack.Symbols, symbols...)
		if sym != "" {
			pack.Symbols = uniqueAppend(pack.Symbols, sym)
		}
		pack.Risks = uniqueAppend(pack.Risks, risks...)
		pack.Verification = uniqueAppend(pack.Verification, verify...)
		if summary != "" {
			pack.Snippets = append(pack.Snippets, summary)
		}
		if step.Tool == "context" && err == nil {
			if ex := extractSourceExcerpt(raw); ex != "" {
				pack.SourceExcerpts = uniqueAppend(pack.SourceExcerpts, truncate(ex, 600))
			}
		}

		toolErr := ""
		if err != nil {
			toolErr = err.Error()
		}
		_ = o.store.InsertToolCall(ctx, ToolCallRecord{
			ID: fmt.Sprintf("%s_%d", runID, stepIdx), RunID: runID, StepIndex: stepIdx,
			ToolName: step.Tool, ArgsJSON: mustJSON(args), ResultSummary: truncate(summary, 500),
			ResultHash: HashResult(raw), DurationMS: dur, Why: step.Why, Error: toolErr,
		})
		compact = append(compact, CompactTrace{Step: stepIdx, Tool: step.Tool, Why: step.Why, Result: truncate(summary, 200)})
	}

	judge := judgeAnswer(pack, plan, compact)
	if len(judge.Issues) > 0 {
		pack.MissesPossible = judge.Issues
	}
	conf := plan.Confidence
	if !judge.Pass {
		conf *= 0.9
	}

	answer := buildAnswerMarkdown(task, plan, pack, compact, constraints)
	brief := BuildAgentBrief(task, plan, pack, compact, constraints, tier)
	if shouldCompressBrief(tier, len(brief)) {
		brief = CompressForAgent(ctx, o.chat, task, plan, brief, 2400)
	}

	res := &Result{
		RunID: runID, Status: "complete", Workflow: plan.Workflow, Intent: plan.Intent,
		Confidence: conf, AnswerMarkdown: answer, AgentBrief: brief, ContextPack: pack,
		ToolTraceCompact: compact,
		RerunSuggestions: []string{
			"Focus only on middleware",
			"Inspect tests only",
			"Use refactor workflow instead",
			"Show full trace via run_trace",
		},
		FeedbackPrompt: "Tell Codehelper what was wrong or what to focus on next via orchestration_feedback.",
	}
	if o.meter != nil {
		res.Usage.MCP = o.meter.Last
	}
	if o.localUsage != nil {
		res.Usage.LocalLLM = *o.localUsage
	}
	res.Usage.AgentFacingTokens = AgentFacingTokensFormat(res, "toon")
	if constraints.PreviousRunID != "" {
		res.ChangedFromPrev = "rerun with updated constraints"
	}

	cj, _ := json.Marshal(constraints)
	_ = o.store.InsertRun(ctx, RunRecord{
		ID: runID, CreatedAt: time.Now().UTC(), Task: task, Workflow: string(plan.Workflow),
		Status: "complete", Confidence: conf, FinalAnswer: brief, ConstraintsJSON: string(cj),
	})
	return res, nil
}

// FeedbackInput stores a correction and optional memory.
type FeedbackInput struct {
	RunID             string
	Message           string
	CorrectionType    string
	PreferredEntities []string
	AvoidEntities     []string
}

// Feedback stores correction and returns rerun constraints.
func (o *Orchestrator) Feedback(ctx context.Context, in FeedbackInput) (Constraints, error) {
	if strings.TrimSpace(in.RunID) == "" || strings.TrimSpace(in.Message) == "" {
		return Constraints{}, fmt.Errorf("run_id and message are required")
	}
	ctype := strings.TrimSpace(in.CorrectionType)
	if ctype == "" {
		ctype = "wrong_scope"
	}
	fbID := NewRunID()
	_ = o.store.InsertFeedback(ctx, FeedbackRecord{
		ID: fbID, RunID: in.RunID, CreatedAt: time.Now().UTC(),
		FeedbackText: in.Message, CorrectionType: ctype, Accepted: true,
		PreferredEntities: in.PreferredEntities, AvoidEntities: in.AvoidEntities,
	})
	rule := fmt.Sprintf("When task mentions %s, prioritize %s before other areas.",
		strings.Join(in.PreferredEntities, "/"), strings.Join(in.PreferredEntities, ", "))
	if len(in.AvoidEntities) > 0 {
		rule += " Avoid: " + strings.Join(in.AvoidEntities, ", ") + "."
	}
	rule += " " + in.Message
	_ = o.store.InsertMemory(ctx, MemoryRecord{
		ID: fbID + "_mem", CreatedAt: time.Now().UTC(), MemoryType: "feedback_memory",
		Rule: rule, SourceRun: in.RunID, Weight: 0.7, Scope: "this_project",
	})
	if len(in.AvoidEntities) > 0 {
		_ = o.store.InsertMemory(ctx, MemoryRecord{
			ID: fbID + "_neg", CreatedAt: time.Now().UTC(), MemoryType: "negative_memory",
			Rule:      "Do not inspect " + strings.Join(in.AvoidEntities, ", ") + " for similar tasks.",
			SourceRun: in.RunID, Weight: 0.8, Scope: "this_project",
		})
	}
	return Constraints{
		PreferredEntities: in.PreferredEntities,
		AvoidEntities:     in.AvoidEntities,
		Instruction:       in.Message,
		PreviousRunID:     in.RunID,
	}, nil
}

// ExplainRun returns human-readable reasoning for a past run.
func (o *Orchestrator) ExplainRun(ctx context.Context, runID string) (string, error) {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	calls, _ := o.store.ListToolCalls(ctx, runID)
	fb, _ := o.store.ListFeedback(ctx, runID)
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s chose workflow %s for task: %s\n\n", run.ID, run.Workflow, run.Task)
	fmt.Fprintf(&b, "Confidence: %.2f\n\n", run.Confidence)
	fmt.Fprintf(&b, "Tool sequence (%d steps):\n", len(calls))
	for _, c := range calls {
		fmt.Fprintf(&b, "- step %d: %s — %s → %s\n", c.StepIndex, c.ToolName, c.Why, truncate(c.ResultSummary, 120))
	}
	if len(fb) > 0 {
		fmt.Fprintf(&b, "\nFeedback received:\n")
		for _, f := range fb {
			fmt.Fprintf(&b, "- [%s] %s\n", f.CorrectionType, f.FeedbackText)
		}
		fmt.Fprintf(&b, "\nFuture runs should apply stored memory from this correction.\n")
	}
	return b.String(), nil
}

func (o *Orchestrator) loadMemoryRules(ctx context.Context, task string) ([]string, error) {
	recs, err := o.store.SearchMemory(ctx, task, 5)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Rule)
	}
	return out, nil
}

func copyArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// extractLocations pulls path:line citations from tool JSON for agent briefs.
func extractLocations(tool, raw string) []string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	var out []string
	appendLoc := func(loc string) {
		loc = strings.TrimSpace(loc)
		if loc != "" && strings.Contains(loc, ":") {
			out = append(out, loc)
		}
	}
	switch tool {
	case "query":
		if hits, ok := parsed["hits"].([]any); ok {
			for i, h := range hits {
				if i >= 6 {
					break
				}
				if h0, ok := h.(map[string]any); ok {
					if loc, _ := h0["loc"].(string); loc != "" {
						appendLoc(loc)
					}
				}
			}
		}
	case "context":
		if bundle, ok := parsed["bundle"].(map[string]any); ok {
			if sym, ok := bundle["symbol"].(map[string]any); ok {
				appendLoc(symLoc(sym))
			}
		}
	case "kickoff":
		if v, ok := parsed["reuse_candidates"].([]any); ok {
			for i, item := range v {
				if i >= 6 {
					break
				}
				if m, ok := item.(map[string]any); ok {
					appendLoc(symLoc(m))
				}
			}
		}
	case "scout":
		for _, key := range []string{"reuse_candidates", "candidates", "hits"} {
			if v, ok := parsed[key].([]any); ok {
				for i, item := range v {
					if i >= 6 {
						break
					}
					if m, ok := item.(map[string]any); ok {
						appendLoc(symLoc(m))
					}
				}
			}
		}
	case "dead_code":
		for _, key := range []string{"candidates", "unreferenced"} {
			if items, ok := parsed[key].([]any); ok {
				for i, item := range items {
					if i >= 5 {
						break
					}
					if m, ok := item.(map[string]any); ok {
						appendLoc(symLoc(m))
					}
				}
			}
		}
	}
	return out
}

func symLoc(m map[string]any) string {
	if loc, _ := m["loc"].(string); loc != "" {
		return loc
	}
	path, _ := m["path"].(string)
	line, _ := m["line"].(float64)
	if path != "" && line > 0 {
		return fmt.Sprintf("%s:%.0f", path, line)
	}
	return path
}

func extractSourceExcerpt(raw string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	bundle, _ := parsed["bundle"].(map[string]any)
	if bundle == nil {
		return ""
	}
	src, _ := bundle["source"].(string)
	return strings.TrimSpace(src)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func uniqueAppend(base []string, more ...string) []string {
	seen := map[string]bool{}
	for _, s := range base {
		seen[s] = true
	}
	for _, s := range more {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		base = append(base, s)
	}
	return base
}

type judgeResult struct {
	Pass            bool
	Issues          []string
	RerunRequired   bool
	RecommendedTool string
}

func judgeAnswer(pack ContextPack, plan Plan, trace []CompactTrace) judgeResult {
	var issues []string
	if len(pack.Files) == 0 && len(pack.Symbols) == 0 {
		issues = append(issues, "No files cited from query/context results")
	}
	hasTestImpact := false
	for _, t := range trace {
		if t.Tool == "test_impact" {
			hasTestImpact = true
		}
	}
	if plan.Intent == IntentBugfix && !hasTestImpact && len(pack.Verification) == 0 {
		issues = append(issues, "No test_impact or verification hints for bugfix workflow")
	}
	return judgeResult{Pass: len(issues) == 0, Issues: issues, RerunRequired: len(issues) > 0}
}

func buildAnswerMarkdown(task string, plan Plan, pack ContextPack, trace []CompactTrace, c Constraints) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Codehelper Result\n\n")
	fmt.Fprintf(&b, "Task type: %s\n", plan.Intent)
	fmt.Fprintf(&b, "Workflow: %s\n", plan.Workflow)
	fmt.Fprintf(&b, "Confidence: %.2f\n\n", plan.Confidence)
	if c.Instruction != "" {
		fmt.Fprintf(&b, "## Scope adjustment\n\n%s\n\n", c.Instruction)
	}
	if plan.Workflow == WorkflowBugfixTriage {
		fmt.Fprintf(&b, "## Bugfix triage\n\n")
		if len(pack.Symbols) > 0 {
			fmt.Fprintf(&b, "Focus symbol: `%s`\n\n", pack.Symbols[0])
		}
		if len(pack.Verification) > 0 {
			fmt.Fprintf(&b, "### Tests / verification\n\n")
			for _, v := range pack.Verification {
				fmt.Fprintf(&b, "- %s\n", v)
			}
			b.WriteString("\n")
		}
	}
	if plan.Workflow == WorkflowFeatureScope {
		fmt.Fprintf(&b, "## Feature scope (kickoff)\n\n")
		if pack.OrientLine != "" {
			fmt.Fprintf(&b, "%s\n\n", pack.OrientLine)
		}
		if len(pack.Symbols) > 0 {
			fmt.Fprintf(&b, "### Reuse candidates\n\n")
			for _, s := range pack.Symbols {
				if s != "" {
					fmt.Fprintf(&b, "- `%s`\n", s)
				}
			}
			b.WriteString("\n")
		}
		if len(pack.Steps) > 0 {
			fmt.Fprintf(&b, "### Steps\n\n")
			for i, s := range pack.Steps {
				fmt.Fprintf(&b, "%d. %s\n", i+1, s)
			}
			b.WriteString("\n")
		}
		if len(pack.Decisions) > 0 {
			fmt.Fprintf(&b, "### Decision points\n\n")
			for _, d := range pack.Decisions {
				fmt.Fprintf(&b, "- %s\n", d)
			}
			b.WriteString("\n")
		}
	}
	if len(pack.Files) > 0 {
		fmt.Fprintf(&b, "## Relevant files\n\n")
		for _, f := range pack.Files {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	if len(pack.Symbols) > 0 && plan.Workflow != WorkflowFeatureScope {
		fmt.Fprintf(&b, "## Symbols inspected\n\n")
		for _, s := range pack.Symbols {
			if s != "" {
				fmt.Fprintf(&b, "- `%s`\n", s)
			}
		}
		b.WriteString("\n")
	}
	if len(plan.Entities) > 0 {
		fmt.Fprintf(&b, "## Task entities\n\n")
		for _, e := range plan.Entities {
			if e != "" {
				fmt.Fprintf(&b, "- `%s`\n", e)
			}
		}
		b.WriteString("\n")
	}
	if len(pack.Snippets) > 1 {
		fmt.Fprintf(&b, "## Additional findings\n\n")
		for i := 1; i < len(pack.Snippets) && i < 3; i++ {
			fmt.Fprintf(&b, "- %s\n", pack.Snippets[i])
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## What I found\n\n")
	if len(pack.Snippets) > 0 {
		fmt.Fprintf(&b, "%s\n\n", pack.Snippets[0])
	} else {
		fmt.Fprintf(&b, "Investigation completed for: %s\n\n", task)
	}
	if plan.Intent == IntentFeature {
		fmt.Fprintf(&b, "Kickoff reuse scan completed — see symbols and files above for extension points.\n\n")
	}
	if plan.Intent == IntentDeadCode {
		fmt.Fprintf(&b, "Dead code scan completed — candidates listed in tool trace; verify with impact before deleting.\n\n")
	}
	if len(pack.Risks) > 0 {
		fmt.Fprintf(&b, "## Risks\n\n")
		for _, r := range pack.Risks {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		b.WriteString("\n")
	}
	if len(pack.Verification) > 0 {
		fmt.Fprintf(&b, "## Verification\n\nRun:\n\n```bash\n%s\n```\n\n", strings.Join(pack.Verification, "\n"))
	}
	if len(pack.MissesPossible) > 0 {
		fmt.Fprintf(&b, "## Possible gaps\n\n")
		for _, m := range pack.MissesPossible {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Tool trace (compact)\n\n")
	for _, t := range trace {
		fmt.Fprintf(&b, "%d. **%s** — %s → %s\n", t.Step, t.Tool, t.Why, t.Result)
	}
	return b.String()
}

func summarizeToolOutput(tool, raw string, callErr error) (summary, topSymbol string, symbols, files, risks, verify []string) {
	if callErr != nil {
		return callErr.Error(), "", nil, nil, nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return truncate(raw, 300), "", nil, nil, nil, nil
	}
	switch tool {
	case "query":
		if hits, ok := parsed["hits"].([]any); ok {
			names := make([]string, 0, len(hits))
			for i, h := range hits {
				if i >= 5 {
					break
				}
				if h0, ok := h.(map[string]any); ok {
					n, _ := h0["name"].(string)
					id, _ := h0["id"].(string)
					if n != "" {
						names = append(names, n)
						symbols = append(symbols, n)
						if topSymbol == "" {
							if id != "" && strings.HasPrefix(id, "sym:") {
								topSymbol = id
							} else {
								topSymbol = n
							}
						}
						if loc, _ := h0["loc"].(string); loc != "" {
							files = append(files, strings.Split(loc, ":")[0])
						}
					}
				}
			}
			if len(names) > 0 {
				summary = fmt.Sprintf("Found %d symbols: %s", len(hits), strings.Join(names, ", "))
			} else {
				summary = fmt.Sprintf("Found %d symbols", len(hits))
			}
		}
	case "context":
		if bundle, ok := parsed["bundle"].(map[string]any); ok {
			if sym, ok := bundle["symbol"].(map[string]any); ok {
				topSymbol, _ = sym["name"].(string)
				if topSymbol != "" {
					symbols = append(symbols, topSymbol)
				}
				if loc, _ := sym["loc"].(string); loc != "" {
					files = append(files, strings.Split(loc, ":")[0])
				}
			}
			nCallers := 0
			if callers, ok := bundle["callers"].([]any); ok {
				nCallers = len(callers)
				for i, c := range callers {
					if i >= 5 {
						break
					}
					if m, ok := c.(map[string]any); ok {
						n, _ := m["name"].(string)
						loc, _ := m["loc"].(string)
						if n != "" && (strings.HasPrefix(n, "Test") || strings.HasPrefix(n, "test")) {
							verify = append(verify, n)
						}
						if isTestishPath(loc) {
							p := strings.Split(loc, ":")[0]
							files = append(files, p)
							verify = append(verify, "go test "+p)
						}
					}
				}
			}
			if topSymbol != "" {
				summary = fmt.Sprintf("Context %s: %d callers", topSymbol, nCallers)
			} else {
				summary = fmt.Sprintf("Loaded symbol context (%d callers)", nCallers)
			}
		}
		if br, ok := parsed["blast_radius"].(map[string]any); ok {
			if tier, _ := br["risk_tier"].(string); tier != "" {
				risks = append(risks, "risk_tier="+tier)
			}
			if dep, ok := br["dependents"].(float64); ok && dep > 0 {
				risks = append(risks, fmt.Sprintf("dependents=%.0f", dep))
			}
		}
	case "impact":
		imp, ok := parsed["impact"].(map[string]any)
		if !ok {
			imp, _ = parsed["blast_radius"].(map[string]any)
		}
		if imp != nil {
			tier, _ := imp["risk_tier"].(string)
			if tier != "" {
				risks = append(risks, "impact risk="+tier)
			}
			if dep, ok := imp["dependents"].(float64); ok {
				if tier != "" {
					summary = fmt.Sprintf("Impact: risk=%s dependents=%.0f", tier, dep)
				} else {
					summary = fmt.Sprintf("Impact: %.0f dependents", dep)
				}
			} else if summary == "" {
				summary = "Computed blast radius"
			}
		}
	case "test_impact":
		summary = "Mapped test impact"
		if tests, ok := parsed["tests"].([]any); ok && len(tests) > 0 {
			summary = fmt.Sprintf("Tests to run: %d", len(tests))
			for i, t := range tests {
				if i >= 8 {
					break
				}
				if m, ok := t.(map[string]any); ok {
					if n, _ := m["name"].(string); n != "" {
						verify = append(verify, n)
					}
					if loc, _ := m["loc"].(string); loc != "" && isTestishPath(loc) {
						files = append(files, strings.Split(loc, ":")[0])
					}
				}
			}
		}
		if tfs, ok := parsed["test_files"].([]any); ok {
			for i, f := range tfs {
				if i >= 8 {
					break
				}
				if s, ok := f.(string); ok && s != "" {
					files = append(files, s)
					verify = append(verify, "run tests in "+s)
				}
			}
		}
		if len(verify) == 0 {
			if note, _ := parsed["note"].(string); note != "" {
				summary = "test impact: " + truncate(note, 120)
			}
		}
	case "kickoff":
		summary = "Kickoff task starter pack ready"
		if v, ok := parsed["reuse_candidates"].([]any); ok && len(v) > 0 {
			summary = fmt.Sprintf("Kickoff reuse: %d candidates", len(v))
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if n, _ := m["name"].(string); n != "" {
						symbols = append(symbols, n)
						if topSymbol == "" {
							topSymbol = n
						}
						if loc, _ := m["loc"].(string); loc != "" {
							files = append(files, strings.Split(loc, ":")[0])
						}
					}
				}
			}
		}
		if v, ok := parsed["verification"].([]any); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					verify = append(verify, s)
				}
			}
		}
	case "project_context":
		if pt, _ := parsed["project_type"].(string); pt != "" {
			summary = "orient: project_type=" + pt
		}
		if cmds, ok := parsed["suggested_verify_commands"].([]any); ok {
			for _, x := range cmds {
				if s, ok := x.(string); ok {
					verify = append(verify, s)
				}
			}
		}
		if cmds, ok := parsed["verification"].([]any); ok {
			for _, x := range cmds {
				if s, ok := x.(string); ok {
					verify = append(verify, s)
				}
			}
		}
	case "scout":
		summary = "Reuse candidates ranked"
	case "dead_code":
		summary = "Dead/unreferenced symbols listed"
		if items, ok := parsed["candidates"].([]any); ok {
			names := make([]string, 0, len(items))
			for i, item := range items {
				if i >= 5 {
					break
				}
				if m, ok := item.(map[string]any); ok {
					if n, _ := m["name"].(string); n != "" {
						names = append(names, n)
						symbols = append(symbols, n)
						if topSymbol == "" {
							topSymbol = n
						}
						if loc, _ := m["loc"].(string); loc != "" {
							files = append(files, strings.Split(loc, ":")[0])
						}
					}
				}
			}
			summary = fmt.Sprintf("Dead code: %d candidates", len(items))
			if len(names) > 0 {
				summary += ": " + strings.Join(names, ", ")
			}
		} else if items, ok := parsed["unreferenced"].([]any); ok {
			summary = fmt.Sprintf("Dead code: %d unreferenced", len(items))
		}
	case "detect_changes", "review_diff", "diagnostics":
		summary = tool + " complete"
	default:
		summary = tool + " done"
	}
	return summary, topSymbol, symbols, files, risks, verify
}
