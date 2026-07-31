package mcpsvc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/plan"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterAgentPlanTools wires goal.md agent_plan and agent_execute_todo MCP tools.
func RegisterAgentPlanTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("agent_plan",
		mcp.WithDescription("Create or refresh a multi-todo implementation plan as JSON under `.codehelper/tasks/` (when persist=true). Input: natural-language feature/fix request. Output: task_id + todos (Planned or Approved). Does NOT edit product source — only task store files. Use when the user wants an editable checklist before coding; for one-shot investigation without task files use kickoff or plan instead. After the user accepts the plan, call agent_execute_todo once per approved todo. Set approve_todos=true only when the user already accepted every todo."),
		mcp.WithString("request", mcp.Required(), mcp.Description("User feature or bugfix request in natural language")),
		mcp.WithString("task_id", mcp.Description("Existing task id to regenerate the plan in place; omit to create a new task")),
		mcp.WithString("project_type", mcp.Description("Optional stack override when auto-detect is wrong (e.g. go, node, python)")),
		mcp.WithString("changed_area", mcp.Description("Scope hint: frontend | backend | fullstack")),
		mcp.WithBoolean("persist", mcp.Description("Write task JSON under .codehelper/tasks/ (default true). false returns an in-memory plan only."), mcp.DefaultBool(true)),
		mcp.WithBoolean("approve_todos", mcp.Description("If true, mark all generated todos Approved so agent_execute_todo can run them immediately"), mcp.DefaultBool(false)),
		mcp.WithBoolean("enrich_llm", mcp.Description("Enrich todos via local LLM when configured (default true)"), mcp.DefaultBool(true)),
		mcp.WithBoolean("quick", mcp.Description("Pattern-only skeleton; skips LLM enrichment even if enrich_llm=true"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Indexed repository name when multiple roots are registered")),
		annotTaskMutate(),
	), timedTool("agent_plan", agentPlanHandler(regRef)))

	s.AddTool(mcp.NewTool("agent_execute_todo",
		mcp.WithDescription("Execute exactly ONE Approved todo from an agent_plan task through the local LLM agent loop (tool calls may read and edit the workspace, then optionally verify). Requires: (1) task_id from agent_plan, (2) at least one todo with status Approved (not Planned), (3) local LLM via CODEHELPER_LLM_* env. Omit todo_id to run the next executable Approved todo. Default verify=true runs the project verify gate after writes; set verify=false only when the user explicitly skips checks. Caps: max_tool_rounds / max_fix_rounds bound the loop. Mutates the workspace — never use for read-only audits (use query, context, or investigate). After the last todo, prefer finish_check. Not a substitute for orchestrate (investigation workflow) or apply_patch_workspace_file (single deterministic patch)."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task id returned by agent_plan (required)")),
		mcp.WithString("todo_id", mcp.Description("Specific Approved todo id; default = next executable Approved todo in the task")),
		mcp.WithBoolean("verify", mcp.Description("After writes, run the argv verify gate (lint/build/test). Default true."), mcp.DefaultBool(true)),
		mcp.WithNumber("max_tool_rounds", mcp.Description("Hard cap on agent tool-call rounds for this todo (omit for default)")),
		mcp.WithNumber("max_fix_rounds", mcp.Description("Max diagnostic fix rounds after a failed verify (default 3)")),
		mcp.WithString("repo", mcp.Description("Indexed repository name when multiple roots are registered")),
		annotTaskMutate(),
	), timedTool("agent_execute_todo", agentExecuteTodoHandler(regRef)))
}

func agentPlanHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		request := strings.TrimSpace(argString(args, "request"))
		if request == "" {
			return mcp.NewToolResultError("request is required"), nil
		}
		quick := argBool(args, "quick", false)
		enrich := argBool(args, "enrich_llm", true) && !quick
		out, err := plan.BuildEnriched(ctx, plan.Input{
			Request:     request,
			ProjectType: argString(args, "project_type"),
			ChangedArea: argString(args, "changed_area"),
			RepoRoot:    repo.RootPath,
			Quick:       quick,
		}, plan.EnrichConfig{
			LLM: llm.ConfigFromEnv(), Tools: NewLocalToolCaller(reg, repo.RootPath), EnrichLLM: enrich,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if argBool(args, "approve_todos", false) {
			for i := range out.Todos {
				out.Todos[i].Status = taskstore.TodoApproved
			}
		}
		persist := argBool(args, "persist", true)
		taskID := strings.TrimSpace(argString(args, "task_id"))
		st := taskstore.New(repo.RootPath)
		var task *taskstore.Task
		if taskID != "" {
			task, err = st.Load(taskID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			task.UserRequest = request
			task.Plan = out.Plan
			task.Todos = out.Todos
			task.DecisionPoints = out.DecisionPoints
			if persist {
				_ = st.AppendEvent(task, taskstore.Event{Type: "plan_regenerated", Actor: "agent_plan"})
				if err := st.Save(task); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
			}
		} else if persist {
			task, err = st.Create(request, request, "plan")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			task.Plan = out.Plan
			task.Todos = out.Todos
			task.DecisionPoints = out.DecisionPoints
			_ = st.AppendEvent(task, taskstore.Event{Type: "plan_created", Actor: "agent_plan"})
			if err := st.Save(task); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			taskID = task.ID
		} else {
			task = &taskstore.Task{
				UserRequest: request,
				Title:       request,
				Plan:        out.Plan,
				Todos:       out.Todos,
			}
		}
		resp := map[string]any{
			"task_id":               taskID,
			"task":                  task,
			"recommended_next_tool": "agent_execute_todo",
			"hint":                  "Edit todos (user_notes, status) then call agent_execute_todo one todo at a time",
		}
		if out.Plan.ExpandRequest.AskUser {
			resp["ask_user"] = true
			resp["ask_reason"] = out.Plan.ExpandRequest.AskReason
		}
		b, _ := json.MarshalIndent(resp, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	}
}

func agentExecuteTodoHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		taskID := strings.TrimSpace(argString(args, "task_id"))
		if taskID == "" {
			return mcp.NewToolResultError("task_id is required"), nil
		}
		st := taskstore.New(repo.RootPath)
		task, err := st.Load(taskID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		llmCfg := llm.ConfigFromEnv()
		if !llmCfg.Ready() {
			return mcp.NewToolResultError("LLM not configured: set CODEHELPER_LLM_* env vars"), nil
		}
		tools := NewLocalToolCaller(reg, repo.RootPath)
		maxRounds := int(mcp.ParseInt64(req, "max_tool_rounds", 0))
		maxFix := int(mcp.ParseInt64(req, "max_fix_rounds", 3))
		execRes, task, err := agent.ExecuteTodo(ctx, agent.ExecuteTodoOptions{
			WorkspaceRoot: repo.RootPath,
			Task:          task,
			TodoID:        strings.TrimSpace(argString(args, "todo_id")),
			LLM:           llmCfg,
			Tools:         tools,
			Verify:        argBool(args, "verify", true),
			MaxToolRounds: maxRounds,
			MaxFixRounds:  maxFix,
			AutoVerify:    true,
			AutoReview:    true,
		})
		resp := map[string]any{
			"execution": execRes,
			"task":      task,
		}
		if err != nil {
			resp["error"] = err.Error()
		}
		b, _ := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(string(b)), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}
