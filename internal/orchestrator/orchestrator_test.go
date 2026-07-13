package orchestrator

import (
	"context"
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

func TestWorkflowStepsNonEmpty(t *testing.T) {
	for _, wf := range []Workflow{WorkflowBugfixTriage, WorkflowFeatureScope, WorkflowRefactorImpact, WorkflowExplainCode, WorkflowReviewGate, WorkflowDeadCodeScan} {
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
		RepoRoot: dir, RepoName: "test", Invoker: inv, Store: st, Chat: nil,
	})
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
