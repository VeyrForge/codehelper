package mcpsvc

import (
	"context"

	"github.com/VeyrForge/codehelper/internal/web"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// webHandler serves the `web` MCP tool: a fast, HTTP-only web verification check
// (codehelper's optimized Playwright alternative — no browser for the common
// case). It does not render client-side JavaScript.
func webHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		url := argString(args, "url")
		if url == "" {
			return mcp.NewToolResultError("url is required"), nil
		}
		c := web.Check{
			URL:            url,
			Method:         argString(args, "method"),
			Body:           argString(args, "body"),
			TimeoutSec:     int(mcp.ParseInt64(req, "timeout_sec", 0)),
			FollowRedirect: argBool(args, "follow_redirect", false),
			Insecure:       argBool(args, "insecure", false),
			AllowPrivate:   argBool(args, "allow_private", false),
			ExtractText:    argBool(args, "extract_text", false),
			ExpectStatus:   int(mcp.ParseInt64(req, "expect_status", 0)),
			ExpectContains: argStringSlice(args, "expect_contains"),
			ExpectAbsent:   argStringSlice(args, "expect_absent"),
			ExpectRegex:    argString(args, "expect_regex"),
			ExpectJSONPath: argString(args, "expect_json_path"),
			ExpectJSONVal:  argString(args, "expect_json_value"),
			MaxLatencyMs:   int(mcp.ParseInt64(req, "max_latency_ms", 0)),
		}
		if headers, ok := args["headers"].(map[string]any); ok {
			c.Headers = map[string]string{}
			for k, v := range headers {
				if s, ok := v.(string); ok {
					c.Headers[k] = s
				}
			}
		}
		res := web.Run(ctx, c)
		return mustToolResultFormatted(res, resolveFormat(args))
	}
}

// argStringSlice reads a string or []string/[]any argument into []string.
func argStringSlice(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
