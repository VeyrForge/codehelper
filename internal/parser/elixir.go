package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	ex "github.com/smacker/go-tree-sitter/elixir"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseElixir extracts modules and defs.
func ParseElixir(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(ex.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "call":
			target := n.ChildByFieldName("target")
			if target != nil && target.Type() == "identifier" && target.Content(buf) == "defmodule" {
				args := n.ChildByFieldName("arguments")
				if args != nil {
					name := FirstIdentifier(args, buf)
					if name != "" {
						sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "elixir", "", "")
						out.Symbols = append(out.Symbols, sym)
						out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					}
				}
			}
			if target != nil && target.Type() == "identifier" {
				id := target.Content(buf)
				if id == "def" || id == "defp" {
					args := n.ChildByFieldName("arguments")
					fn := FirstIdentifier(args, buf)
					if fn != "" {
						sym := symbol(repoID, relPath, fn, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "elixir", "", "")
						out.Symbols = append(out.Symbols, sym)
						out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					}
				}
			}
		}
	})
	return out, nil
}
