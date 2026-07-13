package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/research"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

// ExecuteTodoOptions runs one approved/planned todo through the agent loop.
type ExecuteTodoOptions struct {
	WorkspaceRoot string
	Task          *taskstore.Task
	TodoID        string
	LLM           llm.Config
	Tools         ToolCaller
	Verify        bool
	MaxToolRounds int
	MaxFixRounds  int
	AutoVerify    bool
	AutoReview    bool
	Hooks         Hooks
	Log           func(string)
}

// FinishSummary is the auto-run finish gate when all todos are terminal.
type FinishSummary struct {
	CanClaimDone      bool     `json:"can_claim_done"`
	CompletionState   string   `json:"completion_state"`
	MissingBeforeDone []string `json:"missing_before_done,omitempty"`
}

// ExecuteTodoResult is the outcome of one todo execution.
type ExecuteTodoResult struct {
	TodoID               string         `json:"todo_id"`
	TodoStatus           string         `json:"todo_status"`
	Evidence             string         `json:"evidence,omitempty"`
	Agent                *Result        `json:"agent,omitempty"`
	Gate                 *GateResult    `json:"gate,omitempty"`
	Finish               *FinishSummary `json:"finish,omitempty"`
	BlockedReason        string         `json:"blocked_reason,omitempty"`
	WrittenRelativePaths []string       `json:"written_relative_paths,omitempty"`
}

