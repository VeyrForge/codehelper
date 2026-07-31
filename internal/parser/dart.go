package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Dart has no tree-sitter binding in go-tree-sitter; this is a line-oriented
// lite extractor: types/methods/fields, import URIs, and heuristic call edges
// (incl. Class.method receivers). Methods use SymbolKindMethod + brace-stack
// ParentID; top-level funcs stay parentless. Constructors indexed as methods.
// Flutter densify: widget/screen/entrypoint roles, extends→inherits, GoRoute sites.
// Confidence: Low / lite — prefer lexical query; do not claim High.

var (
	reDartType      = regexp.MustCompile(`(?m)^\s*(?:abstract\s+|sealed\s+|base\s+|final\s+|mixin\s+)?(?:class|mixin|enum|extension|typedef)\s+(\w+)`)
	reDartFunc      = regexp.MustCompile(`(?m)^\s*(?:(?:static|const|factory|external|Future(?:Or)?|void|int|String|bool|double|dynamic|num|List|Map|Set|Iterable|Widget|State)\s+)+(\w+)\s*\(`)
	reDartFuncLoose = regexp.MustCompile(`(?m)^\s*(?:[\w.<>,\s\[\]]+?\s+)?(\w+)\s*\([^;]*\)\s*(?:async\s*)?(?:\{|=>)`)
	reDartNamedCtor = regexp.MustCompile(`(?m)^\s*(\w+)\.(\w+)\s*\(`)
	reDartCtorSemi  = regexp.MustCompile(`(?m)^\s*(\w+)\s*\(.*\)\s*;`)
	reDartField     = regexp.MustCompile(`(?m)^\s*(?:static\s+|covariant\s+)*(?:late\s+)?(?:final|const|var)\s+(?:[\w.<>,\?\s\[\]]+\s+)?(\w+)\s*(?:=|;|$)`)
	reDartGetter    = regexp.MustCompile(`(?m)^\s*(?:[\w.<>,\s\[\]]+?\s+)?get\s+(\w+)\s*(?:=>|\{)`)
	reDartSetter    = regexp.MustCompile(`(?m)^\s*set\s+(\w+)\s*\(`)
	reDartImport    = regexp.MustCompile(`(?m)^\s*(?:import|export)\s+['"]([^'"]+)['"]`)
	reDartPart      = regexp.MustCompile(`(?m)^\s*part(?:\s+of)?\s+['"]([^'"]+)['"]`)
	// Groups: optional Capitalized/identifier receiver, callee name.
	reDartCall      = regexp.MustCompile(`(?:^|[^\w.])(?:([A-Za-z_][\w]*)\s*\.\s*)?([A-Za-z_][\w]*)\s*\(`)
	reDartExtends   = regexp.MustCompile(`\b(?:extends|with|implements)\s+([A-Za-z_][\w.]*)`)
	reDartRoutePath = regexp.MustCompile(`(?i)path:\s*['"]([^'"]+)['"]`)
	reDartRouteName = regexp.MustCompile(`(?i)name:\s*['"]([^'"]+)['"]`)
	reDartRouteDest = regexp.MustCompile(`(?i)(?:builder|pageBuilder):\s*\([^)]*\)\s*(?:async\s*)?=>\s*(?:const\s+)?([A-Za-z_][\w]*)`)
	reDartPageRoute = regexp.MustCompile(`(?is)MaterialPageRoute\s*\([^)]*builder:\s*\([^)]*\)\s*=>\s*(?:const\s+)?([A-Za-z_][\w]*)`)
	reDartRouterVar = regexp.MustCompile(`(?m)^\s*(?:final|var|late\s+final)?\s*(?:GoRouter\s+)?(appRouter|router|goRouter)\s*=`)
)

