package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	php "github.com/smacker/go-tree-sitter/php"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	laravelRoutePattern = regexp.MustCompile(`(?i)Route::(get|post|put|patch|delete|options|any|match|resource|apiResource)\s*\(`)
	wpHookPattern       = regexp.MustCompile(`(?i)add_(action|filter)\s*\(\s*['"]([^'"]+)['"]\s*,\s*([^\),]+)`)
)

// ParsePHP extracts classes, methods, and functions.
func ParsePHP(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(php.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	frameworks := DetectFrameworkPacks(relPath, nil, string(buf))
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "namespace_use_declaration", "use_statement":
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c != nil && c.Type() == "name" {
					mod := c.Content(buf)
					if mod != "" {
						out.Imports = append(out.Imports, mod)
						out.Edges = append(out.Edges, types.Reference{
							ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
							RepoID:     repoID,
							Kind:       types.RefKindImports,
							SourceID:   fid,
							TargetID:   moduleNodeID(repoID, mod),
							Confidence: 0.8,
						})
					}
				}
			}
		case "function_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "method_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "class_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "simple_assignment_expression":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left == nil {
				return
			}
			name := sanitizeCallbackName(left.Content(buf))
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, "state"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if right != nil {
				addReadEdgesFromNode(repoID, relPath, sym.ID, right, buf, out)
			}
		}
	})
	addPHPFrameworkSymbols(repoID, relPath, buf, out, frameworks)
	return out, nil
}

func addPHPFrameworkSymbols(repoID, relPath string, buf []byte, out *ParseResult, frameworks []string) {
	lines := strings.Split(string(buf), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if m := laravelRoutePattern.FindStringSubmatch(trim); len(m) > 1 {
			name := fmt.Sprintf("route_%s_%d", strings.ToLower(m[1]), i+1)
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(withFramework(frameworks, string(FrameworkLaravel)), "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
		if m := wpHookPattern.FindStringSubmatch(trim); len(m) > 3 {
			cb := sanitizeCallbackName(m[3])
			if cb == "" {
				cb = fmt.Sprintf("wp_hook_callback_%d", i+1)
			}
			sym := symbol(repoID, relPath, cb, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(withFramework(frameworks, string(FrameworkWordPress)), "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
	}
}

func sanitizeCallbackName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	s = strings.ReplaceAll(s, "::class", "")
	s = strings.ReplaceAll(s, "::", "_")
	s = strings.ReplaceAll(s, "->", "_")
	s = strings.ReplaceAll(s, "$this", "this")
	s = strings.ReplaceAll(s, "[", "")
	s = strings.ReplaceAll(s, "]", "")
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
