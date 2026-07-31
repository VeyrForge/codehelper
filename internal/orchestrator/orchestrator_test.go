package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseOnOff(t *testing.T) {
	on, err := ParseOnOff("enable")
	if err != nil || !on {
		t.Fatalf("enable: on=%v err=%v", on, err)
	}
	off, err := ParseOnOff("off")
	if err != nil || off {
		t.Fatalf("off: on=%v err=%v", off, err)
	}
	if _, err := ParseOnOff("maybe"); err == nil {
		t.Fatal("expected error for invalid on/off")
	}
}

func TestClassifyTaskBugfix(t *testing.T) {
	plan := ClassifyTask("fix redirect bug after login not working", Constraints{}, nil)
	if plan.Intent != IntentBugfix {
		t.Fatalf("intent=%s want bugfix", plan.Intent)
	}
	if plan.Workflow != WorkflowBugfixTriage {
		t.Fatalf("workflow=%s", plan.Workflow)
	}
}

func TestClassifyTaskFeature(t *testing.T) {
	plan := ClassifyTask("add OAuth support for users", Constraints{}, nil)
	if plan.Intent != IntentFeature {
		t.Fatalf("intent=%s want feature", plan.Intent)
	}
}

func TestClassifyTaskDeadCode(t *testing.T) {
	plan := ClassifyTask("find dead unreferenced symbols in auth package", Constraints{}, nil)
	if plan.Intent != IntentDeadCode {
		t.Fatalf("intent=%s want dead_code", plan.Intent)
	}
	if plan.Workflow != WorkflowDeadCodeScan {
		t.Fatalf("workflow=%s", plan.Workflow)
	}
}

func TestClassifyTaskPerfAndSecurity(t *testing.T) {
	perf := ClassifyTask("performance audit of hotspots and latency", Constraints{}, nil)
	if perf.Intent != IntentPerf || perf.Workflow != WorkflowPerfAudit {
		t.Fatalf("perf: intent=%s workflow=%s", perf.Intent, perf.Workflow)
	}
	sec := ClassifyTask("security review for injection and hardcoded secrets", Constraints{}, nil)
	if sec.Intent != IntentSecurity || sec.Workflow != WorkflowSecurityReview {
		t.Fatalf("security: intent=%s workflow=%s", sec.Intent, sec.Workflow)
	}
}

func TestWorkflowStepsNonEmpty(t *testing.T) {
	for _, wf := range []Workflow{
		WorkflowBugfixTriage, WorkflowFeatureScope, WorkflowRefactorImpact, WorkflowExplainCode,
		WorkflowReviewGate, WorkflowDeadCodeScan, WorkflowPerfAudit, WorkflowSecurityReview,
	} {
		steps := WorkflowSteps(wf)
		if len(steps) == 0 {
			t.Fatalf("no steps for %s", wf)
		}
	}
}

type mockInvoker struct {
	responses map[string]string
	calls     []string
}

func (m *mockInvoker) Call(_ context.Context, name string, _ map[string]any) (string, error) {
	m.calls = append(m.calls, name)
	if r, ok := m.responses[name]; ok {
		return r, nil
	}
	return "{}", nil
}

func TestClassifyTaskPreservesCamelCaseEntity(t *testing.T) {
	plan := ClassifyTask("explain requireOrchestrationEnabled gating", Constraints{}, nil)
	found := false
	for _, e := range plan.Entities {
		if e == "requireOrchestrationEnabled" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected CamelCase entity preserved, got %v", plan.Entities)
	}
	if plan.Workflow != WorkflowExplainCode {
		t.Fatalf("workflow=%s want explain_code", plan.Workflow)
	}
}

