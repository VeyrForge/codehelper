package parser

import (
	"context"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// GDScript (Godot) is line-oriented and Python-like, and go-tree-sitter ships no
// GDScript grammar, so we extract symbols with anchored line patterns — the
// SymbolLite approach already used for dart/sql/bash. This is enough to make
// funcs, classes, signals, enums, consts and (exported) vars searchable. Before
// this, .gd files fell through to generic text and were effectively invisible to
// query/scout, so whole Godot codebases (the editor, gameplay) couldn't be found.
var gdDecls = []struct {
	re   *regexp.Regexp
	kind types.SymbolKind
}{
	{regexp.MustCompile(`^\s*(?:static\s+)?func\s+(\w+)`), types.SymbolKindFunction},
	{regexp.MustCompile(`^\s*class_name\s+(\w+)`), types.SymbolKindClass},
	{regexp.MustCompile(`^\s*class\s+(\w+)`), types.SymbolKindClass},
	{regexp.MustCompile(`^\s*enum\s+(\w+)`), types.SymbolKindEnum},
	{regexp.MustCompile(`^\s*signal\s+(\w+)`), types.SymbolKindVariable},
	{regexp.MustCompile(`^\s*const\s+(\w+)`), types.SymbolKindVariable},
	{regexp.MustCompile(`^\s*(?:@\w+(?:\([^)]*\))?\s+)*var\s+(\w+)`), types.SymbolKindVariable},
}

// parseGDScriptLite extracts top-level GDScript declarations by line.
func parseGDScriptLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	line := 0
	for _, ln := range strings.Split(string(buf), "\n") {
		line++
		for _, d := range gdDecls {
			m := d.re.FindStringSubmatch(ln)
			if m == nil || m[1] == "" {
				continue
			}
			sym := symbol(repoID, relPath, m[1], d.kind, line, line, "gdscript", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			break // first matching pattern wins; one declaration per line
		}
	}
	return out, nil
}
