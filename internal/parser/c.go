package parser

import (
	"context"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	clang "github.com/smacker/go-tree-sitter/c"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseC extracts functions, structs, and #include edges.
func ParseC(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(clang.GetLanguage())
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
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "c", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "struct_specifier":
			tn := ChildName(n, "name", buf)
			if tn == "" {
				tn = "struct_anon_" + strconv.Itoa(int(n.StartPoint().Row))
			}
			sym := symbol(repoID, relPath, tn, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "c", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
	})
	return out, nil
}

func includeString(n *sitter.Node, buf []byte) string {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "string_literal" || c.Type() == "system_lib_string" {
			s := strings.Trim(c.Content(buf), "<>\"")
			return s
		}
	}
	return ""
}
