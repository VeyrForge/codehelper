package parser

import sitter "github.com/smacker/go-tree-sitter"

// FirstIdentifier returns first identifier text under n (DFS).
// Also accepts grammar-specific name nodes: simple_identifier (Kotlin),
// alias (Elixir module paths like Phoenix.Router).
func FirstIdentifier(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "identifier", "type_identifier", "property_identifier",
		"simple_identifier", "alias":
		return n.Content(buf)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		if s := FirstIdentifier(n.Child(i), buf); s != "" {
			return s
		}
	}
	return ""
}
