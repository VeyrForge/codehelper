package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	tsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	tst "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseResult holds symbols and edges extracted from one source file.
type ParseResult struct {
	Symbols []types.Symbol
	Edges   []types.Reference
	Imports []string
}

// ParseTypeScript parses buf as TypeScript/TSX based on file extension.
func ParseTypeScript(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	_ = ctx
	ext := strings.ToLower(filepath.Ext(relPath))
	var lang *sitter.Language
	switch ext {
	case ".tsx":
		lang = tsx.GetLanguage()
	case ".ts", ".js", ".jsx", ".mjs", ".cjs":
		lang = tst.GetLanguage()
	default:
		lang = tst.GetLanguage()
	}
	p := sitter.NewParser()
	p.SetLanguage(lang)
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	root := tree.RootNode()
	out := &ParseResult{}
	langName := "typescript"
	if ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
		langName = "javascript"
	}
	frameworks := DetectFrameworkPacks(relPath, nil, string(buf))
	fid := FileNodeID(repoID, relPath)
	Walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "import_statement":
			if src := importSource(n, buf); src != "" {
				out.Imports = append(out.Imports, src)
				modID := moduleNodeID(repoID, src)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, modID, "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   modID,
					Confidence: 0.9,
				})
			}
		case "call_expression":
			// CommonJS require('…') / require("…") — Express and most Node libs.
			if mod := cjsRequireModule(n, buf); mod != "" {
				out.Imports = append(out.Imports, mod)
				modID := moduleNodeID(repoID, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, modID, "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   modID,
					Confidence: 0.85,
				})
			}
		case "function_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			role := ""
			if looksLikeRouteHandlerName(name) {
				role = "entrypoint"
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(frameworks, role), parentFromStack(n))
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, buildJSInstanceScope(n, buf))
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "method_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, "", parentClassID(n, buf))
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, buildJSInstanceScope(n, buf))
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "class_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			classFW := frameworks
			if looksLikeNestFile(relPath, buf) {
				classFW = withFramework(classFW, "nestjs")
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(classFW, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if looksLikeNestFile(relPath, buf) {
				extractNestDI(n, buf, repoID, relPath, sym.ID, out)
			}
		case "variable_declarator":
			// Capture function-like variables used heavily in React/Next.js:
			// const Page = () => {}
			// const handler = async function() {}
			id := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if id == nil || val == nil {
				return
			}
			name := strings.TrimSpace(id.Content(buf))
			if name == "" {
				return
			}
			var body *sitter.Node
			role := ""
			if isFunctionLikeTSNode(val.Type()) {
				body = val
			} else if fnNode := wrappedFunctionNode(val, buf); fnNode != nil {
				body = fnNode
				role = "wrapped_component"
			}
			if body == nil {
				if isCapacitorPluginRegistration(val, buf) {
					sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(withFramework(frameworks, string(FrameworkCapacitor)), "plugin"), parentFromStack(n))
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					addReadEdgesFromNode(repoID, relPath, sym.ID, val, buf, out)
				}
				return
			}
			if looksLikeRouteHandlerName(name) {
				role = "entrypoint"
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(frameworks, role), parentFromStack(n))
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(body, buf, repoID, relPath, sym.ID, out, buildJSInstanceScope(body, buf))
			addReadEdgesFromNode(repoID, relPath, sym.ID, body, buf, out)
		case "export_statement":
			// Frameworks often use anonymous default exports for entrypoints.
			exportBody := strings.TrimSpace(n.Content(buf))
			if strings.HasPrefix(exportBody, "export default") && looksLikeAnonymousDefaultFunction(exportBody) {
				sym := symbol(repoID, relPath, "default_export", types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(frameworks, "entrypoint"), "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
		case "assignment_expression":
			// CommonJS / Express-style prototype APIs:
			//   app.use = function use(...) {}
			//   exports.Router = Router
			//   proto.send = function send(...) {}
			// Index under the dotted alias (app.use) so query/context("app.use")
			// resolve; bare property stays in signature as bare= for substring hits.
			if name, alias, body, kind, ok := cjsPrototypeAssign(n, buf); ok {
				symName := name
				if alias != "" {
					symName = alias
				}
				sig := frameworkSignature(frameworks, "cjs_export")
				if name != "" && name != symName {
					if sig != "" {
						sig += "; "
					}
					sig += "bare=" + name
				}
				if alias != "" {
					if sig != "" {
						sig += "; "
					}
					sig += "alias=" + alias
				}
				sym := symbol(repoID, relPath, symName, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, sig, "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				if body != nil {
					extractCallsScoped(body, buf, repoID, relPath, sym.ID, out, buildJSInstanceScope(body, buf))
					addReadEdgesFromNode(repoID, relPath, sym.ID, body, buf, out)
				}
			}
		}
	})
	extractExpressTopLevel(root, buf, repoID, relPath, frameworks, out)
	return out, nil
}

