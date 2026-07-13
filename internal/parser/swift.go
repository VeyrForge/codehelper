package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	swift "github.com/smacker/go-tree-sitter/swift"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseSwift extracts types and functions.
func ParseSwift(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(swift.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "function_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n.ChildByFieldName("name"), buf)
			}
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "swift", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "class_declaration", "struct_declaration", "enum_declaration", "protocol_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n.ChildByFieldName("name"), buf)
			}
			if name == "" {
				return
			}
			k := types.SymbolKindClass
			if n.Type() == "protocol_declaration" {
				k = types.SymbolKindInterface
			}
			if n.Type() == "enum_declaration" {
				k = types.SymbolKindEnum
			}
			sym := symbol(repoID, relPath, name, k, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "swift", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
	})
	return out, nil
}