// ExecuteTodo runs a single todo via ModeAgent with optional verification gate.
func ExecuteTodo(ctx context.Context, opts ExecuteTodoOptions) (*ExecuteTodoResult, *taskstore.Task, error) {
	if opts.Task == nil {
		return nil, nil, fmt.Errorf("task is required")
	}
	if opts.Tools == nil {
		return nil, nil, fmt.Errorf("tools caller is required")
	}
	if !opts.LLM.Ready() {
		return nil, nil, fmt.Errorf("LLM not configured: set CODEHELPER_LLM_* or ~/.codehelper/llm.json")
	}
	todoID := strings.TrimSpace(opts.TodoID)
	if todoID == "" {
		td, _ := taskstore.NextExecutable(opts.Task, true)
		if td == nil {
			return nil, opts.Task, fmt.Errorf("no executable todo (approve todos or complete prior steps)")
		}
		todoID = td.ID
	}
	if err := taskstore.CanExecute(opts.Task, todoID); err != nil {
		return nil, opts.Task, err
	}
	td, _ := taskstore.FindTodo(opts.Task, todoID)
	if td == nil {
		return nil, opts.Task, fmt.Errorf("todo %q not found", todoID)
	}

	prompt := buildTodoPrompt(opts.Task, td)
	if pre, perr := PreflightContext(ctx, opts.Tools, opts.WorkspaceRoot, opts.Task, td); perr != nil {
		td.Status = taskstore.TodoBlocked
		td.BlockedReason = perr.Error()
		_ = taskstore.New(opts.WorkspaceRoot).Save(opts.Task)
		return &ExecuteTodoResult{
			TodoID: todoID, TodoStatus: td.Status, BlockedReason: perr.Error(),
		}, opts.Task, perr
	} else if strings.TrimSpace(pre) != "" {
		prompt = pre + "\n\n" + prompt
	}
	td.Status = taskstore.TodoInProgress
	_ = taskstore.New(opts.WorkspaceRoot).AppendEvent(opts.Task, taskstore.Event{
		Type: "todo_started", Actor: "agent", TodoID: todoID, Details: td.Title,
	})

	res, err := Run(ctx, Options{
		Mode:          ModeAgent,
		UserText:      prompt,
		TaskID:        opts.Task.ID,
		ForceWrite:    true,
		Hooks:         opts.Hooks,
		Log:           opts.Log,
		LLM:           opts.LLM,
		Tools:         opts.Tools,
		WorkspaceRoot: opts.WorkspaceRoot,
		MaxToolRounds: opts.MaxToolRounds,
	})
	if err != nil {
		td.Status = taskstore.TodoFailed
		td.BlockedReason = err.Error()
		_ = taskstore.New(opts.WorkspaceRoot).Save(opts.Task)
		return &ExecuteTodoResult{
			TodoID: todoID, TodoStatus: td.Status, BlockedReason: err.Error(),
		}, opts.Task, err
	}

	out := &ExecuteTodoResult{
		TodoID:               todoID,
		Agent:                res,
		WrittenRelativePaths: res.WrittenRelativePaths,
	}

	verifyRan := false
	if opts.Verify && len(res.WrittenRelativePaths) > 0 {
		td.Status = taskstore.TodoVerifying
		gateOpts := GateOptions{
			WorkspaceRoot:        opts.WorkspaceRoot,
			WrittenRelativePaths: res.WrittenRelativePaths,
			UserPromptEnriched:   prompt,
			AssistantReplyPrefix: res.Text,
			AutoVerify:           opts.AutoVerify,
			AutoReview:           opts.AutoReview,
			MaxFixRounds:         opts.MaxFixRounds,
			Log:                  opts.Log,
			Hooks:                opts.Hooks,
			LLM:                  opts.LLM,
			Tools:                opts.Tools,
		}
		if gateOpts.MaxFixRounds == 0 {
			gateOpts.MaxFixRounds = defaultFixRounds
		}
		if !gateOpts.AutoVerify {
			gateOpts.AutoVerify = true
		}
		if !gateOpts.AutoReview {
			gateOpts.AutoReview = true
		}
		gate, gerr := RunVerificationGate(ctx, gateOpts)
		out.Gate = gate
		if gerr != nil {
			td.Status = taskstore.TodoFailed
			td.BlockedReason = gerr.Error()
			out.TodoStatus = td.Status
			out.BlockedReason = gerr.Error()
			_ = taskstore.New(opts.WorkspaceRoot).Save(opts.Task)
			return out, opts.Task, gerr
		}
		verifyRan = gate != nil && !gate.RemainingErrors
		if gate != nil && gate.RemainingErrors {
			td.Status = taskstore.TodoDebugging
			td.BlockedReason = "verification or review reported blocking issues — re-execute after fixes"
			out.TodoStatus = td.Status
			out.BlockedReason = td.BlockedReason
			_ = taskstore.New(opts.WorkspaceRoot).AppendEvent(opts.Task, taskstore.Event{
				Type: "todo_debugging", Actor: "agent", TodoID: todoID, Details: td.BlockedReason,
			})
			_ = taskstore.New(opts.WorkspaceRoot).Save(opts.Task)
			return out, opts.Task, nil
		}
	}

	td.Status = taskstore.TodoComplete
	evidence := strings.TrimSpace(res.Text)
	if len(evidence) > 2000 {
		evidence = evidence[:2000] + "…"
	}
	td.Evidence = evidence
	td.BlockedReason = ""
	out.TodoStatus = td.Status
	out.Evidence = evidence

	_ = taskstore.New(opts.WorkspaceRoot).AppendEvent(opts.Task, taskstore.Event{
		Type: "todo_complete", Actor: "agent", TodoID: todoID,
		Details: fmt.Sprintf("wrote %d file(s)", len(res.WrittenRelativePaths)),
	})

	if taskstore.AllTodosTerminal(opts.Task) {
		opts.Task.Status = taskstore.StatusDone
		finish, ferr := runTaskFinishCheck(ctx, opts.WorkspaceRoot, verifyRan)
		if ferr == nil && finish != nil {
			out.Finish = finish
			b, _ := json.Marshal(finish)
			_ = taskstore.New(opts.WorkspaceRoot).AppendEvent(opts.Task, taskstore.Event{
				Type: "finish_check", Actor: "agent", Details: string(b),
			})
			if finish.CanClaimDone {
				ts := taskstore.New(opts.WorkspaceRoot)
				summary := buildTaskFinalSummary(opts.Task, finish)
				opts.Task, _ = ts.SetFinalSummary(opts.Task.ID, summary)
				if mp := memoryProposalText(opts.WorkspaceRoot, opts.Task); mp != "" {
					opts.Task, _ = ts.ProposeMemory(opts.Task.ID, taskstore.MemoryProposal{
						Kind: "pattern", Text: mp, Status: "pending",
					})
				}
			}
		}
	}

	if err := taskstore.New(opts.WorkspaceRoot).Save(opts.Task); err != nil {
		return out, opts.Task, err
	}
	st := taskstore.New(opts.WorkspaceRoot)
	if len(res.WrittenRelativePaths) > 0 {
		opts.Task, _ = st.RecordChangedFiles(opts.Task.ID, res.WrittenRelativePaths)
	}
	if out.Gate != nil {
		passed := !out.Gate.RemainingErrors
		summary := "verify gate passed"
		if !passed {
			summary = "verify gate reported issues"
		}
		opts.Task, _ = st.AddVerificationResult(opts.Task.ID, taskstore.VerificationResult{
			TodoID: todoID, Passed: passed, Summary: summary,
		})
	}
	return out, opts.Task, nil
}

func runTaskFinishCheck(ctx context.Context, root string, verifyRan bool) (*FinishSummary, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := graph.Open(paths.DBPath(abs))
	if err != nil {
		return nil, err
	}
	defer st.Close()
	base := "HEAD~1"
	repoName := filepath.Base(abs)
	rv, err := review.ReviewDiff(ctx, st, review.DiffRequest{
		RepoRoot: abs, RepoName: repoName, Base: base, SeverityFloor: review.SeverityMedium,
		IncludeTests: true, IncludeSecurity: true, IncludePerformance: true, IncludeContracts: true,
	})
	if err != nil {
		return nil, err
	}
	cg, err := review.ContractGuard(ctx, st, abs, repoName, base)
	if err != nil {
		return nil, err
	}
	tg, err := review.TestGap(ctx, st, abs, repoName, base)
	if err != nil {
		return nil, err
	}
	rr := review.BuildReleaseReadiness(rv, cg, tg, review.RiskScore(rv.Findings))
	out := review.BuildFinishCheck(review.FinishCheckInput{
		BaseRef:   base,
		VerifyRan: verifyRan,
		Release:   rr,
	})
	return &FinishSummary{
		CanClaimDone:      out.CanClaimDone,
		CompletionState:   out.CompletionState,
		MissingBeforeDone: out.MissingBeforeDone,
	}, nil
}

