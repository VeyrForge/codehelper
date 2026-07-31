package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// OCaml has no tree-sitter binding in smacker/go-tree-sitter; this is a
// line-oriented lite extractor: module/type/let, open/include imports, and
// heuristic paren-call edges. Space-application calls are not modeled.
// Confidence: Low / lite — prefer lexical query; empty fanout ≠ isolation.

var (
	reOCamlOpen   = regexp.MustCompile(`(?m)^\s*(?:open|include)\s+([\w.]+)`)
	reOCamlModule = regexp.MustCompile(`(?m)^\s*module\s+(\w+)\s*=`)
	reOCamlType   = regexp.MustCompile(`(?m)^\s*type\s+(?:nonrec\s+)?(?:'[\w]+\s+)*(\w+)`)
	reOCamlLet    = regexp.MustCompile(`(?m)^\s*let\s+(?:rec\s+)?(\w+)\s`)
	reOCamlCall   = regexp.MustCompile(`\b([A-Za-z_][\w']*)\s*\(`)
)

var ocamlKeywordSkip = map[string]struct{}{
	"if": {}, "then": {}, "else": {}, "match": {}, "with": {}, "when": {},
	"for": {}, "while": {}, "do": {}, "done": {}, "to": {}, "downto": {},
	"let": {}, "rec": {}, "in": {}, "fun": {}, "function": {}, "try": {},
	"and": {}, "or": {}, "not": {}, "mod": {}, "land": {}, "lor": {},
	"lxor": {}, "lsl": {}, "lsr": {}, "asr": {}, "begin": {}, "end": {},
	"struct": {}, "sig": {}, "module": {}, "open": {}, "include": {},
	"type": {}, "of": {}, "as": {}, "exception": {}, "raise": {},
	"true": {}, "false": {}, "lazy": {}, "assert": {}, "mutable": {},
	"private": {}, "external": {}, "val": {}, "method": {}, "class": {},
	"object": {}, "inherit": {}, "initializer": {}, "new": {},
	"nonrec": {}, "constraint": {},
}

func parseOCamlLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	currentIndent := -1
	parent := ocamlFileStem(relPath)

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)
		ind := indentLen(ln)

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "(*") &&
			ind <= currentIndent && (reOCamlLet.MatchString(ln) || reOCamlModule.MatchString(ln) || reOCamlType.MatchString(ln)) {
			currentFuncID = ""
			currentIndent = -1
		}

		if m := reOCamlOpen.FindStringSubmatch(ln); len(m) > 1 {
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

		if m := reOCamlModule.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, line, line, "ocaml", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			parent = name
			currentFuncID = ""
			continue
		}

		if m := reOCamlType.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := ocamlKeywordSkip[name]; !skip {
				sym := symbol(repoID, relPath, name, types.SymbolKindClass, line, line, "ocaml", "", "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				parent = name
			}
			currentFuncID = ""
			continue
		}

		if m := reOCamlLet.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := ocamlKeywordSkip[name]; skip {
				continue
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "ocaml", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			currentIndent = ind
			emitOCamlCalls(repoID, relPath, currentFuncID, ln, out)
			continue
		}

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "(*") {
			emitOCamlCalls(repoID, relPath, currentFuncID, ln, out)
		}
	}
	return out, nil
}

func emitOCamlCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	for _, m := range reOCamlCall.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := ocamlKeywordSkip[name]; skip {
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

func ocamlFileStem(relPath string) string {
	base := filepath.Base(relPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
