package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	lua "github.com/smacker/go-tree-sitter/lua"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseLua extracts function declarations.
func ParseLua(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(lua.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "function_declaration" && n.Type() != "function_definition" {
			return
		}
		name := ChildName(n, "name", buf)
		if name == "" {
			name = FirstIdentifier(n.ChildByFieldName("name"), buf)
		}
		if name == "" {
			return
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "lua", "", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	})
	return out, nil
}
