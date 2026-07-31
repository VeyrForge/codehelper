//go:build !windows

package mcpsvc

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestLSPHandler_DisabledFallsBack(t *testing.T) {
	t.Setenv("CODEHELPER_LSP", "0")
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"main.go": "package main\n\nfunc Hello() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "action": "references", "path": "main.go",
		"line": 3, "col": 6, "format": "json",
	}
	res, err := lspHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v text=%s", err, res.IsError, errorText(t, res))
	}
	out := decodeStructured[lspResponse](t, res)
	if !out.Fallback || out.OK {
		t.Fatalf("expected graph-fallback: %+v", out)
	}
	if out.Source != "graph-fallback" {
		t.Errorf("source=%q", out.Source)
	}
}

func TestCalleeRef_ProvenanceBand(t *testing.T) {
	cr := calleeRef{}.fill("symref:r:a.go:Helper", 0.9)
	if cr.Provenance != "exact" {
		t.Errorf("provenance=%q want exact", cr.Provenance)
	}
	cr2 := calleeRef{}.fill("sym:r:a.go:1:Helper", 0.5)
	if cr2.Provenance != "inferred" {
		t.Errorf("provenance=%q want inferred", cr2.Provenance)
	}
}

func TestFindImplementations_SourceGraph(t *testing.T) {
	t.Setenv("CODEHELPER_LSP", "0")
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"iface.go": "package x\n\ntype Closer interface {\n\tClose() error\n}\n",
		"impl.go":  "package x\n\ntype File struct{}\nfunc (f File) Close() error { return nil }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "interface": "Closer", "format": "json"}
	res, err := findImplementationsHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v", err)
	}
	out := decodeStructured[findImplResponse](t, res)
	if out.Source != "graph" && out.Source != "graph+lsp" {
		t.Errorf("source=%q", out.Source)
	}
}