// cjsRequireModule extracts the module path from require('x') / require("x").
func cjsRequireModule(n *sitter.Node, buf []byte) string {
	if n == nil || n.Type() != "call_expression" {
		return ""
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Type() != "identifier" {
		return ""
	}
	if strings.TrimSpace(fn.Content(buf)) != "require" {
		return ""
	}
	args := n.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "string", "string_fragment":
			s := strings.TrimSpace(c.Content(buf))
			s = strings.Trim(s, `"'`+"`")
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// extractExpressTopLevel indexes top-level app.get/use/listen/… call sites in
// Express example apps (no enclosing function) so examples gain symbols + call
// edges into aliased APIs (app.use, app.get).
func extractExpressTopLevel(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := string(buf)
	looksExpress := containsFramework(frameworks, string(FrameworkExpress)) ||
		strings.Contains(src, "express()") || strings.Contains(src, "require('express") ||
		strings.Contains(src, `require("express`) || strings.Contains(src, "require('../../") ||
		strings.Contains(src, `require("../..`)
	if !looksExpress {
		return
	}
	fw := withFramework(frameworks, string(FrameworkExpress))
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "expression_statement" {
			return
		}
		// Only top-level statements: parent is program / module.
		if p := n.Parent(); p != nil {
			switch p.Type() {
			case "program", "module", "export_statement":
			default:
				return
			}
		}
		call := n.NamedChild(0)
		if call == nil || call.Type() != "call_expression" {
			return
		}
		fn := call.ChildByFieldName("function")
		if fn == nil || fn.Type() != "member_expression" {
			return
		}
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return
		}
		recv := strings.TrimSpace(obj.Content(buf))
		meth := strings.TrimSpace(prop.Content(buf))
		if !isExpressAPIReceiver(recv) || !isExpressRouteMethod(meth) {
			return
		}
		line := int(n.StartPoint().Row) + 1
		alias := recv + "." + meth
		siteName := fmt.Sprintf("express_%s_%d", meth, line)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "javascript", frameworkSignature(fw, "entrypoint"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitTSCall(repoID, relPath, sym.ID, alias, 0.85, out)
		emitTSCall(repoID, relPath, sym.ID, meth, 0.55, out)
		// Capture calls inside inline route handlers (res.send, next, …).
		if args := call.ChildByFieldName("arguments"); args != nil {
			extractCalls(args, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, args, buf, out)
		}
	})
}

func isExpressAPIReceiver(recv string) bool {
	switch strings.ToLower(strings.TrimSpace(recv)) {
	case "app", "router", "route", "server", "application":
		return true
	default:
		return false
	}
}

func isExpressRouteMethod(m string) bool {
	switch strings.ToLower(m) {
	case "use", "get", "post", "put", "patch", "delete", "all", "listen",
		"set", "engine", "param", "route", "render", "enable", "disable",
		"enabled", "disabled", "path", "handle", "init":
		return true
	default:
		return false
	}
}

// expressMemberAlias returns "app.use" for member_expression callees whose
// receiver is an Express/CJS API object (same scope as cjsPrototypeAssign).
func expressMemberAlias(fn *sitter.Node, buf []byte) string {
	if fn == nil || fn.Type() != "member_expression" {
		return ""
	}
	obj := fn.ChildByFieldName("object")
	prop := fn.ChildByFieldName("property")
	if obj == nil || prop == nil {
		return ""
	}
	recv := strings.TrimSpace(obj.Content(buf))
	meth := strings.TrimSpace(prop.Content(buf))
	if meth == "" || !plausibleJSIdent(meth) {
		return ""
	}
	lower := strings.ToLower(recv)
	switch {
	case lower == "app", lower == "req", lower == "res", lower == "proto",
		lower == "router", lower == "route", lower == "server",
		lower == "request", lower == "response", lower == "application",
		lower == "exports",
		strings.HasSuffix(lower, ".prototype"),
		strings.HasPrefix(lower, "exports."):
		return recv + "." + meth
	default:
		return ""
	}
}

func emitTSCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) {
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

func containsFramework(list []string, want string) bool {
	for _, f := range list {
		if f == want {
			return true
		}
	}
	return false
}

// cjsPrototypeAssign extracts symbols from CommonJS/Express-style member assigns.
// Scoped to exports.*, module.exports.*, *.prototype.*, and app/req/res/proto
// receivers so we do not flood the index with arbitrary object mutations.
func cjsPrototypeAssign(n *sitter.Node, buf []byte) (name, alias string, body *sitter.Node, kind types.SymbolKind, ok bool) {
	left := n.ChildByFieldName("left")
	right := n.ChildByFieldName("right")
	if left == nil || right == nil || left.Type() != "member_expression" {
		return "", "", nil, "", false
	}
	prop := left.ChildByFieldName("property")
	obj := left.ChildByFieldName("object")
	if prop == nil {
		return "", "", nil, "", false
	}
	name = strings.TrimSpace(prop.Content(buf))
	if name == "" || !plausibleJSIdent(name) {
		return "", "", nil, "", false
	}
	objText := ""
	if obj != nil {
		objText = strings.TrimSpace(obj.Content(buf))
	}
	lower := strings.ToLower(objText)
	switch {
	case lower == "exports", lower == "module.exports",
		strings.HasSuffix(lower, ".prototype"),
		lower == "app", lower == "req", lower == "res", lower == "proto",
		lower == "router", lower == "route", lower == "server",
		lower == "request", lower == "response", lower == "application",
		lower == "this",
		strings.HasSuffix(lower, ".proto"),
		strings.HasSuffix(lower, ".prototype"),
		strings.HasPrefix(lower, "exports."),
		strings.HasPrefix(lower, "module.exports."):
		// ok
	default:
		return "", "", nil, "", false
	}
	alias = objText + "." + name
	if isFunctionLikeTSNode(right.Type()) {
		return name, alias, right, types.SymbolKindFunction, true
	}
	// Re-exports: exports.Router = Router
	if right.Type() == "identifier" || right.Type() == "member_expression" {
		return name, alias, nil, types.SymbolKindVariable, true
	}
	return "", "", nil, "", false
}

func plausibleJSIdent(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func isFunctionLikeTSNode(nodeType string) bool {
	switch nodeType {
	case "arrow_function", "function_expression", "generator_function":
		return true
	default:
		return false
	}
}

func wrappedFunctionNode(val *sitter.Node, buf []byte) *sitter.Node {
	if val == nil || val.Type() != "call_expression" {
		return nil
	}
	// Many wrappers (memo/forwardRef/defineComponent) contain function args
	// nested under an arguments node instead of direct children.
	if strings.Contains(val.Content(buf), "=>") || strings.Contains(strings.ToLower(val.Content(buf)), "function(") {
		return val
	}
	for i := 0; i < int(val.ChildCount()); i++ {
		c := val.Child(i)
		if c == nil {
			continue
		}
		if isFunctionLikeTSNode(c.Type()) {
			return c
		}
	}
	return nil
}

func isCapacitorPluginRegistration(val *sitter.Node, buf []byte) bool {
	if val == nil || val.Type() != "call_expression" {
		return false
	}
	return strings.Contains(strings.ToLower(val.Content(buf)), "registerplugin(")
}

func looksLikeAnonymousDefaultFunction(s string) bool {
	ls := strings.ToLower(s)
	return strings.Contains(ls, "=>") || strings.Contains(ls, "function(") || strings.Contains(ls, "function (")
}

func looksLikeRouteHandlerName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func parentFromStack(n *sitter.Node) string {
	_ = n
	return ""
}

func parentClassID(n *sitter.Node, buf []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_declaration" {
			return ChildName(p, "name", buf)
		}
	}
	return ""
}

