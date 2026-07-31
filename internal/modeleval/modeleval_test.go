package modeleval

import (
	"context"
	"strings"
	"testing"
)

func TestLoadSuite_RejectsRetrievalSuite(t *testing.T) {
	t.Parallel()
	_, err := LoadSuite(strings.NewReader(`{"queries":[{"query":"x","must_contain_path":["a.go"]}]}`))
	if err == nil {
		t.Fatal("expected error for retrieval-shaped suite")
	}
	if !strings.Contains(err.Error(), "codehelper eval") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_VacuousNoCommand(t *testing.T) {
	ctx := context.Background()
	s := Suite{Tasks: []Task{{Name: "t1", Prompt: "x"}}}
	res, err := Run(ctx, "m", s, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed != 1 || res.Failed != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestRun_MissingExpectedOnEmptyOutput(t *testing.T) {
	ctx := context.Background()
	s := Suite{Tasks: []Task{{
		Name:             "t1",
		Prompt:           "x",
		ExpectedFindings: []string{"hello"},
	}}}
	res, err := Run(ctx, "m", s, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 {
		t.Fatalf("expected fail on empty output, got %+v", res)
	}
}
