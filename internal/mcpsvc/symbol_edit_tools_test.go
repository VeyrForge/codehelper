//go:build !windows

package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

// buildIndexedRepo creates a tiny git repo, indexes it, and returns a registry
// scoped to it plus an MCP context whose roots point at it — mirroring the
// indexer package's end-to-end test setup so the symbolic-edit handlers run
// against a real graph.
func buildIndexedRepo(t *testing.T, files map[string]string) (*registry.Registry, registry.Entry, context.Context) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "c1")

	if err := indexer.Run(context.Background(), dir, indexer.Options{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	m, err := meta.Read(dir)
	if err != nil || m == nil {
		t.Fatalf("meta.Read: %v", err)
	}
	reg := &registry.Registry{Entries: map[string]registry.Entry{
		m.RepoName: {Name: m.RepoName, RootPath: dir, SchemaVer: meta.SchemaVersion},
	}}
	entry := reg.Entries[m.RepoName]
	return reg, entry, contextWithRoots(dir)
}

func decodeStructured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	sb, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(sb, &out); err != nil {
		t.Fatalf("unmarshal structured: %v\n%s", err, string(sb))
	}
	return out
}

func TestRenameSymbolPreviewFindsDefinitionAndCaller(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\nfunc Helper() int { return 1 }\n",
		"caller.go": "package x\n\nfunc run() int { return Helper() }\n",
	})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name,
		"name": "Helper",
		"to":   "Assist",
	}
	res, err := renameSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(res))
	}
	out := decodeStructured[renameSymbolResponse](t, res)

	if out.Applied {
		t.Fatal("preview must not apply by default")
	}
	if out.Definition != "target.go:3" {
		t.Fatalf("definition loc = %q, want target.go:3", out.Definition)
	}
	if out.GraphConfirmedCount < 2 {
		t.Fatalf("expected >=2 graph-confirmed sites (def + caller), got %d; files=%#v", out.GraphConfirmedCount, out.Files)
	}

	// The definition file and the caller file must each carry a graph-confirmed site.
	sawDef, sawCaller := false, false
	for _, f := range out.Files {
		for _, s := range f.GraphConfirmed {
			if f.Path == "target.go" && s.Line == 3 {
				sawDef = true
			}
			if f.Path == "caller.go" && s.Line == 3 {
				sawCaller = true
			}
		}
	}
	if !sawDef {
		t.Errorf("missing definition site target.go:3; files=%#v", out.Files)
	}
	if !sawCaller {
		t.Errorf("missing caller site caller.go:3; files=%#v", out.Files)
	}
}

func TestRenameSymbolAmbiguousReturnsCandidates(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Dup() {}\n",
		"b.go": "package x\n\nfunc Dup() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name,
		"name": "Dup",
		"to":   "Renamed",
	}
	res, err := renameSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	out := decodeStructured[renameSymbolResponse](t, res)
	if !out.Ambiguous {
		t.Fatalf("expected ambiguous=true for two symbols named Dup; got %#v", out)
	}
	if len(out.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %#v", len(out.Candidates), out.Candidates)
	}
	if len(out.Files) != 0 {
		t.Fatalf("ambiguous response should not include a rename plan; got %#v", out.Files)
	}

	// Disambiguating by path resolves to one definition and produces a plan.
	req.Params.Arguments = map[string]any{
		"repo": repo.Name,
		"name": "Dup",
		"to":   "Renamed",
		"path": "b.go",
	}
	res2, err := renameSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler error (disambiguated): %v", err)
	}
	out2 := decodeStructured[renameSymbolResponse](t, res2)
	if out2.Ambiguous {
		t.Fatalf("path hint should disambiguate; still ambiguous: %#v", out2)
	}
	if out2.Definition != "b.go:3" {
		t.Fatalf("definition = %q, want b.go:3", out2.Definition)
	}
}

func TestRenameSymbolApplyWritesGraphConfirmedSites(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\nfunc Helper() int { return 1 }\n",
		"caller.go": "package x\n\nfunc run() int { return Helper() }\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":  repo.Name,
		"name":  "Helper",
		"to":    "Assist",
		"apply": true,
	}
	res, err := renameSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	out := decodeStructured[renameSymbolResponse](t, res)
	if !out.Applied {
		t.Fatal("expected applied=true")
	}
	if out.AppliedSiteCount < 2 {
		t.Fatalf("expected >=2 applied sites, got %d", out.AppliedSiteCount)
	}
	def, _ := os.ReadFile(filepath.Join(repo.RootPath, "target.go"))
	caller, _ := os.ReadFile(filepath.Join(repo.RootPath, "caller.go"))
	if got := string(def); got != "package x\n\nfunc Assist() int { return 1 }\n" {
		t.Fatalf("target.go not renamed: %q", got)
	}
	if got := string(caller); got != "package x\n\nfunc run() int { return Assist() }\n" {
		t.Fatalf("caller.go not renamed: %q", got)
	}
	if len(out.RevertTokens) == 0 {
		t.Fatal("expected revert tokens for applied files")
	}
}

