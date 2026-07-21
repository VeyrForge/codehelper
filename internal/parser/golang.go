package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	golang "github.com/smacker/go-tree-sitter/golang"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// goImportPath returns the import path from a Go import_spec node, stripping the
// surrounding quotes from the interpreted string literal.
func goImportPath(n *sitter.Node, buf []byte) string {
	pathNode := n.ChildByFieldName("path")
	if pathNode == nil {
		// import_spec may be just the string literal child.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && strings.Contains(c.Type(), "string") {
				pathNode = c
				break
			}
		}
	}
	if pathNode == nil {
		return ""
	}
	return strings.Trim(pathNode.Content(buf), "\"`")
}

// ParseGo extracts package-level funcs and methods from Go source.
func ParseGo(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(golang.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	// Pre-pass: map each same-file function to its result types so `x := NewT()`
	// can bind x to T. This recovers the common constructor pattern that would
	// otherwise leave x.Method() calls as bare, ambiguous method names.
	returns := collectGoReturns(tree.RootNode(), buf)
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "import_spec":
			path := goImportPath(n, buf)
			if path == "" {
				return
			}
			out.Imports = append(out.Imports, path)
			modID := moduleNodeID(repoID, path)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, fid, modID, "imports"),
				RepoID:     repoID,
				Kind:       types.RefKindImports,
				SourceID:   fid,
				TargetID:   modID,
				Confidence: 0.95,
			})
		case "function_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "go", goDocComment(n, buf), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, goScopeTypeOf(buildGoScope(n, buf, returns)))
		case "method_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			// Record the receiver type in ParentID so cross-file method-call
			// resolution can disambiguate by receiver type (e.g. (*Store).Get
			// vs (*Cache).Get) instead of by bare method name.
			recvType := goReceiverType(n, buf)
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "go", goDocComment(n, buf), recvType)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, goScopeTypeOf(buildGoScope(n, buf, returns)))
		case "type_declaration":
			for i := 0; i < int(n.ChildCount()); i++ {
				spec := n.Child(i)
				if spec == nil || spec.Type() != "type_spec" {
					continue
				}
				name := ChildName(spec, "name", buf)
				if name == "" {
					continue
				}
				// Record embedded types so the resolver can resolve calls to
				// promoted (embedded) methods, e.g. d.Foo() where Derived embeds
				// Base and Foo is defined on Base.
				sig := goDocComment(n, buf)
				if embedded := goEmbeddedTypes(spec, buf); len(embedded) > 0 {
					if sig != "" {
						sig += " "
					}
					sig += "embeds=" + strings.Join(embedded, ",")
				}
				kind := types.SymbolKindClass
				if t := spec.ChildByFieldName("type"); t != nil && t.Type() == "interface_type" {
					kind = types.SymbolKindInterface
				}
				sym := symbol(repoID, relPath, name, kind, int(spec.StartPoint().Row)+1, int(spec.EndPoint().Row)+1, "go", sig, "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
		}
	})
	return out, nil
}

// goDocComment returns the contiguous //-comment block immediately preceding a
// declaration (its Go doc comment), cleaned and truncated. Stored in Signature so
// natural-language queries match a symbol by what its doc SAYS, not just its
// identifier — e.g. "reciprocal rank fusion" finding `func RRF` whose name is an
// acronym. Truncated to keep the index light; a blank line ends the block.
func goDocComment(decl *sitter.Node, buf []byte) string {
	const maxDoc = 160
	var lines []string
	below := int(decl.StartPoint().Row)
	for c := decl.PrevNamedSibling(); c != nil && c.Type() == "comment"; c = c.PrevNamedSibling() {
		if below-int(c.EndPoint().Row) > 1 {
			break // blank line gap → not part of this doc block
		}
		t := c.Content(buf)
		t = strings.TrimPrefix(t, "//")
		t = strings.TrimPrefix(t, "/*")
		t = strings.TrimSuffix(t, "*/")
		if t = strings.TrimSpace(t); t != "" {
			lines = append([]string{t}, lines...) // walking upward, so prepend
		}
		below = int(c.StartPoint().Row)
	}
	doc := strings.Join(lines, " ")
	if len(doc) > maxDoc {
		doc = doc[:maxDoc]
	}
	return doc
}

