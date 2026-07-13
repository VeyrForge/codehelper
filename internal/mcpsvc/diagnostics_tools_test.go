package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestParseDiagnostics(t *testing.T) {
	out := parseDiagnostics(strings.Join([]string{
		"# testmod",
		"./bad.go:5:2: undefined: Missing",
		"internal/x/y.go:12: composite literal uses unkeyed fields",
		"src/app.ts(7,10): error TS2304: Cannot find name 'foo'.",
		"random noise line that should be ignored",
	}, "\n"))
	if len(out) != 3 {
		t.Fatalf("expected 3 parsed problems, got %d: %+v", len(out), out)
	}
	if out[0].File != "bad.go" || out[0].Line != 5 || out[0].Col != 2 {
		t.Errorf("go diag with col parsed wrong: %+v", out[0])
	}
	if out[1].File != "internal/x/y.go" || out[1].Line != 12 || out[1].Col != 0 {
		t.Errorf("go vet diag (no col) parsed wrong: %+v", out[1])
	}
	if out[2].File != "src/app.ts" || out[2].Line != 7 || out[2].Col != 10 || out[2].Severity != "error" {
		t.Errorf("tsc diag parsed wrong: %+v", out[2])
	}
}

func TestDiagnosticsHandler_GoBuildCatchesError(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"go.mod": "module testmod\n\ngo 1.21\n",
		"bad.go": "package testmod\n\nfunc Broken() int {\n\treturn Missing\n}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "format": "json"}
	res, err := diagnosticsHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[diagnosticsResponse](t, res)
	if out.Toolchain != "go" {
		t.Errorf("expected go toolchain, got %q", out.Toolchain)
	}
	if out.OK {
		t.Errorf("expected diagnostics to report failure for undefined symbol")
	}
	found := false
	for _, p := range out.Problems {
		if p.File == "bad.go" && p.Line == 4 && strings.Contains(p.Message, "Missing") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a parsed problem at bad.go:4 about Missing, got %+v", out.Problems)
	}
}

func TestDiagnosticsHandler_CleanModuleOK(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"go.mod":  "module testmod\n\ngo 1.21\n",
		"good.go": "package testmod\n\nfunc Add(a, b int) int { return a + b }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "format": "json"}
	res, err := diagnosticsHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[diagnosticsResponse](t, res)
	if !out.OK {
		t.Errorf("expected clean module to pass, got problems=%+v raw=%q", out.Problems, out.RawTail)
	}
	if len(out.Problems) != 0 {
		t.Errorf("clean module should have no problems, got %+v", out.Problems)
	}
}

func TestDiagnosticsHandler_NoToolchain(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\nfunc F() {}\n", // a .go file but no go.mod marker
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "format": "json"}
	res, err := diagnosticsHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("handler: err=%v isErr=%v", err, res.IsError)
	}
	out := decodeStructured[diagnosticsResponse](t, res)
	if out.Toolchain != "" {
		t.Errorf("expected no toolchain detected, got %q", out.Toolchain)
	}
	if !strings.Contains(out.Note, "no toolchain auto-detected") {
		t.Errorf("expected guidance note, got %q", out.Note)
	}
}
