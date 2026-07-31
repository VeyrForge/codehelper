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
		t.Setenv("CODEHELPER_TOOL_PROFILE", "")
		if !minimalToolsEnv() {
			t.Fatalf("expected minimal mode for env %q", on)
		}
		if activeToolProfile(context.Background(), nil) != ToolProfileFocused {
			t.Fatalf("legacy minimal env should resolve to focused")
		}
	}
	for _, off := range []string{"0", "off", "false", "full"} {
		t.Setenv("CODEHELPER_MINIMAL_TOOLS", off)
		t.Setenv("CODEHELPER_TOOL_PROFILE", "")
		if minimalToolsEnv() {
			t.Fatalf("expected not-focused force for env %q", off)
		}
		if !minimalToolsEnvForcesFull() {
			t.Fatalf("expected full force for env %q", off)
		}
		if activeToolProfile(context.Background(), nil) != ToolProfileFull {
			t.Fatalf("legacy minimal off should resolve to full, got %q", activeToolProfile(context.Background(), nil))
		}
	}
	t.Setenv("CODEHELPER_MINIMAL_TOOLS", "")
	t.Setenv("CODEHELPER_TOOL_PROFILE", "")
	if minimalToolsEnv() || minimalToolsEnvForcesFull() {
		t.Fatal("empty minimal env should not force either way")
	}
}

func TestDefaultToolProfileIsFocused(t *testing.T) {
	t.Setenv("CODEHELPER_TOOL_PROFILE", "")
	t.Setenv("CODEHELPER_MINIMAL_TOOLS", "")
	if DefaultToolProfile != ToolProfileFocused {
		t.Fatalf("DefaultToolProfile=%q want focused", DefaultToolProfile)
	}
	if got := activeToolProfile(context.Background(), nil); got != ToolProfileFocused {
		t.Fatalf("default advertise profile=%q want focused", got)
	}
	filter := minimalToolFilter(nil)
	got := filter(context.Background(), allRegisteredToolStubs())
	if len(got) != len(FocusedToolSet) {
		t.Fatalf("default filter should expose %d focused tools, got %d", len(FocusedToolSet), len(got))
	}
}

func TestToolProfileEnv(t *testing.T) {
	t.Setenv("CODEHELPER_MINIMAL_TOOLS", "")
	t.Setenv("CODEHELPER_TOOL_PROFILE", "core")
	if activeToolProfile(context.Background(), nil) != ToolProfileCore {
		t.Fatal("expected core profile")
	}
	filter := minimalToolFilter(nil)
	got := filter(context.Background(), allRegisteredToolStubs())
	if len(got) != len(CoreToolSet) {
		t.Fatalf("core profile should expose %d tools, got %d", len(CoreToolSet), len(got))
	}
	for _, tl := range got {
		if !IsCoreTool(tl.Name) {
			t.Fatalf("core profile leaked non-core tool %q", tl.Name)
		}
	}
	t.Setenv("CODEHELPER_TOOL_PROFILE", "full")
	got = filter(context.Background(), allRegisteredToolStubs())
	if len(got) != len(allRegisteredToolStubs()) {
		t.Fatalf("full profile should pass through")
	}
}

func TestMinimalToolFilter_TrimsToFocusedTools(t *testing.T) {
	t.Setenv("CODEHELPER_TOOL_PROFILE", "")
	t.Setenv("CODEHELPER_MINIMAL_TOOLS", "on")
	filter := minimalToolFilter(nil)
	got := filter(context.Background(), allRegisteredToolStubs())

	if len(got) != len(FocusedToolSet) {
		t.Fatalf("focused mode should expose exactly the %d tools, got %d", len(FocusedToolSet), len(got))
	}
	seen := map[string]bool{}
	for _, tl := range got {
		seen[tl.Name] = true
		if !IsFocusedTool(tl.Name) {
			t.Fatalf("filter leaked non-focused tool %q", tl.Name)
		}
	}
	for _, want := range FocusedToolSet {
		if !seen[want] {
			t.Fatalf("focused mode dropped tool %q", want)
		}
	}
}