// goEmbeddedTypes returns the bare names of types embedded in a struct type_spec
// (anonymous fields), whose methods are promoted onto the embedding type.
func goEmbeddedTypes(spec *sitter.Node, buf []byte) []string {
	st := spec.ChildByFieldName("type")
	if st == nil || st.Type() != "struct_type" {
		return nil
	}
	var fields *sitter.Node
	for i := 0; i < int(st.NamedChildCount()); i++ {
		if c := st.NamedChild(i); c != nil && c.Type() == "field_declaration_list" {
			fields = c
			break
		}
	}
	if fields == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(fields.NamedChildCount()); i++ {
		fd := fields.NamedChild(i)
		if fd == nil || fd.Type() != "field_declaration" {
			continue
		}
		// A named field has a `name` field_identifier; an embedded field does not.
		if fd.ChildByFieldName("name") != nil {
			continue
		}
		if t := goTypeName(fd.ChildByFieldName("type"), buf); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// goReceiverType returns the bare receiver type name of a method declaration,
// stripping pointers and package qualifiers (e.g. `func (s *Store) M()` → "Store").
func goReceiverType(method *sitter.Node, buf []byte) string {
	recv := method.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	for i := 0; i < int(recv.NamedChildCount()); i++ {
		pd := recv.NamedChild(i)
		if pd != nil && pd.Type() == "parameter_declaration" {
			return goTypeName(pd.ChildByFieldName("type"), buf)
		}
	}
	return ""
}

// buildGoScope maps in-scope variable names to their (bare) type name within a
// function or method declaration: the receiver, parameters, and local `:=` / `var`
// declarations whose type is statically obvious. This is single-file, best-effort
// type inference — the precision gate in the resolver still rejects wrong matches,
// so partial coverage only ever helps.
func buildGoScope(decl *sitter.Node, buf []byte, returns map[string][]string) map[string]string {
	scope := map[string]string{}
	addParams := func(list *sitter.Node) {
		if list == nil {
			return
		}
		for i := 0; i < int(list.NamedChildCount()); i++ {
			pd := list.NamedChild(i)
			if pd == nil || pd.Type() != "parameter_declaration" {
				continue
			}
			typ := goTypeName(pd.ChildByFieldName("type"), buf)
			if typ == "" {
				continue
			}
			for j := 0; j < int(pd.NamedChildCount()); j++ {
				if id := pd.NamedChild(j); id != nil && id.Type() == "identifier" {
					scope[id.Content(buf)] = typ
				}
			}
		}
	}
	addParams(decl.ChildByFieldName("receiver"))
	addParams(decl.ChildByFieldName("parameters"))

	Walk(decl, func(n *sitter.Node) {
		switch n.Type() {
		case "short_var_declaration":
			bindGoNames(n.ChildByFieldName("left"), n.ChildByFieldName("right"), buf, scope, returns)
		case "var_spec":
			if typ := goTypeName(n.ChildByFieldName("type"), buf); typ != "" {
				for j := 0; j < int(n.NamedChildCount()); j++ {
					if id := n.NamedChild(j); id != nil && id.Type() == "identifier" {
						scope[id.Content(buf)] = typ
					}
				}
				return
			}
			bindGoNames(n.ChildByFieldName("name"), n.ChildByFieldName("value"), buf, scope, returns)
		}
	})
	return scope
}

// bindGoNames positionally binds an identifier list to the inferred types of an
// expression list (the LHS/RHS of `x, y := a, b`). The single multi-value form
// `x, y := f()` — one call on the right feeding several names on the left — is
// bound from the callee's result types so constructors like `s, err := New()`
// still type `s`.
func bindGoNames(left, right *sitter.Node, buf []byte, scope map[string]string, returns map[string][]string) {
	if left == nil || right == nil {
		return
	}
	names := namedChildrenOfType(left, "identifier")
	if len(names) > 1 && right.NamedChildCount() == 1 {
		if rt := goCallResultTypes(right.NamedChild(0), buf, returns); len(rt) == len(names) {
			for i, id := range names {
				if rt[i] != "" {
					scope[id.Content(buf)] = rt[i]
				}
			}
			return
		}
	}
	for i, id := range names {
		if i >= int(right.NamedChildCount()) {
			break
		}
		if typ := goExprType(right.NamedChild(i), buf, returns); typ != "" {
			scope[id.Content(buf)] = typ
		}
	}
}

func namedChildrenOfType(n *sitter.Node, typ string) []*sitter.Node {
	if n == nil {
		return nil
	}
	var out []*sitter.Node
	// An expression_list wraps the identifiers; a bare identifier is itself the list.
	if n.Type() == typ {
		return []*sitter.Node{n}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c != nil && c.Type() == typ {
			out = append(out, c)
		}
	}
	return out
}

// goTypeName extracts the bare type identifier from a type node, drilling through
// pointers and generics and rejecting package-qualified types (whose methods are
// not local symbols).
func goTypeName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "type_identifier":
		return n.Content(buf)
	case "pointer_type":
		return goTypeName(n.NamedChild(0), buf)
	case "generic_type":
		return goTypeName(n.ChildByFieldName("type"), buf)
	}
	return ""
}

