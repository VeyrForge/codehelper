package mcpsvc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/VeyrForge/codehelper/internal/memory"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterAgentSupportTools wires goal.md §7.2 agent MCP tools (finish_check, agent_memory).
func RegisterAgentSupportTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("finish_check",
		mcp.WithDescription("Hard done gate combining verify hygiene and release readiness"),
		mcp.WithString("base_ref", mcp.DefaultString("HEAD~1")),
		mcp.WithBoolean("verify_ran", mcp.DefaultBool(false)),
		mcp.WithBoolean("verify_abstained", mcp.DefaultBool(false)),
		mcp.WithString("verify_reason", mcp.Description("required when abstained")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotReadOnlyClosedWorld(),
	), timedTool("finish_check", finishCheckHandler(regRef)))

	s.AddTool(mcp.NewTool("agent_memory",
		mcp.WithDescription("Persist and recall project memory (goal.md §25). action=record saves an ADR-style DECISION with its rationale (the WHY) so a later session recalls it instead of re-litigating; search/list retrieve prior decisions, fix patterns, and facts. Also propose/approve/reject for task-scoped proposals."),
		mcp.WithString("action", mcp.Required(), mcp.Description("record|search|list|propose|approve|reject")),
		mcp.WithString("query", mcp.Description("Search query for action=search")),
		mcp.WithNumber("limit", mcp.Description("Max hits for action=search"), mcp.DefaultNumber(8)),
		mcp.WithString("text", mcp.Description("The decision/memory text (record/approve/propose)")),
		mcp.WithString("rationale", mcp.Description("Why this decision was made — the reasoning later sessions need (record/approve)")),
		mcp.WithString("tags", mcp.Description("Optional comma-separated labels for recall, e.g. \"retrieval,perf\"")),
		mcp.WithString("proposal_id", mcp.Description("Proposal id for approve/reject")),
		mcp.WithString("task_id", mcp.Description("Task id when using task proposals")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotTaskMutate(),
	), timedTool("agent_memory", agentMemoryHandler(regRef)))
}

func finishCheckHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		base := argString(args, "base_ref")
		if base == "" {
			base = "HEAD~1"
		}
		stg, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer stg.Close()
		rv, err := review.ReviewDiff(ctx, stg, review.DiffRequest{
			RepoRoot: repo.RootPath, RepoName: repo.Name, Base: base, SeverityFloor: review.SeverityMedium,
			IncludeTests: true, IncludeSecurity: true, IncludePerformance: true, IncludeContracts: true,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		cg, err := review.ContractGuard(ctx, stg, repo.RootPath, repo.Name, base)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tg, err := review.TestGap(ctx, stg, repo.RootPath, repo.Name, base)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rr := review.BuildReleaseReadiness(rv, cg, tg, review.RiskScore(rv.Findings))
		out := review.BuildFinishCheck(review.FinishCheckInput{
			BaseRef:         base,
			VerifyRan:       argBool(args, "verify_ran", false),
			VerifyAbstained: argBool(args, "verify_abstained", false),
			VerifyReason:    argString(args, "verify_reason"),
			Release:         rr,
		})
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	}
}

// splitCommaList parses a comma-separated arg into trimmed, non-empty items.
func splitCommaList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func agentMemoryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
		ms := memory.Open(repo.RootPath)
		switch action {
		case "search":
			q := strings.TrimSpace(argQuery(args))
			if q == "" {
				q = strings.TrimSpace(argString(args, "text"))
			}
			if q == "" {
				return mcp.NewToolResultError("query is required for search"), nil
			}
			limit := int(mcp.ParseInt64(req, "limit", 8))
			hits, err := ms.Search(q, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if hits == nil {
				hits = []memory.RelevantMemory{}
			}
			out := map[string]any{"relevant_memory": hits, "count": len(hits)}
			if len(hits) == 0 {
				out["note"] = "no matching project memory found for this query"
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		case "list":
			hits, err := ms.Search(argString(args, "text"), 12)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(map[string]any{"memory": hits})
			return mcp.NewToolResultText(string(b)), nil
		case "propose":
			text := strings.TrimSpace(argString(args, "text"))
			if text == "" {
				return mcp.NewToolResultError("text is required for propose"), nil
			}
			taskID := strings.TrimSpace(argString(args, "task_id"))
			if taskID != "" {
				t, err := taskstore.New(repo.RootPath).ProposeMemory(taskID, taskstore.MemoryProposal{
					Kind: "pattern", Text: text, Status: "pending",
				})
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				b, _ := json.Marshal(t)
				return mcp.NewToolResultText(string(b)), nil
			}
			return mcp.NewToolResultText(`{"status":"pending","note":"approve via agent_memory action=approve"}`), nil
		case "record":
			// Persist an ADR-style decision (what + WHY) directly, so a later session
			// recalls the rationale via search/plan instead of re-litigating it.
			text := strings.TrimSpace(argString(args, "text"))
			if text == "" {
				return mcp.NewToolResultError("text (the decision) is required for record"), nil
			}
			rec := memory.Decision{
				Text:      text,
				Rationale: strings.TrimSpace(argString(args, "rationale")),
				Tags:      splitCommaList(argString(args, "tags")),
			}
			if err := ms.AddDecisionRecord(rec); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(map[string]any{"ok": true, "recorded": rec})
			return mcp.NewToolResultText(string(b)), nil
		case "approve":
			text := strings.TrimSpace(argString(args, "text"))
			if text != "" {
				_ = ms.AddDecisionRecord(memory.Decision{
					Text:      text,
					Rationale: strings.TrimSpace(argString(args, "rationale")),
					Tags:      splitCommaList(argString(args, "tags")),
				})
			}
			if pid := strings.TrimSpace(argString(args, "proposal_id")); pid != "" {
				_, _ = taskstore.New(repo.RootPath).ResolveMemoryProposal(argString(args, "task_id"), pid, "approved")
			}
			return mcp.NewToolResultText(`{"ok":true}`), nil
		case "reject":
			pid := strings.TrimSpace(argString(args, "proposal_id"))
			if pid == "" {
				return mcp.NewToolResultError("proposal_id required"), nil
			}
			t, err := taskstore.New(repo.RootPath).ResolveMemoryProposal(argString(args, "task_id"), pid, "rejected")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(t)
			return mcp.NewToolResultText(string(b)), nil
		default:
			return mcp.NewToolResultError("action must be propose|approve|reject|search|list"), nil
		}
	}
}
