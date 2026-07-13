package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	scala "github.com/smacker/go-tree-sitter/scala"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseScala extracts defs and classes.
func ParseScala(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(scala.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "function_definition", "function_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n, buf)
			}
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "scala", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "class_definition", "object_definition", "trait_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n, buf)
			}
			if name == "" {
				return
			}
			k := types.SymbolKindClass
			if n.Type() == "trait_definition" {
				k = types.SymbolKindInterface
			}
			sym := symbol(repoID, relPath, name, k, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "scala", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
	})
	return out, nil
}
