package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestTraceHandler_ShortestPath(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"chain.go": "package x\n\nfunc A() { B() }\nfunc B() { C() }\nfunc C() { D() }\nfunc D() {}\n\nfunc Unrelated() {}\n",
	})
	call := func(args map[string]any) traceResponse {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = args
		res, err := traceHandler(reg)(ctx, req)
		if err != nil || res.IsError {
			t.Fatalf("handler: err=%v isErr=%v content=%+v", err, res.IsError, res.Content)
		}
		return decodeStructured[traceResponse](t, res)
	}

	// A → D shortest path is A,B,C,D (3 hops).
	out := call(map[string]any{"repo": repo.Name, "from": "A", "to": "D", "format": "json"})
	if out.Hops != 3 || len(out.Path) != 4 {
		t.Fatalf("expected 3-hop path A,B,C,D, got hops=%d path=%+v", out.Hops, out.Path)
	}
	want := []string{"A", "B", "C", "D"}
	for i, w := range want {
		if out.Path[i].Name != w {
			t.Errorf("path[%d]=%s want %s", i, out.Path[i].Name, w)
		}
	}

	// Reverse: from=D to=A has no forward path, but the tool should detect A→D.
	rev := call(map[string]any{"repo": repo.Name, "from": "D", "to": "A", "format": "json"})
	if len(rev.Path) == 0 {
		t.Errorf("reverse detection failed: expected the tool to find A reaches D, got note=%q", rev.Note)
	}

	// Unreachable: A → Unrelated.
	none := call(map[string]any{"repo": repo.Name, "from": "A", "to": "Unrelated", "format": "json"})
	if len(none.Path) != 0 {
		t.Errorf("expected no path A→Unrelated, got %+v", none.Path)
	}
	if none.Note == "" {
		t.Error("unreachable case should explain why and suggest next steps")
	}

	// Flow mode (no `to`): from A reaches B, C, D.
	flow := call(map[string]any{"repo": repo.Name, "from": "A", "format": "json"})
	names := map[string]bool{}
	for _, s := range flow.Flow {
		names[s.Name] = true
	}
	if !names["B"] || !names["C"] || !names["D"] {
		t.Errorf("flow from A should reach B,C,D; got %+v", flow.Flow)
	}
}

func TestTraceHandler_MissingSymbolGuides(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{"a.go": "package x\nfunc A() {}\n"})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "from": "NopeNotHere"}
	res, _ := traceHandler(reg)(ctx, req)
	if !res.IsError {
		t.Fatal("expected error for unknown symbol")
	}
	msg := errorText(t, res)
	if !strings.Contains(msg, "query") {
		t.Errorf("missing-symbol error should steer to `query`, got %q", msg)
	}
}
