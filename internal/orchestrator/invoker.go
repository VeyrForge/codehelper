package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolInvoker calls MCP tool handlers in-process.
type ToolInvoker interface {
	Call(ctx context.Context, name string, args map[string]any) (string, error)
}

// MCPToolInvoker wraps MCP handlers for orchestration.
type MCPToolInvoker struct {
	handlers    map[string]server.ToolHandlerFunc
	defaultRepo string
}

// NewMCPToolInvoker builds an invoker from handler map and default repo name.
func NewMCPToolInvoker(handlers map[string]server.ToolHandlerFunc, defaultRepo string) *MCPToolInvoker {
	return &MCPToolInvoker{handlers: handlers, defaultRepo: defaultRepo}
}

// Call invokes a tool by name.
func (c *MCPToolInvoker) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	h, ok := c.handlers[name]
	if !ok {
		return "", fmt.Errorf("orchestrator: tool %q not available", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if c.defaultRepo != "" {
		if raw, _ := args["repo"].(string); strings.TrimSpace(raw) == "" {
			args["repo"] = c.defaultRepo
		}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		return "", err
	}
	return flattenToolResult(res), nil
}

func flattenToolResult(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var text string
	for _, content := range res.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			text = strings.TrimSpace(tc.Text)
			break
		}
		if tc, ok := content.(*mcp.TextContent); ok && tc != nil {
			text = strings.TrimSpace(tc.Text)
			break
		}
	}
	if res.IsError {
		b, err := json.MarshalIndent(map[string]any{"isError": true, "message": text}, "", "  ")
		if err != nil {
			return text
		}
		return string(b)
	}
	if text != "" {
		return text
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}
