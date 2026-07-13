package mcpsvc

import (
	"context"

	"github.com/VeyrForge/codehelper/internal/orchestrator/eval"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// EvalRunner returns a benchmark runner wired to MCP tool handlers.
func EvalRunner(reg *registry.Registry) eval.Runner {
	return eval.Runner{
		Handlers: func(r *registry.Registry) map[string]func(context.Context, map[string]any) (string, error) {
			return evalHandlerMap(r)
		},
		RefreshIndex: true,
		Config:       eval.DefaultConfig(),
	}
}

func evalHandlerMap(reg *registry.Registry) map[string]func(context.Context, map[string]any) (string, error) {
	raw := AllToolHandlers(reg)
	out := make(map[string]func(context.Context, map[string]any) (string, error), len(raw))
	for name, fn := range raw {
		out[name] = wrapHandler(name, fn)
	}
	return out
}

func wrapHandler(name string, fn server.ToolHandlerFunc) func(context.Context, map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := fn(ctx, req)
		if err != nil {
			return "", err
		}
		return flattenEvalResult(res), nil
	}
}

func flattenEvalResult(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
		if tc, ok := c.(*mcp.TextContent); ok && tc != nil {
			return tc.Text
		}
	}
	return ""
}
