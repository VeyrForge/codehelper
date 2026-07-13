package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/verify"
)

const (
	maxReviewAppend  = 50_000
	maxVerifyCmdTail = 16_000
	defaultFixRounds = 3
	maxFixRoundsCap  = 24
)

// DiagnosticsFunc lets clients (e.g. the VS Code extension) report
// error-level diagnostics for the given repo-relative paths. It returns a
// plain-text summary plus whether any error-level diagnostics exist. When nil,
// the gate derives failure state from the profile verify commands instead.
type DiagnosticsFunc func(relativePaths []string) (text string, hasErrors bool)

// GateOptions configures the post-agent verification gate, a faithful port of
// the original VS Code host policy (verify + diagnostics + bounded fix rounds
// + optional review).
type GateOptions struct {
	WorkspaceRoot string
	// WrittenRelativePaths are repo-relative paths written during the turn.
	WrittenRelativePaths []string
	// PriorTurns are the messages before this turn's user message.
	PriorTurns []Turn
	// UserPromptEnriched is the same enriched prompt passed to Run for this turn.
	UserPromptEnriched string
	// AssistantReplyPrefix is the assistant reply produced before the gate runs.
	AssistantReplyPrefix string

	// AutoVerify runs profile verify commands (default true via DefaultGateOptions).
	AutoVerify bool
	// AutoReview runs review.ReviewDiff against HEAD~1 (default true).
	AutoReview bool
	// MaxFixRounds bounds diagnostic-fix continuation rounds (0 disables).
	MaxFixRounds int

	Diagnostics DiagnosticsFunc
	Log         func(string)
	Hooks       Hooks

	// LLM and Tools are required only when fix rounds may run.
	LLM   llm.Config
	Tools ToolCaller
}

// GateResult carries the markdown appendix to render under the agent reply.
type GateResult struct {
	MarkdownAppendix string `json:"markdown_appendix"`
	// RemainingErrors reports whether error state persisted after fix rounds.
	RemainingErrors bool `json:"remaining_errors"`
	// WrittenRelativePaths includes paths added by fix rounds.
	WrittenRelativePaths []string `json:"written_relative_paths,omitempty"`
}

// ApplyGateDefaults fills the policy defaults used by the original host.
func ApplyGateDefaults(o *GateOptions) {
	if o.MaxFixRounds < 0 {
		o.MaxFixRounds = 0
	}
	if o.MaxFixRounds > maxFixRoundsCap {
		o.MaxFixRounds = maxFixRoundsCap
	}
}

func escMd(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "`", "\\`")
}

var codeFenceRe = regexp.MustCompile("```")

func escapeCodeFence(text string) string {
	return codeFenceRe.ReplaceAllString(text, "`\\`\\`\\`")
}

