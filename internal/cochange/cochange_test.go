package cochange

import "testing"

// TestFromCommits verifies the coupling core on synthetic history: a strongly
// co-changing file surfaces with high confidence; a coincidental one is filtered
// by min-support; and a giant sweeping commit is ignored as noise.
func TestFromCommits(t *testing.T) {
	commits := [][]string{
		{"register.go", "tool_a.go"},
		{"register.go", "tool_b.go"},
		{"register.go", "tool_c.go"},
		{"register.go", "tool_d.go"},
		{"register.go", "readme.md"}, // readme co-changes once → below min-support
		{"unrelated.go", "other.go"},
		// a sweeping refactor touching everything — must be ignored (noise)
		{"register.go", "a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go"},
	}
	opts := Options{MinSupport: 2, MinConfidence: 0.4, MaxFilesPerCommit: 5}

	// Query the hub: each tool_*.go changed with register.go, but only once each →
	// below min-support 2, so none should surface. Confirms support gating works.
	if rules := fromCommits(commits, "register.go", opts); len(rules) != 0 {
		t.Errorf("no single file reaches support>=2 with register.go, got %+v", rules)
	}

	// Build history where tool_x.go reliably co-changes with register.go.
	strong := [][]string{
		{"register.go", "tool_x.go"},
		{"register.go", "tool_x.go"},
		{"register.go", "tool_x.go"},
		{"register.go", "solo.go"},
	}
	rules := fromCommits(strong, "tool_x.go", Options{MinSupport: 2, MinConfidence: 0.5})
	if len(rules) != 1 || rules[0].File != "register.go" {
		t.Fatalf("expected register.go coupled to tool_x.go, got %+v", rules)
	}
	if rules[0].Support != 3 || rules[0].Confidence < 0.99 {
		t.Errorf("tool_x.go always changed with register.go: want support 3 conf 1.0, got %+v", rules[0])
	}
}

// TestFromCommits_belowSupportReturnsNil: a file with too little history yields no
// rules (so the MCP simply omits the section rather than guessing).
func TestFromCommits_belowSupportReturnsNil(t *testing.T) {
	if r := fromCommits([][]string{{"a.go", "b.go"}}, "a.go", Options{MinSupport: 3}); r != nil {
		t.Errorf("below min-support should be nil, got %+v", r)
	}
}
