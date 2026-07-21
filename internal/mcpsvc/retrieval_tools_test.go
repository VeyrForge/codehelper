//go:build !windows

package mcpsvc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestSearchHybridAndContextBundle(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"lib/api.go": `package lib

// Client is the public entry.
type Client struct{}

func (c *Client) Save() {
	hashPassword()
}

func hashPassword() {}
`,
	})

	shReq := mcp.CallToolRequest{}
	shReq.Params.Arguments = map[string]any{
		"repo":   repo.Name,
		"query":  "Save",
		"path":   "lib/",
		"top_k":  float64(10),
		"format": "json",
	}
	shRes, err := searchHybridHandler(reg)(ctx, shReq)
	if err != nil || shRes.IsError {
		t.Fatalf("search_hybrid: err=%v res=%s", err, resultText(shRes))
	}
	var sh searchHybridResponse
	if err := json.Unmarshal([]byte(resultText(shRes)), &sh); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, resultText(shRes))
	}
	if !strings.Contains(sh.FusionNote, "RRF") {
		t.Fatalf("expected RRF fusion note, got %q", sh.FusionNote)
	}
	if len(sh.PublicAPIMap) == 0 {
		t.Fatal("expected public_api_map for path=lib/")
	}

	cbReq := mcp.CallToolRequest{}
	cbReq.Params.Arguments = map[string]any{
		"repo":   repo.Name,
		"name":   "Save",
		"path":   "lib/api.go",
		"format": "json",
	}
	cbRes, err := contextBundleHandler(reg)(ctx, cbReq)
	if err != nil || cbRes.IsError {
		t.Fatalf("context_bundle: err=%v res=%s", err, resultText(cbRes))
	}
	text := resultText(cbRes)
	if !strings.Contains(text, "Save") {
		t.Fatalf("expected Save in bundle: %s", text)
	}
	if !strings.Contains(text, "hashPassword") && !strings.Contains(text, "callers") {
		t.Fatalf("expected graph fields in bundle: %s", text)
	}
}

func TestRetrievalFacadesInCatalog(t *testing.T) {
	names := map[string]struct{}{}
	for _, n := range AllMCPToolNames() {
		names[n] = struct{}{}
	}
	for _, want := range []string{"search_hybrid", "context_bundle"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("missing %s in catalog", want)
		}
		if !IsFocusedTool(want) {
			t.Fatalf("%s should be in MinimalToolSet", want)
		}
	}
}
