package orchestrator

import (
	"strings"
	"testing"
)

func TestWorkflowExplainUsesBriefBody(t *testing.T) {
	steps := WorkflowSteps(WorkflowExplainCode)
	if steps[1].Args["body"] != "brief" {
		t.Fatalf("body=%v", steps[1].Args["body"])
	}
}

func TestWorkflowDeadCodeIncludesScout(t *testing.T) {
	steps := WorkflowSteps(WorkflowDeadCodeScan)
	if steps[2].Tool != "scout" {
		t.Fatalf("steps=%v", steps)
	}
}

func TestExtractSourceExcerpt(t *testing.T) {
	raw := `{"bundle":{"source":"func Run() { return 1 }"}}`
	got := extractSourceExcerpt(raw)
	if !strings.Contains(got, "func Run") {
		t.Fatalf("got=%q", got)
	}
}
