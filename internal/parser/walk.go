package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// Walk traverses the syntax tree depth-first.
func Walk(n *sitter.Node, fn func(*sitter.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for i := 0; i < int(n.ChildCount()); i++ {
		Walk(n.Child(i), fn)
	}
}

// ChildName returns named child content.
func ChildName(n *sitter.Node, field string, buf []byte) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Content(buf)
}

// ChildByType returns the first direct child with the given node type.
func ChildByType(n *sitter.Node, typ string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == typ {
			return c
		}
	}
	return nil
}
