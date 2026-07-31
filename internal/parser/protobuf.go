package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	pb "github.com/smacker/go-tree-sitter/protobuf"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseProtobuf extracts messages, enums, services, RPCs, and import paths.
// RPCs are methods with ParentID = enclosing service name. Import edges target
// the .proto path string (moduleNodeID). Confidence: High for symbols; Medium
// for import path resolution (no path rewrite / well-known mapping).
func ParseProtobuf(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(pb.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "message_name", "enum_name", "service_name", "rpc_name":
			name := strings.TrimSpace(n.Content(buf))
			if name == "" {
				return
			}
			var kind types.SymbolKind
			parent := ""
			sig := ""
			par := n.Parent()
			switch n.Type() {
			case "message_name":
				kind = types.SymbolKindClass
			case "enum_name":
				kind = types.SymbolKindEnum
			case "service_name":
				kind = types.SymbolKindInterface
			case "rpc_name":
				kind = types.SymbolKindMethod
				parent = protobufEnclosingService(n, buf)
				if par != nil {
					sig = compactProtoWhitespace(par.Content(buf))
				}
			}
			if par == nil {
				return
			}
			ls := int(par.StartPoint().Row) + 1
			le := int(par.EndPoint().Row) + 1
			sym := symbol(repoID, relPath, name, kind, ls, le, "protobuf", sig, parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "import":
			path := protobufImportPath(n, buf)
			if path == "" {
				return
			}
			out.Imports = append(out.Imports, path)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, fid, moduleNodeID(repoID, path), "imports"),
				RepoID:     repoID,
				Kind:       types.RefKindImports,
				SourceID:   fid,
				TargetID:   moduleNodeID(repoID, path),
				Confidence: 0.9,
			})
		}
	})
	return out, nil
}

func protobufEnclosingService(n *sitter.Node, buf []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "service" {
			continue
		}
		if sn := ChildByType(p, "service_name"); sn != nil {
			return strings.TrimSpace(sn.Content(buf))
		}
		return ""
	}
	return ""
}

func protobufImportPath(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	str := ChildByType(n, "string")
	if str == nil {
		return ""
	}
	return unquoteProtoString(str.Content(buf))
}

func unquoteProtoString(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func compactProtoWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
