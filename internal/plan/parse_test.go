package plan

import (
	"testing"

	"github.com/VeyrForge/codehelper/internal/taskstore"
)

func TestParsePersistPayloadJSON(t *testing.T) {
	raw := `{"plan":{"goal":"Add feature"},"todos":[{"id":"todo-1","title":"Inspect","description":"Look around"}]}`
	p, err := ParsePersistPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Plan.Goal != "Add feature" {
		t.Fatalf("goal=%q", p.Plan.Goal)
	}
	if len(p.Todos) != 1 || p.Todos[0].Status != taskstore.TodoPlanned {
		t.Fatalf("todos=%+v", p.Todos)
	}
}

func TestParsePersistPayloadFence(t *testing.T) {
	text := "Some plan prose\n\n```json\n{\"plan\":{\"goal\":\"g\"},\"todos\":[{\"id\":\"todo-1\",\"title\":\"t\",\"description\":\"d\"}]}\n```\n"
	p, err := ParsePersistPayload(text)
	if err != nil {
		t.Fatal(err)
	}
	if p.Plan.Goal != "g" {
		t.Fatalf("goal=%q", p.Plan.Goal)
	}
}
