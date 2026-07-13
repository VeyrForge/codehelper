package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	cs "github.com/smacker/go-tree-sitter/csharp"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseCSharp extracts types and methods.
func ParseCSharp(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(cs.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "class_declaration", "interface_declaration", "struct_declaration", "record_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			k := types.SymbolKindClass
			if n.Type() == "interface_declaration" {
				k = types.SymbolKindInterface
			}
			sym := symbol(repoID, relPath, name, k, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "csharp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "method_declaration", "constructor_declaration":
			name := ChildName(n, "name", buf)
			if name == "" && n.Type() == "constructor_declaration" {
				name = "ctor"
			}
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "csharp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
		}
	})
	return out, nil
}
