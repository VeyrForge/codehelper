package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	ruby "github.com/smacker/go-tree-sitter/ruby"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseRuby extracts methods, classes/modules, require/load import edges, and
// call edges. Methods record their enclosing class/module name in ParentID.
func ParseRuby(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(ruby.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	var classStack []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "call":
			if mod := rubyRequireModule(n, buf); mod != "" {
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
			// Still walk children (nested calls are rare but harmless).
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
		case "method", "singleton_method":
			name := ChildName(n, "name", buf)
			if name != "" {
				parent := ""
				if len(classStack) > 0 {
					parent = classStack[len(classStack)-1]
				}
				sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "ruby", "", parent)
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				extractCalls(n, buf, repoID, relPath, sym.ID, out)
				rubyEmitConstantReads(n, buf, repoID, relPath, sym.ID, out)
			}
		case "class", "module":
			name := ChildName(n, "name", buf)
			if name != "" {
				sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "ruby", "", "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				classStack = append(classStack, name)
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
				classStack = classStack[:len(classStack)-1]
				return
			}
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
		default:
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
		}
	}
	walk(tree.RootNode())
	extractSinatraDSL(repoID, relPath, buf, out)
	return out, nil
}

// sinatraRouteDSL matches top-level / class-body Sinatra route registrations:
//   get '/' do … end
//   post "/x", :provides => :json do … end
var sinatraRouteDSL = regexp.MustCompile(`(?m)^\s*(get|post|put|patch|delete|options|head|link|unlink)\s+['"]`)

// extractSinatraDSL indexes Sinatra HTTP verb DSL calls as entrypoints that
// call the matching Base method (get/post/…) so impact on get sees app routes.
func extractSinatraDSL(repoID, relPath string, buf []byte, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	if !strings.Contains(src, "Sinatra") && !strings.Contains(relPath, "sinatra") &&
		!sinatraRouteDSL.MatchString(src) {
		return
	}
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		m := sinatraRouteDSL.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		verb := strings.ToLower(m[1])
		siteName := fmt.Sprintf("sinatra_%s_%d", verb, i+1)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "ruby", "frameworks=sinatra;role=entrypoint", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, verb)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, sym.ID, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   sym.ID,
			TargetID:   tgt,
			Confidence: 0.85,
		})
		// Also call route — all verbs funnel through route().
		rt := fmt.Sprintf("symref:%s:%s:route", repoID, relPath)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, sym.ID, rt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   sym.ID,
			TargetID:   rt,
			Confidence: 0.7,
		})
	}
}

// rubyRequireModule extracts the module path from require / require_relative / load.
func rubyRequireModule(n *sitter.Node, buf []byte) string {
	if n == nil || n.Type() != "call" {
		return ""
	}
	ident := n.Child(0)
	if ident == nil || ident.Type() != "identifier" {
		return ""
	}
	fn := strings.TrimSpace(ident.Content(buf))
	switch fn {
	case "require", "require_relative", "load":
	default:
		return ""
	}
	var args *sitter.Node
	if a := n.ChildByFieldName("arguments"); a != nil {
		args = a
	} else {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "argument_list" {
				args = c
				break
			}
		}
	}
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil {
			continue
		}
		if mod := rubyStringContent(c, buf); mod != "" {
			return mod
		}
	}
	return ""
}

func rubyStringContent(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if n.Type() == "string_content" {
		return strings.TrimSpace(n.Content(buf))
	}
	if n.Type() == "string" {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "string_content" {
				return strings.TrimSpace(c.Content(buf))
			}
		}
	}
	return ""
}

// rubyEmitConstantReads emits reads for Constant / Foo::Bar references so class
// inbound works when methods only call through constants (Sinatra helpers, etc.).
func rubyEmitConstantReads(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	seen := map[string]bool{}
	Walk(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		var tok string
		switch n.Type() {
		case "constant":
			tok = strings.TrimSpace(n.Content(buf))
		case "scope_resolution":
			// Foo::Bar — take the rightmost constant.
			tok = strings.TrimSpace(n.Content(buf))
			if i := strings.LastIndex(tok, "::"); i >= 0 {
				tok = tok[i+2:]
			}
		default:
			return
		}
		if tok == "" || tok[0] < 'A' || tok[0] > 'Z' || seen[tok] {
			return
		}
		switch tok {
		case "TrueClass", "FalseClass", "NilClass", "Object", "Class", "Module",
			"String", "Integer", "Float", "Array", "Hash", "Symbol", "Proc",
			"Enumerable", "Kernel", "BasicObject":
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
			Confidence: 0.6,
		})
	})
}