func TestRenameSymbolClassifiesTextualOnlyAndGuardsApply(t *testing.T) {
	// "Helper" also appears in a comment and a string literal — neither is a
	// resolved graph edge, so both must be reported as textual-only and NOT
	// written unless include_textual=true.
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\nfunc Helper() int { return 1 }\n",
		"notes.go":  "package x\n\n// Helper does work\nvar msg = \"Helper\"\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":  repo.Name,
		"name":  "Helper",
		"to":    "Assist",
		"apply": true, // include_textual omitted -> defaults false
	}
	res, err := renameSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	out := decodeStructured[renameSymbolResponse](t, res)
	if out.TextualOnlyCount < 2 {
		t.Fatalf("expected >=2 textual-only sites (comment + string), got %d; files=%#v", out.TextualOnlyCount, out.Files)
	}
	// notes.go must be untouched because textual-only sites are not applied by default.
	notes, _ := os.ReadFile(filepath.Join(repo.RootPath, "notes.go"))
	if got := string(notes); got != "package x\n\n// Helper does work\nvar msg = \"Helper\"\n" {
		t.Fatalf("textual-only sites should NOT be written without include_textual; notes.go=%q", got)
	}
	// The definition was graph-confirmed, so it IS renamed.
	def, _ := os.ReadFile(filepath.Join(repo.RootPath, "target.go"))
	if got := string(def); got != "package x\n\nfunc Assist() int { return 1 }\n" {
		t.Fatalf("definition should be renamed: %q", got)
	}
}

func TestInsertAtSymbolComputesLine(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"f.go": "package x\n\nfunc Foo() {\n\tprintln(1)\n}\n",
	})

	cases := []struct {
		position string
		wantLine int
	}{
		{"before", 3},        // func Foo() { is on line 3
		{"start_of_body", 4}, // first body line
		{"end_of_body", 5},   // closing brace line
		{"after", 6},         // line after the def
	}
	for _, tc := range cases {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{
			"repo":     repo.Name,
			"name":     "Foo",
			"position": tc.position,
			"text":     "// inserted",
		}
		res, err := insertAtSymbolHandler(reg)(ctx, req)
		if err != nil {
			t.Fatalf("%s: handler error: %v", tc.position, err)
		}
		if res.IsError {
			t.Fatalf("%s: tool error: %s", tc.position, resultText(res))
		}
		out := decodeStructured[insertAtSymbolResponse](t, res)
		if out.Applied {
			t.Fatalf("%s: preview must not apply", tc.position)
		}
		if out.InsertAtLine != tc.wantLine {
			t.Fatalf("position %s: insert_at_line=%d want %d (preview:\n%s)", tc.position, out.InsertAtLine, tc.wantLine, out.Preview)
		}
	}
}

func TestInsertAtSymbolApplyWritesBlock(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"f.go": "package x\n\nfunc Foo() {\n\tprintln(1)\n}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":     repo.Name,
		"name":     "Foo",
		"position": "before",
		"text":     "// a doc comment",
		"apply":    true,
	}
	res, err := insertAtSymbolHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	out := decodeStructured[insertAtSymbolResponse](t, res)
	if !out.Applied || out.RevertToken == "" {
		t.Fatalf("expected applied with revert_token; got %#v", out)
	}
	got, _ := os.ReadFile(filepath.Join(repo.RootPath, "f.go"))
	want := "package x\n\n// a doc comment\nfunc Foo() {\n\tprintln(1)\n}\n"
	if string(got) != want {
		t.Fatalf("f.go after insert =\n%q\nwant\n%q", string(got), want)
	}
}

func TestWriteWorkspaceFile_RejectsEmptyContent(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"go.mod": "module testmod\n\ngo 1.21\n",
		"a.go":   "package testmod\n\nfunc Ready() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":    repo.Name,
		"path":    "empty.txt",
		"content": "",
	}
	res, err := writeWorkspaceFileHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected error for empty content")
	}
	msg := errorText(t, res)
	if !strings.Contains(msg, "empty") || !strings.Contains(msg, "allow_empty") {
		t.Fatalf("expected empty/allow_empty guidance, got %q", msg)
	}
	if _, err := os.Stat(filepath.Join(repo.RootPath, "empty.txt")); !os.IsNotExist(err) {
		t.Fatalf("empty file should not have been created")
	}
}
