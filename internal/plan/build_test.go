package plan

import (
	"testing"
)

func TestBuildProducesTodos(t *testing.T) {
	root := t.TempDir()
	out, err := Build(Input{
		Request:  "add rate limiting to the API",
		RepoRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Plan.Goal == "" {
		t.Fatal("expected plan goal")
	}
	if len(out.Todos) == 0 {
		t.Fatal("expected at least one todo")
	}
	for _, td := range out.Todos {
		if td.ID == "" || td.Title == "" {
			t.Fatalf("invalid todo: %+v", td)
		}
		if td.Status != "planned" {
			t.Fatalf("todo status = %q", td.Status)
		}
	}
}

func TestBuildRequiresRequest(t *testing.T) {
	_, err := Build(Input{RepoRoot: t.TempDir()})
	if err == nil {
		t.Fatal("expected error for empty request")
	}
}