// profileVerifyCommands reads (or generates) the project profile and returns
// the suggested verify command lines (tests first, then lint).
func profileVerifyCommands(root string) ([]string, error) {
	pr, err := profile.Read(root)
	if err != nil || pr == nil {
		if _, werr := profile.Write(root); werr != nil {
			return nil, werr
		}
		pr, err = profile.Read(root)
		if err != nil {
			return nil, err
		}
	}
	if pr == nil {
		return nil, nil
	}
	var cmds []string
	cmds = append(cmds, pr.TestCommands...)
	cmds = append(cmds, pr.LintCommands...)
	out := cmds[:0]
	for _, c := range cmds {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

type verifyRunOutcome struct {
	failures []string // formatted "$ cmd → exit N" plus output tail
}

func (v verifyRunOutcome) hasErrors() bool { return len(v.failures) > 0 }

func (v verifyRunOutcome) text() string {
	return strings.Join(v.failures, "\n\n")
}

// runProfileVerify executes verify commands via internal/verify and records markdown
// via push; it returns the failure outcome used as a diagnostics substitute.
func runProfileVerify(ctx context.Context, root string, cmds []string, push func(string)) verifyRunOutcome {
	var outcome verifyRunOutcome
	results := verify.RunCommandLines(ctx, cmds, verify.RunCommandsOptions{
		RepoRoot: root,
		ExecMode: verify.ExecArgv,
	})
	for _, res := range results {
		push(fmt.Sprintf("**`$ %s`**", escMd(res.Cmdline)))
		if ctx.Err() != nil {
			push("*(stopped before command)*")
			break
		}
		push(fmt.Sprintf("exit **%d**", res.ExitCode))
		tail := res.Output
		if len(tail) > maxVerifyCmdTail {
			tail = tail[:maxVerifyCmdTail] + "\n…(truncated)"
		}
		if strings.TrimSpace(tail) != "" {
			push("```text\n" + escapeCodeFence(strings.TrimSpace(tail)) + "\n```")
		}
		if res.TimedOut || res.ExitCode != 0 {
			failTail := strings.TrimSpace(tail)
			if len(failTail) > 4000 {
				failTail = failTail[:4000] + "\n…(truncated)"
			}
			outcome.failures = append(outcome.failures,
				fmt.Sprintf("$ %s → exit %d\n%s", res.Cmdline, res.ExitCode, failTail))
		}
	}
	return outcome
}

// RunVerificationGate runs profile verify commands, summarizes error state on
// touched paths, optionally drives bounded Agent fix rounds, and optionally
// appends an in-process review of the HEAD~1 diff.
func RunVerificationGate(ctx context.Context, opts GateOptions) (*GateResult, error) {
	log := opts.Log
	if log == nil {
		log = func(string) {}
	}
	var chunks []string
	push := func(s string) {
		chunks = append(chunks, s)
		preview := s
		if strings.HasPrefix(preview, "`") {
			preview = strings.ReplaceAll(preview, "\n", " ")
			if len(preview) > 240 {
				preview = preview[:240]
			}
		}
		log(preview)
	}
	ApplyGateDefaults(&opts)

	root := strings.TrimSpace(opts.WorkspaceRoot)
	if root == "" {
		return &GateResult{MarkdownAppendix: "\n---\n*(Open a folder workspace to run verification.)*\n"}, nil
	}

	written := dedupeNonEmpty(opts.WrittenRelativePaths)

	push("\n---\n### Verification gate\n")

	verifyCmds := []string{}
	var verifyOutcome verifyRunOutcome
	if len(written) == 0 {
		push("*No tracked file writes — skipped profile verify (nothing to correlate with touched paths).*")
	} else if opts.AutoVerify {
		push("Running profile verify commands…")
		cmds, err := profileVerifyCommands(root)
		if err != nil {
			push(fmt.Sprintf("**Verify runner error:** %s", escMd(err.Error())))
		} else {
			verifyCmds = cmds
			if len(cmds) == 0 {
				push("*(no verify commands in project profile)*")
			}
			verifyOutcome = runProfileVerify(ctx, root, cmds, push)
		}
	} else {
		push("*Automatic profile verify commands are disabled.*")
	}

	diagText, diagHasErrors := evaluateDiagnostics(opts.Diagnostics, written, verifyOutcome)

	push("### Problems (errors on touched files)")
	switch {
	case len(written) == 0:
		push("*(no touched paths)*")
	case diagText != "":
		push("```text\n" + escapeCodeFence(diagText) + "\n```")
	default:
		push("No error-level diagnostics on touched paths.")
	}

	assistantRolling := opts.AssistantReplyPrefix
	maxFix := opts.MaxFixRounds

	if diagHasErrors && maxFix > 0 && ctx.Err() == nil && opts.Tools != nil {
		push(fmt.Sprintf("Starting **diagnostic-fix** continuation (≤ **%d** rounds, configurable).", maxFix))

		round := 0
		for round < maxFix && diagHasErrors {
			if ctx.Err() != nil {
				push("*Stopped during fix continuation.*")
				break
			}
			round++

			priorForFollow := make([]Turn, 0, len(opts.PriorTurns)+2)
			priorForFollow = append(priorForFollow, opts.PriorTurns...)
			priorForFollow = append(priorForFollow,
				Turn{Role: "user", Text: opts.UserPromptEnriched},
				Turn{Role: "assistant", Text: assistantRolling},
			)

			diagForPrompt := diagText
			if diagForPrompt == "" {
				diagForPrompt = "(unavailable)"
			}
			fixMsg := fmt.Sprintf(
				"[Orchestrator — diagnostic fix · round %d/%d]\n"+
					"Editors were saved after verify attempts. Problems on paths you touched (repo-relative):\n"+
					"```text\n%s\n```\n\n"+
					"Produce **minimal** fixes that resolve these errors using **write_workspace_file** only when edits are required; "+
					"do not regress behavior. Prefer matching existing formatting and conventions in this repo.\n",
				round, maxFix, escapeCodeFence(diagForPrompt))

			follow, err := Run(ctx, Options{
				Mode:          ModeAgent,
				UserText:      fixMsg,
				PriorTurns:    priorForFollow,
				Hooks:         opts.Hooks,
				Log:           log,
				LLM:           opts.LLM,
				Tools:         opts.Tools,
				WorkspaceRoot: root,
			})
			if err != nil {
				push(fmt.Sprintf("**Fix round error:** %s", escMd(err.Error())))
				break
			}

			followText := strings.TrimSpace(follow.Text)
			if followText == "" {
				followText = "_(empty follow-up)_"
			}
			assistantRolling += fmt.Sprintf("\n\n#### Fix round %d\n\n%s", round, followText)
			for _, p := range follow.WrittenRelativePaths {
				relNorm := strings.ReplaceAll(p, "\\", "/")
				if !containsString(written, relNorm) {
					written = append(written, relNorm)
				}
			}

			if opts.Diagnostics != nil {
				diagText, diagHasErrors = opts.Diagnostics(written)
			} else {
				verifyOutcome = runProfileVerify(ctx, root, verifyCmds, push)
				diagText, diagHasErrors = verifyOutcome.text(), verifyOutcome.hasErrors()
			}
		}

		push("### Problems after fix rounds")
		switch {
		case diagText != "":
			push("```text\n" + escapeCodeFence(diagText) + "\n```")
		case len(written) == 0:
			push("*(no touched paths)*")
		default:
			push("No error-level diagnostics on touched paths.")
		}
	} else if diagHasErrors && maxFix == 0 {
		push("*Errors remain — max fix rounds is 0 so no fix loop ran.*")
	}

	if opts.AutoReview && len(written) > 0 && ctx.Err() == nil {
		push("### Agent review (**HEAD~1** diff)")
		if body, err := runInProcessReview(ctx, root); err != nil {
			push(fmt.Sprintf("**Review error:** %s", escMd(err.Error())))
		} else {
			clipped := body
			if len(clipped) > maxReviewAppend {
				clipped = clipped[:maxReviewAppend] + "\n…(truncated)"
			}
			push("```json\n" + escapeCodeFence(clipped) + "\n```")
		}
	} else if !opts.AutoReview && len(written) > 0 {
		push("*Automatic agent review skipped.*")
	}

	return &GateResult{
		MarkdownAppendix:     "\n" + strings.Join(chunks, "\n\n"),
		RemainingErrors:      diagHasErrors,
		WrittenRelativePaths: written,
	}, nil
}

func evaluateDiagnostics(fn DiagnosticsFunc, written []string, outcome verifyRunOutcome) (string, bool) {
	if fn != nil {
		return fn(written)
	}
	return outcome.text(), outcome.hasErrors()
}

func runInProcessReview(ctx context.Context, root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	st, err := graph.Open(paths.DBPath(abs))
	if err != nil {
		return "", err
	}
	defer st.Close()
	res, err := review.ReviewDiff(ctx, st, review.DiffRequest{
		RepoRoot: abs, RepoName: filepath.Base(abs), Base: "HEAD~1",
		SeverityFloor: review.SeverityMedium, IncludeTests: true, IncludeSecurity: true,
		IncludePerformance: true, IncludeContracts: true,
	})
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
