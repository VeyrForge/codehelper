package mcpsvc

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestReviewHandler_Graceful(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\n\nfunc A() int { return 1 }\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "format": "json"}
	res, err := reviewHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	// A temp repo without git history can't diff, so review must fail cleanly
	// (IsError) rather than panic; the happy path is covered by the real-repo eval.
	if !res.IsError {
		out := decodeStructured[reviewResponse](t, res)
		if out.Verdict == "" {
			t.Fatal("expected a verdict or a clean error")
		}
	}
}
