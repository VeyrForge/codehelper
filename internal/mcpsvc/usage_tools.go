package mcpsvc

import (
	"context"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/usage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// usageReportHandler returns the per-project usage report: codehelper tool output
// (context volume injected, by tool / session / client — works for every client)
// plus Claude's real billed token usage parsed from its transcripts. It reads
// only local files, so it never triggers an index.
func usageReportHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepo(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		refs := 20
		if v, ok := args["refs"].(float64); ok {
			refs = int(v)
		}
		rep, err := usage.BuildReport(repo.RootPath, refs)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rep.GeneratedAt = time.Now()
		rep.Verbose = argBool(args, "verbose", false)
		if strings.EqualFold(argString(args, "format"), "json") {
			return mcp.NewToolResultText(rep.RenderJSON()), nil
		}
		return mcp.NewToolResultText(rep.Render()), nil
	}
}
