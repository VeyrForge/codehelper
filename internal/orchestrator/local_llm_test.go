package orchestrator

import (
	"context"
	"strings"
	"testing"
)

type fakeChat struct {
	reply string
	err   error
}

func (f fakeChat) Model() string { return "fake" }

func (f fakeChat) Complete(ctx context.Context, system, user string) (string, error) {
	return f.reply, f.err
}

func TestClassifyTaskHybridFallsBackOnBadJSON(t *testing.T) {
	base := ClassifyTask("fix login redirect bug", Constraints{}, nil)
	got := ClassifyTaskHybrid(context.Background(), fakeChat{reply: "not json"}, "fix login redirect bug", Constraints{}, nil)
	if got.Intent != base.Intent {
		t.Fatalf("intent=%s want %s", got.Intent, base.Intent)
	}
}

func TestClassifyTaskHybridUsesLLMPlan(t *testing.T) {
	reply := `{"intent":"bugfix","workflow":"bugfix_triage","confidence":0.88,"entities":["OAuth","middleware"],"queries":["OAuth callback middleware redirect"],"avoid":["password reset"]}`
	got := ClassifyTaskHybrid(context.Background(), fakeChat{reply: reply}, "redirect after login", Constraints{}, nil)
	if got.Intent != IntentBugfix {
		t.Fatalf("intent=%s", got.Intent)
	}
	if len(got.Queries) == 0 || got.Queries[0] != "OAuth callback middleware redirect" {
		t.Fatalf("queries=%v", got.Queries)
	}
	if got.Confidence < 0.85 {
		t.Fatalf("confidence=%f", got.Confidence)
	}
}

func TestShouldSkipLLMClassifyHighConfidence(t *testing.T) {
	plan := ClassifyTask("fix login redirect bug not working", Constraints{}, nil)
	if !ShouldSkipLLMClassify(plan) {
		t.Fatalf("expected skip for confident bugfix plan conf=%.2f", plan.Confidence)
	}
}

func TestClassifyTaskHybridSkipsLLMWhenConfident(t *testing.T) {
	calls := 0
	chat := fakeChat{reply: `{"intent":"bugfix","workflow":"bugfix_triage","confidence":0.99,"entities":[],"queries":[]}`}
	wrapped := fakeChatCounting{inner: chat, calls: &calls}
	got := ClassifyTaskHybrid(context.Background(), wrapped, "fix crash bug error fail", Constraints{}, nil)
	if got.Intent != IntentBugfix {
		t.Fatalf("intent=%s", got.Intent)
	}
	if calls != 0 {
		t.Fatalf("expected 0 LLM calls when deterministic confidence high, got %d", calls)
	}
}

type fakeChatCounting struct {
	inner fakeChat
	calls *int
}

func (f fakeChatCounting) Model() string { return f.inner.Model() }

func (f fakeChatCounting) Complete(ctx context.Context, system, user string) (string, error) {
	*f.calls++
	return f.inner.Complete(ctx, system, user)
}

func TestBuildAgentBriefIncludesLocations(t *testing.T) {
	brief := BuildAgentBrief("explain symref", Plan{Intent: IntentExplain, Workflow: WorkflowExplainCode, Confidence: 0.9},
		ContextPack{Locations: []string{"internal/graph/resolve.go:223"}, Symbols: []string{"ResolveSymrefs"}},
		nil, Constraints{}, TierFast)
	if !strings.Contains(brief, "Locations:") || !strings.Contains(brief, "resolve.go:223") {
		t.Fatalf("missing locations in brief: %q", brief)
	}
}

func TestCompressForAgentSkipsWhenAlreadyShort(t *testing.T) {
	chat := fakeChat{reply: "should not be used"}
	short := "brief already"
	got := CompressForAgent(context.Background(), chat, "task", Plan{Workflow: WorkflowExplainCode}, short, 2400)
	if got != short {
		t.Fatalf("got %q want %q", got, short)
	}
}

func TestCompressForAgentUsesLocalLLM(t *testing.T) {
	long := strings.Repeat("symbol `Run` in internal/foo.go with callers. ", 80)
	chat := fakeChat{reply: "- Focus `Run`\n- File internal/foo.go"}
	got := CompressForAgent(context.Background(), chat, "explain Run", Plan{Workflow: WorkflowExplainCode, Confidence: 0.9}, long, 2400)
	if !strings.Contains(got, "Run") {
		t.Fatalf("compressed lost symbol: %q", got)
	}
}

func TestMeteringChatTracksUsage(t *testing.T) {
	var usage UsageTotals
	chat := wrapMeteringChat(fakeChat{reply: "ok"}, &usage)
	if _, err := chat.Complete(context.Background(), "sys", "user"); err != nil {
		t.Fatal(err)
	}
	if usage.ToolCalls != 1 || usage.RespTokens == 0 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestValidateLLMPlanRejectsInvalidWorkflow(t *testing.T) {
	base := ClassifyTask("add feature", Constraints{}, nil)
	got := validateLLMPlan(llmPlanSchema{
		Intent: "feature", Workflow: "not_a_real_workflow", Confidence: 0.9,
	}, base, Constraints{})
	if got.Workflow != WorkflowFeatureScope {
		t.Fatalf("workflow=%s", got.Workflow)
	}
}
