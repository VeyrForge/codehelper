package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	cs "github.com/smacker/go-tree-sitter/csharp"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseCSharp extracts types, methods, using-directive import edges, and calls.
// Methods record their enclosing type in ParentID so symref resolution can use
// receiver-type disambiguation; type identifiers in method bodies emit reads
// for Unity/MonoBehaviour class inbound (who references this type).
func ParseCSharp(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(cs.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	var typeStack []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "using_directive":
			if mod := csharpUsingName(n, buf); mod != "" {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.85,
				})
			}
			return
		case "class_declaration", "interface_declaration", "struct_declaration", "record_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
				return
			}
			k := types.SymbolKindClass
			if n.Type() == "interface_declaration" {
				k = types.SymbolKindInterface
			}
			sym := symbol(repoID, relPath, name, k, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "csharp", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			typeStack = append(typeStack, name)
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
			typeStack = typeStack[:len(typeStack)-1]
			return
		case "method_declaration", "constructor_declaration":
			name := ChildName(n, "name", buf)
			if name == "" && n.Type() == "constructor_declaration" {
				name = "ctor"
			}
			if name == "" {
				return
			}
			parent := ""
			if len(typeStack) > 0 {
				parent = typeStack[len(typeStack)-1]
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "csharp", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			csharpEmitTypeReads(n, buf, repoID, relPath, sym.ID, out)
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(tree.RootNode())
	return out, nil
}

func csharpUsingName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "qualified_name", "identifier", "name", "alias_qualified_name":
			mod := strings.TrimSpace(c.Content(buf))
			if mod != "" && mod != "static" && mod != "global" {
				return mod
			}
		}
	}
	return ""
}

// csharpEmitTypeReads emits reads for capitalized type identifiers in a method
// body (Unity scripts referencing other MonoBehaviours / ScriptableObjects).
func csharpEmitTypeReads(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	seen := map[string]bool{}
	Walk(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "identifier", "type_identifier", "generic_name":
		default:
			return
		}
		tok := strings.TrimSpace(n.Content(buf))
		// generic_name may be "List<Foo>" — take the simple type head.
		if i := strings.IndexAny(tok, "<["); i > 0 {
			tok = tok[:i]
		}
		if tok == "" || tok[0] < 'A' || tok[0] > 'Z' || seen[tok] {
			return
		}
		// Skip common BCL noise.
		switch tok {
		case "String", "Int32", "Boolean", "Object", "Void", "Task", "List",
			"Dictionary", "IEnumerator", "MonoBehaviour", "ScriptableObject",
			"GameObject", "Transform", "Vector2", "Vector3", "Quaternion",
			"Debug", "Mathf", "Time", "Input", "Coroutine":
			return
		}
		seen[tok] = true
		tgt := "symref:" + repoID + ":" + relPath + ":" + tok
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.55,
		})
	})
}
