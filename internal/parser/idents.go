package parser

import sitter "github.com/smacker/go-tree-sitter"

// FirstIdentifier returns first identifier text under n (DFS).
func FirstIdentifier(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if n.Type() == "identifier" || n.Type() == "type_identifier" || n.Type() == "property_identifier" {
		return n.Content(buf)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		if s := FirstIdentifier(n.Child(i), buf); s != "" {
			return s
		}
	}
	return ""
}
