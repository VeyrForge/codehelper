package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	cpp "github.com/smacker/go-tree-sitter/cpp"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseCpp extracts C++ declarations.
func ParseCpp(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(cpp.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "preproc_include":
			path := includeString(n, buf)
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
				Confidence: 0.85,
			})
		case "function_definition":
			decl := n.ChildByFieldName("declarator")
			name := FirstIdentifier(decl, buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "class_specifier":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "namespace_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
	})
	return out, nil
}
