package helpcatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/mcpsvc"
)

func TestToolRefsComplete(t *testing.T) {
	t.Parallel()
	names := mcpsvc.AllMCPToolNames()
	for _, n := range names {
		if _, ok := toolRefs[n]; !ok {
			t.Errorf("toolRefs missing %q (registered in mcpsvc catalog)", n)
		}
	}
	if len(toolRefs) != len(names) {
		t.Errorf("toolRefs has %d entries, catalog has %d tools", len(toolRefs), len(names))
	}
}

func TestWriteProjectReference(t *testing.T) {
	dir := t.TempDir()
	if err := WriteProjectReference(dir); err != nil {
		t.Fatalf("WriteProjectReference: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".codehelper", "MCP_TOOLS.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(b)
	want := mcpsvc.MCPToolCatalogBrief().Count
	if !strings.Contains(body, "Tool count:") {
		t.Fatalf("missing tool count: %s", body[:min(120, len(body))])
	}
	if want != 62 {
		t.Fatalf("catalog count drifted: %d", want)
	}
	if !strings.Contains(body, "**kickoff**") && !strings.Contains(body, "- **kickoff**") {
		t.Fatalf("expected kickoff in generated catalog")
	}
}

func TestResolveTopic_tool(t *testing.T) {
	t.Parallel()
	kind, tool, cli, group := ResolveTopic("query")
	if kind != "tool" || tool == nil || cli != nil || group != nil {
		t.Fatalf("ResolveTopic(query) = %q tool=%v cli=%v group=%v", kind, tool, cli, group)
	}
	if tool.Name != "query" {
		t.Fatalf("got tool %q", tool.Name)
	}
}

func TestResolveTopic_group(t *testing.T) {
	t.Parallel()
	kind, _, _, group := ResolveTopic("graph")
	if kind != "group" || len(group) == 0 {
		t.Fatalf("ResolveTopic(graph) = %q group=%v", kind, group)
	}
}

func TestToolsMainOnly(t *testing.T) {
	t.Parallel()
	tools := Tools(Filter{MainOnly: true})
	if len(tools) != len(mcpsvc.MCPMainTools) {
		t.Fatalf("main tools: got %d want %d", len(tools), len(mcpsvc.MCPMainTools))
	}
}
