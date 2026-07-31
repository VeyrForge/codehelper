package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Perl has no tree-sitter binding in smacker/go-tree-sitter; this is a
// line-oriented lite extractor: package/sub, use/require imports, and
// heuristic paren-call edges (&name / name()).
// Confidence: Low / lite — prefer lexical query; empty fanout ≠ isolation.

var (
	rePerlPackage = regexp.MustCompile(`(?m)^\s*package\s+([\w:]+)\s*;`)
	rePerlUse     = regexp.MustCompile(`(?m)^\s*(?:use|require)\s+([\w:./]+)`)
	rePerlSub     = regexp.MustCompile(`(?m)^\s*sub\s+(\w+)\s*(?:\([^)]*\))?\s*\{?`)
	rePerlCall    = regexp.MustCompile(`(?:&)?\b([A-Za-z_]\w*)\s*\(`)
)

var perlKeywordSkip = map[string]struct{}{
	"if": {}, "elsif": {}, "else": {}, "unless": {}, "while": {}, "until": {},
	"for": {}, "foreach": {}, "do": {}, "given": {}, "when": {}, "default": {},
	"sub": {}, "package": {}, "use": {}, "require": {}, "no": {}, "my": {},
	"our": {}, "local": {}, "state": {}, "return": {}, "last": {}, "next": {},
	"redo": {}, "goto": {}, "die": {}, "warn": {}, "print": {}, "printf": {},
	"say": {}, "open": {}, "close": {}, "defined": {}, "undef": {}, "exists": {},
	"delete": {}, "push": {}, "pop": {}, "shift": {}, "unshift": {}, "splice": {},
	"map": {}, "grep": {}, "sort": {}, "keys": {}, "values": {}, "each": {},
	"chomp": {}, "chop": {}, "split": {}, "join": {}, "sprintf": {},
	"bless": {}, "ref": {}, "scalar": {}, "wantarray": {}, "eval": {},
	"BEGIN": {}, "END": {}, "CHECK": {}, "INIT": {}, "UNITCHECK": {},
	"true": {}, "false": {}, "q": {}, "qq": {}, "qw": {}, "qx": {}, "qr": {},
}

func parsePerlLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	braceDepth := 0
	inSub := false
	pkgName := perlFileStem(relPath)

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)

		if m := rePerlPackage.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if i := strings.LastIndex(name, "::"); i >= 0 {
				name = name[i+2:]
			}
			pkgName = name
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, line, line, "perl", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			continue
		}

		if m := rePerlUse.FindStringSubmatch(ln); len(m) > 1 {
			mod := strings.TrimSpace(m[1])
			mod = strings.Trim(mod, `"'`)
			if mod != "" && mod != "strict" && mod != "warnings" && mod != "utf8" &&
				!strings.HasPrefix(mod, "v") && !strings.HasPrefix(mod, "5.") {
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

		if m := rePerlSub.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := perlKeywordSkip[name]; skip {
				continue
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "perl", "", pkgName)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			inSub = true
			braceDepth = strings.Count(ln, "{") - strings.Count(ln, "}")
			if braceDepth < 0 {
				braceDepth = 0
			}
			emitPerlCalls(repoID, relPath, currentFuncID, ln, out)
			continue
		}

		if inSub && currentFuncID != "" {
			if trim != "" && !strings.HasPrefix(trim, "#") {
				emitPerlCalls(repoID, relPath, currentFuncID, ln, out)
			}
			braceDepth += strings.Count(ln, "{") - strings.Count(ln, "}")
			if braceDepth <= 0 {
				inSub = false
				currentFuncID = ""
				braceDepth = 0
			}
		}
	}
	return out, nil
}

func emitPerlCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	for _, m := range rePerlCall.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := perlKeywordSkip[name]; skip {
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

func perlFileStem(relPath string) string {
	base := filepath.Base(relPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