var dartKeywordSkip = map[string]struct{}{
	"if": {}, "for": {}, "while": {}, "switch": {}, "return": {}, "throw": {},
	"new": {}, "await": {}, "assert": {}, "class": {}, "enum": {}, "mixin": {},
	"typedef": {}, "extension": {}, "import": {}, "export": {}, "part": {},
	"void": {}, "int": {}, "String": {}, "bool": {}, "double": {}, "dynamic": {},
	"var": {}, "final": {}, "const": {}, "late": {}, "static": {}, "factory": {},
	"abstract": {}, "sealed": {}, "base": {}, "get": {}, "set": {}, "is": {},
	"as": {}, "super": {}, "this": {}, "true": {}, "false": {}, "null": {},
	"try": {}, "catch": {}, "finally": {}, "do": {}, "else": {}, "case": {},
	"default": {}, "break": {}, "continue": {}, "rethrow": {}, "with": {},
	"implements": {}, "extends": {}, "on": {}, "when": {}, "sync": {}, "async": {},
	"yield": {}, "required": {}, "covariant": {}, "external": {},
	// Type constructors / common noise — keep call graph honest (Low confidence).
	"List": {}, "Map": {}, "Set": {}, "Iterable": {}, "Future": {}, "FutureOr": {},
	"Stream": {}, "Widget": {}, "State": {}, "Key": {}, "ValueKey": {}, "ObjectKey": {},
	"Text": {}, "Column": {}, "Row": {}, "Center": {}, "Container": {}, "SizedBox": {},
	"Padding": {}, "Scaffold": {}, "MaterialApp": {}, "CupertinoApp": {},
	"print": {}, "debugPrint": {}, "num": {}, "Object": {}, "Type": {},
	// Common stdlib / instance noise — keep call graph honest (Low confidence).
	"toString": {}, "toUpperCase": {}, "toLowerCase": {}, "trim": {}, "trimLeft": {},
	"trimRight": {}, "substring": {}, "contains": {}, "startsWith": {}, "endsWith": {},
	"isEmpty": {}, "isNotEmpty": {}, "length": {}, "add": {}, "remove": {}, "clear": {},
	"toList": {}, "toSet": {}, "toMap": {}, "map": {}, "where": {}, "forEach": {},
	"fold": {}, "reduce": {}, "any": {}, "every": {}, "first": {}, "last": {},
	"hashCode": {}, "runtimeType": {}, "noSuchMethod": {},
}

var dartWidgetBases = map[string]struct{}{
	"StatelessWidget": {}, "StatefulWidget": {}, "ConsumerWidget": {},
	"HookWidget": {}, "InheritedWidget": {}, "PreferredSizeWidget": {},
	"ConsumerStatefulWidget": {}, "HookConsumerWidget": {},
}

type dartTypeFrame struct {
	name      string
	openDepth int
}

func parseDartLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	s := string(buf)
	frameworks := DetectFrameworkPacks(relPath, nil, s)
	lines := strings.Split(s, "\n")
	line := 0
	var currentFuncID string
	funcOpenDepth := -1
	braceDepth := 0
	var typeStack []dartTypeFrame

	for _, ln := range lines {
		line++
		// Windows fixtures / git checkout may leave CR on the line after Split("\n").
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)

		if strings.HasPrefix(trim, "//") {
			braceDepth += dartBraceDelta(ln)
			dartPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		if m := reDartImport.FindStringSubmatch(ln); len(m) > 1 {
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
			braceDepth += dartBraceDelta(ln)
			continue
		}
		if m := reDartPart.FindStringSubmatch(ln); len(m) > 1 {
			mod := strings.TrimSpace(m[1])
			if mod != "" {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.75,
				})
			}
			braceDepth += dartBraceDelta(ln)
			continue
		}

		if m := reDartType.FindStringSubmatch(ln); len(m) > 1 && m[1] != "" {
			name := m[1]
			kind := types.SymbolKindClass
			if strings.Contains(ln, "enum ") {
				kind = types.SymbolKindEnum
			} else if strings.Contains(ln, "mixin ") {
				kind = types.SymbolKindInterface
			} else if strings.Contains(ln, "typedef ") {
				kind = types.SymbolKindTypeAlias
			} else if strings.Contains(ln, "extension ") {
				kind = types.SymbolKindClass
			}
			role := dartClassRole(ln, name, frameworks, relPath)
			fw := frameworks
			if role != "" {
				fw = withFramework(fw, string(FrameworkFlutter))
			}
			parent := dartParentName(typeStack)
			sym := symbol(repoID, relPath, name, kind, line, line, "dart", frameworkSignature(fw, role), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitDartInheritance(repoID, relPath, sym.ID, ln, out)
			before := braceDepth
			braceDepth += dartBraceDelta(ln)
			if strings.Contains(ln, "{") {
				openedTo := before + 1
				if braceDepth >= openedTo {
					typeStack = append(typeStack, dartTypeFrame{name: name, openDepth: openedTo})
				}
			}
			dartPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		// Getters/setters: index as methods/functions without treating as call sites of "get".
		if m := reDartGetter.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := dartKeywordSkip[name]; !skip && name != "" {
				parent := dartParentName(typeStack)
				kind := types.SymbolKindFunction
				if parent != "" {
					kind = types.SymbolKindMethod
				}
				sym := symbol(repoID, relPath, name, kind, line, line, "dart", "", parent)
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				if idx := strings.Index(ln, "=>"); idx >= 0 {
					emitDartCalls(repoID, relPath, sym.ID, ln[idx+2:], out)
				} else if idx := strings.Index(ln, "{"); idx >= 0 {
					currentFuncID = sym.ID
					before := braceDepth
					braceDepth += dartBraceDelta(ln)
					funcOpenDepth = before + 1
					emitDartCalls(repoID, relPath, currentFuncID, ln[idx+1:], out)
					if braceDepth < funcOpenDepth {
						currentFuncID = ""
						funcOpenDepth = -1
					}
					dartPopClosedTypes(&typeStack, braceDepth)
					continue
				}
			}
			braceDepth += dartBraceDelta(ln)
			dartPopClosedTypes(&typeStack, braceDepth)
			continue
		}
		if m := reDartSetter.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := dartKeywordSkip[name]; !skip && name != "" {
				parent := dartParentName(typeStack)
				kind := types.SymbolKindFunction
				if parent != "" {
					kind = types.SymbolKindMethod
				}
				sym := symbol(repoID, relPath, name, kind, line, line, "dart", "", parent)
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
			braceDepth += dartBraceDelta(ln)
			dartPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		// Named constructors: Class.named(…) — method ParentID=Class.
		if m := reDartNamedCtor.FindStringSubmatch(ln); len(m) > 2 {
			cls, ctor := m[1], m[2]
			parent := dartParentName(typeStack)
			if parent == "" || parent == cls {
				if _, skip := dartKeywordSkip[ctor]; !skip && ctor != "" {
					if parent == "" {
						parent = cls
					}
					sym := symbol(repoID, relPath, ctor, types.SymbolKindMethod, line, line, "dart", "", parent)
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					currentFuncID = sym.ID
					before := braceDepth
					braceDepth += dartBraceDelta(ln)
					funcOpenDepth = before + 1
					if idx := strings.Index(ln, "{"); idx >= 0 {
						emitDartCalls(repoID, relPath, currentFuncID, ln[idx+1:], out)
					}
					// Initializer-list / redirecting ctors often end with `;` (no body).
					if !strings.Contains(ln, "{") || braceDepth < funcOpenDepth {
						currentFuncID = ""
						funcOpenDepth = -1
					}
					dartPopClosedTypes(&typeStack, braceDepth)
					continue
				}
			}
		}

		// Unnamed constructor ending with `;` (no body): ClassName(…);
		if m := reDartCtorSemi.FindStringSubmatch(ln); len(m) > 1 {
			parent := dartParentName(typeStack)
			if parent != "" && m[1] == parent {
				sym := symbol(repoID, relPath, m[1], types.SymbolKindMethod, line, line, "dart", frameworkSignature(frameworks, "constructor"), parent)
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				braceDepth += dartBraceDelta(ln)
				dartPopClosedTypes(&typeStack, braceDepth)
				continue
			}
		}

		fnName := ""
		if m := reDartFunc.FindStringSubmatch(ln); len(m) > 1 {
			fnName = m[1]
		} else if m := reDartFuncLoose.FindStringSubmatch(ln); len(m) > 1 {
			cand := m[1]
			if _, skip := dartKeywordSkip[cand]; !skip && cand != "" {
				fnName = cand
			}
		}
		if fnName != "" {
			if _, skip := dartKeywordSkip[fnName]; skip {
				fnName = ""
			}
		}
		parent := dartParentName(typeStack)
		isCtor := fnName != "" && parent != "" && fnName == parent
		if fnName != "" {
			kind := types.SymbolKindFunction
			if parent != "" {
				kind = types.SymbolKindMethod
			}
			role := dartFuncRole(fnName, frameworks, relPath)
			fw := frameworks
			if role != "" {
				fw = withFramework(fw, string(FrameworkFlutter))
			}
			sig := frameworkSignature(fw, role)
			if isCtor {
				sig = frameworkSignature(fw, "constructor")
			}
			sym := symbol(repoID, relPath, fnName, kind, line, line, "dart", sig, parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			before := braceDepth
			braceDepth += dartBraceDelta(ln)
			funcOpenDepth = before + 1
			if idx := strings.Index(ln, "{"); idx >= 0 {
				emitDartCalls(repoID, relPath, currentFuncID, ln[idx+1:], out)
			} else if idx := strings.Index(ln, "=>"); idx >= 0 {
				emitDartCalls(repoID, relPath, currentFuncID, ln[idx+2:], out)
			}
			if (!strings.Contains(ln, "{") && !strings.Contains(ln, "=>")) || braceDepth < funcOpenDepth || strings.Contains(ln, "=>") {
				currentFuncID = ""
				funcOpenDepth = -1
			}
			dartPopClosedTypes(&typeStack, braceDepth)
			continue
		}

		// Instance/static fields inside a type (not methods).
		if currentFuncID == "" && parent != "" {
			if m := reDartField.FindStringSubmatch(ln); len(m) > 1 {
				name := m[1]
				if _, skip := dartKeywordSkip[name]; !skip && name != "" && name != parent {
					sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "dart", "", parent)
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				}
			}
		}

		if currentFuncID != "" && trim != "" {
			emitDartCalls(repoID, relPath, currentFuncID, ln, out)
		}
		braceDepth += dartBraceDelta(ln)
		if currentFuncID != "" && (funcOpenDepth < 0 || braceDepth < funcOpenDepth) {
			currentFuncID = ""
			funcOpenDepth = -1
		}
		dartPopClosedTypes(&typeStack, braceDepth)
	}
	addDartFrameworkSymbols(repoID, relPath, s, out, frameworks)
	return out, nil
}