func TestMinimalToolFilter_PassthroughWhenFull(t *testing.T) {
	t.Setenv("CODEHELPER_TOOL_PROFILE", "full")
	t.Setenv("CODEHELPER_MINIMAL_TOOLS", "")
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
	if !IsMainTool("kickoff") || !IsMainTool("investigate") || !IsMainTool("orchestrate") {
		t.Fatal("composites kickoff/investigate/orchestrate must be main tools")
	}
	if IsMainTool("dead_code") {
		t.Fatal("dead_code is a specialist, not a main tool")
	}
}

func TestConceptualToolSlots(t *testing.T) {
	wantSlots := []string{"project", "search", "understand", "impact", "change", "check", "browser", "workflow"}
	if len(ConceptualToolSlots) != len(wantSlots) {
		t.Fatalf("ConceptualToolSlots=%d want %d", len(ConceptualToolSlots), len(wantSlots))
	}
	catalog := map[string]bool{}
	for _, n := range AllMCPToolNames() {
		catalog[n] = true
	}
	seenSlot := map[string]bool{}
	for i, s := range ConceptualToolSlots {
		if s.Slot != wantSlots[i] {
			t.Errorf("slot[%d]=%q want %q", i, s.Slot, wantSlots[i])
		}
		if seenSlot[s.Slot] {
			t.Errorf("duplicate slot %q", s.Slot)
		}
		seenSlot[s.Slot] = true
		if !catalog[s.Tool] {
			t.Errorf("slot %q tool %q not in catalog", s.Slot, s.Tool)
		}
		if !IsCoreTool(s.Tool) {
			t.Errorf("slot %q tool %q must be in CoreToolSet", s.Slot, s.Tool)
		}
		if !IsFocusedTool(s.Tool) {
			t.Errorf("slot %q tool %q must be in FocusedToolSet", s.Slot, s.Tool)
		}
	}
}

// TestFocusedToolSet guards the default advertise-surface invariants: conceptual
// slots covered, composites present, only real catalog tools, and ~8–12 count.
func TestFocusedToolSet(t *testing.T) {
	catalog := map[string]bool{}
	for _, n := range AllMCPToolNames() {
		catalog[n] = true
	}
	seen := map[string]bool{}
	for _, n := range FocusedToolSet {
		if seen[n] {
			t.Errorf("FocusedToolSet has duplicate %q", n)
		}
		seen[n] = true
		if !catalog[n] {
			t.Errorf("FocusedToolSet has %q which is not a registered tool", n)
		}
	}
	for _, s := range ConceptualToolSlots {
		if !seen[s.Tool] {
			t.Errorf("FocusedToolSet missing conceptual slot tool %q (%s)", s.Tool, s.Slot)
		}
	}
	for _, n := range []string{"orchestrate", "investigate", "apply_patch_workspace_file", "finish_check"} {
		if !seen[n] {
			t.Errorf("FocusedToolSet must keep day-to-day tool %q", n)
		}
	}
	if len(FocusedToolSet) < 8 || len(FocusedToolSet) > 12 {
		t.Errorf("FocusedToolSet=%d tools; keep default advertise in the 8–12 composite band", len(FocusedToolSet))
	}
	if len(CoreToolSet) != 8 {
		t.Errorf("CoreToolSet=%d want 8 (one per conceptual slot)", len(CoreToolSet))
	}
	if len(FocusedToolSet) >= len(AllMCPToolNames()) {
		t.Errorf("FocusedToolSet (%d) should be smaller than the full catalog (%d)", len(FocusedToolSet), len(AllMCPToolNames()))
	}
	// Specialists stay registered but hidden from focused advertise.
	for _, hidden := range []string{"search_hybrid", "context_bundle", "trace", "dead_code", "ast_query"} {
		if IsFocusedTool(hidden) {
			t.Errorf("%s should be hidden from focused advertise (still callable by name)", hidden)
		}
	}
}

func TestMinimalToolSetAlias(t *testing.T) {
	if len(MinimalToolSet) != len(FocusedToolSet) {
		t.Fatalf("MinimalToolSet should alias FocusedToolSet")
	}
	for i := range FocusedToolSet {
		if MinimalToolSet[i] != FocusedToolSet[i] {
			t.Fatalf("MinimalToolSet[%d]=%q FocusedToolSet[%d]=%q", i, MinimalToolSet[i], i, FocusedToolSet[i])
		}
	}
}
