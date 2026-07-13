package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/llm"
)

func TestEscapeHelpers(t *testing.T) {
	if got := escMd("a`b\\c"); got != "a\\`b\\\\c" {
		t.Errorf("escMd = %q", got)
	}
	if got := escapeCodeFence("x```y"); strings.Contains(got, "```") {
		t.Errorf("escapeCodeFence left a fence: %q", got)
	}
}

func TestGateNoWorkspaceRoot(t *testing.T) {
	res, err := RunVerificationGate(context.Background(), GateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.MarkdownAppendix, "Open a folder workspace") {
		t.Errorf("appendix = %q", res.MarkdownAppendix)
	}
}

func TestGateSkipsVerifyWithoutWrites(t *testing.T) {
	res, err := RunVerificationGate(context.Background(), GateOptions{
		WorkspaceRoot: t.TempDir(),
		AutoVerify:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.MarkdownAppendix, "No tracked file writes") {
		t.Errorf("appendix = %q", res.MarkdownAppendix)
	}
	if !strings.Contains(res.MarkdownAppendix, "(no touched paths)") {
		t.Errorf("appendix = %q", res.MarkdownAppendix)
	}
	if res.RemainingErrors {
		t.Error("RemainingErrors should be false")
	}
}

func TestGateBoundedFixRoundsWithClientDiagnostics(t *testing.T) {
	// LLM always answers without tool calls so each fix round terminates fast.
	srv := stubLLM(t, []string{completionWithContent("Fixed the issue in `a.go`.")})
	defer srv.Close()

	diagCalls := 0
	res, err := RunVerificationGate(context.Background(), GateOptions{
		WorkspaceRoot:        t.TempDir(),
		WrittenRelativePaths: []string{"a.go"},
		AutoVerify:           false,
		AutoReview:           false,
		MaxFixRounds:         2,
		UserPromptEnriched:   "fix a.go",
		AssistantReplyPrefix: "done",
		Diagnostics: func(paths []string) (string, bool) {
			diagCalls++
			// Errors persist forever: gate must stop at MaxFixRounds.
			return "a.go:1:1 boom", true
		},
		LLM:   llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools: &stubTools{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 1 initial evaluation + 1 re-check per round.
	if diagCalls != 3 {
		t.Errorf("diagnostics evaluated %d times, want 3", diagCalls)
	}
	if !res.RemainingErrors {
		t.Error("RemainingErrors should stay true")
	}
	if !strings.Contains(res.MarkdownAppendix, "Problems after fix rounds") {
		t.Errorf("appendix missing post-fix section: %q", res.MarkdownAppendix)
	}
}

func TestGateZeroFixRoundsReportsSkip(t *testing.T) {
	res, err := RunVerificationGate(context.Background(), GateOptions{
		WorkspaceRoot:        t.TempDir(),
		WrittenRelativePaths: []string{"a.go"},
		AutoVerify:           false,
		AutoReview:           false,
		MaxFixRounds:         0,
		Diagnostics: func([]string) (string, bool) {
			return "a.go:1:1 boom", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.MarkdownAppendix, "no fix loop ran") {
		t.Errorf("appendix = %q", res.MarkdownAppendix)
	}
	if !res.RemainingErrors {
		t.Error("RemainingErrors should be true")
	}
}