func TestOrchestrateExactSymbolShortCircuitsToInvestigate(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	inv := &mockInvoker{responses: map[string]string{
		"investigate": `{"target":"requireOrchestrationEnabled","query":{"hits":[{"id":"sym:t:a.go:42:requireOrchestrationEnabled","name":"requireOrchestrationEnabled","loc":"internal/mcpsvc/orchestration_tools.go:42"},{"id":"sym:t:a.go:33:orchestrationDisabledResult","name":"orchestrationDisabledResult","loc":"internal/mcpsvc/orchestration_tools.go:33"}]},"context":{"bundle":{"symbol":{"name":"requireOrchestrationEnabled","loc":"internal/mcpsvc/orchestration_tools.go:42"}}}}`,
		"query":       `{"hits":[{"name":"orchestrationDisabledResult","id":"sym:t:a.go:33:orchestrationDisabledResult","loc":"internal/mcpsvc/orchestration_tools.go:33"},{"name":"requireOrchestrationEnabled","id":"sym:t:a.go:42:requireOrchestrationEnabled","loc":"internal/mcpsvc/orchestration_tools.go:42"}]}`,
		"context":     `{"bundle":{"symbol":{"name":"orchestrationDisabledResult","loc":"internal/mcpsvc/orchestration_tools.go:33"}}}`,
	}}
	orch := New(Options{
		RepoRoot: dir, RepoName: "test", Invoker: inv, Store: st, Chat: deterministicChat(),
	})
	res, err := orch.Run(t.Context(), "explain requireOrchestrationEnabled gating", Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 1 || inv.calls[0] != "investigate" {
		t.Fatalf("expected investigate short-circuit, got calls=%v", inv.calls)
	}
	found := false
	for _, s := range res.ContextPack.Symbols {
		if s == "requireOrchestrationEnabled" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected requireOrchestrationEnabled in pack symbols, got %v", res.ContextPack.Symbols)
	}
	payload, ok := res.AgentPayload(false).(SlimAgentPayload)
	if !ok {
		t.Fatalf("payload type %T", res.AgentPayload(false))
	}
	if len(payload.RecommendedNextTools) == 0 || payload.RecommendedNextTools[0] != "change_kit" {
		t.Fatalf("expected change_kit first after short-circuit, got %v", payload.RecommendedNextTools)
	}
}

func TestSummarizeToolOutput_PicksExactEntityOverSibling(t *testing.T) {
	raw := `{"hits":[{"name":"orchestrationDisabledResult","id":"sym:t:a.go:33:orchestrationDisabledResult","loc":"internal/mcpsvc/orchestration_tools.go:33"},{"name":"requireOrchestrationEnabled","id":"sym:t:a.go:42:requireOrchestrationEnabled","loc":"internal/mcpsvc/orchestration_tools.go:42"}]}`
	_, top, _, _, _, _ := summarizeToolOutput("query", raw, nil,
		"how does requireOrchestrationEnabled gate orchestrate",
		[]string{"requireOrchestrationEnabled"})
	if !strings.Contains(top, "requireOrchestrationEnabled") {
		t.Fatalf("topSymbol=%q want requireOrchestrationEnabled (or sym: id)", top)
	}
}

func TestSummarizeToolOutput_DispatchTable(t *testing.T) {
	required := []string{
		"query", "context", "impact", "test_impact", "kickoff", "investigate",
		"project_context", "scout", "dead_code", "detect_changes", "review_diff", "diagnostics",
	}
	for _, tool := range required {
		if _, ok := toolOutputSummarizers[tool]; !ok {
			t.Fatalf("toolOutputSummarizers missing %q", tool)
		}
	}

	cases := []struct {
		tool string
		raw  string
		want string
	}{
		{tool: "scout", raw: `{}`, want: "Reuse candidates ranked"},
		{tool: "diagnostics", raw: `{}`, want: "diagnostics complete"},
		{tool: "detect_changes", raw: `{}`, want: "detect_changes complete"},
		{tool: "review_diff", raw: `{}`, want: "review_diff complete"},
		{tool: "dead_code", raw: `{"candidates":[{"name":"Unused","loc":"a.go:1"}]}`, want: "Dead code: 1 candidates"},
		{tool: "impact", raw: `{"impact":{"risk_tier":"high","dependents":12}}`, want: "Impact: risk=high dependents=12"},
		{tool: "unknown_tool_xyz", raw: `{}`, want: "unknown_tool_xyz done"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			summary, _, _, _, _, _ := summarizeToolOutput(tc.tool, tc.raw, nil, "task", nil)
			if !strings.Contains(summary, tc.want) {
				t.Fatalf("summary=%q want substring %q", summary, tc.want)
			}
		})
	}

	summary, _, _, _, _, _ := summarizeToolOutput("query", "{}", errors.New("boom"), "task", nil)
	if summary != "boom" {
		t.Fatalf("callErr summary=%q want boom", summary)
	}
}

