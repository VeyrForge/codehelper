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
	Walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "import_statement":
			if src := importSource(n, buf); src != "" {
				out.Imports = append(out.Imports, src)
				modID := moduleNodeID(repoID, src)
				fid := FileNodeID(repoID, relPath)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, modID, "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   modID,
					Confidence: 0.9,
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
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "method_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, "", parentClassID(n))
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "class_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
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
			extractCalls(body, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, body, buf, out)
		case "export_statement":
			// Frameworks often use anonymous default exports for entrypoints.
			exportBody := strings.TrimSpace(n.Content(buf))
			if strings.HasPrefix(exportBody, "export default") && looksLikeAnonymousDefaultFunction(exportBody) {
				sym := symbol(repoID, relPath, "default_export", types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(frameworks, "entrypoint"), "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
		}
	})
	return out, nil
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

func parentClassID(n *sitter.Node) string {
	p := n.Parent()
	for p != nil {
		if p.Type() == "class_declaration" {
			return ""
		}
		p = p.Parent()
	}
	return ""
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
	emit := func(name string) {
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
			Confidence: 0.5,
		})
	}
	Walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "call_expression", "call", "invocation_expression":
			if typeOf != nil {
				if fn := n.ChildByFieldName("function"); fn != nil && fn.Type() == "selector_expression" {
					op := fn.ChildByFieldName("operand")
					fld := fn.ChildByFieldName("field")
					if op != nil && op.Type() == "identifier" && fld != nil {
						if t := typeOf(op.Content(buf)); t != "" {
							emit(t + "." + fld.Content(buf))
							return
						}
					}
				}
			}
			// JS/TS/Go/Rust/Python/C#: callee is the "function" field.
			emit(calleeName(n.ChildByFieldName("function"), buf))
		case "method_invocation":
			// Java: the method name is its own field.
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(buf))
			} else {
				emit(calleeName(n.ChildByFieldName("function"), buf))
			}
		case "function_call_expression":
			// PHP: foo() / \Ns\foo() — callee is the "function" field.
			emit(calleeName(n.ChildByFieldName("function"), buf))
		case "member_call_expression", "nullsafe_member_call_expression", "scoped_call_expression":
			// PHP: $obj->m() / $obj?->m() / Class::m() — method name is its own field.
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(buf))
			}
		case "object_creation_expression", "new_expression":
			// Constructor calls (Java/C#/JS): record the type being constructed.
			emit(calleeName(firstNonNull(n.ChildByFieldName("type"), n.ChildByFieldName("constructor")), buf))
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
		"name": // PHP simple callee name (function_call_expression.function)
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
		case "identifier", "field_identifier", "type_identifier", "property_identifier":
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
	// Python builtins
	"super": true, "isinstance": true,
}
