package taskstore

import (
	"testing"
)

func TestStoreCreatePatchAndExecuteGate(t *testing.T) {
	root := t.TempDir()
	st := New(root)
	task, err := st.Create("Fix bug", "fix the login bug", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Fatal("expected task id")
	}
	task.Todos = []Todo{
		{ID: "t1", Title: "Step 1", Status: TodoPlanned},
		{ID: "t2", Title: "Step 2", Status: TodoPlanned},
	}
	if err := st.Save(task); err != nil {
		t.Fatal(err)
	}

	loaded, err := st.Load(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Todos) != 2 {
		t.Fatalf("todos = %d", len(loaded.Todos))
	}

	updated, err := st.PatchTodo(task.ID, "t1", func(td *Todo) error {
		td.UserNotes = "focus on auth package"
		td.Status = TodoApproved
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Todos[0].UserNotes != "focus on auth package" {
		t.Fatalf("notes = %q", updated.Todos[0].UserNotes)
	}
	if err := CanExecute(updated, "t1"); err != nil {
		t.Fatalf("t1 should execute: %v", err)
	}
	if err := CanExecute(updated, "t2"); err == nil {
		t.Fatal("t2 should be blocked until t1 completes")
	}

	updated.Todos[0].Status = TodoComplete
	if err := st.Save(updated); err != nil {
		t.Fatal(err)
	}
	updated.Todos[1].Status = TodoApproved
	if err := st.Save(updated); err != nil {
		t.Fatal(err)
	}
	if err := CanExecute(updated, "t2"); err != nil {
		t.Fatalf("t2 should execute after t1: %v", err)
	}

	ids, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("list = %v", ids)
	}
}