func dartParentName(stack []dartTypeFrame) string {
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1].name
}

func dartPopClosedTypes(stack *[]dartTypeFrame, braceDepth int) {
	for len(*stack) > 0 && braceDepth < (*stack)[len(*stack)-1].openDepth {
		*stack = (*stack)[:len(*stack)-1]
	}
}

func dartClassRole(classLine, name string, frameworks []string, relPath string) string {
	lower := strings.ToLower(classLine)
	p := strings.ToLower(filepath.ToSlash(relPath))
	isFlutter := containsFramework(frameworks, string(FrameworkFlutter))
	extendsWidget := false
	for base := range dartWidgetBases {
		if strings.Contains(classLine, "extends "+base) || strings.Contains(classLine, "with "+base) {
			extendsWidget = true
			break
		}
	}
	if !isFlutter && !extendsWidget && !strings.Contains(lower, "package:flutter") {
		// Path convention alone is weak without Flutter markers.
		if !(strings.Contains(p, "/screens/") || strings.Contains(p, "/widgets/") ||
			strings.Contains(p, "/pages/")) {
			return ""
		}
	}
	if strings.HasSuffix(name, "Screen") || strings.HasSuffix(name, "Page") ||
		strings.Contains(p, "/screens/") || strings.Contains(p, "/pages/") {
		return "screen"
	}
	if strings.HasSuffix(name, "App") && extendsWidget {
		return "entrypoint"
	}
	if extendsWidget || strings.HasSuffix(name, "Widget") || strings.HasSuffix(name, "Card") ||
		strings.Contains(p, "/widgets/") {
		return "widget"
	}
	return ""
}

func dartFuncRole(name string, frameworks []string, relPath string) string {
	if !containsFramework(frameworks, string(FrameworkFlutter)) {
		return ""
	}
	if name == "main" {
		return "entrypoint"
	}
	_ = relPath
	return ""
}

func emitDartInheritance(repoID, relPath, fromID, ln string, out *ParseResult) {
	for _, m := range reDartExtends.FindAllStringSubmatch(ln, -1) {
		base := m[1]
		if base == "" {
			continue
		}
		// Skip type args noise like State<HomeScreen> — take bare head.
		if i := strings.IndexByte(base, '<'); i >= 0 {
			base = base[:i]
		}
		if base == "" {
			continue
		}
		tgt := "symref:" + repoID + ":" + relPath + ":" + base
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromID, tgt, "inherits"),
			RepoID:     repoID,
			Kind:       types.RefKindInherits,
			SourceID:   fromID,
			TargetID:   tgt,
			Confidence: 0.7,
		})
	}
}

