package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Haskell has no tree-sitter binding in smacker/go-tree-sitter; this is a
// line-oriented lite extractor: module/import, data/newtype/type/class,
// top-level lowercase bindings (+ optional :: sigs), and paren-call edges.
// Space-application calls are not modeled.
// Confidence: Low / lite — prefer lexical query; empty fanout ≠ isolation.

var (
	reHaskellModule = regexp.MustCompile(`(?m)^\s*module\s+([\w.]+)`)
	reHaskellImport = regexp.MustCompile(`(?m)^\s*import\s+(?:qualified\s+)?([\w.]+)`)
	reHaskellData   = regexp.MustCompile(`(?m)^\s*(?:data|newtype|type|class)\s+(\w+)`)
	reHaskellSig    = regexp.MustCompile(`(?m)^([a-z_][\w']*)\s*::`)
	reHaskellFun    = regexp.MustCompile(`(?m)^([a-z_][\w']*)(?:\s+[^=]+)?\s*=`)
	reHaskellCall   = regexp.MustCompile(`\b([A-Za-z_][\w']*)\s*\(`)
)

var haskellKeywordSkip = map[string]struct{}{
	"if": {}, "then": {}, "else": {}, "case": {}, "of": {}, "where": {},
	"let": {}, "in": {}, "do": {}, "module": {}, "import": {}, "qualified": {},
	"as": {}, "hiding": {}, "data": {}, "newtype": {}, "type": {}, "class": {},
	"instance": {}, "deriving": {}, "default": {}, "foreign": {},
	"infix": {}, "infixl": {}, "infixr": {}, "forall": {},
	"True": {}, "False": {}, "Nothing": {}, "Just": {}, "Left": {}, "Right": {},
	"return": {}, "pure": {}, "fmap": {}, "map": {}, "filter": {}, "foldr": {},
	"foldl": {}, "zip": {}, "unzip": {}, "show": {}, "read": {}, "error": {},
	"undefined": {}, "otherwise": {},
}

func parseHaskellLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	currentIndent := -1
	parent := haskellFileStem(relPath)
	seenFun := map[string]struct{}{}

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)
		ind := indentLen(ln)

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "--") &&
			ind <= currentIndent && (reHaskellFun.MatchString(ln) || reHaskellSig.MatchString(ln) ||
			reHaskellData.MatchString(ln) || reHaskellModule.MatchString(ln)) {
			currentFuncID = ""
			currentIndent = -1
		}

		if m := reHaskellModule.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if i := strings.LastIndex(name, "."); i >= 0 {
				name = name[i+1:]
			}
			parent = name
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, line, line, "haskell", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = ""
			continue
		}

		if m := reHaskellImport.FindStringSubmatch(ln); len(m) > 1 {
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

		if m := reHaskellData.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := haskellKeywordSkip[name]; !skip {
				sym := symbol(repoID, relPath, name, types.SymbolKindClass, line, line, "haskell", "", "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				parent = name
			}
			currentFuncID = ""
			continue
		}

		// Prefer binding lines over type sigs for the function body/calls scope.
		if m := reHaskellFun.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if haskellOkFun(name) {
				if _, seen := seenFun[name]; !seen {
					seenFun[name] = struct{}{}
					sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "haskell", "", parent)
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				}
				// Re-bind current even on equation clauses (same name).
				for _, s := range out.Symbols {
					if s.Name == name && s.Kind == types.SymbolKindFunction {
						currentFuncID = s.ID
						break
					}
				}
				currentIndent = ind
				emitHaskellCalls(repoID, relPath, currentFuncID, ln, out)
				continue
			}
		}

		if m := reHaskellSig.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if haskellOkFun(name) {
				if _, seen := seenFun[name]; !seen {
					seenFun[name] = struct{}{}
					sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "haskell", "", parent)
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				}
			}
			continue
		}

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "--") {
			emitHaskellCalls(repoID, relPath, currentFuncID, ln, out)
		}
	}
	return out, nil
}

func haskellOkFun(name string) bool {
	if name == "" {
		return false
	}
	if _, skip := haskellKeywordSkip[name]; skip {
		return false
	}
	r := []rune(name)[0]
	return unicode.IsLower(r) || r == '_'
}

func emitHaskellCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	if fromSym == "" {
		return
	}
	for _, m := range reHaskellCall.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := haskellKeywordSkip[name]; skip {
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

func haskellFileStem(relPath string) string {
	base := filepath.Base(relPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
