package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// scout should surface a real call site of the top reuse candidate so the agent
// can copy the calling convention.
func TestScoutHandler_UsageExample(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"store.go":  "package x\n\n// AcquireLock takes the global lock.\nfunc AcquireLock() error { return nil }\n",
		"caller.go": "package x\n\nfunc bootstrap() error {\n\tif err := AcquireLock(); err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n",
	})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":   repo.Name,
		"task":   "acquire the global lock",
		"format": "json",
	}
	res, err := scoutHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := decodeStructured[scoutResponse](t, res)
	if len(out.ReuseCandidates) == 0 {
		t.Fatal("expected reuse candidates")
	}
	if out.UsageOfTop == nil {
		t.Fatalf("expected a usage example for the top candidate; got none (candidates=%+v)", out.ReuseCandidates)
	}
	if out.UsageOfTop.Caller != "bootstrap" {
		t.Errorf("expected caller 'bootstrap', got %q", out.UsageOfTop.Caller)
	}
	if got := out.UsageOfTop.Code; got == "" || !strings.Contains(got, "AcquireLock()") {
		t.Errorf("usage code should show the call site, got %q", got)
	}
	if out.UsageOfTop.Loc == "" {
		t.Error("usage example missing location")
	}
}
