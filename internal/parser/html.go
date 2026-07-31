package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	html "github.com/smacker/go-tree-sitter/html"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseHTML extracts the anchors you actually look up in markup: elements with an
// id ("#main"), and custom elements / web components (tag names containing a
// hyphen, e.g. <user-card>). These become symbols so "where is #checkout" or a
// component usage resolves through query/context. Structure-only (no call graph).
func ParseHTML(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(html.GetLanguage())
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
		if name == "" || strings.ContainsAny(name, " \t\n<>\"'") {
			return
		}
		sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "html", "", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "start_tag", "self_closing_tag":
			tag := ""
			var idVal string
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				switch c.Type() {
				case "tag_name":
					tag = text(c)
				case "attribute":
					an := ""
					for j := 0; j < int(c.ChildCount()); j++ {
						cc := c.Child(j)
						switch cc.Type() {
						case "attribute_name":
							an = text(cc)
						case "quoted_attribute_value", "attribute_value":
							if an == "id" {
								idVal = strings.Trim(text(cc), `"'`)
							}
						}
					}
				}
			}
			// Custom element / web component: tag with a hyphen (e.g. <user-card>).
			if strings.Contains(tag, "-") {
				emit(tag, types.SymbolKindClass, n)
			}
			if idVal != "" {
				emit("#"+idVal, types.SymbolKindVariable, n)
			}
		}
	})
	return out, nil
}
