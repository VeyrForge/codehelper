package mcpsvc

import (
	"context"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/memory"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/research"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterAgentSupportTools wires goal.md §7.2 agent MCP tools (finish_check, agent_memory).
func RegisterAgentSupportTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("finish_check",
		mcp.WithDescription("Hard done gate: verify hygiene + release readiness. Returns can_claim_done / completion_state + what_next / recommended_next_tools. Claim done ONLY when can_claim_done=true. Fail-closed: verify_abstained needs allow_claim_without_verify=true; also requires edit proof (diff vs base) or diagnostics_ran=true. On shallow/ephemeral beds returns completion_state=abstain (structured, not error) — do not invent a green gate."),
		mcp.WithString("base_ref", mcp.Description("Git ref to diff against for release readiness (default HEAD~1)"), mcp.DefaultString("HEAD~1")),
		mcp.WithBoolean("verify_ran", mcp.Description("Set true after a green argv verify run"), mcp.DefaultBool(false)),
		mcp.WithBoolean("verify_abstained", mcp.Description("Set true when verify could not run (no cmds / ephemeral bed); pair with verify_reason AND allow_claim_without_verify"), mcp.DefaultBool(false)),
		mcp.WithString("verify_reason", mcp.Description("required when abstained")),
		mcp.WithBoolean("allow_claim_without_verify", mcp.Description("Explicit allow to claim done when verify_abstained=true (fail-closed otherwise)"), mcp.DefaultBool(false)),
		mcp.WithBoolean("diagnostics_ran", mcp.Description("Set true after diagnostics; counts as proof when the diff is empty (read-only QA)"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("finish_check", finishCheckHandler(regRef)))

	s.AddTool(mcp.NewTool("agent_memory",
		mcp.WithDescription("Persist and recall project memory (goal.md §25). action=record saves an ADR-style DECISION with its rationale (the WHY) so a later session recalls it instead of re-litigating; search/list retrieve prior decisions, fix patterns, and facts. Also propose/approve/reject for task-scoped proposals. Writes require learning enabled in .codehelper/learning.json; when disabled, responses set learning_enabled=false explicitly."),
		mcp.WithString("action", mcp.Required(), mcp.Description("record|search|list|propose|approve|reject")),
		mcp.WithString("query", mcp.Description("Search query for action=search")),
		mcp.WithNumber("limit", mcp.Description("Max hits for action=search"), mcp.DefaultNumber(8)),
		mcp.WithString("text", mcp.Description("The decision/memory text (record/approve/propose)")),
		mcp.WithString("rationale", mcp.Description("Why this decision was made — the reasoning later sessions need (record/approve)")),
		mcp.WithString("tags", mcp.Description("Optional comma-separated labels for recall, e.g. \"retrieval,perf\"")),
		mcp.WithString("proposal_id", mcp.Description("Proposal id for approve/reject")),
		mcp.WithString("task_id", mcp.Description("Task id when using task proposals")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotTaskMutate(),
	), timedTool("agent_memory", agentMemoryHandler(regRef)))
}

func finishCheckHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		format := resolveFormat(args)
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
			return mustToolResultFormatted(review.BuildFinishCheckAbstain(base, "review_diff unavailable: "+err.Error()), format)
		}
		cg, err := review.ContractGuard(ctx, stg, repo.RootPath, repo.Name, base)
		if err != nil {
			return mustToolResultFormatted(review.BuildFinishCheckAbstain(base, "contract_guard unavailable: "+err.Error()), format)
		}
		tg, err := review.TestGap(ctx, stg, repo.RootPath, repo.Name, base)
		if err != nil {
			return mustToolResultFormatted(review.BuildFinishCheckAbstain(base, "test_gap unavailable: "+err.Error()), format)
		}
		rr := review.BuildReleaseReadiness(rv, cg, tg, review.RiskScore(rv.Findings))
		hasEditProof := false
		if files, derr := gitutil.DiffAgainst(repo.RootPath, base); derr == nil {
			hasEditProof = len(files) > 0
		} else if base == "HEAD~1" {
			if files, derr2 := gitutil.DiffAgainst(repo.RootPath, "HEAD"); derr2 == nil {
				hasEditProof = len(files) > 0
			}
		}
		out := review.BuildFinishCheck(review.FinishCheckInput{
			BaseRef:                 base,
			VerifyRan:               argBool(args, "verify_ran", false),
			VerifyAbstained:         argBool(args, "verify_abstained", false),
			VerifyReason:            argString(args, "verify_reason"),
			AllowClaimWithoutVerify: argBool(args, "allow_claim_without_verify", false),
			HasEditProof:            hasEditProof,
			DiagnosticsRan:          argBool(args, "diagnostics_ran", false),
			Release:                 rr,
		})
		return mustToolResultFormatted(out, format)
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

func learningPolicyFields(repoRoot string) map[string]any {
	enabled := research.LearningEnabled(repoRoot)
	mode := research.LearningMode(repoRoot)
	out := map[string]any{
		"learning_enabled": enabled,
		"learning_mode":    mode,
		"learning_policy":  ".codehelper/learning.json",
	}
	if !enabled {
		out["note"] = "project learning/memory writes are DISABLED in .codehelper/learning.json (enabled=false). search/list still work on any existing memory; record/propose/approve refuse until you set enabled=true (or state=enabled)."
		out["what_next"] = "To enable: edit .codehelper/learning.json → {\"enabled\":true,\"mode\":\"approval\"} then retry agent_memory action=record|propose"
	}
	return out
}

func agentMemoryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		format := resolveFormat(args)
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
		ms := memory.Open(repo.RootPath)
		policy := learningPolicyFields(repo.RootPath)
		enabled := research.LearningEnabled(repo.RootPath)

		refuseWrite := func(op string) (*mcp.CallToolResult, error) {
			out := map[string]any{
				"ok":                     false,
				"action":                 op,
				"learning_enabled":       false,
				"learning_mode":          research.LearningMode(repo.RootPath),
				"learning_policy":        ".codehelper/learning.json",
				"error_category":         ErrCategoryValidation,
				"recovery_hint":          RecoveryReportToUser,
				"is_retryable":           false,
				"message":                "agent_memory " + op + " refused: project learning is disabled in .codehelper/learning.json",
				"note":                   policy["note"],
				"what_next":              policy["what_next"],
				"recommended_next_tools": []string{"project_context", "kickoff", "query"},
			}
			return mustToolResultFormatted(out, format)
		}

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
			out := map[string]any{
				"relevant_memory":  hits,
				"count":            len(hits),
				"learning_enabled": enabled,
				"learning_mode":    research.LearningMode(repo.RootPath),
				"learning_policy":  ".codehelper/learning.json",
			}
			if !enabled {
				out["policy_note"] = policy["note"]
			}
			if len(hits) == 0 {
				out["note"] = "no matching project memory found for this query"
			}
			return mustToolResultFormatted(out, format)
		case "list":
			hits, err := ms.Search(argString(args, "text"), 12)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out := map[string]any{
				"memory":           hits,
				"learning_enabled": enabled,
				"learning_mode":    research.LearningMode(repo.RootPath),
				"learning_policy":  ".codehelper/learning.json",
			}
			if !enabled {
				out["policy_note"] = policy["note"]
			}
			return mustToolResultFormatted(out, format)
		case "propose":
			if !enabled {
				return refuseWrite("propose")
			}
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
				out := map[string]any{"task": t, "learning_enabled": true, "learning_mode": research.LearningMode(repo.RootPath)}
				return mustToolResultFormatted(out, format)
			}
			return mustToolResultFormatted(map[string]any{
				"status":           "pending",
				"note":             "approve via agent_memory action=approve",
				"learning_enabled": true,
				"learning_mode":    research.LearningMode(repo.RootPath),
			}, format)
		case "record":
			if !enabled {
				return refuseWrite("record")
			}
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
			return mustToolResultFormatted(map[string]any{
				"ok": true, "recorded": rec,
				"learning_enabled": true,
				"learning_mode":    research.LearningMode(repo.RootPath),
			}, format)
		case "approve":
			if !enabled {
				return refuseWrite("approve")
			}
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
			return mustToolResultFormatted(map[string]any{
				"ok": true, "learning_enabled": true, "learning_mode": research.LearningMode(repo.RootPath),
			}, format)
		case "reject":
			if !enabled {
				return refuseWrite("reject")
			}
			pid := strings.TrimSpace(argString(args, "proposal_id"))
			if pid == "" {
				return mcp.NewToolResultError("proposal_id required"), nil
			}
			t, err := taskstore.New(repo.RootPath).ResolveMemoryProposal(argString(args, "task_id"), pid, "rejected")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mustToolResultFormatted(map[string]any{
				"task": t, "learning_enabled": true, "learning_mode": research.LearningMode(repo.RootPath),
			}, format)
		default:
			return mcp.NewToolResultError("action must be record|propose|approve|reject|search|list"), nil
		}
	}
}
