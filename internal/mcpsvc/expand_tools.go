package mcpsvc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/VeyrForge/codehelper/internal/patterns"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterExpandRequestTools wires expand_request and pattern selection MCP tools.
func RegisterExpandRequestTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("expand_request",
		mcp.WithDescription("Expand a vague user request into inferred requirements, risks, and pattern-pack hints before planning. Use early in intake; output feeds `agent_plan` / `kickoff`. Does not edit code."),
		mcp.WithString("request", mcp.Required()),
		mcp.WithString("project_type", mcp.Description("optional override")),
		mcp.WithString("changed_area", mcp.Description("frontend|backend|fullstack")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotReadOnlyClosedWorld(),
	), timedTool("expand_request", expandRequestHandler(regRef)))

	s.AddTool(mcp.NewTool("select_pattern",
		mcp.WithDescription("Pick the best-matching feature pattern for a natural-language request from project pattern packs. Use before `agent_plan` when the task type is unclear; pass `request` (not feature_type)."),
		mcp.WithString("request", mcp.Required(), mcp.Description("Natural-language feature request")),
		mcp.WithString("project_type", mcp.Description("optional")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotReadOnlyClosedWorld(),
	), timedTool("select_pattern", selectPatternHandler(regRef)))

}

func expandRequestHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		packs, err := patterns.LoadAll(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		in := patterns.ExpandInput{
			Request:     strings.TrimSpace(argString(args, "request")),
			ProjectType: pickProjectType(repo.RootPath, argString(args, "project_type")),
			ChangedArea: argString(args, "changed_area"),
		}
		if in.Request == "" {
			return mcp.NewToolResultError("request required"), nil
		}
		out := patterns.ExpandRequest(in, packs)
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	}
}

func selectPatternHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		packs, err := patterns.LoadAll(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		q := strings.TrimSpace(argString(args, "request"))
		if q == "" {
			return mcp.NewToolResultError("request required"), nil
		}
		pt := pickProjectType(repo.RootPath, argString(args, "project_type"))
		p, score := patterns.SelectPattern(q, pt, packs)
		triggers := p.Triggers
		if triggers == nil {
			triggers = []string{}
		}
		out := map[string]any{
			"pattern_id": p.ID,
			"score":      score,
			"matched":    p.ID != "" && score > 0,
			"triggers":   triggers,
		}
		if p.ID == "" || score <= 0 {
			out["note"] = "no bundled or repository pattern matched this request; use expand_request for generic inferred requirements"
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	}
}

func pickProjectType(repoRoot, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if pr, err := profile.ReadOrGenerate(repoRoot); err == nil && pr != nil {
		return pr.ProjectType
	}
	return ""
}
