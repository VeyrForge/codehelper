package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestSymbolMissGuidance_Healthish(t *testing.T) {
	dir := t.TempDir()
	res := symbolMissGuidance(context.Background(), nil, dir, "x", "impact", "health", "json")
	if res == nil || res.IsError {
		t.Fatalf("expected structured miss, got %#v %s", res, resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "change_kit") {
		t.Fatalf("expected change_kit steer: %s", text)
	}
	if !strings.Contains(text, "recommended_next_tools") {
		t.Fatalf("expected recommended_next_tools: %s", text)
	}
}

func TestSymbolMissGuidance_DiskMatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	if err := os.WriteFile(p, []byte("package main\n\nfunc UniqueProbeXYZ() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := symbolMissGuidance(context.Background(), nil, dir, "x", "context", "UniqueProbeXYZ", "json")
	if res == nil || res.IsError {
		t.Fatalf("expected disk_matches payload: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "disk_matches") && !strings.Contains(text, "main.go") {
		t.Fatalf("expected disk match note: %s", text)
	}
}

func TestContext_EmptyNameErrorMentionsAliases(t *testing.T) {
	reg, _, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error for missing name")
	}
	msg := resultText(res)
	if strings.Contains(msg, "not `symbol`") || strings.Contains(msg, "NOT symbol") {
		t.Fatalf("error must not forbid symbol alias: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "alias") {
		t.Fatalf("error should mention aliases: %s", msg)
	}
}

func TestContext_HealthishMissSteersToChangeKit(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "name": "health", "format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("healthish miss should be structured guidance, not hard error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "change_kit") {
		t.Fatalf("expected change_kit steer: %s", resultText(res))
	}
}

func TestImpact_HealthishMissSteersToChangeKit(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "target": "health", "format": "json"}
	res, err := impactHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("healthish miss should be structured guidance, not hard error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "change_kit") {
		t.Fatalf("expected change_kit steer: %s", resultText(res))
	}
}

func TestRename_AcceptsNewNameAlias(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() int { return 1 }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "name": "Helper", "new_name": "Helper2", "dry_run": true, "format": "json",
	}
	res, err := renameSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("new_name alias should work: %s", resultText(res))
	}
}

func TestInsert_AcceptsContentAlias(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() int { return 1 }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "name": "Helper", "position": "after",
		"content": "// probe\n", "dry_run": true, "format": "json",
	}
	res, err := insertAtSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("content alias should work: %s", resultText(res))
	}
}
