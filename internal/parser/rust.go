package parser

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rs "github.com/smacker/go-tree-sitter/rust"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseRust extracts fn/impl/struct/enum/trait items. Each symbol's Signature is
// filled with its leading /// doc comment plus a compact parameter/return sketch,
// so natural-language and cross-lingual queries match what a symbol DOES — not
// just its bare identifier (which is all an embedding model would otherwise see).
//
// Also emits:
//   - reads edges for type_identifier uses (so Router / public structs get inbound
//     type-use edges for impact/context);
//   - implements edges for `impl Trait for Type`;
//   - Axum-style route/handler registration call edges (.route / get(handler)).
func ParseRust(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(rs.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "function_item":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			kind := types.SymbolKindFunction
			parent := ""
			if implType := rustEnclosingImplType(n, buf); implType != "" {
				kind = types.SymbolKindMethod
				parent = implType // receiver type name (matches Go ParentID convention)
			}
			sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "rust", rustSignature(n, buf), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addRustTypeUseEdges(n, buf, repoID, relPath, sym.ID, out)
			extractAxumRouteHandlers(n, buf, repoID, relPath, sym.ID, out)
		case "struct_item", "enum_item", "trait_item", "type_item", "union_item":
			name := rustTypeName(n, buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "rust", rustDoc(n, buf), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "impl_item":
			t := rustImplType(n, buf)
			if t != "" {
				sym := symbol(repoID, relPath, "impl_"+t, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "rust", rustDoc(n, buf), "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				if trait := rustImplTrait(n, buf); trait != "" {
					emitRustImplements(repoID, relPath, t, trait, out)
				}
			}
		}
	})
	return out, nil
}

func rustImplType(n *sitter.Node, buf []byte) string {
	t := n.ChildByFieldName("type")
	if t != nil {
		return rustSimpleTypeName(t, buf)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "type_identifier" {
			return c.Content(buf)
		}
	}
	return ""
}

// rustEnclosingImplType returns the impl's type name when n is a method inside
// an impl_item, else "".
func rustEnclosingImplType(n *sitter.Node, buf []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "impl_item" {
			return rustImplType(p, buf)
		}
	}
	return ""
}

// rustImplTrait returns the trait name for `impl Trait for Type`, or "".
func rustImplTrait(n *sitter.Node, buf []byte) string {
	if tr := n.ChildByFieldName("trait"); tr != nil {
		return rustSimpleTypeName(tr, buf)
	}
	var beforeFor string
	sawFor := false
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "for" || c.Content(buf) == "for" {
			sawFor = true
			break
		}
		if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" || c.Type() == "generic_type" {
			beforeFor = rustSimpleTypeName(c, buf)
		}
	}
	if sawFor {
		return beforeFor
	}
	return ""
}

func rustSimpleTypeName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "type_identifier":
		return n.Content(buf)
	case "scoped_type_identifier":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Content(buf)
		}
	case "generic_type":
		if t := n.ChildByFieldName("type"); t != nil {
			return rustSimpleTypeName(t, buf)
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Type() == "type_identifier" {
			return c.Content(buf)
		}
	}
	return ""
}

func emitRustImplements(repoID, relPath, typeName, traitName string, out *ParseResult) {
	typeName = strings.TrimSpace(typeName)
	traitName = strings.TrimSpace(traitName)
	if typeName == "" || traitName == "" || typeName == traitName {
		return
	}
	src := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, typeName)
	tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, traitName)
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, src, tgt, "implements"),
		RepoID:     repoID,
		Kind:       types.RefKindImplements,
		SourceID:   src,
		TargetID:   tgt,
		Confidence: 0.85,
	})
}

// addRustTypeUseEdges emits reads edges from fromSym to every type_identifier
// referenced in n (params, return type, body) — inbound type-use for impact.
func addRustTypeUseEdges(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || fromSym == "" {
		return
	}
	fromName := fromSym
	if idx := strings.LastIndex(fromSym, ":"); idx >= 0 && idx < len(fromSym)-1 {
		fromName = fromSym[idx+1:]
	}
	seen := map[string]struct{}{}
	Walk(root, func(n *sitter.Node) {
		tok := ""
		switch n.Type() {
		case "type_identifier":
			tok = strings.TrimSpace(n.Content(buf))
		case "scoped_identifier", "scoped_type_identifier":
			// Router::new / axum::Router — type-use the leading path segment when capitalized.
			if path := n.ChildByFieldName("path"); path != nil {
				tok = rustSimpleTypeName(path, buf)
				if tok == "" {
					tok = strings.TrimSpace(path.Content(buf))
				}
			}
			if tok == "" {
				return
			}
			if tok[0] < 'A' || tok[0] > 'Z' {
				return
			}
		default:
			return
		}
		if tok == "" || tok == fromName {
			return
		}
		if p := n.Parent(); p != nil && n.Type() == "type_identifier" {
			switch p.Type() {
			case "struct_item", "enum_item", "trait_item", "type_item", "union_item", "function_item":
				if p.ChildByFieldName("name") == n {
					return
				}
			}
		}
		key := strings.ToLower(tok)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.7,
		})
	})
}

