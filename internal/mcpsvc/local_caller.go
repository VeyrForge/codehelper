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

// AgentToolNames lists the MCP tools exposed to the LLM agent loop.
var AgentToolNames = []string{
	"project_context",
	"query",
	"context",
	"impact",
	"detect_changes",
	"read_workspace_file",
	"write_workspace_file",
	"apply_patch_workspace_file",
	"revert_workspace_edit",
	"list_workspace_directory",
}

// NewLocalToolCaller builds an in-process caller scoped to workspaceRoot.
// The workspace root substitutes for MCP client roots when resolving the
// default repo, matching what the stdio transport derives from the IDE.
func NewLocalToolCaller(reg *registry.Registry, workspaceRoot string) *LocalToolCaller {
	c := &LocalToolCaller{
		reg: reg,
		handlers: map[string]server.ToolHandlerFunc{
			"project_context":            projectContextHandler(reg),
			"query":                      queryHandler(reg),
			"context":                    contextHandler(reg),
			"impact":                     impactHandler(reg),
			"detect_changes":             detectChangesHandler(reg),
			"read_workspace_file":        readWorkspaceFileHandler(reg),
			"write_workspace_file":       writeWorkspaceFileHandler(reg),
			"apply_patch_workspace_file": applyPatchWorkspaceFileHandler(reg),
			"revert_workspace_edit":      revertWorkspaceEditHandler(reg),
			"list_workspace_directory":   listWorkspaceDirectoryHandler(reg),
		},
		defaultRepo: repoNameForRoot(reg, workspaceRoot),
	}
	return c
}

// DefaultRepo returns the registry repo name resolved for the workspace root.
func (c *LocalToolCaller) DefaultRepo() string {
	return c.defaultRepo
}

// repoNameForRoot maps an absolute workspace root onto a registry entry,
// mirroring the MCP-roots matching used for IDE sessions.
func repoNameForRoot(reg *registry.Registry, root string) string {
	root = strings.TrimSpace(root)
	if reg == nil || root == "" {
		return ""
	}
	rootN := normalizeComparablePath(root)
	var parentMatches []registry.Entry
	for _, e := range reg.List() {
		repoRoot := normalizeComparablePath(e.RootPath)
		if repoRoot == rootN || pathContains(repoRoot, rootN) {
			return e.Name
		}
		if pathContains(rootN, repoRoot) {
			parentMatches = append(parentMatches, e)
		}
	}
	if len(parentMatches) == 1 {
		return parentMatches[0].Name
	}
	return ""
}

// toolsAcceptingRepo lists tools whose handlers resolve an optional repo arg.
var toolsAcceptingRepo = map[string]bool{
	"project_context":            true,
	"query":                      true,
	"context":                    true,
	"impact":                     true,
	"detect_changes":             true,
	"context_pack":               true,
	"read_workspace_file":        true,
	"write_workspace_file":       true,
	"apply_patch_workspace_file": true,
	"list_workspace_directory":   true,
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
	if toolsAcceptingRepo[name] {
		if raw, _ := args["repo"].(string); strings.TrimSpace(raw) != "" && c.defaultRepo != "" && strings.TrimSpace(raw) != c.defaultRepo {
			b, _ := json.MarshalIndent(map[string]any{
				"isError": true,
				"message": fmt.Sprintf("repo %q is not the active workspace (%q); other projects are not accessible", strings.TrimSpace(raw), c.defaultRepo),
			}, "", "  ")
			return string(b), nil
		}
		if s, _ := args["repo"].(string); strings.TrimSpace(s) == "" && c.defaultRepo != "" {
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
