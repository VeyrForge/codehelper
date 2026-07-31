package questiongate

import (
	"testing"

	"github.com/VeyrForge/codehelper/internal/profile"
)

func TestEvaluate_FrameworkRedundant(t *testing.T) {
	p := &profile.ProjectProfile{ProjectType: "go"}
	out := Evaluate(Input{
		Task:              "fix thing",
		ProposedQuestions: []string{"Which framework is this?"},
	}, p)
	if out.AskUser {
		t.Fatalf("expected no ask: %s", out.Reason)
	}
}

func TestEvaluate_CriticalTopic(t *testing.T) {
	out := Evaluate(Input{
		ProposedQuestions: []string{"Does this change auth session storage?"},
	}, nil)
	if !out.AskUser {
		t.Fatal("expected ask for auth topic")
	}
}
