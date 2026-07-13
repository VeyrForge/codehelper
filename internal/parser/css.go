package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	css "github.com/smacker/go-tree-sitter/css"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseCSS extracts the things you actually search a stylesheet for: class and id
// selectors (".btn", "#header"), @keyframes, and CSS custom properties (--var).
// These become symbols so "where is the button styled" / "find the --brand color"
// resolve via the same query/context tools as code. Selectors-only (no call graph).
func ParseCSS(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(css.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	text := func(n *sitter.Node) string {
		if n == nil {
			return ""
		}
		return string(buf[n.StartByte():n.EndByte()])
	}
	emit := func(name string, kind types.SymbolKind, n *sitter.Node) {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, " \t\n{}()") {
			return
		}
		sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "css", "", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "class_selector":
			emit("."+strings.TrimLeft(text(n), "."), types.SymbolKindClass, n)
		case "id_selector":
			emit("#"+strings.TrimLeft(text(n), "#"), types.SymbolKindClass, n)
		case "keyframes_statement":
			// @keyframes <name> { ... } — the name is the keyframe identifier.
			emit("@keyframes "+text(n.ChildByFieldName("name")), types.SymbolKindFunction, n)
		case "declaration":
			// Custom properties: --brand-color: ...;
			if t := text(n); strings.HasPrefix(strings.TrimSpace(t), "--") {
				if i := strings.IndexByte(t, ':'); i > 0 {
					emit(strings.TrimSpace(t[:i]), types.SymbolKindVariable, n)
				}
			}
		}
	})
	return out, nil
}
