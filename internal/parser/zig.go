package parser

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Zig has no tree-sitter binding in go-tree-sitter; this is a line-oriented
// lite extractor: fn/struct/enum/alias/field/variant symbols, @import paths,
// and heuristic call edges (incl. Module.fn receivers). Methods use
// SymbolKindMethod + brace-stack ParentID; top-level fns stay parentless.
// Confidence: Low / lite — prefer lexical query; do not claim High.

var (
	reZigImport = regexp.MustCompile(`@import\s*\(\s*["']([^"']+)["']\s*\)`)
	reZigFn     = regexp.MustCompile(`(?m)^\s*(?:pub\s+|export\s+|extern\s+|inline\s+)*fn\s+(\w+)\s*\(`)
	reZigType   = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?const\s+(\w+)\s*=\s*(struct|enum|union|opaque|error)\b`)
	// Type alias / const binding that is not a container or @builtin/import.
	// RE2 has no (?!…); containers are handled by reZigType first; reject @-RHS here.
	reZigAlias = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?const\s+(\w+)\s*=\s*([^@;\s][^;]*?)\s*;`)
	// Struct/union field: name: Type,
	reZigField = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?(\w+)\s*:\s*[^=,\n{]+,?\s*$`)
	// Enum variant: name,  or  name = value,
	reZigVariant = regexp.MustCompile(`(?m)^\s*(\w+)\s*(?:=\s*[^,\n]+)?\s*,?\s*$`)
	// Groups: optional receiver (helpers / Greeter), callee name.
	reZigCall = regexp.MustCompile(`(?:^|[^\w.])(?:([A-Za-z_][\w]*)\s*\.\s*)?([A-Za-z_][\w]*)\s*\(`)
)

var zigKeywordSkip = map[string]struct{}{
	"if": {}, "for": {}, "while": {}, "switch": {}, "return": {}, "try": {},
	"catch": {}, "defer": {}, "errdefer": {}, "async": {}, "await": {},
	"suspend": {}, "resume": {}, "fn": {}, "const": {}, "var": {}, "pub": {},
	"export": {}, "extern": {}, "inline": {}, "comptime": {},
	"struct": {}, "enum": {}, "union": {}, "opaque": {}, "error": {},
	"true": {}, "false": {}, "null": {}, "undefined": {}, "unreachable": {},
	"break": {}, "continue": {}, "orelse": {}, "and": {}, "or": {},
	"test": {}, "usingnamespace": {}, "asm": {}, "volatile": {},
	"align": {}, "linksection": {}, "callconv": {}, "anytype": {},
	"anyframe": {}, "anyopaque": {}, "type": {}, "void": {}, "noreturn": {},
	"bool": {}, "isize": {}, "usize": {}, "f16": {}, "f32": {}, "f64": {},
	"f80": {}, "f128": {}, "c_char": {}, "c_short": {}, "c_ushort": {},
	"c_int": {}, "c_uint": {}, "c_long": {}, "c_ulong": {}, "c_longlong": {},
	"c_ulonglong": {}, "c_longdouble": {},
	"self": {}, "allocator": {},
	// Common @builtins that surface as bare names when `\b` matches after `@`.
	"import": {}, "as": {}, "ptrCast": {}, "alignCast": {}, "bitCast": {},
	"intCast": {}, "floatCast": {}, "enumFromInt": {}, "intFromEnum": {},
	"intFromBool": {}, "boolFromInt": {}, "TypeOf": {}, "typeInfo": {},
	"sizeOf": {}, "alignOf": {}, "offsetOf": {}, "field": {}, "fieldParentPtr": {},
	"call": {}, "memcpy": {}, "memset": {}, "memcmp": {}, "panic": {},
	"compileError": {}, "compileLog": {}, "embedFile": {}, "src": {},
	"This": {}, "hasDecl": {}, "hasField": {}, "Type": {}, "Frame": {},
}

type zigTypeFrame struct {
	name      string
	kind      string // struct|enum|union|opaque|error
	openDepth int
}

func parseZigLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	funcOpenDepth := -1
	braceDepth := 0
	var typeStack []zigTypeFrame

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)

		if strings.HasPrefix(trim, "//") {
			braceDepth += zigBraceDelta(ln)
			zigPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		if m := reZigImport.FindStringSubmatch(ln); len(m) > 1 {
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
		}

		if m := reZigType.FindStringSubmatch(ln); len(m) > 1 && m[1] != "" {
			name := m[1]
			container := m[2]
			kind := types.SymbolKindClass
			switch container {
			case "enum":
				kind = types.SymbolKindEnum
			case "error":
				kind = types.SymbolKindTypeAlias
			}
			parent := zigParentName(typeStack)
			sym := symbol(repoID, relPath, name, kind, line, line, "zig", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			before := braceDepth
			braceDepth += zigBraceDelta(ln)
			if strings.Contains(ln, "{") {
				openedTo := before + 1
				if braceDepth >= openedTo {
					typeStack = append(typeStack, zigTypeFrame{name: name, kind: container, openDepth: openedTo})
				}
			}
			zigPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		if m := reZigAlias.FindStringSubmatch(ln); len(m) > 1 && m[1] != "" {
			name := m[1]
			rhs := strings.TrimSpace(m[2])
			skipAlias := strings.HasPrefix(rhs, "@") ||
				strings.HasPrefix(rhs, "struct") || strings.HasPrefix(rhs, "enum") ||
				strings.HasPrefix(rhs, "union") || strings.HasPrefix(rhs, "opaque") ||
				strings.HasPrefix(rhs, "error")
			if !skipAlias {
				if _, skip := zigKeywordSkip[name]; !skip {
					parent := zigParentName(typeStack)
					sym := symbol(repoID, relPath, name, types.SymbolKindTypeAlias, line, line, "zig", "", parent)
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				}
			}
			braceDepth += zigBraceDelta(ln)
			zigPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		if m := reZigFn.FindStringSubmatch(ln); len(m) > 1 {
			fnName := m[1]
			if _, skip := zigKeywordSkip[fnName]; skip {
				braceDepth += zigBraceDelta(ln)
				zigPopClosedTypes(&typeStack, braceDepth)
				continue
			}
			parent := zigParentName(typeStack)
			kind := types.SymbolKindFunction
			if parent != "" {
				kind = types.SymbolKindMethod
			}
			sym := symbol(repoID, relPath, fnName, kind, line, line, "zig", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			before := braceDepth
			braceDepth += zigBraceDelta(ln)
			funcOpenDepth = before + 1
			if idx := strings.Index(ln, "{"); idx >= 0 {
				emitZigCalls(repoID, relPath, currentFuncID, ln[idx+1:], out)
			}
			if !strings.Contains(ln, "{") || braceDepth < funcOpenDepth {
				currentFuncID = ""
				funcOpenDepth = -1
			}
			zigPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		// Inside a type body (not a function): fields / enum variants.
		if currentFuncID == "" && len(typeStack) > 0 && trim != "" && !strings.HasPrefix(trim, "//") {
			top := typeStack[len(typeStack)-1]
			if top.kind == "enum" {
				if m := reZigVariant.FindStringSubmatch(ln); len(m) > 1 {
					name := m[1]
					if _, skip := zigKeywordSkip[name]; !skip && zigLooksLikeIdent(name) {
						sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "zig", "", top.name)
						out.Symbols = append(out.Symbols, sym)
						out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					}
				}
			} else if top.kind == "struct" || top.kind == "union" {
				if m := reZigField.FindStringSubmatch(ln); len(m) > 1 {
					name := m[1]
					if _, skip := zigKeywordSkip[name]; !skip && zigLooksLikeIdent(name) {
						sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "zig", "", top.name)
						out.Symbols = append(out.Symbols, sym)
						out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					}
				}
			}
		}

		if currentFuncID != "" && trim != "" {
			emitZigCalls(repoID, relPath, currentFuncID, ln, out)
		}
		braceDepth += zigBraceDelta(ln)
		if currentFuncID != "" && (funcOpenDepth < 0 || braceDepth < funcOpenDepth) {
			currentFuncID = ""
			funcOpenDepth = -1
		}
		zigPopClosedTypes(&typeStack, braceDepth)
	}
	return out, nil
}

func zigParentName(stack []zigTypeFrame) string {
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1].name
}

func zigPopClosedTypes(stack *[]zigTypeFrame, braceDepth int) {
	for len(*stack) > 0 && braceDepth < (*stack)[len(*stack)-1].openDepth {
		*stack = (*stack)[:len(*stack)-1]
	}
}

func zigLooksLikeIdent(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func emitZigCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	emitCall := func(name string, conf float64) {
		if name == "" {
			return
		}
		if _, skip := zigKeywordSkip[name]; skip {
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
		if _, skip := zigKeywordSkip[name]; skip {
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
	for _, m := range reZigCall.FindAllStringSubmatchIndex(ln, -1) {
		if len(m) < 6 {
			continue
		}
		recv, name := "", ln[m[4]:m[5]]
		if m[2] >= 0 && m[3] > m[2] {
			recv = ln[m[2]:m[3]]
		}
		if name == "" {
			continue
		}
		// Honesty: `@import(` / `@as(` — skip builtins (match may start after `@`).
		nameStart := m[4]
		if nameStart > 0 && ln[nameStart-1] == '@' {
			continue
		}
		if recv != "" {
			recvStart := m[2]
			if recvStart > 0 && ln[recvStart-1] == '@' {
				continue
			}
		}
		emitCall(name, 0.45)
		// Module/type receiver: helpers.upper → helpers read + helpers.upper call.
		if recv != "" {
			emitRead(recv, 0.7)
			emitCall(recv+"."+name, 0.65)
		}
	}
}

func zigBraceDelta(s string) int {
	n := 0
	inStr := false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == quote {
				inStr = false
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = true
			quote = c
		case '{':
			n++
		case '}':
			n--
		}
	}
	return n
}
