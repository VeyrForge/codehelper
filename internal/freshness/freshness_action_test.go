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
