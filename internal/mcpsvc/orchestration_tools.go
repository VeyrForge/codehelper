package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/orchestrator"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// orchestrationHandlers returns read-only investigation tools the workflow engine may call.
func orchestrationHandlers(reg *registry.Registry) map[string]server.ToolHandlerFunc {
	h := map[string]server.ToolHandlerFunc{
		"project_context": projectContextHandler(reg),
		"query":           queryHandler(reg),
		"context":         contextHandler(reg),
		"impact":          impactHandler(reg),
		"test_impact":     testImpactHandler(reg),
		"kickoff":         kickoffHandler(reg),
		"scout":           scoutHandler(reg),
		"detect_changes":  detectChangesHandler(reg),
		"review_diff":     reviewDiffHandler(reg),
		"diagnostics":     diagnosticsHandler(reg),
		"dead_code":       deadCodeHandler(reg),
	}
	return h
}

func orchestrationDisabledResult() *mcp.CallToolResult {
	return mcp.NewToolResultError(
		"local orchestration is disabled for this project. " +
			"Enable with `codehelper orchestration enable` (or the `orchestration` tool with action=enable), " +
			"then retry. Until enabled, orchestrate / orchestration_memory / orchestration_rerun / " +
			"orchestration_feedback / run_trace / explain_run will refuse rather than silently no-op.",
	)
}

func requireOrchestrationEnabled(repoRoot string) error {
	if !orchestrator.Enabled(repoRoot) {
		return fmt.Errorf("local orchestration is disabled")
	}
	return nil
}

