package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	pb "github.com/smacker/go-tree-sitter/protobuf"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseProtobuf extracts messages, enums, and services.
func ParseProtobuf(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(pb.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		var name string
		var kind types.SymbolKind
		switch n.Type() {
		case "message_name":
			name = strings.TrimSpace(n.Content(buf))
			kind = types.SymbolKindClass
		case "enum_name":
			name = strings.TrimSpace(n.Content(buf))
			kind = types.SymbolKindEnum
		case "service_name":
			name = strings.TrimSpace(n.Content(buf))
			kind = types.SymbolKindInterface
		default:
			return
		}
		if name == "" {
			return
		}
		par := n.Parent()
		if par == nil {
			return
		}
		ls := int(par.StartPoint().Row) + 1
		le := int(par.EndPoint().Row) + 1
		sym := symbol(repoID, relPath, name, kind, ls, le, "protobuf", "", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	})
	return out, nil
}
