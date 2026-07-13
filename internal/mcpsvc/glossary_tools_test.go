package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
)

// TestGlossaryFlowOnFreshProject indexes a tiny throwaway project end-to-end and
// drives review -> promote -> list through the real handlers, proving the seed is
// generated at index time, cross-referenced against the symbol graph, and
// promotable into the committable glossary. It also confirms the project_type fix
// (profile written at index time) on the same fresh project.
func TestGlossaryFlowOnFreshProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Isolate the global registry under a temp HOME so we never touch ~/.codehelper.
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	mustWrite(t, root, "go.mod", "module demo\n\ngo 1.22\n")
	mustWrite(t, root, "resolver.go", `package demo

// ResolveSymbol resolves a symbol by name.
func ResolveSymbol(name string) string { return name }

// ResolveImport resolves an import path.
func ResolveImport(path string) string { return path }

// Resolver caches resolution results.
type Resolver struct{ Cache map[string]string }
`)
	gitInit(t, root)
	// Per-project tools assert the repo is in the current workspace (scopeRoots
	// falls back to CWD), so make the temp project the working directory.
	t.Chdir(root)

	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("registry load: %v", err)
	}
	if err := reg.Upsert("demo", root, "", 2); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	_, handlers := hookedRegister(reg)

	// review — auto-indexes on first use, which generates vocab.json + profile.
	res, err := callTool(t, nil, handlers, "glossary", map[string]any{
		"action": "review", "repo": "demo", "limit": float64(20),
	})
	mustOK(t, "glossary review", res, err)
	var rv struct {
		Candidates []struct {
			Term          string `json:"term"`
			SymbolMatches int    `json:"symbol_matches"`
			ConnectsTo    []struct {
				Name string `json:"name"`
			} `json:"connects_to"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(resultText(res)), &rv); err != nil {
		t.Fatalf("review json: %v\n%s", err, resultText(res))
	}
	if len(rv.Candidates) == 0 {
		t.Fatalf("expected candidate terms, got none: %s", resultText(res))
	}
	var resolve *struct {
		Term          string `json:"term"`
		SymbolMatches int    `json:"symbol_matches"`
		ConnectsTo    []struct {
			Name string `json:"name"`
		} `json:"connects_to"`
	}
	for i := range rv.Candidates {
		if rv.Candidates[i].Term == "resolve" {
			resolve = &rv.Candidates[i]
		}
	}
	if resolve == nil {
		t.Fatalf("expected 'resolve' among candidates: %s", resultText(res))
	}
	if resolve.SymbolMatches == 0 || len(resolve.ConnectsTo) == 0 {
		t.Fatalf("term 'resolve' should connect to symbols, got %+v", resolve)
	}

	// promote — writes to the committable glossary, attaching connected symbols.
	res, err = callTool(t, nil, handlers, "glossary", map[string]any{
		"action": "promote", "repo": "demo",
		"term": "resolve", "definition": "name/path resolution of symbols and imports",
	})
	mustOK(t, "glossary promote", res, err)
	if !strings.Contains(resultText(res), "connects_to") {
		t.Errorf("promote should report connected symbols: %s", resultText(res))
	}

	// list — the approved glossary now contains the promoted term.
	res, err = callTool(t, nil, handlers, "glossary", map[string]any{"action": "list", "repo": "demo"})
	mustOK(t, "glossary list", res, err)
	if !strings.Contains(resultText(res), `"resolve"`) {
		t.Errorf("glossary list missing promoted term: %s", resultText(res))
	}

	// project_type fix: the same fresh project resolves to "go", not "unknown".
	res, err = callTool(t, nil, handlers, "project_context", map[string]any{"repo": "demo"})
	mustOK(t, "project_context", res, err)
	if !strings.Contains(resultText(res), "go") || strings.Contains(resultText(res), `project_type":"unknown"`) {
		t.Errorf("expected project_type go on fresh project, got: %s", resultText(res))
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
}
