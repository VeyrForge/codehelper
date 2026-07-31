package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type similarResponse struct {
	Target    string                   `json:"target"`
	Similar   []retrieval.RankedSymbol `json:"similar"`
	Note      string                   `json:"note,omitempty"`
	Freshness any                      `json:"freshness,omitempty"`
}

// similarHandler finds symbols whose implementation resembles a named target —
// similar-implementation search from goal.md, distinct from scout's task-oriented reuse.
func similarHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		name := strings.TrimSpace(argString(args, "name"))
		if name == "" {
			name = strings.TrimSpace(argString(args, "target"))
		}
		if name == "" {
			return mcp.NewToolResultError("name is required — the symbol to find similar implementations for (e.g. QueryHybridWithOptions)."), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = 8
		}
		hits, err := retrieval.FindSimilarSymbols(ctx, st, repo.Name, repo.RootPath, name, topK)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := similarResponse{
			Target:  name,
			Similar: hits,
			Note:    fmt.Sprintf("Ranked by signature/name/path similarity to %q — confirm with `context` before extending one.", name),
		}
		if len(hits) == 0 {
			out.Note = fmt.Sprintf("No similar symbols found for %q. Try `query` with a broader concept or `scout` with a task description.", name)
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}