// buildJSInstanceScope maps local instances to class names from new expressions
// and explicit TypeScript annotations.
func buildJSInstanceScope(root *sitter.Node, buf []byte) func(string) string {
	scope := map[string]string{}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "variable_declarator" {
			return
		}
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := strings.TrimSpace(nameNode.Content(buf))
		if i := strings.IndexByte(name, ':'); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name == "" {
			return
		}
		if value := n.ChildByFieldName("value"); value != nil && value.Type() == "new_expression" {
			if ctor := value.ChildByFieldName("constructor"); ctor != nil {
				if typ := calleeName(ctor, buf); typ != "" {
					scope[name] = typ
					return
				}
			}
		}
		Walk(n, func(c *sitter.Node) {
			if _, ok := scope[name]; ok || c.Type() != "type_identifier" {
				return
			}
			if typ := strings.TrimSpace(c.Content(buf)); typ != "" {
				scope[name] = typ
			}
		})
	})
	return func(name string) string { return scope[strings.TrimSpace(name)] }
}

func importSource(n *sitter.Node, buf []byte) string {
	src := n.ChildByFieldName("source")
	if src == nil {
		return ""
	}
	t := src.Content(buf)
	t = strings.Trim(t, `"'`)
	return t
}

// FileNodeID returns stable id for a file vertex.
func FileNodeID(repoID, relPath string) string {
	return fmt.Sprintf("file:%s:%s", repoID, relPath)
}

// extractCalls walks a definition body and emits a `calls` symref edge per
// invocation. It is language-agnostic across the tree-sitter grammars used by
// this package (JS/TS, Go, Python, Rust, Java, C#): different grammars name the
// call and callee nodes differently, so callee resolution probes the common
// field names rather than assuming one grammar.
func extractCalls(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	extractCallsScoped(root, buf, repoID, relPath, fromSym, out, nil)
}

// extractCallsScoped is extractCalls with optional receiver-type inference. When
// typeOf is non-nil (Go), a method call `x.Foo()` whose receiver `x` has a known
// type T is emitted as a type-qualified symref `T.Foo`, letting the resolver pick
// (*T).Foo over an unrelated type's Foo. typeOf nil reproduces the bare-name
// behaviour for every other language.
func extractCallsScoped(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult, typeOf func(string) string) {
	emit := func(name string, confidence float64) {
		if name == "" || !isCallableName(name) {
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: confidence,
		})
	}
	Walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "call_expression", "call", "invocation_expression":
			if typeOf != nil {
				if fn := n.ChildByFieldName("function"); fn != nil {
					var recv, member *sitter.Node
					switch fn.Type() {
					case "selector_expression":
						recv, member = fn.ChildByFieldName("operand"), fn.ChildByFieldName("field")
					case "member_expression":
						recv, member = fn.ChildByFieldName("object"), fn.ChildByFieldName("property")
					}
					if recv != nil && recv.Type() == "identifier" && member != nil {
						if typ := typeOf(recv.Content(buf)); typ != "" {
							emit(typ+"."+member.Content(buf), 0.9)
							return
						}
					}
				}
			}
			// JS/TS/Go/Rust/Python/C#: callee is the "function" field.
			if fn := n.ChildByFieldName("function"); fn != nil {
				if nm := calleeName(fn, buf); nm != "" {
					emit(nm, 0.5)
				}
				// Express/CJS member calls: also emit app.use so aliases resolve.
				if fn.Type() == "member_expression" {
					if alias := expressMemberAlias(fn, buf); alias != "" {
						emit(alias, 0.5)
					}
				}
				if calleeName(fn, buf) != "" || (fn.Type() == "member_expression" && expressMemberAlias(fn, buf) != "") {
					return
				}
			}
			// Kotlin: no function field — simple_identifier or trailing name on
			// navigation_expression before call_suffix.
			if nm := kotlinCallCallee(n, buf); nm != "" {
				emit(nm, 0.5)
				return
			}
			// Ruby / Elixir: call nodes have no "function" field — method is the
			// leading identifier/constant (route(path), Foo.bar → bar).
			emit(rubyCallCallee(n, buf), 0.5)
			return
		case "method_invocation":
			// Java: the method name is its own field.
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(buf), 0.5)
			} else {
				emit(calleeName(n.ChildByFieldName("function"), buf), 0.5)
			}
		case "function_call_expression":
			// PHP: foo() / \Ns\foo() — callee is the "function" field.
			emit(calleeName(n.ChildByFieldName("function"), buf), 0.5)
		case "member_call_expression", "nullsafe_member_call_expression", "scoped_call_expression":
			// PHP: $obj->m() / $obj?->m() / Class::m() — method name is its own field.
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(buf), 0.5)
			}
		case "object_creation_expression", "new_expression":
			// Constructor calls (Java/C#/JS): record the type being constructed.
			emit(calleeName(firstNonNull(n.ChildByFieldName("type"), n.ChildByFieldName("constructor")), buf), 0.5)
		}
	})
}

