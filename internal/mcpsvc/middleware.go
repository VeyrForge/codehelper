package mcpsvc

import (
	"context"

	"github.com/VeyrForge/codehelper/internal/telemetry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func timedTool(name string, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		done := telemetry.Timer("tool." + name)
		defer done()
		return h(ctx, req)
	}
}
