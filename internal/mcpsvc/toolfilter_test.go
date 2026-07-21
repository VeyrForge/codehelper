package mcpsvc

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// allRegisteredToolStubs builds a tools/list-shaped slice covering every
// catalog tool plus one unknown name, so the filter is exercised against a
// realistic surface without standing up a full server.
func allRegisteredToolStubs() []mcp.Tool {
	names := append(AllMCPToolNames(), "some_third_party_tool")
	out := make([]mcp.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, mcp.Tool{Name: n})
	}
	return out
}

func TestMinimalToolsEnv(t *testing.T) {
	for _, on := range []string{"1", "true", "on", "YES", "Enabled"} {
		t.Setenv("CODEHELPER_MINIMAL_TOOLS", on)
		if !minimalToolsEnv() {
			t.Fatalf("expected minimal mode for env %q", on)
		}
	}
	for _, off := range []string{"", "0", "off", "false", "nope"} {
		t.Setenv("CODEHELPER_MINIMAL_TOOLS", off)
		if minimalToolsEnv() {
			t.Fatalf("expected full mode for env %q", off)
		}
	}
}

func TestMinimalToolFilter_TrimsToMainTools(t *testing.T) {
	t.Setenv("CODEHELPER_MINIMAL_TOOLS", "on")
	filter := minimalToolFilter(nil)
	got := filter(context.Background(), allRegisteredToolStubs())

	if len(got) != len(MinimalToolSet) {
		t.Fatalf("minimal mode should expose exactly the %d focused tools, got %d", len(MinimalToolSet), len(got))
	}
	seen := map[string]bool{}
	for _, tl := range got {
		seen[tl.Name] = true
		if !IsFocusedTool(tl.Name) {
			t.Fatalf("filter leaked non-focused tool %q", tl.Name)
		}
	}
	for _, want := range MinimalToolSet {
		if !seen[want] {
			t.Fatalf("minimal mode dropped focused tool %q", want)
		}
	}
}

func TestMinimalToolFilter_PassthroughWhenOff(t *testing.T) {
	t.Setenv("CODEHELPER_MINIMAL_TOOLS", "off")
	// nil registry => no project resolves => per-project path is inert, so with the
	// env off the filter must pass the full list through untouched.
	filter := minimalToolFilter(nil)
	in := allRegisteredToolStubs()
	got := filter(context.Background(), in)
	if len(got) != len(in) {
		t.Fatalf("full mode should pass all %d tools, got %d", len(in), len(got))
	}
}

func TestIsMainTool(t *testing.T) {
	if !IsMainTool("project_context") {
		t.Fatal("project_context must be a main tool so the agent can always discover the rest")
	}
	if IsMainTool("dead_code") {
		t.Fatal("dead_code is a specialist, not a main tool")
	}
}

// TestMinimalToolSet guards the focused-surface invariants: it must include every
// main tool AND codehelper's graph-navigation differentiators, contain only real
// catalog tools, and stay well under the ~40-tool count where agent tool-selection
// accuracy degrades.
func TestMinimalToolSet(t *testing.T) {
	catalog := map[string]bool{}
	for _, n := range AllMCPToolNames() {
		catalog[n] = true
	}
	seen := map[string]bool{}
	for _, n := range MinimalToolSet {
		if seen[n] {
			t.Errorf("MinimalToolSet has duplicate %q", n)
		}
		seen[n] = true
		if !catalog[n] {
			t.Errorf("MinimalToolSet has %q which is not a registered tool", n)
		}
	}
	// Superset of the main tools — the agent can always discover the rest.
	for _, n := range MCPMainTools {
		if !seen[n] {
			t.Errorf("MinimalToolSet is missing main tool %q", n)
		}
	}
	// The differentiators that make a trimmed surface still worth using.
	for _, n := range []string{
		"trace", "impact", "test_impact", "find_implementations", "diagnostics",
		"apply_patch_workspace_file", "review_diff", "finish_check", "change_kit",
	} {
		if !seen[n] {
			t.Errorf("MinimalToolSet must keep lifecycle tool %q", n)
		}
	}
	if len(MinimalToolSet) >= 40 {
		t.Errorf("MinimalToolSet=%d tools; keep it well under the 40-tool accuracy cliff", len(MinimalToolSet))
	}
	if len(MinimalToolSet) >= len(AllMCPToolNames()) {
		t.Errorf("MinimalToolSet (%d) should be smaller than the full catalog (%d)", len(MinimalToolSet), len(AllMCPToolNames()))
	}
}
