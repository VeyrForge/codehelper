package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestAPISurface_ListsExportedOnly(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"pkg/api.go": "package pkg\n\nfunc Exported() {}\nfunc unexported() {}\ntype Public struct{}\ntype private struct{}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "path": "pkg/", "format": "json"}
	res, err := apiSurfaceHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[apiSurfaceResponse](t, res)
	names := map[string]bool{}
	for _, s := range out.Exported {
		names[s.Name] = true
	}
	if !names["Exported"] || !names["Public"] {
		t.Errorf("expected exported symbols, got %v", names)
	}
	if names["unexported"] || names["private"] {
		t.Errorf("unexported symbols leaked: %v", names)
	}

	// include_unexported=true surfaces internals.
	req.Params.Arguments = map[string]any{"repo": repo.Name, "path": "pkg/", "format": "json", "include_unexported": true}
	res2, _ := apiSurfaceHandler(reg)(ctx, req)
	out2 := decodeStructured[apiSurfaceResponse](t, res2)
	all := map[string]bool{}
	for _, s := range out2.Exported {
		all[s.Name] = true
	}
	if !all["unexported"] {
		t.Errorf("include_unexported should list internals, got %v", all)
	}
}

func TestChangeKit_AssemblesEditContext(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go":      "package x\n\n// Helper does the thing.\nfunc Helper(n int) int { return n + 1 }\n",
		"caller.go":      "package x\n\nfunc run() int { return Helper(5) }\n",
		"target_test.go": "package x\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) { _ = Helper(1) }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "target": "Helper", "format": "json"}
	res, err := changeKitHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[changeKitResponse](t, res)
	if out.Target.Name != "Helper" {
		t.Errorf("wrong target: %+v", out.Target)
	}
	if !strings.Contains(out.Definition, "func Helper") {
		t.Errorf("definition not captured: %q", out.Definition)
	}
	foundCaller := false
	for _, c := range out.Callers {
		if c.Caller == "run" && strings.Contains(c.Code, "Helper(5)") {
			foundCaller = true
		}
	}
	if !foundCaller {
		t.Errorf("expected caller 'run' with call line, got %+v", out.Callers)
	}
	if len(out.Tests) == 0 {
		t.Errorf("expected TestHelper as a covering test, got %+v", out.Tests)
	}
	if len(out.Checklist) == 0 {
		t.Error("expected a consistency checklist")
	}
}

func TestFindImplementations_StructuralMatch(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"iface.go": "package x\n\ntype Closer interface {\n\tClose() error\n}\n",
		"impl.go":  "package x\n\ntype File struct{}\nfunc (f File) Close() error { return nil }\n\ntype Half struct{}\nfunc (h Half) Open() error { return nil }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "interface": "Closer", "format": "json"}
	res, err := findImplementationsHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[findImplResponse](t, res)
	if len(out.Methods) != 1 || out.Methods[0] != "Close" {
		t.Errorf("expected interface method set [Close], got %v", out.Methods)
	}
	var fullImpl bool
	for _, im := range out.Implementations {
		if im.Type == "File" && len(im.Missing) == 0 {
			fullImpl = true
		}
		if im.Type == "Half" && len(im.Missing) == 0 {
			t.Errorf("Half should NOT be a full implementer of Closer")
		}
	}
	if !fullImpl {
		t.Errorf("File should implement Closer, got %+v", out.Implementations)
	}
}

func TestDevkit_MissingTargetsGuide(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\nfunc A() {}\n"})
	for name, h := range map[string]func(args map[string]any) *mcp.CallToolResult{
		"change_kit": func(a map[string]any) *mcp.CallToolResult {
			r := mcp.CallToolRequest{}
			r.Params.Arguments = a
			res, _ := changeKitHandler(reg)(ctx, r)
			return res
		},
		"find_implementations": func(a map[string]any) *mcp.CallToolResult {
			r := mcp.CallToolRequest{}
			r.Params.Arguments = a
			res, _ := findImplementationsHandler(reg)(ctx, r)
			return res
		},
	} {
		key := "target"
		if name == "find_implementations" {
			key = "interface"
		}
		res := h(map[string]any{"repo": repo.Name, key: "NopeMissing"})
		if !res.IsError {
			t.Errorf("%s: expected error for missing symbol", name)
			continue
		}
		if !strings.Contains(errorText(t, res), "query") {
			t.Errorf("%s: missing-symbol error should steer to query", name)
		}
		if name == "change_kit" && !strings.Contains(errorText(t, res), "analyze") {
			t.Errorf("change_kit missing-symbol error should mention re-index/analyze")
		}
	}
}