// RegisterOrchestrationTools wires the guided local investigator MCP tools.
func RegisterOrchestrationTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg

	s.AddTool(mcp.NewTool("orchestration",
		mcp.WithDescription("Project orchestration gate: action=status is read-only; action=enable|disable writes local orchestration config (not source code). When enabled, use orchestrate for guided investigation with tool-trace memory and feedback/rerun. Prefer kickoff for simple one-shot starts; do not call enable unless the user wants the orchestrate loop."),
		mcp.WithString("action", mcp.Required(), mcp.Description("enable | disable | status (status is read-only; enable/disable mutate project orchestration config)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotProjectProfile(),
	), timedTool("orchestration", orchestrationControlHandler(regRef)))

	s.AddTool(mcp.NewTool("orchestrate",
		mcp.WithDescription("WORKFLOW composite (PREFERRED when orchestration is enabled) — classify task → deterministic tool chain → slim agent_brief + what_next + next_queries + compact trace. Prefer over manually chaining query/context/impact. Prefer kickoff for simple feature starts without orchestration. Default payload is token-lean TOON; detail=true for the full pack; format=json for JSON. Requires orchestration enabled. PARAM: task=. On wrong workflow: orchestration_feedback then orchestration_rerun."),
		mcp.WithString("task", mcp.Required(), mcp.Description("What to investigate in natural language (canonical key is task=, same as kickoff)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		mcp.WithBoolean("detail", mcp.Description("When true, include full answer_markdown and context_pack (default false — slim agent_brief + what_next + next_queries)")),
		annotReadOnlyClosedWorld(),
	), timedTool("orchestrate", orchestrateHandler(regRef)))

	s.AddTool(mcp.NewTool("orchestration_rerun",
		mcp.WithDescription("Rerun a previous orchestration with new constraints. Loads prior run, applies instruction/preferred/avoid entities, returns an updated context pack and diff note."),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Previous run id from orchestrate")),
		mcp.WithString("instruction", mcp.Description("What to change about the investigation scope")),
		mcp.WithString("preferred_entities", mcp.Description("Comma-separated entities to prioritize")),
		mcp.WithString("avoid_entities", mcp.Description("Comma-separated entities/areas to avoid")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		mcp.WithBoolean("detail", mcp.Description("When true, include full answer_markdown and context_pack")),
		annotReadOnlyClosedWorld(),
	), timedTool("orchestration_rerun", orchestrationRerunHandler(regRef)))

	s.AddTool(mcp.NewTool("orchestration_feedback",
		mcp.WithDescription("Record a user correction against an orchestrate run_id and update local orchestration memory (preferred/avoid entities + feedback rules). Use when orchestrate chose the wrong scope/symbol/workflow; then call orchestration_rerun with the returned constraints. Writes orchestration memory only — does not edit product source. Requires orchestration enabled. Distinct from agent_memory (ADR/learning.json)."),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Run id from orchestrate to correct")),
		mcp.WithString("message", mcp.Required(), mcp.Description("What was wrong or what to focus on next (aliases: feedback, correction, note)")),
		mcp.WithString("correction_type", mcp.Description("wrong_scope | wrong_symbol | missing_tests | other (default wrong_scope)")),
		mcp.WithString("preferred_entities", mcp.Description("Comma-separated symbols/areas to prioritize on rerun")),
		mcp.WithString("avoid_entities", mcp.Description("Comma-separated symbols/areas to avoid on rerun")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotTaskMutate(),
	), timedTool("orchestration_feedback", orchestrationFeedbackHandler(regRef)))

	s.AddTool(mcp.NewTool("run_trace",
		mcp.WithDescription("Full orchestration run trace on demand: tool calls, arguments summaries, durations, and errors. Use after orchestrate when compact trace is not enough."),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Run id from orchestrate")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("run_trace", runTraceHandler(regRef)))

	s.AddTool(mcp.NewTool("explain_run",
		mcp.WithDescription("Explain why an orchestration run chose its workflow and tool sequence, including feedback applied from prior runs. Use after `orchestrate` when the compact trace is not enough context."),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Run id")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("explain_run", explainRunHandler(regRef)))

	s.AddTool(mcp.NewTool("orchestration_memory",
		mcp.WithDescription("Read-only search of local orchestration routing memory (feedback rules, avoid lists, workflow hints from prior orchestrate runs). Use before orchestrate/orchestration_rerun to reuse project-specific constraints. Requires orchestration enabled. Does not search product code (use query/scout) and is separate from agent_memory/.codehelper/learning.json."),
		mcp.WithString("query", mcp.Description("Free-text search over stored feedback/avoid/workflow hints")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 8)"), mcp.DefaultNumber(8)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("orchestration_memory", orchestrationMemoryHandler(regRef)))
}

func orchestrationControlHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		format := resolveFormat(args)
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
		switch action {
		case "enable", "on":
			if err := orchestrator.SetEnabled(repo.RootPath, true); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		case "disable", "off":
			if err := orchestrator.SetEnabled(repo.RootPath, false); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		case "status", "":
			// status only
		default:
			return mcp.NewToolResultError("action must be enable | disable | status"), nil
		}
		cfg, err := orchestrator.Load(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"enabled": cfg.Enabled,
			"path":    orchestrator.Path(repo.RootPath),
			"tools":   []string{"orchestrate", "orchestration_rerun", "orchestration_feedback", "run_trace", "explain_run", "orchestration_memory"},
			"local_llm": map[string]any{
				"ready":  orchestrator.ResolveLocalChat() != nil,
				"source": localLLMSource(),
			},
			"note": "Call orchestrate(task) for guided investigation when enabled. Local LLM (green engine / CODEHELPER_ENRICH_URL) improves routing when configured. orchestration_memory is separate from agent_memory (learning.json).",
		}
		return mustToolResultFormatted(out, format)
	}
}

func orchestrateHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := requireOrchestrationEnabled(repo.RootPath); err != nil {
			return orchestrationDisabledResult(), nil
		}
		task := strings.TrimSpace(argFirst(args, "task", "goal", "query", "q"))
		if task == "" {
			return mcp.NewToolResultError(
				"task is required — describe what to investigate in natural language. " +
					"Expected: {\"task\":\"how does auth work\"}. Aliases accepted: goal, query, q.",
			), nil
		}
		orch, cleanup, err := newOrchestrator(reg, repo)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer cleanup()
		res, err := orch.Run(ctx, task, orchestrator.Constraints{})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(res.AgentPayload(resolveDetail(args)), resolveFormat(args))
	}
}

func orchestrationRerunHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := requireOrchestrationEnabled(repo.RootPath); err != nil {
			return orchestrationDisabledResult(), nil
		}
		runID := strings.TrimSpace(argString(args, "run_id"))
		if runID == "" {
			return mcp.NewToolResultError("run_id is required"), nil
		}
		orch, cleanup, err := newOrchestrator(reg, repo)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer cleanup()
		store := orchStore(repo.RootPath)
		defer store.Close()
		prev, err := store.GetRun(ctx, runID)
		if err != nil {
			return mcp.NewToolResultError("run not found: " + runID), nil
		}
		c := orchestrator.Constraints{
			Instruction:       strings.TrimSpace(argString(args, "instruction")),
			PreferredEntities: splitCommaList(argString(args, "preferred_entities")),
			AvoidEntities:     splitCommaList(argString(args, "avoid_entities")),
			PreviousRunID:     runID,
		}
		res, err := orch.Run(ctx, prev.Task, c)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res.PreviousWrongNote = "Rerun after run " + runID
		if c.Instruction != "" {
			res.ChangedFromPrev = c.Instruction
		}
		return mustToolResultFormatted(res.AgentPayload(resolveDetail(args)), resolveFormat(args))
	}
}

func resolveDetail(args map[string]any) bool {
	if v, ok := args["detail"].(bool); ok {
		return v
	}
	return false
}

func orchestrationFeedbackHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := requireOrchestrationEnabled(repo.RootPath); err != nil {
			return orchestrationDisabledResult(), nil
		}
		orch, cleanup, err := newOrchestrator(reg, repo)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer cleanup()
		constraints, err := orch.Feedback(ctx, orchestrator.FeedbackInput{
			RunID:             strings.TrimSpace(argString(args, "run_id")),
			Message:           strings.TrimSpace(argFirst(args, "message", "feedback", "correction", "note")),
			CorrectionType:    strings.TrimSpace(argString(args, "correction_type")),
			PreferredEntities: splitCommaList(argString(args, "preferred_entities")),
			AvoidEntities:     splitCommaList(argString(args, "avoid_entities")),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"ok":          true,
			"constraints": constraints,
			"next_step":   "Call orchestration_rerun with run_id and the returned constraints, or orchestrate with a refined task.",
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func runTraceHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := requireOrchestrationEnabled(repo.RootPath); err != nil {
			return orchestrationDisabledResult(), nil
		}
		runID := strings.TrimSpace(argString(args, "run_id"))
		if runID == "" {
			return mcp.NewToolResultError("run_id is required"), nil
		}
		store, err := orchestrator.OpenStore(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer store.Close()
		run, err := store.GetRun(ctx, runID)
		if err != nil {
			return mcp.NewToolResultError("run not found: " + runID), nil
		}
		calls, err := store.ListToolCalls(ctx, runID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fb, _ := store.ListFeedback(ctx, runID)
		out := map[string]any{
			"run": run, "tool_calls": calls, "feedback": fb,
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func explainRunHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		format := resolveFormat(args)
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := requireOrchestrationEnabled(repo.RootPath); err != nil {
			return orchestrationDisabledResult(), nil
		}
		runID := strings.TrimSpace(argString(args, "run_id"))
		if runID == "" {
			return mcp.NewToolResultError("run_id is required"), nil
		}
		orch, cleanup, err := newOrchestrator(reg, repo)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer cleanup()
		text, err := orch.ExplainRun(ctx, runID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(map[string]any{"run_id": runID, "explanation": text}, format)
	}
}

func orchestrationMemoryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		format := resolveFormat(args)
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := requireOrchestrationEnabled(repo.RootPath); err != nil {
			return orchestrationDisabledResult(), nil
		}
		store, err := orchestrator.OpenStore(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer store.Close()
		limit := int(mcp.ParseInt64(req, "limit", 8))
		hits, err := store.SearchMemory(ctx, argString(args, "query"), limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := map[string]any{
			"memory": hits, "count": len(hits),
			"orchestration_enabled": true,
			"note":                  "orchestration_memory is workflow routing memory (feedback/avoid). Project ADR memory is agent_memory + learning.json — separate gate.",
		}
		return mustToolResultFormatted(out, format)
	}
}

func newOrchestrator(reg *registry.Registry, repo registry.Entry) (*orchestrator.Orchestrator, func(), error) {
	store, err := orchestrator.OpenStore(repo.RootPath)
	if err != nil {
		return nil, nil, err
	}
	inner := orchestrator.NewMCPToolInvoker(orchestrationHandlers(reg), repo.Name)
	inv := &orchestrator.MeteredInvoker{Inner: inner}
	orch := orchestrator.New(orchestrator.Options{
		RepoRoot: repo.RootPath,
		RepoName: repo.Name,
		Invoker:  inv,
		Store:    store,
	})
	return orch, func() { store.Close() }, nil
}

func orchStore(repoRoot string) *orchestrator.Store {
	s, err := orchestrator.OpenStore(repoRoot)
	if err != nil {
		return nil
	}
	return s
}

func localLLMSource() string {
	if os.Getenv("CODEHELPER_ENRICH_URL") != "" {
		return "green_engine_enrich_url"
	}
	if os.Getenv("CODEHELPER_LLM_BASE_URL") != "" || os.Getenv("CODEHELPER_LLM_CHAT_URL") != "" {
		return "llm_json_or_env"
	}
	return "none"
}
