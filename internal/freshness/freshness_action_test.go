package freshness

import (
	"testing"
)

func TestInspectNoIndexSetsActionRequired(t *testing.T) {
	dir := t.TempDir()
	r := Inspect(dir)
	if !r.Stale {
		t.Fatal("expected stale")
	}
	if r.ActionRequired == nil {
		t.Fatal("expected action_required")
	}
	if r.ActionRequired.Code != "no_index" {
		t.Fatalf("code: %q", r.ActionRequired.Code)
	}
	if len(r.ActionRequired.Commands) != 1 || len(r.ActionRequired.Commands[0]) < 2 {
		t.Fatalf("commands: %#v", r.ActionRequired.Commands)
	}
}

func TestAttachActionRequiredWorkingTreeUsesForce(t *testing.T) {
	r := Report{
		Stale:       true,
		StaleReason: "working tree changed since index (uncommitted edits, no watch daemon)",
		SuggestedFix: "codehelper analyze --force (or start codehelper watch --daemon)",
	}
	attachActionRequired(&r)
	if r.ActionRequired == nil {
		t.Fatal("expected action_required")
	}
	cmd := r.ActionRequired.Commands
	if len(cmd) != 1 || len(cmd[0]) != 3 || cmd[0][2] != "--force" {
		t.Fatalf("expected analyze --force argv, got %#v", cmd)
	}
}

func TestAttachActionRequiredHeadLagNoForce(t *testing.T) {
	r := Report{
		Stale:       true,
		StaleReason: "git HEAD advanced past indexed commit",
		SuggestedFix: "codehelper analyze (or start codehelper watch --daemon)",
	}
	attachActionRequired(&r)
	if r.ActionRequired == nil {
		t.Fatal("expected action_required")
	}
	cmd := r.ActionRequired.Commands
	if len(cmd) != 1 || len(cmd[0]) != 2 {
		t.Fatalf("expected plain analyze argv, got %#v", cmd)
	}
}