// Axum / tower-http style route registration helpers whose identifier args
// are typically handler functions.
var axumRouteCallees = map[string]bool{
	"route": true, "route_service": true, "nest": true, "nest_service": true,
	"fallback": true, "fallback_service": true, "method_routing": true,
	"get": true, "post": true, "put": true, "delete": true, "patch": true,
	"head": true, "options": true, "trace": true, "any": true, "connect": true,
	"on": true, "on_service": true,
}

// extractAxumRouteHandlers finds get(handler) / .route(path, get(handler)) style
// registrations and emits calls edges to the handler identifiers.
func extractAxumRouteHandlers(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || fromSym == "" {
		return
	}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		name := calleeName(n.ChildByFieldName("function"), buf)
		if !axumRouteCallees[name] {
			return
		}
		args := n.ChildByFieldName("arguments")
		if args == nil {
			return
		}
		emitHandlerArgs(args, buf, repoID, relPath, fromSym, out)
	})
}

func emitHandlerArgs(args *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	Walk(args, func(n *sitter.Node) {
		if n.Type() != "identifier" {
			return
		}
		name := strings.TrimSpace(n.Content(buf))
		if name == "" || !isCallableName(name) || axumRouteCallees[name] {
			return
		}
		if p := n.Parent(); p != nil && p.Type() == "field_expression" {
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		for _, e := range out.Edges {
			if e.Kind == types.RefKindCalls && e.SourceID == fromSym && e.TargetID == tgt {
				return
			}
		}
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.85,
		})
	})
}

// rustTypeName returns the declared name of a struct/enum/trait/type/union item.
func rustTypeName(n *sitter.Node, buf []byte) string {
	if name := ChildName(n, "name", buf); name != "" {
		return name
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "type_identifier" {
			return c.Content(buf)
		}
	}
	return ""
}

// rustSignature combines the leading doc comment (natural-language meaning) with a
// compact parameter + return-type sketch. The doc comes first so an embedding
// model sees prose ("schedules experts and prefetches their weights") before the
// type identifiers, which is what makes a cross-lingual query bridge to the symbol.
func rustSignature(n *sitter.Node, buf []byte) string {
	var parts []string
	if doc := rustDoc(n, buf); doc != "" {
		parts = append(parts, doc)
	}
	if params := n.ChildByFieldName("parameters"); params != nil {
		if c := strings.TrimSpace(params.Content(buf)); c != "" && c != "()" {
			parts = append(parts, c)
		}
	}
	if ret := n.ChildByFieldName("return_type"); ret != nil {
		if c := strings.TrimSpace(ret.Content(buf)); c != "" {
			parts = append(parts, "-> "+c)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// rustDoc returns the contiguous ///, //!, or /** */ doc-comment block immediately
// preceding a declaration, cleaned and truncated. Attribute lines (#[inline], etc.)
// between the doc and the item are skipped, matching how Rustdoc associates docs.
func rustDoc(decl *sitter.Node, buf []byte) string {
	const maxDoc = 200
	var lines []string
	below := int(decl.StartPoint().Row)
	for c := decl.PrevNamedSibling(); c != nil; c = c.PrevNamedSibling() {
		typ := c.Type()
		// Attributes sit between a doc comment and its item; skip them without
		// breaking the block, but don't let a blank gap above them leak in.
		if typ == "attribute_item" {
			below = int(c.StartPoint().Row)
			continue
		}
		if typ != "line_comment" && typ != "block_comment" {
			break
		}
		if below-int(c.EndPoint().Row) > 1 {
			break // blank line gap → not part of this doc block
		}
		lines = append([]string{rustCommentText(c, buf)}, lines...) // walking upward, so prepend
		below = int(c.StartPoint().Row)
	}
	doc := strings.TrimSpace(strings.Join(lines, " "))
	if len(doc) > maxDoc {
		doc = doc[:maxDoc]
	}
	return doc
}

// rustCommentText strips the comment markers from a line/block comment, preferring
// the inner doc_comment node (the text after /// or //!) when tree-sitter exposes it.
func rustCommentText(c *sitter.Node, buf []byte) string {
	for i := 0; i < int(c.NamedChildCount()); i++ {
		if ch := c.NamedChild(i); ch != nil && ch.Type() == "doc_comment" {
			return strings.TrimSpace(ch.Content(buf))
		}
	}
	t := c.Content(buf)
	t = strings.TrimPrefix(t, "///")
	t = strings.TrimPrefix(t, "//!")
	t = strings.TrimPrefix(t, "//")
	t = strings.TrimPrefix(t, "/**")
	t = strings.TrimPrefix(t, "/*")
	t = strings.TrimSuffix(t, "*/")
	t = strings.ReplaceAll(t, "\n", " ")
	return strings.TrimSpace(strings.Trim(t, "* "))
}
