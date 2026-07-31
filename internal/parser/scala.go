package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	scala "github.com/smacker/go-tree-sitter/scala"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseScala extracts defs, classes/objects/traits, import edges, and call edges.
// Methods record enclosing object/class/trait in ParentID (Swift/Kotlin pattern).
// Call resolution is name-only heuristic — Medium symbols / Low–Medium calls.
func ParseScala(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(scala.GetLanguage())
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
		case "import_declaration":
			if mod := scalaImportName(n, buf); mod != "" {
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
		case "function_definition", "function_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n, buf)
			}
			if name == "" {
				return
			}
			parent := ""
			if len(typeStack) > 0 {
				parent = typeStack[len(typeStack)-1]
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "scala", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			return
		case "class_definition", "object_definition", "trait_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n, buf)
			}
			if name == "" {
				for i := 0; i < int(n.NamedChildCount()); i++ {
					walk(n.NamedChild(i))
				}
				return
			}
			k := types.SymbolKindClass
			if n.Type() == "trait_definition" {
				k = types.SymbolKindInterface
			}
			sym := symbol(repoID, relPath, name, k, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "scala", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			scalaEmitInheritance(n, buf, repoID, relPath, sym.ID, out)
			typeStack = append(typeStack, name)
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
			typeStack = typeStack[:len(typeStack)-1]
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return out, nil
}

func scalaImportName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	// import demo.helpers.Format — path / identifier / stable_id children.
	var best string
	Walk(n, func(c *sitter.Node) {
		switch c.Type() {
		case "stable_identifier", "stable_type_identifier", "identifier", "type_identifier":
			s := strings.TrimSpace(c.Content(buf))
			s = strings.TrimSuffix(s, "._")
			s = strings.TrimSuffix(s, ".*")
			if s != "" && len(s) > len(best) {
				best = s
			}
		}
	})
	return best
}

// scalaEmitInheritance records extends/with clauses as inherits / implements edges.
// Traits used via `with` are implements; class/trait `extends` is inherits.
func scalaEmitInheritance(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if n == nil {
		return
	}
	seen := map[string]bool{}
	emit := func(leaf string, kind types.ReferenceKind) {
		leaf = strings.TrimSpace(leaf)
		if i := strings.IndexAny(leaf, "[("); i > 0 {
			leaf = leaf[:i]
		}
		if i := strings.LastIndex(leaf, "."); i >= 0 {
			leaf = leaf[i+1:]
		}
		if leaf == "" || seen[leaf] {
			return
		}
		seen[leaf] = true
		tgt := "symref:" + repoID + ":" + relPath + ":" + leaf
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, string(kind)),
			RepoID:     repoID,
			Kind:       kind,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.75,
		})
	}

	// Prefer structured children when the grammar exposes them.
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "extends_clause", "template":
			withMode := false
			Walk(c, func(x *sitter.Node) {
				txt := strings.TrimSpace(x.Content(buf))
				if txt == "with" {
					withMode = true
					return
				}
				switch x.Type() {
				case "type_identifier", "identifier", "stable_type_identifier", "stable_identifier":
					if withMode {
						emit(txt, types.RefKindImplements)
					} else {
						emit(txt, types.RefKindInherits)
					}
				}
			})
		}
	}

	// Fallback: textual extends/with before the template body `{`.
	if len(seen) > 0 {
		return
	}
	head := n.Content(buf)
	if idx := strings.Index(head, "{"); idx >= 0 {
		head = head[:idx]
	}
	lower := head
	if i := strings.Index(lower, " extends "); i >= 0 {
		rest := strings.TrimSpace(head[i+len(" extends "):])
		parts := strings.Split(rest, " with ")
		for j, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if j == 0 {
				emit(p, types.RefKindInherits)
			} else {
				emit(p, types.RefKindImplements)
			}
		}
	} else if i := strings.Index(lower, " with "); i >= 0 {
		rest := strings.TrimSpace(head[i+len(" with "):])
		for _, p := range strings.Split(rest, " with ") {
			emit(strings.TrimSpace(p), types.RefKindImplements)
		}
	}
}
