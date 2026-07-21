package parser

import (
	"context"
	"fmt"
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

// gdCallRe matches Foo.bar( / bar( call sites. Groups: optional receiver, callee.
var gdCallRe = regexp.MustCompile(`(?:^|[^\w.])(?:([A-Za-z_]\w*)\s*\.\s*)?([A-Za-z_]\w*)\s*\(`)

// gdPreloadRe matches preload("res://…") / load("res://…") for import edges.
var gdPreloadRe = regexp.MustCompile(`\b(?:preload|load)\s*\(\s*["']([^"']+)["']\s*\)`)

// gdConnectRe matches .connect(callback) where callback is a bare identifier.
var gdConnectRe = regexp.MustCompile(`\.connect\s*\(\s*([A-Za-z_]\w*)\s*[,)]`)

var gdCallSkip = map[string]bool{
	"if": true, "elif": true, "else": true, "for": true, "while": true,
	"match": true, "when": true, "return": true, "await": true, "assert": true,
	"pass": true, "break": true, "continue": true, "class": true, "func": true,
	"signal": true, "enum": true, "const": true, "var": true, "static": true,
	"and": true, "or": true, "not": true, "in": true, "is": true, "as": true,
	"preload": true, "load": true, "print": true, "prints": true, "printerr": true,
	"push_error": true, "push_warning": true, "range": true, "len": true,
	"str": true, "int": true, "float": true, "bool": true, "typeof": true,
}

// parseGDScriptLite extracts top-level GDScript declarations and call edges.
func parseGDScriptLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	line := 0
	var currentFuncID string
	currentFuncIndent := -1
	for _, ln := range strings.Split(string(buf), "\n") {
		line++
		trim := strings.TrimSpace(ln)
		ind := indentLen(ln)

		// Leaving a function body: dedent to or above the func's indent on a
		// non-empty, non-comment line.
		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, "#") && ind <= currentFuncIndent {
			currentFuncID = ""
			currentFuncIndent = -1
		}

		declMatched := false
		for _, d := range gdDecls {
			m := d.re.FindStringSubmatch(ln)
			if m == nil || m[1] == "" {
				continue
			}
			sym := symbol(repoID, relPath, m[1], d.kind, line, line, "gdscript", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if d.kind == types.SymbolKindFunction {
				currentFuncID = sym.ID
				currentFuncIndent = ind
			}
			declMatched = true
			// Local var/const inside a function may still call helpers on the RHS
			// (e.g. `var x = preload("res://…")`).
			if currentFuncID != "" && d.kind != types.SymbolKindFunction && ind > currentFuncIndent {
				emitGDScriptCalls(repoID, relPath, fid, currentFuncID, ln, out)
			}
			break
		}
		// Top-level extends ClassName → file reads the parent class.
		if !declMatched {
			if m := gdExtendsRe.FindStringSubmatch(ln); len(m) > 1 {
				tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, m[1])
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, tgt, "reads"),
					RepoID:     repoID,
					Kind:       types.RefKindReads,
					SourceID:   fid,
					TargetID:   tgt,
					Confidence: 0.85,
				})
			}
		}
		if declMatched {
			continue
		}
		if currentFuncID == "" {
			continue
		}
		emitGDScriptCalls(repoID, relPath, fid, currentFuncID, ln, out)
	}
	return out, nil
}

func indentLen(ln string) int {
	n := 0
	for _, r := range ln {
		if r == ' ' {
			n++
		} else if r == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}

// gdTypedVarRe matches `var x: ClassName` / `@onready var x: ClassName`.
var gdTypedVarRe = regexp.MustCompile(`(?i)\bvar\s+\w+\s*:\s*([A-Z][A-Za-z0-9_]*)`)

// gdNewRe matches ClassName.new( construction.
var gdNewRe = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\s*\.\s*new\s*\(`)

// gdExtendsRe matches `extends ClassName` / `extends "res://…"`.
var gdExtendsRe = regexp.MustCompile(`(?i)^\s*extends\s+([A-Z][A-Za-z0-9_]*)`)

// gdEmitRe matches signal.emit / emit_signal("name").
var gdEmitRe = regexp.MustCompile(`(?:\b([A-Za-z_]\w*)\s*\.\s*emit\s*\(|\bemit_signal\s*\(\s*["']([^"']+)["'])`)

func emitGDScriptCalls(repoID, relPath, fid, fromSym, ln string, out *ParseResult) {
	for _, m := range gdPreloadRe.FindAllStringSubmatch(ln, -1) {
		path := strings.TrimSpace(m[1])
		if path == "" {
			continue
		}
		tgt := moduleNodeID(repoID, path)
		out.Imports = append(out.Imports, path)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fid, tgt, "imports"),
			RepoID:     repoID,
			Kind:       types.RefKindImports,
			SourceID:   fid,
			TargetID:   tgt,
			Confidence: 0.85,
		})
	}
	emitCall := func(name string, conf float64) {
		name = strings.TrimSpace(name)
		if name == "" || gdCallSkip[name] || !isCallableName(name) {
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
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
		name = strings.TrimSpace(name)
		if name == "" || gdCallSkip[name] || !isCallableName(name) {
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
	for _, m := range gdCallRe.FindAllStringSubmatch(ln, -1) {
		recv := strings.TrimSpace(m[1])
		callee := m[2]
		emitCall(callee, 0.5)
		// Capitalized receiver → class inbound (ClassName.method / singleton).
		if recv != "" && recv[0] >= 'A' && recv[0] <= 'Z' {
			emitRead(recv, 0.7)
			emitCall(recv+"."+callee, 0.65)
		}
	}
	for _, m := range gdConnectRe.FindAllStringSubmatch(ln, -1) {
		emitCall(m[1], 0.55)
	}
	for _, m := range gdNewRe.FindAllStringSubmatch(ln, -1) {
		emitRead(m[1], 0.8)
		emitCall(m[1], 0.75)
	}
	for _, m := range gdTypedVarRe.FindAllStringSubmatch(ln, -1) {
		emitRead(m[1], 0.75)
	}
	for _, m := range gdEmitRe.FindAllStringSubmatch(ln, -1) {
		if m[1] != "" {
			emitCall(m[1], 0.7)
			emitCall(m[1]+".emit", 0.65)
		}
		if len(m) > 2 && m[2] != "" {
			emitCall(m[2], 0.7)
		}
	}
}
