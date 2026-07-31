package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/websearch"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// webSearchHandler serves the `web_search` MCP tool: a real web search via the
// configured provider (Tavily/Brave with a free key, or keyless DuckDuckGo),
// returning a compact ranked list the agent can then fetch/verify with the `web`
// or `browser` tools. It does not crawl — it finds the URLs to look at.
func webSearchHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		query := argString(args, "query")
		if query == "" {
			return mcp.NewToolResultError("query is required"), nil
		}
		count := int(mcp.ParseInt64(req, "count", 0))
		resp, err := websearch.Search(ctx, query, count, argString(args, "provider"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(renderSearch(resp)), nil
	}
}

func renderSearch(r *websearch.Response) string {
	var b strings.Builder
	fmt.Fprintf(&b, "web_search (%s) · %q · %d results\n", r.Provider, r.Query, len(r.Results))
	if strings.TrimSpace(r.Answer) != "" {
		fmt.Fprintf(&b, "\nanswer: %s\n", r.Answer)
	}
	for i, res := range r.Results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, strings.TrimSpace(res.Title), res.URL)
		if s := strings.TrimSpace(res.Snippet); s != "" {
			fmt.Fprintf(&b, "   %s\n", s)
		}
	}
	if len(r.Results) == 0 {
		b.WriteString("\n(no results)\n")
	}
	return b.String()
}
