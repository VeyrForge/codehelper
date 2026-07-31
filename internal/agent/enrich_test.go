package agent

import (
	"strings"
	"testing"
)

func TestEnrichUserText(t *testing.T) {
	out := EnrichUserText(EnrichInput{
		Text:        "explain main",
		Workspace:   "/tmp/repo",
		Attachments: []string{"cmd/main.go"},
		ModelHint:   "gpt-4",
	})
	if !strings.Contains(out, "<workspace_folder>/tmp/repo</workspace_folder>") {
		t.Fatalf("missing workspace folder: %q", out)
	}
	if !strings.Contains(out, "<path>cmd/main.go</path>") {
		t.Fatalf("missing attachment: %q", out)
	}
	if !strings.Contains(out, "<llm_model>gpt-4</llm_model>") {
		t.Fatalf("missing model hint: %q", out)
	}
}
