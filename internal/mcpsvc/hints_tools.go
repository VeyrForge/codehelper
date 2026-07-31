package mcpsvc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/VeyrForge/codehelper/internal/hints"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterHintsTools wires the `hints` tool: a GLOBAL, cross-project memory of
// learned rules ("don't forget X when working with Y"), keyed by technology so
// they apply to any matching project — not just this one. Stored in
// ~/.codehelper/learned_hints.json, which syncs/exports across machines.
func RegisterHintsTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("hints",
		mcp.WithDescription("Global, cross-project learned hints/rules ('don't forget X when working with Y'), keyed by framework/language/dependency/project_type and applied to any project that matches. Use action=add to remember something you discovered (so future work on this stack — in any project — gets the hint up front); action=list to review; action=remove to delete by id. Persisted in ~/.codehelper/learned_hints.json (local-first, syncable)."),
		mcp.WithString("action", mcp.Required(), mcp.Description("add|list|remove")),
		mcp.WithString("scope_type", mcp.Description("framework|language|dependency|project_type|global — what the hint is keyed on (default global)")),
		mcp.WithString("scope", mcp.Description("the tech this applies to, e.g. wordpress, go, tailwindcss, laravel (leave empty for global)")),
		mcp.WithString("text", mcp.Description("the hint/rule to remember (action=add)")),
		mcp.WithString("id", mcp.Description("hint id (action=remove)")),
		mcp.WithString("repo", mcp.Description("optional: the project where this was learned (recorded as source)")),
		annotTaskMutate(),
	), timedTool("hints", hintsHandler(reg)))
}

func hintsHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
		switch action {
		case "add":
			text := strings.TrimSpace(argString(args, "text"))
			if text == "" {
				return mcp.NewToolResultError("text is required for add"), nil
			}
			scopeType := hints.NormalizeScopeType(argString(args, "scope_type"))
			scope := strings.TrimSpace(argString(args, "scope"))
			if scopeType != hints.ScopeGlobal && scope == "" {
				return mcp.NewToolResultError("scope is required unless scope_type=global (e.g. scope_type=framework scope=wordpress)"), nil
			}
			// Best-effort source attribution from the resolved workspace.
			source := strings.TrimSpace(argString(args, "repo"))
			if source == "" {
				if e, err := resolveRepo(ctx, reg, ""); err == nil {
					source = e.Name
				}
			}
			h, err := hints.Add(scopeType, scope, text, source)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.MarshalIndent(map[string]any{
				"ok": true, "hint": h,
				"note": "stored globally — applied to any project matching this scope; review with action=list",
			}, "", "  ")
			return mcp.NewToolResultText(string(b)), nil

		case "list":
			list, err := hints.List(argString(args, "scope_type"), argString(args, "scope"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if list == nil {
				list = []hints.Hint{}
			}
			b, _ := json.MarshalIndent(map[string]any{"hints": list, "count": len(list)}, "", "  ")
			return mcp.NewToolResultText(string(b)), nil

		case "remove":
			id := strings.TrimSpace(argString(args, "id"))
			if id == "" {
				return mcp.NewToolResultError("id is required for remove (get it from action=list)"), nil
			}
			ok, err := hints.Remove(id)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(map[string]any{"ok": ok})
			return mcp.NewToolResultText(string(b)), nil

		default:
			return mcp.NewToolResultError("action must be add|list|remove"), nil
		}
	}
}
