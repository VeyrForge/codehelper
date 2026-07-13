package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildAgentBriefCapsLists(t *testing.T) {
	pack := ContextPack{
		Symbols:      []string{"A", "B", "C", "D", "E", "F", "G"},
		Files:        []string{"f1.go", "f2.go", "f3.go", "f4.go", "f5.go", "f6.go", "f7.go", "f8.go", "f9.go"},
		Verification: []string{"go test ./...", "make lint"},
	}
	brief := BuildAgentBrief("explain auth", Plan{Intent: IntentExplain, Workflow: WorkflowExplainCode, Confidence: 0.9}, pack, []CompactTrace{
		{Step: 1, Tool: "query", Result: "3 hits"},
	}, Constraints{}, TierFast)
	if strings.Contains(brief, "`G`") {
		t.Fatal("expected symbols capped")
	}
	if strings.Contains(brief, "f9.go") {
		t.Fatal("expected files capped")
	}
	if !strings.Contains(brief, "Verify:") {
		t.Fatal("expected verification line")
	}
}

func TestAgentPayloadOmitsHeavyFieldsByDefault(t *testing.T) {
	res := &Result{
		RunID: "run1", Status: "complete", Workflow: WorkflowExplainCode, Intent: IntentExplain,
		Confidence: 0.8, AgentBrief: "brief", AnswerMarkdown: "long markdown",
		ContextPack: ContextPack{Files: []string{"a.go"}},
	}
	payload, ok := res.AgentPayload(false).(map[string]any)
	if !ok {
		t.Fatal("expected map payload")
	}
	if _, ok := payload["answer_markdown"]; ok {
		t.Fatal("answer_markdown should be omitted in slim payload")
	}
	if _, ok := payload["context_pack"]; ok {
		t.Fatal("context_pack should be omitted in slim payload")
	}
	if payload["agent_brief"] != "brief" {
		t.Fatal("agent_brief missing")
	}
	full := res.AgentPayload(true)
	if _, ok := full.(*Result); !ok {
		t.Fatal("full payload should be Result")
	}
}

func TestAgentFacingTokensUsesSlimPayload(t *testing.T) {
	res := &Result{
		RunID: "run1", Status: "complete", AgentBrief: strings.Repeat("x", 400),
		ContextPack:    ContextPack{Files: []string{"big.go"}},
		AnswerMarkdown: strings.Repeat("y", 8000),
	}
	slim := AgentFacingTokens(res)
	fullBytes, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if slim >= estimateTokens(len(fullBytes)) {
		t.Fatalf("slim=%d should be less than full=%d", slim, estimateTokens(len(fullBytes)))
	}
}
