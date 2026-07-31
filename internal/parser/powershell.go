package parser

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// PowerShell Lite densify — .ps1/.psm1 function/filter symbols + heuristic calls.
// Honest Low/Lite: no AST, no pipeline graph, no module export fidelity.
// Prefer lexical query; empty fanout ≠ isolation.

var (
	rePSFunction = regexp.MustCompile(`(?i)^\s*(?:function|filter)\s+([A-Za-z_][\w-]*)\s*(?:[\(\{]|$)`)
	rePSIdent    = regexp.MustCompile(`(?i)[A-Za-z_][\w-]*`)
)

func parsePowerShellLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	lines := strings.Split(string(buf), "\n")
	defined := map[string]string{} // lower name → id

	// Pass 1: mint function/filter symbols.
	for i, raw := range lines {
		ln := strings.TrimRight(raw, "\r")
		if m := rePSFunction.FindStringSubmatch(ln); m != nil {
			name := m[1]
			line := i + 1
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "powershell", "function", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			defined[strings.ToLower(name)] = sym.ID
		}
	}

	// Pass 2: brace-scoped calls to in-file functions only.
	type fnFrame struct {
		id        string
		openDepth int
	}
	var stack []fnFrame
	braceDepth := 0
	for _, raw := range lines {
		ln := strings.TrimRight(raw, "\r")
		trim := strings.TrimSpace(ln)
		if trim == "" || strings.HasPrefix(trim, "#") {
			braceDepth += psBraceDelta(ln)
			for len(stack) > 0 && braceDepth <= stack[len(stack)-1].openDepth {
				stack = stack[:len(stack)-1]
			}
			continue
		}

		if m := rePSFunction.FindStringSubmatch(ln); m != nil {
			id := defined[strings.ToLower(m[1])]
			if id != "" {
				stack = append(stack, fnFrame{id: id, openDepth: braceDepth})
			}
		}

		var cur string
		if len(stack) > 0 {
			cur = stack[len(stack)-1].id
		}
		if cur != "" && !rePSFunction.MatchString(ln) {
			for _, m := range rePSIdent.FindAllStringIndex(ln, -1) {
				start, end := m[0], m[1]
				if start > 0 {
					prev := rune(ln[start-1])
					if unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '_' || prev == '-' || prev == '$' || prev == '.' {
						continue
					}
				}
				callees := ln[start:end]
				if psKeyword(callees) {
					continue
				}
				tgt := defined[strings.ToLower(callees)]
				if tgt == "" || tgt == cur {
					continue
				}
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, cur, tgt, "calls"),
					RepoID:     repoID,
					Kind:       types.RefKindCalls,
					SourceID:   cur,
					TargetID:   tgt,
					Confidence: 0.55,
				})
			}
		}

		braceDepth += psBraceDelta(ln)
		for len(stack) > 0 && braceDepth <= stack[len(stack)-1].openDepth {
			stack = stack[:len(stack)-1]
		}
	}
	return out, nil
}

func psKeyword(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "if", "else", "elseif", "foreach", "for", "while", "do", "switch", "return",
		"exit", "throw", "try", "catch", "finally", "trap", "break", "continue",
		"param", "begin", "process", "end", "dynamicparam", "function", "filter",
		"workflow", "configuration", "class", "enum", "using", "hidden", "static",
		"true", "false", "null", "and", "or", "not", "xor":
		return true
	}
	return false
}

func psBraceDelta(line string) int {
	delta := 0
	inSingle, inDouble := false, false
	for _, r := range line {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case inSingle || inDouble:
			continue
		case r == '{':
			delta++
		case r == '}':
			delta--
		}
	}
	return delta
}
