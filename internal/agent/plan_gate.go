package agent

import (
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/taskstore"
)

// IsWorkspaceWriteTool reports MCP tools that mutate workspace files.
func IsWorkspaceWriteTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "write_workspace_file", "apply_patch_workspace_file":
		return true
	default:
		return false
	}
}

// WritesAllowed reports whether agent-mode write tools may run for this turn.
func WritesAllowed(mode Mode, workspaceRoot, taskID string, forceWrite bool) (bool, string) {
	if mode != ModeAgent && mode != ModeDebug {
		return false, ""
	}
	if forceWrite {
		return true, ""
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false, "Create and approve a plan first (missing task_id). Save a plan under .codehelper/tasks/ and approve at least one todo, or pass force_write for CLI automation."
	}
	t, err := taskstore.New(workspaceRoot).Load(taskID)
	if err != nil {
		return false, fmt.Sprintf("Create and approve a plan first (task %q not found).", taskID)
	}
	if len(t.Todos) == 0 {
		return false, "Create and approve a plan first (task has no todos)."
	}
	hasApproved := false
	for _, td := range t.Todos {
		switch td.Status {
		case taskstore.TodoApproved, taskstore.TodoInProgress, taskstore.TodoVerifying,
			taskstore.TodoDebugging, taskstore.TodoFailed, taskstore.TodoComplete:
			hasApproved = true
		}
	}
	if !hasApproved {
		return false, "Create and approve a plan first (no approved todos). Approve todos in the Plan tab or via POST /v1/tasks/{id}/approve-all."
	}
	return true, ""
}
