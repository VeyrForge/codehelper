package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestRequestArgStringIdentifierFirst(t *testing.T) {
	req := &mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"hunks": []any{map[string]any{"old_string": strings.Repeat("x", 500), "new_string": "y"}},
		"path":  "includes/mt/class-mt-currency.php",
		"repo":  "translate-plugin",
	}
	got := requestArgString(req)

	// The path must appear and precede the bulky hunks, so it survives the preview
	// cap that previously buried it behind alphabetically-sorted keys.
	pi := strings.Index(got, "path=includes/mt/class-mt-currency.php")
	hi := strings.Index(got, "hunks=")
	if pi < 0 {
		t.Fatalf("path missing from preview: %q", got)
	}
	if hi >= 0 && pi > hi {
		t.Fatalf("path should precede hunks in preview: %q", got)
	}
	if strings.Contains(got, strings.Repeat("x", 100)) {
		t.Fatalf("bulky string should be elided, not inlined: %q", got)
	}
	if !strings.Contains(got, "hunks=<1 items>") {
		t.Errorf("hunks should be summarized by count: %q", got)
	}
}

func TestCanonicalClient(t *testing.T) {
	cases := map[string]string{
		"":                  "unknown",
		"claude-code":       "claude-code",
		"Claude Code":       "claude-code",
		"claude-ai":         "claude-code",
		"Cursor":            "cursor",
		"cursor-vscode":     "cursor",
		"codex-cli":         "codex",
		"some-other-client": "some-other-client",
	}
	for in, want := range cases {
		if got := canonicalClient(in); got != want {
			t.Errorf("canonicalClient(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUsageResultText(t *testing.T) {
	if got := usageResultText(nil); got != "" {
		t.Errorf("nil = %q, want empty", got)
	}
	if got := usageResultText("not a result"); got != "" {
		t.Errorf("wrong type = %q, want empty", got)
	}
	res := mcp.NewToolResultText("hello world")
	if got := usageResultText(res); got != "hello world" {
		t.Errorf("text result = %q, want %q", got, "hello world")
	}
}
