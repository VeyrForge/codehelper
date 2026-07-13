package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptText_KnownIDs(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"intake_project_brief", "planning_contract", "agent_guardrails"} {
		text, err := PromptText(id)
		if err != nil {
			t.Fatalf("PromptText(%s): %v", id, err)
		}
		if strings.TrimSpace(text) == "" {
			t.Fatalf("empty prompt text for %s", id)
		}
	}
}

func TestDefaultSuitePromptCases_Pass(t *testing.T) {
	t.Parallel()
	suite := Suite{Prompts: Default().Prompts}
	res, err := Run(t.Context(), "", "ignored", suite, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 0 {
		t.Fatalf("default prompt cases must pass; failed=%d", res.Failed)
	}
}

func TestPromptContracts_IncludeUncertaintyAndFailureLanguage(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"intake_project_brief", "planning_contract", "agent_guardrails"} {
		text, err := PromptText(id)
		if err != nil {
			t.Fatalf("PromptText(%s): %v", id, err)
		}
		if !strings.Contains(text, "[UNCERTAIN]") {
			t.Fatalf("prompt %s missing [UNCERTAIN] contract marker", id)
		}
	}
}

func TestQueryCasePasses_AnyGroup(t *testing.T) {
	t.Parallel()
	paths := []string{"internal/mcpsvc/register.go", "other.go"}
	qc := QueryCase{
		MustContainAnyPath: [][]string{
			{"internal/retrieval/context.go"},
			{"internal/mcpsvc/register.go"},
		},
	}
	if !queryCasePasses(paths, qc) {
		t.Fatal("expected second group to match")
	}
}

func TestDefaultSuiteRetrievalCases_Pass(t *testing.T) {
	if os.Getenv("CODEHELPER_EVAL_INTEGRATION") == "" {
		t.Skip("set CODEHELPER_EVAL_INTEGRATION=1 to run retrieval integration against workspace index")
	}
	root := os.Getenv("CODEHELPER_EVAL_ROOT")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		for {
			if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
				root = wd
				break
			}
			parent := filepath.Dir(wd)
			if parent == wd {
				t.Fatal("could not find repo root; set CODEHELPER_EVAL_ROOT")
			}
			wd = parent
		}
	}
	suite := Suite{Queries: Default().Queries}
	res, err := Run(t.Context(), root, "codehelper", suite, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed != 0 {
		t.Fatalf("default retrieval cases failed=%d: %+v", res.Failed, res.Cases)
	}
}

func TestGoldenSuite_Loads(t *testing.T) {
	t.Parallel()
	s, err := Golden()
	if err != nil {
		t.Fatalf("Golden: %v", err)
	}
	if len(s.Queries) == 0 {
		t.Fatalf("expected golden suite to contain query cases")
	}
}
