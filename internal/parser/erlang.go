package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Erlang is separate from Elixir (which has a tree-sitter grammar). smacker/
// go-tree-sitter has no Erlang binding — this is a line-oriented lite
// extractor: -module/-include, function heads, and heuristic call edges.
// Confidence: Low / lite — prefer lexical query; empty fanout ≠ isolation.

var (
	reErlangModule  = regexp.MustCompile(`(?m)^\s*-module\s*\(\s*([A-Za-z_]\w*)\s*\)\s*\.`)
	reErlangInclude = regexp.MustCompile(`(?m)^\s*-include(?:_lib)?\s*\(\s*"([^"]+)"\s*\)\s*\.`)
	reErlangExport  = regexp.MustCompile(`(?m)^\s*-export\s*\(`)
	reErlangFunc    = regexp.MustCompile(`(?m)^([A-Za-z_]\w*)\s*\([^)]*\)\s*(?:when\s+[^->]+)?->`)
	reErlangCall    = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*\(`)
)

var erlangKeywordSkip = map[string]struct{}{
	"if": {}, "case": {}, "receive": {}, "try": {}, "catch": {}, "begin": {},
	"end": {}, "fun": {}, "when": {}, "of": {}, "after": {}, "andalso": {},
	"orelse": {}, "not": {}, "bnot": {}, "div": {}, "rem": {}, "band": {},
	"bor": {}, "bxor": {}, "bsl": {}, "bsr": {}, "true": {}, "false": {},
	"module": {}, "export": {}, "import": {}, "include": {}, "include_lib": {},
	"record": {}, "define": {}, "ifdef": {}, "ifndef": {}, "else": {},
	"endif": {}, "undef": {}, "spec": {}, "type": {}, "opaque": {},
	"callback": {}, "behaviour": {}, "behavior": {},
}

func parseErlangLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	currentIndent := -1
	modName := erlangFileStem(relPath)

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)
		ind := indentLen(ln)

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "%") &&
			ind <= currentIndent && reErlangFunc.MatchString(ln) {
			currentFuncID = ""
			currentIndent = -1
		}

		if m := reErlangModule.FindStringSubmatch(ln); len(m) > 1 {
			modName = m[1]
			sym := symbol(repoID, relPath, modName, types.SymbolKindNamespace, line, line, "erlang", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			continue
		}

		if m := reErlangInclude.FindStringSubmatch(ln); len(m) > 1 {
			mod := strings.TrimSpace(m[1])
			if mod != "" {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.85,
				})
			}
			continue
		}

		if reErlangExport.MatchString(ln) {
			continue
		}

		if m := reErlangFunc.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := erlangKeywordSkip[name]; skip {
				continue
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "erlang", "", modName)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			currentIndent = ind
			emitErlangCalls(repoID, relPath, currentFuncID, ln, out)
			continue
		}

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "%") {
			emitErlangCalls(repoID, relPath, currentFuncID, ln, out)
		}
	}
	return out, nil
}

func emitErlangCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	for _, m := range reErlangCall.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := erlangKeywordSkip[name]; skip {
			continue
		}
		if !isCallableName(name) {
			continue
		}
		tgt := "symref:" + repoID + ":" + relPath + ":" + name
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.4,
		})
	}
}

func erlangFileStem(relPath string) string {
	base := filepath.Base(relPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
