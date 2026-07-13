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
		mcp.WithDescription("Create or refresh a persisted editable plan with todos from expand_request intake"),
		mcp.WithString("request", mcp.Required(), mcp.Description("User feature/fix request")),
		mcp.WithString("task_id", mcp.Description("Optional existing task to refresh plan in place")),
		mcp.WithString("project_type", mcp.Description("optional override")),
		mcp.WithString("changed_area", mcp.Description("frontend|backend|fullstack")),
		mcp.WithBoolean("persist", mcp.Description("Write task JSON under .codehelper/tasks/"), mcp.DefaultBool(true)),
		mcp.WithBoolean("approve_todos", mcp.Description("Set all todos to approved status"), mcp.DefaultBool(false)),
		mcp.WithBoolean("enrich_llm", mcp.Description("Enrich plan with LLM when configured"), mcp.DefaultBool(true)),
		mcp.WithBoolean("quick", mcp.Description("Pattern-only skeleton"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotTaskMutate(),
	), timedTool("agent_plan", agentPlanHandler(regRef)))

	s.AddTool(mcp.NewTool("agent_execute_todo",
		mcp.WithDescription("Execute one approved/planned todo through the agent loop with optional verify gate"),
		mcp.WithString("task_id", mcp.Required()),
		mcp.WithString("todo_id", mcp.Description("Todo id; default is next executable")),
		mcp.WithBoolean("verify", mcp.Description("Run post-write verification gate"), mcp.DefaultBool(true)),
		mcp.WithNumber("max_tool_rounds", mcp.Description("Agent tool rounds cap")),
		mcp.WithNumber("max_fix_rounds", mcp.Description("Diagnostic fix rounds after verify")),
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
