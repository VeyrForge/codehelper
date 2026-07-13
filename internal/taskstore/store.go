package taskstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Store persists tasks as JSON files under .codehelper/tasks/.
type Store struct {
	root string
}

// New returns a task store for repoRoot.
func New(repoRoot string) *Store {
	return &Store{root: repoRoot}
}

func (s *Store) tasksDir() string {
	return filepath.Join(paths.RepoIndexDir(s.root), "tasks")
}

func (s *Store) taskPath(id string) string {
	return filepath.Join(s.tasksDir(), id+".json")
}

// Create allocates a new task id and writes the initial file.
func (s *Store) Create(title, userRequest, mode string) (*Task, error) {
	if err := os.MkdirAll(s.tasksDir(), 0o755); err != nil {
		return nil, err
	}
	id, err := s.nextID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	t := &Task{
		ID:          id,
		Title:       strings.TrimSpace(title),
		UserRequest: strings.TrimSpace(userRequest),
		Status:      StatusOpen,
		Mode:        strings.TrimSpace(mode),
		Todos:       []Todo{},
		Decisions:   []string{},
		Events:      []Event{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if t.Title == "" {
		t.Title = t.UserRequest
	}
	if t.Mode == "" {
		t.Mode = "plan"
	}
	if err := s.Save(t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) nextID() (string, error) {
	prefix := "ch_" + time.Now().UTC().Format("20060102") + "_"
	ids, err := s.List()
	if err != nil {
		return "", err
	}
	max := 0
	for _, id := range ids {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(id, prefix+"%d", &n); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("%s%03d", prefix, max+1), nil
}

// Load reads a task by id.
func (s *Store) Load(id string) (*Task, error) {
	b, err := os.ReadFile(s.taskPath(id))
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	normalizeTask(&t)
	return &t, nil
}

// Save writes task atomically.
func (s *Store) Save(t *Task) error {
	if t == nil || strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("task id required")
	}
	if err := os.MkdirAll(s.tasksDir(), 0o755); err != nil {
		return err
	}
	normalizeTask(t)
	t.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.taskPath(t.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.taskPath(t.ID))
}

// List returns sorted task ids.
func (s *Store) List() ([]string, error) {
	dir := s.tasksDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

// ReplacePlan updates plan and todos (user edit).
func (s *Store) ReplacePlan(id string, plan Plan, todos []Todo) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	t.Plan = plan
	if todos == nil {
		todos = []Todo{}
	}
	t.Todos = todos
	t.Events = append(t.Events, Event{
		Type: "plan_updated", Timestamp: time.Now().UTC(), Actor: "user",
		Details: fmt.Sprintf("%d todos", len(todos)),
	})
	return t, s.Save(t)
}

// PatchTodo updates fields on one todo.
func (s *Store) PatchTodo(id, todoID string, patch func(*Todo) error) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	found := false
	for i := range t.Todos {
		if t.Todos[i].ID != todoID {
			continue
		}
		if err := patch(&t.Todos[i]); err != nil {
			return nil, err
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("todo %q not found", todoID)
	}
	t.Events = append(t.Events, Event{
		Type: "todo_updated", Timestamp: time.Now().UTC(), Actor: "user", TodoID: todoID,
	})
	return t, s.Save(t)
}

// AppendEvent records a timeline entry and saves.
func (s *Store) AppendEvent(t *Task, ev Event) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	t.Events = append(t.Events, ev)
	return s.Save(t)
}

func normalizeTask(t *Task) {
	if t.Todos == nil {
		t.Todos = []Todo{}
	}
	if t.Decisions == nil {
		t.Decisions = []string{}
	}
	if t.Events == nil {
		t.Events = []Event{}
	}
	if t.Messages == nil {
		t.Messages = []Message{}
	}
	if t.ChangedFiles == nil {
		t.ChangedFiles = []string{}
	}
	if t.Commands == nil {
		t.Commands = []CommandRecord{}
	}
	if t.VerificationResults == nil {
		t.VerificationResults = []VerificationResult{}
	}
	if t.ReviewResults == nil {
		t.ReviewResults = []ReviewResult{}
	}
	if t.MemoryProposals == nil {
		t.MemoryProposals = []MemoryProposal{}
	}
	if t.DecisionPoints == nil {
		t.DecisionPoints = []DecisionPoint{}
	}
	if t.Plan.Assumptions == nil {
		t.Plan.Assumptions = []string{}
	}
	if t.Plan.DoneCriteria == nil {
		t.Plan.DoneCriteria = []string{}
	}
}

// FindTodo returns todo by id and its index.
func FindTodo(t *Task, todoID string) (*Todo, int) {
	for i := range t.Todos {
		if t.Todos[i].ID == todoID {
			return &t.Todos[i], i
		}
	}
	return nil, -1
}

// NextExecutable returns the first todo that is approved or planned (if autoApprovePlanned).
func NextExecutable(t *Task, autoApprovePlanned bool) (*Todo, int) {
	for i := range t.Todos {
		td := &t.Todos[i]
		ok := td.Status == TodoApproved || (autoApprovePlanned && td.Status == TodoPlanned)
		if !ok {
			continue
		}
		if PriorTodosDone(t, i) {
			return td, i
		}
	}
	return nil, -1
}

// PriorTodosDone is true when every todo before index is complete or skipped.
func PriorTodosDone(t *Task, index int) bool {
	for i := 0; i < index; i++ {
		st := t.Todos[i].Status
		if st != TodoComplete && st != TodoSkipped {
			return false
		}
	}
	return true
}

// CanExecute reports whether todoID may run (strict one-at-a-time).
func CanExecute(t *Task, todoID string) error {
	_, idx := FindTodo(t, todoID)
	if idx < 0 {
		return fmt.Errorf("todo %q not found", todoID)
	}
	td := t.Todos[idx]
	switch td.Status {
	case TodoComplete, TodoSkipped:
		return fmt.Errorf("todo %q already %s", todoID, td.Status)
	case TodoBlocked:
		return fmt.Errorf("todo %q is blocked: %s", todoID, td.BlockedReason)
	case TodoInProgress, TodoVerifying:
		return fmt.Errorf("todo %q is already %s", todoID, td.Status)
	case TodoFailed, TodoDebugging:
		// allow bounded retry after verify/debug failure
	case TodoApproved:
		// normal execute path
	case TodoPlanned:
		return fmt.Errorf("todo %q must be approved before execute (status=Planned); use approve-all or PATCH status", todoID)
	default:
		return fmt.Errorf("todo %q must be approved, failed, or debugging before execute (status=%s)", todoID, td.Status)
	}
	if !PriorTodosDone(t, idx) {
		return fmt.Errorf("complete prior todos before executing %q", todoID)
	}
	return nil
}

// AllTodosTerminal is true when every todo is complete or skipped.
func AllTodosTerminal(t *Task) bool {
	if len(t.Todos) == 0 {
		return false
	}
	for _, td := range t.Todos {
		if td.Status != TodoComplete && td.Status != TodoSkipped {
			return false
		}
	}
	return true
}

// ApproveAllPlanned sets every planned todo to approved.
func (s *Store) ApproveAllPlanned(id string) (*Task, error) {
	t, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	changed := 0
	for i := range t.Todos {
		if t.Todos[i].Status == TodoPlanned {
			t.Todos[i].Status = TodoApproved
			changed++
		}
	}
	if changed > 0 {
		t.Events = append(t.Events, Event{
			Type: "todos_approved", Timestamp: time.Now().UTC(), Actor: "user",
			Details: fmt.Sprintf("%d todos approved", changed),
		})
	}
	return t, s.Save(t)
}
