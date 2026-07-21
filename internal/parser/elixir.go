package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	ex "github.com/smacker/go-tree-sitter/elixir"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseElixir extracts modules and defs.
//
// tree-sitter-elixir stores module names as `alias` nodes (e.g. Phoenix.Router)
// under an `arguments` child that is NOT a named field — ChildByFieldName("arguments")
// is nil. Def names live as identifier children of a nested call under arguments.
func ParseElixir(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(ex.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "call" {
			return
		}
		target := n.ChildByFieldName("target")
		if target == nil {
			target = ChildByType(n, "identifier")
		}
		if target == nil || target.Type() != "identifier" {
			return
		}
		id := target.Content(buf)
		args := n.ChildByFieldName("arguments")
		if args == nil {
			args = ChildByType(n, "arguments")
		}
		switch id {
		case "defmodule":
			name := elixirModuleName(args, buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "elixir", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if body := ChildByType(n, "do_block"); body != nil {
				extractCalls(body, buf, repoID, relPath, sym.ID, out)
			}
		case "def", "defp":
			fn := elixirDefName(args, buf)
			if fn == "" {
				return
			}
			sym := symbol(repoID, relPath, fn, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "elixir", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if body := ChildByType(n, "do_block"); body != nil {
				extractCalls(body, buf, repoID, relPath, sym.ID, out)
			}
		}
	})
	return out, nil
}

func elixirModuleName(args *sitter.Node, buf []byte) string {
	if args == nil {
		return ""
	}
	if alias := ChildByType(args, "alias"); alias != nil {
		return alias.Content(buf)
	}
	return FirstIdentifier(args, buf)
}

func elixirDefName(args *sitter.Node, buf []byte) string {
	if args == nil {
		return ""
	}
	// def foo(opts) → arguments → call → identifier "foo"
	if call := ChildByType(args, "call"); call != nil {
		if t := call.ChildByFieldName("target"); t != nil && t.Type() == "identifier" {
			return t.Content(buf)
		}
		if id := ChildByType(call, "identifier"); id != nil {
			return id.Content(buf)
		}
	}
	if alias := ChildByType(args, "alias"); alias != nil {
		return alias.Content(buf)
	}
	return FirstIdentifier(args, buf)
}
