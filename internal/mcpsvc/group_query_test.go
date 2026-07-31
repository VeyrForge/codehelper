//go:build !windows

package mcpsvc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestQuery_GroupFanOut(t *testing.T) {
	_, apiEntry, apiCtx := buildIndexedRepo(t, map[string]string{
		"user.go": "package api\n\ntype UserService struct{}\n\nfunc (u *UserService) FindAll() []string { return nil }\n",
	})
	_, webEntry, _ := buildIndexedRepo(t, map[string]string{
		"client.go": "package web\n\ntype UserService struct{}\n",
	})
	reg := &registry.Registry{
		Entries: map[string]registry.Entry{
			apiEntry.Name: apiEntry,
			webEntry.Name: webEntry,
		},
		Groups: map[string]registry.WorkspaceGroup{},
	}
	if err := reg.UpsertGroup(registry.WorkspaceGroup{
		ID: "platform", Name: "Platform", Members: []string{apiEntry.Name, webEntry.Name},
	}); err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": apiEntry.Name, "query": "UserService", "group": "platform", "format": "json",
	}
	res, err := queryHandler(reg)(apiCtx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[queryToolResponse](t, res)
	if out.GroupQuery == nil {
		t.Fatal("expected group_query payload")
	}
	if out.GroupQuery.Count < 2 {
		t.Fatalf("expected hits in both repos, got %#v", out.GroupQuery.Hits)
	}
	repos := map[string]bool{}
	for _, h := range out.GroupQuery.Hits {
		repos[h.Repo] = true
	}
	if !repos[apiEntry.Name] || !repos[webEntry.Name] {
		t.Fatalf("missing repo in hits: %#v", out.GroupQuery.Hits)
	}
	if !out.GroupQuery.Ambiguous {
		t.Fatal("expected ambiguous=true for duplicate UserService")
	}
}

func TestQuery_GroupMissingReturnsError(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\nfunc R() {}\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "query": "R", "group": "nope", "format": "json",
	}
	res, err := queryHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	msg := errorText(t, res)
	if !strings.Contains(msg, "not found") {
		t.Fatalf("expected not found error, got %q", msg)
	}
}

func TestQuery_PathFilterSingleRepo(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"lib/a.go":    "package x\nfunc Alpha() {}\n",
		"sample/b.go": "package x\nfunc Beta() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "query": "Alpha", "path": "lib/", "format": "json",
	}
	res, err := queryHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[queryToolResponse](t, res)
	hits, ok := out.Hits.([]any)
	if !ok {
		// concise mode returns []compactHit via json round-trip as []any maps
		if len(out.EvidencePaths) == 0 {
			t.Fatalf("expected path-filtered hits, got %#v", out.Hits)
		}
		return
	}
	for _, h := range hits {
		m, _ := h.(map[string]any)
		if p, _ := m["path"].(string); p != "" && !strings.Contains(p, "lib/") {
			t.Fatalf("path filter leak: %#v", m)
		}
	}
}