// calleeName resolves the trailing simple name of a callee node, handling the
// member/selector/attribute/scoped forms used across grammars.
func calleeName(fn *sitter.Node, buf []byte) string {
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier", "field_identifier", "type_identifier",
		"property_identifier", "shorthand_property_identifier",
		"simple_identifier", // Kotlin
		"name":              // PHP simple callee name (function_call_expression.function)
		return fn.Content(buf)
	case "qualified_name":
		// PHP \Ns\sub\func -> trailing simple name.
		for i := int(fn.NamedChildCount()) - 1; i >= 0; i-- {
			if c := fn.NamedChild(i); c != nil && c.Type() == "name" {
				return c.Content(buf)
			}
		}
		return fn.Content(buf)
	}
	// member_expression(JS).property, selector_expression(Go).field,
	// attribute(Python).attribute, scoped_identifier(Rust/Java).name,
	// field_expression(Rust).field, member_access_expression(C#).name.
	for _, field := range []string{"property", "field", "attribute", "name"} {
		if c := fn.ChildByFieldName(field); c != nil {
			if nm := calleeName(c, buf); nm != "" {
				return nm
			}
		}
	}
	// Fallback: last identifier-like named child.
	for i := int(fn.NamedChildCount()) - 1; i >= 0; i-- {
		c := fn.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "identifier", "field_identifier", "type_identifier", "property_identifier", "simple_identifier":
			return c.Content(buf)
		}
	}
	return ""
}

func firstNonNull(nodes ...*sitter.Node) *sitter.Node {
	for _, n := range nodes {
		if n != nil {
			return n
		}
	}
	return nil
}

// rubyCallCallee resolves the method name on a tree-sitter-ruby `call` node.
// Bare calls (`route(path)`) use a leading identifier; receiver calls
// (`Foo.bar` / `obj.bar`) use the trailing identifier after `.`.
func rubyCallCallee(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	var firstIdent, lastIdent string
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "identifier", "simple_identifier":
			name := strings.TrimSpace(c.Content(buf))
			if firstIdent == "" {
				firstIdent = name
			}
			lastIdent = name
		case "argument_list", "call_suffix", "arguments":
			// Stop before arguments so we don't pick arg identifiers.
			if lastIdent != "" {
				return lastIdent
			}
			return firstIdent
		}
	}
	if lastIdent != "" && lastIdent != firstIdent {
		return lastIdent
	}
	return firstIdent
}

// kotlinCallCallee resolves the callee on a tree-sitter-kotlin call_expression,
// which has no "function" field: bare calls use a leading simple_identifier;
// member calls use the trailing simple_identifier on navigation_expression.
func kotlinCallCallee(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "simple_identifier":
			return strings.TrimSpace(c.Content(buf))
		case "navigation_expression":
			var last string
			var walkNav func(*sitter.Node)
			walkNav = func(x *sitter.Node) {
				if x == nil {
					return
				}
				if x.Type() == "simple_identifier" {
					last = strings.TrimSpace(x.Content(buf))
				}
				for j := 0; j < int(x.ChildCount()); j++ {
					walkNav(x.Child(j))
				}
			}
			walkNav(c)
			return last
		case "call_suffix":
			return ""
		}
	}
	return ""
}

// isCallableName filters out names that are never user-defined symbols worth a
// graph edge (language builtins and the like keep symref noise out of the
// resolver without affecting precision).
func isCallableName(name string) bool {
	if name == "" {
		return false
	}
	return !builtinCallNames[name]
}

// builtinCallNames are ubiquitous language builtins/keywords that would only add
// unresolvable symref noise. Kept intentionally small and high-confidence.
var builtinCallNames = map[string]bool{
	// Go builtins
	"len": true, "cap": true, "make": true, "new": true, "append": true,
	"copy": true, "delete": true, "panic": true, "recover": true, "print": true,
	"println": true, "close": true, "complex": true, "real": true, "imag": true,
	// JS/TS ubiquitous
	"require": true, "parseInt": true, "parseFloat": true,
	// Ruby loaders (imports edges cover these; calls would be noise)
	"require_relative": true, "load": true,
	// Python builtins
	"super": true, "isinstance": true,
}
