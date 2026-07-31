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
		mcp.WithDescription("Turn a natural-language feature/fix request into a persisted multi-todo plan under `.codehelper/tasks/` (when persist=true). Use AFTER expand_request/select_pattern when the user wants an editable checklist; use kickoff/plan instead for one-shot investigation without task files. Does not edit product source — only task JSON. Next step: agent_execute_todo one approved todo at a time. Pass approve_todos=true only when the user already accepted the plan."),
		mcp.WithString("request", mcp.Required(), mcp.Description("User feature/fix request in natural language")),
		mcp.WithString("task_id", mcp.Description("Existing task id to refresh the plan in place (omit to create)")),
		mcp.WithString("project_type", mcp.Description("Optional stack override when auto-detect is wrong")),
		mcp.WithString("changed_area", mcp.Description("frontend | backend | fullstack — scopes todo generation")),
		mcp.WithBoolean("persist", mcp.Description("Write task JSON under .codehelper/tasks/ (default true)"), mcp.DefaultBool(true)),
		mcp.WithBoolean("approve_todos", mcp.Description("Mark all todos approved so agent_execute_todo can run them"), mcp.DefaultBool(false)),
		mcp.WithBoolean("enrich_llm", mcp.Description("Enrich plan via local LLM when configured (default true)"), mcp.DefaultBool(true)),
		mcp.WithBoolean("quick", mcp.Description("Pattern-only skeleton without LLM enrichment"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotTaskMutate(),
	), timedTool("agent_plan", agentPlanHandler(regRef)))

	s.AddTool(mcp.NewTool("agent_execute_todo",
		mcp.WithDescription("Execute exactly ONE approved todo from an agent_plan task through the local agent tool loop (reads/edits/verify). Requires task_id from agent_plan; omit todo_id to take the next executable todo. Default verify=true runs the argv verify gate after writes. Mutates the workspace — never use for read-only audits (use query/context/investigate instead). Prefer finish_check after the last todo. Caps: max_tool_rounds / max_fix_rounds bound the loop."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task id returned by agent_plan")),
		mcp.WithString("todo_id", mcp.Description("Specific todo id; default = next executable/approved todo")),
		mcp.WithBoolean("verify", mcp.Description("Run post-write verify gate (lint/build/test argv); default true"), mcp.DefaultBool(true)),
		mcp.WithNumber("max_tool_rounds", mcp.Description("Max agent tool-call rounds for this todo (safety cap)")),
		mcp.WithNumber("max_fix_rounds", mcp.Description("Max diagnostic fix rounds after a failed verify")),
		mcp.WithString("repo", mcp.Description("Repository name")),
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
