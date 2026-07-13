package mcpsvc

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/toon"
	"github.com/mark3labs/mcp-go/mcp"
)

// resolveFormat picks the response text encoding for the array-heavy tools.
// Order: explicit `format` arg, then CODEHELPER_MCP_FORMAT env, else TOON.
//
// Why this matters for tokens: MCP clients that support structuredContent ALWAYS
// prefer it over the text block, so while a tool attached the JSON payload as
// structuredContent the model never actually read the token-efficient TOON — it
// read the JSON. For the default TOON encoding we therefore return the TOON as
// the ONLY content (no structuredContent), which is what finally delivers the
// savings to the model. format=json — and the small fixed-shape tools that call
// toolResultStructured directly — still return structuredContent for clients that
// consume it programmatically.
func resolveFormat(args map[string]any) string {
	if f := strings.ToLower(strings.TrimSpace(argString(args, "format"))); f != "" {
		return f
	}
	if f := strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_MCP_FORMAT"))); f != "" {
		return f
	}
	return "toon"
}

// toolResultFormatted encodes payload for the array-heavy tools. The default TOON
// encoding is returned as TEXT ONLY so the model actually reads the compact form
// (see resolveFormat); format=json falls back to the structured JSON result.
func toolResultFormatted(payload any, format string) (*mcp.CallToolResult, error) {
	if format == "json" {
		return toolResultStructured(payload)
	}
	t, err := toon.Marshal(payload)
	if err != nil {
		return toolResultStructured(payload) // safe fallback to JSON
	}
	return mcp.NewToolResultText(t), nil
}

// mustToolResultFormatted is the error-swallowing variant for handlers.
func mustToolResultFormatted(payload any, format string) (*mcp.CallToolResult, error) {
	r, err := toolResultFormatted(payload, format)
	if err != nil {
		return mcp.NewToolResultError("failed to encode tool result"), nil
	}
	return r, nil
}

// toolResultStructured returns MCP structuredContent plus identical JSON text.
// Used by small fixed-shape tools (project_context, workspace, edit previews)
// whose responses are consumed programmatically rather than read as prose.
func toolResultStructured(payload any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultStructured(payload, string(b)), nil
}

// mustToolResultStructured encodes payload as structuredContent plus JSON text.
func mustToolResultStructured(payload any) (*mcp.CallToolResult, error) {
	r, err := toolResultStructured(payload)
	if err != nil {
		return mcp.NewToolResultError("failed to encode tool result"), nil
	}
	return r, nil
}

// toolResultStructuredFromBytes uses pre-marshaled JSON as text while structuredContent uses payload.
func toolResultStructuredFromBytes(payload any, textJSON []byte) *mcp.CallToolResult {
	return mcp.NewToolResultStructured(payload, string(textJSON))
}
