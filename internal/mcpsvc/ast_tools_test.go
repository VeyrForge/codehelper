package mcpsvc

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestASTQueryHandler_FindsGoFunctions(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Alpha() {}\n\nfunc beta() error { return nil }\n",
		"b.go": "package x\n\ntype T struct{}\n\nfunc (t T) Gamma() {}\n",
	})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":     repo.Name,
		"language": "go",
		"pattern":  `(function_declaration name: (identifier) @name)`,
		"format":   "json",
	}
	res, err := astQueryHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res.Content)
	}
	out := decodeStructured[astQueryResponse](t, res)
	names := map[string]bool{}
	for _, m := range out.Matches {
		names[m.Text] = true
	}
	if !names["Alpha"] || !names["beta"] {
		t.Errorf("expected Alpha and beta funcs, got %v", names)
	}
	if names["Gamma"] {
		t.Errorf("method Gamma should not match function_declaration")
	}
	if out.FilesScanned < 2 {
		t.Errorf("expected at least 2 files scanned, got %d", out.FilesScanned)
	}
}

func TestASTQueryHandler_PathGlobNarrows(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"keep/a.go": "package x\nfunc Keep() {}\n",
		"skip/b.go": "package y\nfunc Skip() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":      repo.Name,
		"language":  "go",
		"pattern":   `(function_declaration name: (identifier) @name)`,
		"path_glob": "keep/",
		"format":    "json",
	}
	res, err := astQueryHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[astQueryResponse](t, res)
	if len(out.Matches) != 1 || out.Matches[0].Text != "Keep" {
		t.Fatalf("path_glob did not narrow to keep/: %+v", out.Matches)
	}
}

func TestASTQueryHandler_BadPatternErrorsCleanly(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":     repo.Name,
		"language": "go",
		"pattern":  `(this is not balanced`,
	}
	res, err := astQueryHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected a tool error for a malformed pattern")
	}
}

// Security: a path_glob containing traversal sequences must not read files
// outside the repo — it simply matches no relative path and returns nothing.
func TestASTQueryHandler_PathGlobNoTraversal(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\nfunc Inside() {}\n",
	})
	for _, glob := range []string{"../", "../../", "/etc/", "..\\"} {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{
			"repo": repo.Name, "language": "go", "format": "json",
			"pattern": `(function_declaration name: (identifier) @n)`, "path_glob": glob,
		}
		res, err := astQueryHandler(reg)(ctx, req)
		if err != nil || res.IsError {
			t.Fatalf("glob %q: err=%v isErr=%v", glob, err, res.IsError)
		}
		out := decodeStructured[astQueryResponse](t, res)
		if len(out.Matches) != 0 {
			t.Errorf("traversal glob %q should match nothing, got %+v", glob, out.Matches)
		}
	}
}

// A cancelled context must abort with a clean error, not scan the whole tree.
func TestASTQueryHandler_HonorsCancellation(t *testing.T) {
	reg, repo, baseCtx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\nfunc A() {}\n",
		"b.go": "package x\nfunc B() {}\n",
	})
	ctx, cancel := context.WithCancel(baseCtx)
	cancel() // cancel before running
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "language": "go",
		"pattern": `(function_declaration name: (identifier) @n)`,
	}
	res, err := astQueryHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("transport err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected a cancellation error")
	}
}

func TestASTQueryHandler_UnsupportedLanguage(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":     repo.Name,
		"language": "cobol",
		"pattern":  `(x)`,
	}
	res, _ := astQueryHandler(reg)(ctx, req)
	if !res.IsError {
		t.Fatalf("expected error for unsupported language")
	}
}
