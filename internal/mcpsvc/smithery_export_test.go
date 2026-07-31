package mcpsvc

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/mark3labs/mcp-go/server"
)

// smitheryServerCard is the metadata Smithery scores for stdio MCPB bundles.
type smitheryServerCard struct {
	ServerInfo struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description,omitempty"`
	} `json:"serverInfo"`
	Tools   []smitheryTool   `json:"tools"`
	Prompts []smitheryPrompt `json:"prompts,omitempty"`
}

type smitheryTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

type smitheryPrompt struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// smitheryDefaultOutputSchema is attached only in the Smithery export manifest
// (not live MCP tools/list) so directory scoring sees outputSchema without
// risking strict-client rejection from production tool registration.
var smitheryDefaultOutputSchema = json.RawMessage(`{"type":"object","description":"Tool result payload (TOON or JSON text per format= parameter).","additionalProperties":true}`)

// TestExportSmitheryServerCard writes Smithery-scorable tool metadata when
// SMITHERY_EXPORT=/path/to/server-card.json is set (used by scripts/publish-smithery.sh).
func TestExportSmitheryServerCard(t *testing.T) {
	outPath := strings.TrimSpace(os.Getenv("SMITHERY_EXPORT"))
	if outPath == "" {
		t.Skip("set SMITHERY_EXPORT to write server card JSON")
	}

	reg, err := registry.Load()
	if err != nil {
		t.Skipf("registry load failed: %v", err)
	}
	srv := server.NewMCPServer("codehelper", version.Current())
	RegisterAll(srv, reg)

	names := make([]string, 0, len(srv.ListTools()))
	for name := range srv.ListTools() {
		names = append(names, name)
	}
	sort.Strings(names)

	card := smitheryServerCard{}
	card.ServerInfo.Name = "Codehelper by VeyrForge"
	card.ServerInfo.Version = version.Current()
	card.ServerInfo.Description = "Local-first code intelligence MCP by VeyrForge: symbol/call graph, impact analysis, hybrid search, browser QA, and 60+ tools for Cursor, Claude Code, and Codex. Your code stays on your machine."

	for _, name := range names {
		st := srv.ListTools()[name]
		raw, err := json.Marshal(st.Tool)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		var wire struct {
			Description  string          `json:"description"`
			InputSchema  json.RawMessage `json:"inputSchema"`
			OutputSchema json.RawMessage `json:"outputSchema"`
			Annotations  json.RawMessage `json:"annotations"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if len(wire.InputSchema) == 0 {
			t.Fatalf("%s: missing inputSchema", name)
		}
		desc := strings.TrimSpace(wire.Description)
		if desc == "" {
			t.Fatalf("%s: missing description", name)
		}
		tool := smitheryTool{
			Name:        name,
			Description: desc,
			InputSchema: wire.InputSchema,
		}
		if len(wire.Annotations) > 0 && string(wire.Annotations) != "null" {
			tool.Annotations = wire.Annotations
		}
		if len(wire.OutputSchema) > 0 && string(wire.OutputSchema) != "null" {
			tool.OutputSchema = wire.OutputSchema
		} else {
			tool.OutputSchema = smitheryDefaultOutputSchema
		}
		card.Tools = append(card.Tools, tool)
	}

	card.Prompts = []smitheryPrompt{
		{Name: "detect_impact", Description: "Pre-change impact checklist grounded in the indexed call graph."},
		{Name: "generate_map", Description: "Architecture map from the symbol/call graph."},
		{Name: "intake_project_brief", Description: "Structured intake: ask only architecture-shifting questions and document assumptions."},
		{Name: "planning_contract", Description: "Output contract before edits: files, tests, verify commands, rollback."},
		{Name: "agent_guardrails", Description: "Always-on rails: graph tools first, argv-mode verify, no debug prints."},
	}

	b, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d tools, %d prompts to %s", len(card.Tools), len(card.Prompts), outPath)
}
