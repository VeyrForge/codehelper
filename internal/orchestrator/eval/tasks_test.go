package eval

import "testing"

func TestTasksForProject_EmptySymbols(t *testing.T) {
	tasks := tasksForProject(Anchor{})
	if len(tasks) != 5 {
		t.Fatalf("expected 5 tasks, got %d", len(tasks))
	}
	for _, tc := range tasks {
		if tc.Task == "" {
			t.Fatalf("empty task text in %+v", tc)
		}
	}
}

func TestTasksForProject_SingleSymbol(t *testing.T) {
	tasks := tasksForProject(Anchor{Symbols: []string{"RegisterAll"}, ProjectType: "go"})
	if len(tasks) != 5 {
		t.Fatalf("expected 5 tasks, got %d", len(tasks))
	}
	refactor := tasks[3]
	if refactor.Name != "refactor_impact" {
		t.Fatalf("unexpected task: %+v", refactor)
	}
	if refactor.MustContain[0] != "registerall" {
		t.Fatalf("refactor anchor wrong: %v", refactor.MustContain)
	}
}

func TestPickSecondAnchor(t *testing.T) {
	got := pickSecondAnchor([]string{"Foo", "Bar"}, "Foo")
	if got != "Bar" {
		t.Fatalf("got %q want Bar", got)
	}
	if pickSecondAnchor(nil, "main") != "main" {
		t.Fatal("expected fallback to first when no second symbol")
	}
}
