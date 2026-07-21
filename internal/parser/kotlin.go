package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	kt "github.com/smacker/go-tree-sitter/kotlin"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseKotlin extracts declarations and call edges.
//
// The tree-sitter Kotlin grammar used here does not expose a "name" field on
// function_declaration / class_declaration — names live as sibling
// simple_identifier / type_identifier nodes (extension receivers appear as
// user_type "." simple_identifier before the parameter list).
func ParseKotlin(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(kt.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "function_declaration":
			name := kotlinDeclName(n, buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "kotlin", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
		case "class_declaration", "object_declaration":
			name := kotlinDeclName(n, buf)
			if name == "" {
				return
			}
			kind := types.SymbolKindClass
			if kotlinIsInterface(n) {
				kind = types.SymbolKindInterface
			}
			sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "kotlin", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
	})
	return out, nil
}

// kotlinDeclName reads the declaration name from siblings when the grammar has
// no name field. For extension functions (`fun Route.route(...)`) the last
// simple_identifier before function_value_parameters is the function name.
func kotlinDeclName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if s := ChildName(n, "name", buf); s != "" {
		return s
	}
	switch n.Type() {
	case "function_declaration":
		var lastSimple string
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if c.Type() == "simple_identifier" {
				lastSimple = c.Content(buf)
			}
			if c.Type() == "function_value_parameters" {
				break
			}
		}
		return lastSimple
	case "class_declaration", "object_declaration":
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "type_identifier" {
				return c.Content(buf)
			}
		}
	}
	return ""
}

func kotlinIsInterface(n *sitter.Node) bool {
	if n == nil {
		return false
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && !c.IsNamed() && c.Type() == "interface" {
			return true
		}
	}
	return false
}
