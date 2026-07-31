package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// R has no tree-sitter binding in smacker/go-tree-sitter; this is a
// line-oriented lite extractor: name <- function / = function, library/require/
// source imports, and heuristic paren-call edges.
// Confidence: Low / lite — prefer lexical query; empty fanout ≠ isolation.

var (
	reRFunc    = regexp.MustCompile(`(?m)^\s*((?:\.[A-Za-z_]|[A-Za-z_])[\w.]*)\s*(?:<-|=)\s*function\s*\(`)
	reRLibrary = regexp.MustCompile(`(?m)^\s*(?:library|require)\s*\(\s*['"]?([^'",)\s]+)`)
	reRSource  = regexp.MustCompile(`(?m)^\s*source\s*\(\s*['"]([^'"]+)['"]`)
	reRCall    = regexp.MustCompile(`\b((?:\.[A-Za-z_]|[A-Za-z_])[\w.]*)\s*\(`)
)

var rKeywordSkip = map[string]struct{}{
	"if": {}, "else": {}, "for": {}, "while": {}, "repeat": {}, "function": {},
	"in": {}, "next": {}, "break": {}, "return": {}, "switch": {},
	"TRUE": {}, "FALSE": {}, "NULL": {}, "NA": {}, "NaN": {}, "Inf": {},
	"library": {}, "require": {}, "source": {}, "c": {}, "list": {},
	"data.frame": {}, "matrix": {}, "array": {}, "factor": {},
	"length": {}, "names": {}, "print": {}, "cat": {}, "paste": {},
	"stop": {}, "warning": {}, "message": {}, "tryCatch": {},
}

func parseRLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	currentIndent := -1
	parent := rFileStem(relPath)

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)
		ind := indentLen(ln)

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "#") &&
			ind <= currentIndent && reRFunc.MatchString(ln) {
			currentFuncID = ""
			currentIndent = -1
		}

		if m := reRLibrary.FindStringSubmatch(ln); len(m) > 1 {
			mod := strings.TrimSpace(m[1])
			if mod != "" {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.8,
				})
			}
			continue
		}
		if m := reRSource.FindStringSubmatch(ln); len(m) > 1 {
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

		if m := reRFunc.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := rKeywordSkip[name]; skip {
				continue
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "r", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			currentIndent = ind
			emitRCalls(repoID, relPath, currentFuncID, ln, out)
			continue
		}

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "#") {
			emitRCalls(repoID, relPath, currentFuncID, ln, out)
		}
	}
	return out, nil
}

func emitRCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	for _, m := range reRCall.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := rKeywordSkip[name]; skip {
			continue
		}
		if strings.EqualFold(name, "function") {
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

func rFileStem(relPath string) string {
	base := filepath.Base(relPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
