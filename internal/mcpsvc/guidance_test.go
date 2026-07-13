package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// errorText extracts the text of a tool-error result.
func errorText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected a tool error, got success")
	}
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("no text content in error result")
	return ""
}

// Errors are feedback: context on a missing symbol must point at the recovery
// tool (query), not just say "not found".
func TestGuidance_ContextMissingSymbolPointsToQuery(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\nfunc Real() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "name": "DoesNotExist123"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	msg := errorText(t, res)
	if !strings.Contains(msg, "query") {
		t.Errorf("not-found error should steer to `query`, got: %q", msg)
	}
	if !strings.Contains(msg, "analyze") {
		t.Errorf("not-found error should mention the stale-index fallback, got: %q", msg)
	}
}

func TestGuidance_ContextEmptyNameGuides(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\nfunc R() {}\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "name": "  "}
	res, _ := contextHandler(reg)(ctx, req)
	msg := errorText(t, res)
	if !strings.Contains(msg, "sym:") {
		t.Errorf("empty-name error should explain the sym: id form, got: %q", msg)
	}
}

// No stale tool references (cypher/scip/list_repos were removed) survive in
// runtime guidance — pointing the agent at a deleted tool makes it fail.
func TestGuidance_NoDeadToolReferencesInContextNote(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\nfunc Lonely() {}\n", // a symbol with no edges → triggers the note
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "name": "Lonely", "format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	var note string
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			note = tc.Text
		}
	}
	for _, dead := range []string{"cypher", "path_between", "list_repos", "scip"} {
		if strings.Contains(note, dead) {
			t.Errorf("context note references removed tool %q: %q", dead, note)
		}
	}
}

// Zero query hits must yield actionable next steps, not a dead end.
func TestGuidance_QueryEmptyResultsSteersNextAction(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\nfunc Real() {}\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "query": "zzzznomatchqwerty", "format": "json"}
	res, err := queryHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[queryToolResponse](t, res)
	if !strings.Contains(out.RetrievalNote, "No indexed symbols matched") {
		t.Skipf("query matched something; empty path not exercised (note=%q)", out.RetrievalNote)
	}
	if !strings.Contains(out.RetrievalNote, "ast_query") || !strings.Contains(out.RetrievalNote, "analyze") {
		t.Errorf("empty-result note should name concrete next moves (ast_query, analyze), got: %q", out.RetrievalNote)
	}
}

// The MCP server must scope to its working directory when a client doesn't send
// workspace roots — otherwise tools error "workspace not initialized" and the
// agent abandons them, and the cross-project isolation guard goes inert.
func TestWorkingDirRootFallback(t *testing.T) {
	roots, ok := workingDirRoot()
	if !ok || len(roots) == 0 {
		t.Fatalf("workingDirRoot should return the CWD as a root, got ok=%v roots=%v", ok, roots)
	}
}