func buildTodoPrompt(task *taskstore.Task, td *taskstore.Todo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Execute exactly one plan todo.\n\n")
	fmt.Fprintf(&b, "Task: %s\n", task.UserRequest)
	fmt.Fprintf(&b, "Todo ID: %s\n", td.ID)
	fmt.Fprintf(&b, "Title: %s\n", td.Title)
	if strings.TrimSpace(td.Goal) != "" {
		fmt.Fprintf(&b, "Goal: %s\n", td.Goal)
	}
	if strings.TrimSpace(td.Description) != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", td.Description)
	}
	if strings.TrimSpace(td.UserNotes) != "" {
		fmt.Fprintf(&b, "\nUser notes (must follow):\n%s\n", td.UserNotes)
	}
	if len(td.Files) > 0 {
		fmt.Fprintf(&b, "\nFiles likely involved: %s\n", strings.Join(td.Files, ", "))
	}
	if len(td.ReuseSymbols) > 0 {
		fmt.Fprintf(&b, "Reuse symbols: %s\n", strings.Join(td.ReuseSymbols, ", "))
	}
	if len(td.AvoidDuplicating) > 0 {
		fmt.Fprintf(&b, "Do not duplicate: %s\n", strings.Join(td.AvoidDuplicating, ", "))
	}
	if len(td.RequiredContext) > 0 {
		fmt.Fprintf(&b, "Required context: %s\n", strings.Join(td.RequiredContext, "; "))
	}
	if strings.TrimSpace(td.ImplementationNotes) != "" {
		fmt.Fprintf(&b, "Implementation notes: %s\n", td.ImplementationNotes)
	}
	if len(td.VerifyCommands) > 0 {
		fmt.Fprintf(&b, "Verification commands for this todo: %s\n", strings.Join(td.VerifyCommands, "; "))
	}
	fmt.Fprintf(&b, "\nRules: complete only this todo; do not start other todos; search before creating; minimal diffs.\n")
	return b.String()
}

func buildTaskFinalSummary(task *taskstore.Task, finish *FinishSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Completed\n%s\n\n", task.UserRequest)
	if len(task.ChangedFiles) > 0 {
		fmt.Fprintf(&b, "## Files Changed\n")
		for _, f := range task.ChangedFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	if len(task.Plan.ReuseCandidates) > 0 {
		fmt.Fprintf(&b, "## Existing Code Reused\n%s\n\n", strings.Join(task.Plan.ReuseCandidates, ", "))
	} else if len(task.Plan.ExistingCodeFound) > 0 {
		var refs []string
		for _, c := range task.Plan.ExistingCodeFound {
			if c.Path != "" {
				refs = append(refs, c.Path)
			}
		}
		if len(refs) > 0 {
			fmt.Fprintf(&b, "## Existing Code Reused\n%s\n\n", strings.Join(refs, ", "))
		}
	}
	fmt.Fprintf(&b, "## Verification\nfinish_check can_claim_done=%v; completion_state=%s\n", finish.CanClaimDone, finish.CompletionState)
	for _, vr := range task.VerificationResults {
		if strings.TrimSpace(vr.Summary) != "" {
			fmt.Fprintf(&b, "- %s (passed=%v)\n", vr.Summary, vr.Passed)
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "## Security Review\nreview_diff + finish_check gates ran (see review results on task)\n\n")
	fmt.Fprintf(&b, "## Performance Review\nimpact_tier=%s; no unbounded hot-path changes flagged by core gates\n\n", task.Plan.ImpactTier)
	fmt.Fprintf(&b, "## Contract Review\nfinish_check includes contract/release signals; approve breaking changes explicitly\n\n")
	if len(task.Plan.DoneCriteria) > 0 {
		fmt.Fprintf(&b, "## Manual Steps\nReview done criteria: %s\n\n", strings.Join(task.Plan.DoneCriteria, "; "))
	}
	if len(finish.MissingBeforeDone) > 0 {
		fmt.Fprintf(&b, "## Remaining Risks\n%s\n", strings.Join(finish.MissingBeforeDone, "; "))
	}
	return b.String()
}

func memoryProposalText(repoRoot string, task *taskstore.Task) string {
	if task == nil || !research.LearningEnabled(repoRoot) {
		return ""
	}
	req := strings.TrimSpace(task.UserRequest)
	if req == "" {
		return ""
	}
	return "Completed task pattern: " + req
}
