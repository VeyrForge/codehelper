package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	py "github.com/smacker/go-tree-sitter/python"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	fastAPIDecoratorPattern = regexp.MustCompile(`(?i)^\s*@(?:\w+\.)?(get|post|put|patch|delete|options|head)\s*\(`)
	djangoPathPattern       = regexp.MustCompile(`(?i)\bpath\s*\(\s*['"][^'"]*['"]\s*,\s*([A-Za-z_][A-Za-z0-9_\.]*)`)
)

// ParsePython extracts symbols from Python source.
func ParsePython(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(py.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	frameworks := DetectFrameworkPacks(relPath, nil, string(buf))
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "import_statement", "import_from_statement":
			if src := pyImportModule(n, buf); src != "" {
				out.Imports = append(out.Imports, src)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, FileNodeID(repoID, relPath), moduleNodeID(repoID, src), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   FileNodeID(repoID, relPath),
					TargetID:   moduleNodeID(repoID, src),
					Confidence: 0.85,
				})
			}
		case "function_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "python", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
			// Decorators sit on the parent decorated_definition, not inside the
			// function body — walk them so Depends/app.get appear as call edges.
			if p := n.Parent(); p != nil && p.Type() == "decorated_definition" {
				extractPythonDecoratorCalls(p, buf, repoID, relPath, sym.ID, out)
			}
		case "class_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "python", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "assignment":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left == nil {
				return
			}
			name := strings.TrimSpace(left.Content(buf))
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "python", frameworkSignature(frameworks, "state"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if right != nil {
				addReadEdgesFromNode(repoID, relPath, sym.ID, right, buf, out)
			}
		}
	})
	addPythonFrameworkSymbols(repoID, relPath, buf, out, frameworks)
	addPythonDICallEdges(tree.RootNode(), buf, repoID, relPath, out)
	return out, nil
}

func pyImportModule(n *sitter.Node, buf []byte) string {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == "dotted_name" {
			return strings.TrimSpace(c.Content(buf))
		}
	}
	return ""
}

func extractPythonDecoratorCalls(decorated *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if decorated == nil {
		return
	}
	for i := 0; i < int(decorated.ChildCount()); i++ {
		c := decorated.Child(i)
		if c == nil || c.Type() != "decorator" {
			continue
		}
		extractCalls(c, buf, repoID, relPath, fromSym, out)
	}
}

func addPythonFrameworkSymbols(repoID, relPath string, buf []byte, out *ParseResult, frameworks []string) {
	lines := strings.Split(string(buf), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if m := fastAPIDecoratorPattern.FindStringSubmatch(trim); len(m) > 1 {
			name := fmt.Sprintf("fastapi_%s_%d", strings.ToLower(m[1]), i+1)
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "python", frameworkSignature(withFramework(frameworks, string(FrameworkFastAPI)), "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
		if m := djangoPathPattern.FindStringSubmatch(trim); len(m) > 1 {
			view := strings.ReplaceAll(strings.TrimSpace(m[1]), ".", "_")
			name := fmt.Sprintf("django_path_%s_%d", view, i+1)
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "python", frameworkSignature(withFramework(frameworks, string(FrameworkDjango)), "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
	}
}

// addPythonDICallEdges attaches Depends / include_router calls that live at
// module scope (common in FastAPI tutorials) to the local `app`/`router`
// symbol so they participate in the call graph after symref resolution.
func addPythonDICallEdges(root *sitter.Node, buf []byte, repoID, relPath string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	fallback := ""
	for _, s := range out.Symbols {
		switch s.Name {
		case "app", "router":
			if fallback == "" {
				fallback = s.ID
			}
		}
	}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call" {
			return
		}
		name := calleeName(n.ChildByFieldName("function"), buf)
		if name != "Depends" && name != "include_router" {
			return
		}
		from := enclosingPythonFunctionSym(n, buf, repoID, relPath, out)
		if from == "" {
			from = fallback
		}
		if from == "" {
			return
		}
		// Skip duplicates already emitted from function-body extractCalls.
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		for _, e := range out.Edges {
			if e.Kind == types.RefKindCalls && e.SourceID == from && e.TargetID == tgt {
				return
			}
		}
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, from, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   from,
			TargetID:   tgt,
			Confidence: 0.85,
		})
	})
}

func enclosingPythonFunctionSym(n *sitter.Node, buf []byte, repoID, relPath string, out *ParseResult) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "function_definition" {
			continue
		}
		name := ChildName(p, "name", buf)
		if name == "" {
			return ""
		}
		ls := int(p.StartPoint().Row) + 1
		want := fmt.Sprintf("sym:%s:%s:%d:%s", repoID, relPath, ls, name)
		for _, s := range out.Symbols {
			if s.ID == want {
				return s.ID
			}
		}
		// Fallback: first symbol with this name in the file.
		for _, s := range out.Symbols {
			if s.Name == name {
				return s.ID
			}
		}
		return ""
	}
	return ""
}
