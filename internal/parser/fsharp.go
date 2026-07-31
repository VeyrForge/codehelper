package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// F# has no tree-sitter binding in smacker/go-tree-sitter; this is a
// line-oriented lite extractor: module/type/let/member, open imports, and
// heuristic paren-call edges. Space-application calls are not modeled.
// Confidence: Low / lite — prefer lexical query; empty fanout ≠ isolation.

var (
	reFSharpModule = regexp.MustCompile(`(?m)^\s*module\s+(?:private\s+|internal\s+)?([\w.]+)`)
	reFSharpOpen   = regexp.MustCompile(`(?m)^\s*open\s+([\w.]+)`)
	reFSharpType   = regexp.MustCompile(`(?m)^\s*type\s+(?:private\s+|internal\s+)?(\w+)`)
	reFSharpLet    = regexp.MustCompile(`(?m)^\s*let\s+(?:rec\s+|inline\s+|mutable\s+)*(\w+)\s*[^=(]*[=(]`)
	reFSharpMember = regexp.MustCompile(`(?m)^\s*(?:override|member|abstract|static\s+member)\s+(?:private\s+|internal\s+)?(?:(?:this|_|[\w]+)\.)?(\w+)\s*[^=(]*[=(]`)
	reFSharpCall   = regexp.MustCompile(`\b([A-Za-z_][\w]*)\s*\(`)
)

var fsharpKeywordSkip = map[string]struct{}{
	"if": {}, "then": {}, "else": {}, "elif": {}, "match": {}, "with": {},
	"for": {}, "while": {}, "do": {}, "done": {}, "to": {}, "downto": {},
	"let": {}, "rec": {}, "in": {}, "fun": {}, "function": {}, "return": {},
	"yield": {}, "use": {}, "try": {}, "finally": {}, "raise": {}, "failwith": {},
	"new": {}, "null": {}, "true": {}, "false": {}, "and": {}, "or": {},
	"not": {}, "module": {}, "open": {}, "type": {}, "of": {}, "as": {},
	"begin": {}, "end": {}, "struct": {}, "class": {}, "interface": {},
	"inherit": {}, "member": {}, "override": {}, "abstract": {}, "static": {},
	"private": {}, "internal": {}, "public": {}, "mutable": {}, "inline": {},
	"namespace": {}, "exception": {}, "when": {}, "lazy": {}, "assert": {},
}

func parseFSharpLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	currentIndent := -1
	typeName := fsharpFileStem(relPath)

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)
		ind := indentLen(ln)

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "//") && ind <= currentIndent {
			currentFuncID = ""
			currentIndent = -1
		}

		if m := reFSharpOpen.FindStringSubmatch(ln); len(m) > 1 {
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

		if m := reFSharpModule.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			// Prefer leaf of dotted module path for symbol name stability.
			if i := strings.LastIndex(name, "."); i >= 0 {
				name = name[i+1:]
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, line, line, "fsharp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			typeName = name
			continue
		}

		if m := reFSharpType.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, line, line, "fsharp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			typeName = name
			continue
		}

		fnName := ""
		parent := ""
		if m := reFSharpMember.FindStringSubmatch(ln); len(m) > 1 {
			fnName = m[1]
			parent = typeName
		} else if m := reFSharpLet.FindStringSubmatch(ln); len(m) > 1 {
			fnName = m[1]
			parent = typeName
		}
		if fnName != "" {
			if _, skip := fsharpKeywordSkip[fnName]; skip {
				fnName = ""
			}
		}
		if fnName != "" {
			sym := symbol(repoID, relPath, fnName, types.SymbolKindFunction, line, line, "fsharp", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			currentIndent = ind
			continue
		}

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "//") {
			emitFSharpCalls(repoID, relPath, currentFuncID, ln, out)
		}
	}
	return out, nil
}

func emitFSharpCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	for _, m := range reFSharpCall.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := fsharpKeywordSkip[name]; skip {
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

func fsharpFileStem(relPath string) string {
	base := filepath.Base(relPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
