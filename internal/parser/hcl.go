package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	hcl "github.com/smacker/go-tree-sitter/hcl"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseHCL extracts blocks as coarse symbols.
func ParseHCL(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(hcl.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "block" {
			return
		}
		lbl := n.ChildByFieldName("type")
		if lbl == nil {
			return
		}
		bt := lbl.Content(buf)
		args := n.ChildByFieldName("labels")
		name := bt
		if args != nil && args.ChildCount() > 0 {
			name = bt + ":" + args.Child(0).Content(buf)
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "hcl", "", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	})
	return out, nil
}