// addDartFrameworkSymbols indexes GoRouter / MaterialPageRoute navigation sites.
func addDartFrameworkSymbols(repoID, relPath, src string, out *ParseResult, frameworks []string) {
	isFlutter := containsFramework(frameworks, string(FrameworkFlutter)) ||
		strings.Contains(strings.ToLower(src), "package:flutter/") ||
		strings.Contains(src, "GoRouter") || strings.Contains(src, "GoRoute")
	if !isFlutter {
		return
	}
	fw := withFramework(frameworks, string(FrameworkFlutter))

	for _, block := range dartExtractGoRouteBlocks(src) {
		path := ""
		if pm := reDartRoutePath.FindStringSubmatch(block.body); len(pm) > 1 {
			path = pm[1]
		}
		rname := ""
		if nm := reDartRouteName.FindStringSubmatch(block.body); len(nm) > 1 {
			rname = nm[1]
		}
		dest := ""
		if dm := reDartRouteDest.FindStringSubmatch(block.body); len(dm) > 1 {
			dest = dm[1]
		}
		symName := ""
		switch {
		case rname != "":
			symName = "route:" + rname
		case path != "":
			symName = "route:" + path
		default:
			continue
		}
		sym := symbol(repoID, relPath, symName, types.SymbolKindFunction, block.line, block.line, "dart", frameworkSignature(fw, "route"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		if dest != "" {
			tgt := "symref:" + repoID + ":" + relPath + ":" + dest
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, sym.ID, tgt, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   sym.ID,
				TargetID:   tgt,
				Confidence: 0.55,
			})
		}
	}

	for _, m := range reDartPageRoute.FindAllStringSubmatchIndex(src, -1) {
		if len(m) < 4 {
			continue
		}
		dest := src[m[2]:m[3]]
		if dest == "" {
			continue
		}
		line := 1 + strings.Count(src[:m[0]], "\n")
		symName := "route:MaterialPageRoute->" + dest
		sym := symbol(repoID, relPath, symName, types.SymbolKindFunction, line, line, "dart", frameworkSignature(fw, "route"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		tgt := "symref:" + repoID + ":" + relPath + ":" + dest
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, sym.ID, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   sym.ID,
			TargetID:   tgt,
			Confidence: 0.55,
		})
	}

	for _, m := range reDartRouterVar.FindAllStringSubmatchIndex(src, -1) {
		if len(m) < 4 {
			continue
		}
		name := src[m[2]:m[3]]
		line := 1 + strings.Count(src[:m[0]], "\n")
		sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "dart", frameworkSignature(fw, "navigator"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	}
}

type dartRouteBlock struct {
	body string
	line int
}

// dartExtractGoRouteBlocks finds GoRoute(…) spans with nested-paren awareness.
// Skips GoRouter( — that is the navigator factory, not a route entry.
func dartExtractGoRouteBlocks(src string) []dartRouteBlock {
	var out []dartRouteBlock
	for i := 0; i < len(src); {
		idx := strings.Index(src[i:], "GoRoute(")
		if idx < 0 {
			break
		}
		start := i + idx
		// GoRouter( also contains the GoRoute( prefix — skip the navigator.
		if start+8 <= len(src) && src[start:start+8] == "GoRouter" {
			i = start + 8
			continue
		}
		open := start + len("GoRoute")
		if open >= len(src) || src[open] != '(' {
			i = start + 1
			continue
		}
		depth := 0
		end := -1
		for j := open; j < len(src); j++ {
			switch src[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = j
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		out = append(out, dartRouteBlock{
			body: src[open+1 : end],
			line: 1 + strings.Count(src[:start], "\n"),
		})
		i = end + 1
	}
	return out
}

func emitDartCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	emitCall := func(name string, conf float64) {
		if name == "" {
			return
		}
		if _, skip := dartKeywordSkip[name]; skip {
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
		if _, skip := dartKeywordSkip[name]; skip {
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
	for _, m := range reDartCall.FindAllStringSubmatch(ln, -1) {
		recv, name := m[1], m[2]
		if name == "" {
			continue
		}
		emitCall(name, 0.45)
		// Type/library receiver: Helpers.format / Tone.casual-style Type.member calls.
		if recv != "" && len(recv) > 0 && recv[0] >= 'A' && recv[0] <= 'Z' {
			emitRead(recv, 0.7)
			emitCall(recv+"."+name, 0.65)
		}
	}
}

func dartBraceDelta(s string) int {
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