// goExprType infers the bare type produced by an expression on the RHS of an
// assignment: the statically obvious constructor forms `T{}`, `&T{}`, `new(T)`,
// and a call to a same-file function with a single result type (`x := NewT()`).
func goExprType(n *sitter.Node, buf []byte, returns map[string][]string) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "composite_literal":
		return goTypeName(n.ChildByFieldName("type"), buf)
	case "unary_expression":
		return goExprType(n.ChildByFieldName("operand"), buf, returns)
	case "call_expression":
		fn := n.ChildByFieldName("function")
		if fn != nil && fn.Content(buf) == "new" {
			if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
				return goTypeName(args.NamedChild(0), buf)
			}
		}
		if rt := goCallResultTypes(n, buf, returns); len(rt) == 1 && rt[0] != "" {
			return rt[0]
		}
	}
	return ""
}

// collectGoReturns maps each top-level function in the file to the bare type
// names of its results, so an assignment from a call (`x := NewStore()`) can be
// typed. Only same-file functions are indexed — cross-package return types need
// type information the heuristic resolver deliberately does without.
func collectGoReturns(root *sitter.Node, buf []byte) map[string][]string {
	m := map[string][]string{}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "function_declaration" {
			return
		}
		name := ChildName(n, "name", buf)
		if name == "" {
			return
		}
		if rt := goResultTypes(n.ChildByFieldName("result"), buf); len(rt) > 0 {
			m[name] = rt
		}
	})
	return m
}

// goResultTypes returns the bare type name of each result of a function, in
// order. A multi-name group `(x, y Cache)` expands to one entry per name; an
// unnameable result (map, func, package-qualified, …) yields "".
func goResultTypes(result *sitter.Node, buf []byte) []string {
	if result == nil {
		return nil
	}
	if result.Type() != "parameter_list" {
		return []string{goTypeName(result, buf)}
	}
	var out []string
	for i := 0; i < int(result.NamedChildCount()); i++ {
		pd := result.NamedChild(i)
		if pd == nil || pd.Type() != "parameter_declaration" {
			continue
		}
		t := goTypeName(pd.ChildByFieldName("type"), buf)
		names := 0
		for j := 0; j < int(pd.NamedChildCount()); j++ {
			if c := pd.NamedChild(j); c != nil && c.Type() == "identifier" {
				names++
			}
		}
		if names == 0 {
			names = 1
		}
		for k := 0; k < names; k++ {
			out = append(out, t)
		}
	}
	return out
}

// goCallResultTypes returns the result types of a call to a same-file function,
// or nil for calls to methods/package functions (whose return type is unknown
// without cross-file type info).
func goCallResultTypes(n *sitter.Node, buf []byte, returns map[string][]string) []string {
	if n == nil || n.Type() != "call_expression" || returns == nil {
		return nil
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Type() != "identifier" {
		return nil
	}
	return returns[fn.Content(buf)]
}

// goScopeTypeOf adapts a name→type scope map into the typeOf lookup that
// extractCallsScoped uses to qualify method calls with their receiver type.
func goScopeTypeOf(scope map[string]string) func(string) string {
	if len(scope) == 0 {
		return nil
	}
	return func(name string) string { return scope[name] }
}
