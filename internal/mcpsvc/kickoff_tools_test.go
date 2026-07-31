package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestKickoff_DemotesFixtureReuse(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"sample/demo/lock.go": "package demo\n\nfunc AcquireLock() error { return nil }\n",
		"pkg/lock.go":         "package pkg\n\n// AcquireLock is the production lock helper.\nfunc AcquireLock() error { return nil }\n",
		"pkg/use.go":          "package pkg\n\nfunc boot() error {\n\treturn AcquireLock()\n}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":   repo.Name,
		"task":   "acquire the global lock",
		"format": "json",
	}
	res, err := kickoffHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := decodeStructured[kickoffResponse](t, res)
	if len(out.ReuseCandidates) == 0 {
		t.Fatal("expected reuse candidates")
	}
	top := out.ReuseCandidates[0].Loc
	if strings.Contains(top, "sample/") {
		t.Fatalf("top reuse still under sample/: %s (collision=%q)", top, out.CollisionNote)
	}
	if !strings.Contains(top, "pkg/") {
		t.Logf("top loc=%s candidates=%+v note=%q", top, out.ReuseCandidates, out.CollisionNote)
	}
}
