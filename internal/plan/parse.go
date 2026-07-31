package plan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/internal/taskstore"
)

var jsonFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*([\\s\\S]*?)```")

// PersistPayload is the structured plan artifact for task persistence.
type PersistPayload struct {
	Plan  taskstore.Plan   `json:"plan"`
	Todos []taskstore.Todo `json:"todos"`
}

// ParsePersistPayload extracts plan JSON from assistant markdown or raw JSON.
func ParsePersistPayload(text string) (PersistPayload, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return PersistPayload{}, fmt.Errorf("empty plan text")
	}
	if strings.HasPrefix(raw, "{") {
		return decodePersistPayload(raw)
	}
	m := jsonFenceRe.FindStringSubmatch(raw)
	if len(m) < 2 {
		return PersistPayload{}, fmt.Errorf("no ```json plan block found; include {\"plan\":{...},\"todos\":[...]}")
	}
	return decodePersistPayload(strings.TrimSpace(m[1]))
}

func decodePersistPayload(raw string) (PersistPayload, error) {
	var p PersistPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return PersistPayload{}, fmt.Errorf("invalid plan JSON: %w", err)
	}
	if strings.TrimSpace(p.Plan.Goal) == "" && len(p.Todos) == 0 {
		return PersistPayload{}, fmt.Errorf("plan must include goal or todos")
	}
	if p.Todos == nil {
		p.Todos = []taskstore.Todo{}
	}
	for i := range p.Todos {
		if strings.TrimSpace(p.Todos[i].Status) == "" {
			p.Todos[i].Status = taskstore.TodoPlanned
		}
		if strings.TrimSpace(p.Todos[i].ID) == "" {
			p.Todos[i].ID = fmt.Sprintf("todo-%d", i+1)
		}
	}
	return p, nil
}
