package plan

import (
	"context"
	"testing"

	"github.com/VeyrForge/codehelper/internal/llm"
)

func TestBuildEnrichedFallsBackWithoutLLM(t *testing.T) {
	root := t.TempDir()
	out, err := BuildEnriched(context.Background(), Input{
		Request:  "add caching layer",
		RepoRoot: root,
	}, EnrichConfig{EnrichLLM: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Plan.Goal == "" || len(out.Todos) == 0 {
		t.Fatal("expected skeleton plan")
	}
}

func TestBuildEnrichedSkipsWhenQuick(t *testing.T) {
	root := t.TempDir()
	out, err := BuildEnriched(context.Background(), Input{
		Request:  "add caching",
		RepoRoot: root,
		Quick:    true,
	}, EnrichConfig{
		EnrichLLM: true,
		LLM:       llm.Config{BaseURL: "http://127.0.0.1:1", Model: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Todos) == 0 {
		t.Fatal("expected todos")
	}
}