func TestOrchestrateExplainRunsContextAfterQuery(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	inv := &mockInvoker{responses: map[string]string{
		"query":   `{"hits":[{"id":"sym:test:internal/foo.go:10:Run","name":"Run","loc":"internal/foo.go:10"}]}`,
		"context": `{"bundle":{"symbol":{"name":"Run","loc":"internal/foo.go:10"},"callers":[]},"blast_radius":{"risk_tier":"low","dependents":1}}`,
	}}
	orch := New(Options{
		RepoRoot: dir, RepoName: "test", Invoker: inv, Store: st, Chat: deterministicChat(),
	})
	// Weak symbol "Run" must NOT short-circuit — keep query→context explain chain.
	res, err := orch.Run(t.Context(), "how does Run work", Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Workflow != WorkflowExplainCode {
		t.Fatalf("workflow=%s", res.Workflow)
	}
	gotContext := false
	for _, c := range inv.calls {
		if c == "context" {
			gotContext = true
		}
	}
	if !gotContext {
		t.Fatalf("expected context in trace, got %v", inv.calls)
	}
	if len(res.ContextPack.Snippets) == 0 {
		t.Fatal("expected snippets from context step")
	}
	if res.AgentBrief == "" {
		t.Fatal("expected agent_brief")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if Enabled(dir) {
		t.Fatal("expected disabled by default")
	}
	if err := SetEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	if !Enabled(dir) {
		t.Fatal("expected enabled")
	}
	cfg, err := Load(dir)
	if err != nil || !cfg.Enabled {
		t.Fatalf("load: cfg=%+v err=%v", cfg, err)
	}
}

func TestStoreRunAndTrace(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	runID := NewRunID()
	if err := st.InsertRun(ctx, RunRecord{
		ID: runID, CreatedAt: time.Now().UTC(), Task: "test", Workflow: "bugfix_triage",
		Status: "complete", Confidence: 0.8, FinalAnswer: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertToolCall(ctx, ToolCallRecord{
		ID: runID + "_1", RunID: runID, StepIndex: 1, ToolName: "query",
		ArgsJSON: `{"query":"auth"}`, ResultSummary: "3 hits", ResultHash: "abc", DurationMS: 10, Why: "find",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(ctx, runID)
	if err != nil || run.Task != "test" {
		t.Fatalf("get run: %+v err=%v", run, err)
	}
	calls, err := st.ListToolCalls(ctx, runID)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls: %+v err=%v", calls, err)
	}
}

func TestExtractLocationsInvestigate(t *testing.T) {
	raw := `{"recipe":"security","repo_sink_scan":{"findings":[{"file":"src/acl.c","line":42,"rule":"c-unsafe-buffer"},{"file":"lib/response.js","line":10,"rule":"library-redirect"}]}}`
	locs := extractLocations("investigate", raw)
	if len(locs) < 2 {
		t.Fatalf("expected investigate sink locations, got %v", locs)
	}
	if locs[0] != "src/acl.c:42" {
		t.Fatalf("got %v", locs)
	}
}

func TestJudgeAnswerSecurityAbstain(t *testing.T) {
	j := judgeAnswer(ContextPack{}, Plan{Workflow: WorkflowSecurityReview}, nil)
	if j.Pass {
		t.Fatal("expected fail/abstain when no files")
	}
	if len(j.Issues) == 0 || !strings.Contains(j.Issues[0], "ABSTAIN") {
		t.Fatalf("expected ABSTAIN issue, got %v", j.Issues)
	}
}

func TestSecurityReviewKickoffIncludesFindingsSection(t *testing.T) {
	steps := WorkflowSteps(WorkflowSecurityReview)
	found := false
	for _, s := range steps {
		if s.Tool != "kickoff" {
			continue
		}
		sec, _ := s.Args["sections"].(string)
		if strings.Contains(sec, "findings") {
			found = true
		}
	}
	if !found {
		t.Fatal("security_review kickoff must include findings in sections")
	}
}

func TestBuildAgentBriefSecurityAbstain(t *testing.T) {
	brief := BuildAgentBrief("security review", Plan{Workflow: WorkflowSecurityReview, Intent: IntentSecurity}, ContextPack{}, nil, Constraints{}, TierFast)
	if !strings.Contains(brief, "ABSTAIN") {
		t.Fatalf("expected ABSTAIN in brief, got %s", brief)
	}
}
