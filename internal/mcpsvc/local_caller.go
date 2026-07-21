package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// LocalToolCaller invokes the agent-facing MCP tool handlers in-process,
// bypassing stdio/HTTP transports. It is used by the Go agent loop so the
// core does not need an MCP round-trip to its own binary.
type LocalToolCaller struct {
	reg         *registry.Registry
	handlers    map[string]server.ToolHandlerFunc
	defaultRepo string
}

// AgentToolNames is the serve / in-process agent surface for feature lifecycle
// work (add / remove / review). It used to be a 10-tool subset that omitted
// kickoff, change_kit, plan, review_diff, finish_check, etc. — so HTTP serve
// and agent chat could not run the same loops as full MCP stdio. Keep this
// well under ~40 tools (selection accuracy cliff) while covering orient →
// edit → gate end-to-end.
var AgentToolNames = []string{
	// bootstrap / orient
	"project_context", "kickoff", "scope",
	// find / understand
	"query", "scout", "context", "trace", "impact", "test_impact",
	"search_hybrid", "context_bundle",
	"api_surface", "ast_query", "find_implementations",
	// plan / edit
	"plan", "change_kit", "rename_symbol", "insert_at_symbol",
	"read_workspace_file", "write_workspace_file",
	"apply_patch_workspace_file", "revert_workspace_edit",
	"list_workspace_directory",
	// analysis
	"dead_code", "hotspots", "diagnostics", "detect_changes", "since",
	// gates
	"review_diff", "review", "verify", "finish_check",
}

// NewLocalToolCaller builds an in-process caller scoped to workspaceRoot.
// The workspace root substitutes for MCP client roots when resolving the
// default repo, matching what the stdio transport derives from the IDE.
func NewLocalToolCaller(reg *registry.Registry, workspaceRoot string) *LocalToolCaller {
	all := AllToolHandlers(reg)
	handlers := make(map[string]server.ToolHandlerFunc, len(AgentToolNames))
	for _, name := range AgentToolNames {
		if h, ok := all[name]; ok {
			handlers[name] = h
		}
	}
	return &LocalToolCaller{
		reg:         reg,
		handlers:    handlers,
		defaultRepo: repoNameForRoot(reg, workspaceRoot),
	}
}

// DefaultRepo returns the registry repo name resolved for the workspace root.
func (c *LocalToolCaller) DefaultRepo() string {
	return c.defaultRepo
}

// repoNameForRoot maps an absolute workspace root onto a registry entry,
// mirroring the MCP-roots matching used for IDE sessions (deepest nested
// registered project wins when cwd sits under multiple roots).
func repoNameForRoot(reg *registry.Registry, root string) string {
	root = strings.TrimSpace(root)
	if reg == nil || root == "" {
		return ""
	}
	if name, _, ok := repoNameForRoots(reg, []string{normalizeComparablePath(root)}); ok {
		return name
	}
	return ""
}

// Call invokes one tool and flattens its MCP result to the same text payload
// a remote MCP client would see. Tool-level failures are returned as JSON
// error payloads (not Go errors) so the LLM loop can react to them.
func (c *LocalToolCaller) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	h, ok := c.handlers[name]
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if c.defaultRepo != "" {
		if raw, _ := args["repo"].(string); strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != c.defaultRepo {
			b, _ := json.MarshalIndent(map[string]any{
				"isError": true,
				"message": fmt.Sprintf("repo %q is not the active workspace (%q); other projects are not accessible", strings.TrimSpace(raw), c.defaultRepo),
			}, "", "  ")
			return string(b), nil
		}
		if s, _ := args["repo"].(string); strings.TrimSpace(s) == "" {
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

// WorkspaceToolsAvailable always holds for the in-process caller; the agent
// loop uses it to pick strict breadth requirements.
func (c *LocalToolCaller) WorkspaceToolsAvailable() bool {
	return true
}

// flattenToolResult mirrors the extension's normalizeCallToolResult: unwrap
// the first text content part, surface isError as a JSON envelope.
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
