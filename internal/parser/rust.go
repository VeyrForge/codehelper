package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rs "github.com/smacker/go-tree-sitter/rust"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseRust extracts fn/impl/struct/enum/trait items. Each symbol's Signature is
// filled with its leading /// doc comment plus a compact parameter/return sketch,
// so natural-language and cross-lingual queries match what a symbol DOES — not
// just its bare identifier (which is all an embedding model would otherwise see).
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
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "rust", rustSignature(n, buf), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
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
			}
		}
	})
	return out, nil
}

func rustImplType(n *sitter.Node, buf []byte) string {
	t := n.ChildByFieldName("type")
	if t != nil {
		return t.Content(buf)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "type_identifier" {
			return c.Content(buf)
		}
	}
	return ""
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
