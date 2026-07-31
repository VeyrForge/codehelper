package mcpsvc

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/verify"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestArgCommandLine_JSONArray(t *testing.T) {
	got, note, err := argCommandLine(map[string]any{
		"lint_cmd": []any{"cmd", "/c", "echo", "lint-ok"},
	}, "lint_cmd", "lint")
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Fatalf("native JSON array should not need recovery note, got %q", note)
	}
	if strings.HasPrefix(strings.TrimSpace(got), "[") {
		t.Fatalf("argv still looks mangled: %q", got)
	}
	if !strings.Contains(got, "cmd") || !strings.Contains(got, "lint-ok") {
		t.Fatalf("unexpected cmdline %q", got)
	}
	// Round-trip through JoinArgv of known tokens must match.
	want := verify.JoinArgv([]string{"cmd", "/c", "echo", "lint-ok"})
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestArgCommandLine_GoSprintMangled(t *testing.T) {
	// Exact Windows MCP footgun from HONEST review: Sprint of []string.
	got, note, err := argCommandLine(map[string]any{
		"lint_cmd": "[cmd /c echo lint-ok]",
	}, "lint_cmd")
	if err != nil {
		t.Fatal(err)
	}
	if note == "" || !strings.Contains(strings.ToLower(note), "mangled") {
		t.Fatalf("expected recovery note, got %q", note)
	}
	if strings.HasPrefix(strings.TrimSpace(got), "[") || strings.Contains(got, "[cmd") {
		t.Fatalf("still mangled: %q", got)
	}
	want := verify.JoinArgv([]string{"cmd", "/c", "echo", "lint-ok"})
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestArgCommandLine_PlainString(t *testing.T) {
	got, note, err := argCommandLine(map[string]any{"test_cmd": "go test ./..."}, "test_cmd", "test")
	if err != nil {
		t.Fatal(err)
	}
	if note != "" || got != "go test ./..." {
		t.Fatalf("got %q note=%q", got, note)
	}
}

func TestArgCommandLine_Unrecoverable(t *testing.T) {
	_, _, err := argCommandLine(map[string]any{
		"lint_cmd": `[token with"quote inside]`,
	}, "lint_cmd")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, verify.ErrMangledArgv) {
		t.Fatalf("want ErrMangledArgv, got %v", err)
	}
}

func TestArgAliasCorrection(t *testing.T) {
	note := argAliasCorrection(map[string]any{"symbol": "Helper"}, "name", "symbol", "sym")
	if note == "" || !strings.Contains(note, "symbol") || !strings.Contains(note, "name=") {
		t.Fatalf("expected teach-fix note, got %q", note)
	}
	if got := argAliasCorrection(map[string]any{"name": "Helper", "symbol": "x"}, "name", "symbol"); got != "" {
		t.Fatalf("canonical present → no note, got %q", got)
	}
}

func TestVerify_AcceptsJSONArrayArgv(t *testing.T) {
	dir := t.TempDir()
	lint := []any{"echo", "lint-ok"}
	if runtime.GOOS == "windows" {
		lint = []any{"cmd", "/c", "echo", "lint-ok"}
	}
	reg := &registry.Registry{Entries: map[string]registry.Entry{}}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo_root": dir,
		"lint_cmd":  lint,
		"format":    "json",
	}
	res, err := verifyHandler(reg)(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	txt := resultText(res)
	if res.IsError {
		t.Fatalf("verify JSON-array argv should not error: %s", txt)
	}
	if strings.Contains(txt, `"[echo`) || strings.Contains(txt, `"[cmd`) || strings.Contains(txt, "executable file not found") {
		t.Fatalf("argv still mangled / not found: %s", txt)
	}
	if !strings.Contains(txt, "accepted") {
		t.Fatalf("unexpected verify output: %s", truncateSmoke(txt, 600))
	}
}

func TestVerify_RecoversSprintMangledArgv(t *testing.T) {
	dir := t.TempDir()
	mangled := "[cmd /c echo lint-ok]"
	if runtime.GOOS != "windows" {
		mangled = "[echo lint-ok]"
	}
	reg := &registry.Registry{Entries: map[string]registry.Entry{}}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo_root": dir,
		"lint_cmd":  mangled,
		"format":    "json",
	}
	res, err := verifyHandler(reg)(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	txt := resultText(res)
	if res.IsError {
		t.Fatalf("mangled argv should recover, not error: %s", txt)
	}
	if strings.Contains(txt, "executable file not found") || strings.Contains(txt, `"[cmd`) || strings.Contains(txt, `"[echo`) {
		t.Fatalf("still broken after recover: %s", txt)
	}
	if !strings.Contains(strings.ToLower(txt), "mangled") && !strings.Contains(txt, "Accepted") {
		t.Fatalf("expected recovery note: %s", truncateSmoke(txt, 600))
	}
}

func TestContext_SymbolAliasSurfacesCorrection(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\nfunc Helper() int { return 1 }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "symbol": "Helper", "format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("symbol alias should resolve: %s", resultText(res))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "param_correction") && !strings.Contains(txt, "Accepted alias") {
		t.Fatalf("expected alias teach-fix note, got: %s", truncateSmoke(txt, 800))
	}
}
