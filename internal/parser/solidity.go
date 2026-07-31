package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Solidity has no tree-sitter binding in go-tree-sitter; this is a line-oriented
// lite extractor: contract/library/interface + function/event/struct symbols,
// import paths, `is` inherits/implements, and heuristic calls including
// Library.fn receivers. Confidence: Medium symbols / Low–Medium calls.

var (
	reSolImportQuoted = regexp.MustCompile(`(?m)^\s*import\s+(?:\{[^}]*\}\s+from\s+|\*\s+as\s+\w+\s+from\s+)?["']([^"']+)["']`)
	reSolType         = regexp.MustCompile(`(?m)^\s*(?:abstract\s+)?(contract|library|interface)\s+(\w+)`)
	reSolIsBases      = regexp.MustCompile(`\bis\s+([^{/;]+)`)
	reSolStructEnum   = regexp.MustCompile(`(?m)^\s*(struct|enum)\s+(\w+)`)
	reSolFn           = regexp.MustCompile(`(?m)^\s*function\s+(\w+)\s*\(`)
	reSolEventError   = regexp.MustCompile(`(?m)^\s*(event|error|modifier)\s+(\w+)\s*(?:\(|\{|$)`)
	// Groups: optional Capitalized receiver, callee name.
	reSolCall = regexp.MustCompile(`(?:^|[^\w.])(?:([A-Z][A-Za-z0-9_]*)\s*\.\s*)?([A-Za-z_][\w]*)\s*\(`)
)

var solKeywordSkip = map[string]struct{}{
	"if": {}, "for": {}, "while": {}, "do": {}, "return": {}, "require": {},
	"assert": {}, "revert": {}, "emit": {}, "new": {}, "delete": {},
	"true": {}, "false": {}, "this": {}, "super": {}, "msg": {}, "block": {},
	"tx": {}, "abi": {}, "type": {}, "bytes": {}, "string": {}, "address": {},
	"bool": {}, "int": {}, "uint": {}, "fixed": {}, "ufixed": {},
	"mapping": {}, "memory": {}, "storage": {}, "calldata": {},
	"public": {}, "private": {}, "internal": {}, "external": {},
	"view": {}, "pure": {}, "payable": {}, "virtual": {}, "override": {},
	"immutable": {}, "constant": {}, "indexed": {}, "anonymous": {},
	"pragma": {}, "import": {}, "from": {}, "as": {}, "is": {},
	"contract": {}, "library": {}, "interface": {}, "abstract": {},
	"struct": {}, "enum": {}, "function": {}, "modifier": {}, "event": {},
	"error": {}, "constructor": {}, "fallback": {}, "receive": {},
	"unchecked": {}, "assembly": {}, "using": {}, "try": {}, "catch": {},
	"else": {}, "break": {}, "continue": {}, "throw": {},
}

func parseSolidityLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	currentFuncIndent := -1
	typeName := solFileStem(relPath)

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)
		ind := solIndentLen(ln)

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "//") && ind <= currentFuncIndent {
			currentFuncID = ""
			currentFuncIndent = -1
		}

		if strings.HasPrefix(trim, "//") || trim == "" {
			continue
		}

		if m := reSolImportQuoted.FindStringSubmatch(ln); len(m) > 1 {
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

		if m := reSolType.FindStringSubmatch(ln); len(m) > 2 {
			name := m[2]
			kind := types.SymbolKindClass
			if m[1] == "interface" {
				kind = types.SymbolKindInterface
			}
			sym := symbol(repoID, relPath, name, kind, line, line, "solidity", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			typeName = name
			emitSolInheritance(repoID, relPath, sym.ID, kind, ln, out)
			continue
		}

		if m := reSolStructEnum.FindStringSubmatch(ln); len(m) > 2 {
			name := m[2]
			kind := types.SymbolKindClass
			if m[1] == "enum" {
				kind = types.SymbolKindEnum
			}
			sym := symbol(repoID, relPath, name, kind, line, line, "solidity", "", typeName)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			continue
		}

		if m := reSolEventError.FindStringSubmatch(ln); len(m) > 2 {
			name := m[2]
			kind := types.SymbolKindFunction
			switch m[1] {
			case "event", "error":
				kind = types.SymbolKindTypeAlias
			}
			sym := symbol(repoID, relPath, name, kind, line, line, "solidity", "", typeName)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			continue
		}

		if m := reSolFn.FindStringSubmatch(ln); len(m) > 1 {
			fnName := m[1]
			if _, skip := solKeywordSkip[fnName]; skip {
				continue
			}
			sym := symbol(repoID, relPath, fnName, types.SymbolKindFunction, line, line, "solidity", "", typeName)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			currentFuncIndent = ind
			continue
		}

		if currentFuncID != "" {
			emitSolCalls(repoID, relPath, currentFuncID, ln, out)
		}
	}
	return out, nil
}

// emitSolInheritance emits inherits/implements for `contract C is A, B`.
// Interface parents → implements; contract/library parents → inherits.
func emitSolInheritance(repoID, relPath, fromSym string, fromKind types.SymbolKind, ln string, out *ParseResult) {
	m := reSolIsBases.FindStringSubmatch(ln)
	if len(m) < 2 {
		return
	}
	for _, part := range strings.Split(m[1], ",") {
		base := strings.TrimSpace(part)
		// Drop constructor-style args: Ownable(msg.sender)
		if i := strings.IndexByte(base, '('); i >= 0 {
			base = strings.TrimSpace(base[:i])
		}
		if base == "" || !isCallableName(base) {
			continue
		}
		if _, skip := solKeywordSkip[base]; skip {
			continue
		}
		kind := types.RefKindInherits
		edgeKind := "inherits"
		conf := 0.8
		// Interface extending another interface, or contract listing an I*-shaped
		// parent → implements (agents treat interface inbound as impact).
		if fromKind == types.SymbolKindInterface || (len(base) > 1 && base[0] == 'I' && base[1] >= 'A' && base[1] <= 'Z') {
			kind = types.RefKindImplements
			edgeKind = "implements"
			conf = 0.75
		}
		tgt := "symref:" + repoID + ":" + relPath + ":" + base
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, edgeKind),
			RepoID:     repoID,
			Kind:       kind,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
}

func emitSolCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	emitCall := func(name string, conf float64) {
		if name == "" {
			return
		}
		if _, skip := solKeywordSkip[name]; skip {
			return
		}
		if !isCallableName(name) {
			return
		}
		tgt := "symref:" + repoID + ":" + relPath + ":" + name
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
	emitRead := func(name string, conf float64) {
		if name == "" || !isCallableName(name) {
			return
		}
		if _, skip := solKeywordSkip[name]; skip {
			return
		}
		tgt := "symref:" + repoID + ":" + relPath + ":" + name
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
	for _, m := range reSolCall.FindAllStringSubmatch(ln, -1) {
		recv, name := m[1], m[2]
		if name == "" {
			continue
		}
		emitCall(name, 0.45)
		// Library/type receiver: Helpers.format → Helpers read + Helpers.format call.
		if recv != "" {
			emitRead(recv, 0.7)
			emitCall(recv+"."+name, 0.65)
		}
	}
}

func solIndentLen(ln string) int {
	n := 0
	for _, r := range ln {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func solFileStem(relPath string) string {
	base := filepath.Base(relPath)
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}
