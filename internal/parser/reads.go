package parser

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var defaultReadStopWords = map[string]struct{}{
	"if": {}, "else": {}, "for": {}, "while": {}, "switch": {}, "case": {}, "default": {},
	"return": {}, "break": {}, "continue": {}, "function": {}, "class": {}, "const": {},
	"let": {}, "var": {}, "new": {}, "this": {}, "super": {}, "true": {}, "false": {},
	"null": {}, "undefined": {}, "async": {}, "await": {}, "import": {}, "export": {},
	"from": {}, "def": {}, "lambda": {}, "pass": {}, "yield": {}, "none": {}, "self": {},
	"public": {}, "private": {}, "protected": {}, "static": {}, "final": {}, "interface": {},
	"extends": {}, "implements": {}, "try": {}, "catch": {}, "finally": {}, "throw": {},
}

func addReadEdgesFromNode(repoID, relPath, fromSym string, root *sitter.Node, buf []byte, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	fromName := fromSym
	if idx := strings.LastIndex(fromSym, ":"); idx >= 0 && idx < len(fromSym)-1 {
		fromName = fromSym[idx+1:]
	}

	seen := map[string]struct{}{}
	Walk(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		if !isReadIdentifierNodeType(n.Type()) {
			return
		}
		if isDeclarationPosition(n) {
			return
		}
		tok := strings.TrimSpace(n.Content(buf))
		t := strings.ToLower(strings.TrimSpace(tok))
		if t == "" {
			return
		}
		if _, stop := defaultReadStopWords[t]; stop {
			return
		}
		if strings.EqualFold(tok, fromName) {
			return
		}
		if _, dup := seen[t]; dup {
			return
		}
		seen[t] = struct{}{}
		targetID := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, targetID, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   targetID,
			Confidence: 0.5,
		})
	})
}

func isReadIdentifierNodeType(nodeType string) bool {
	switch nodeType {
	case "identifier", "property_identifier", "shorthand_property_identifier", "member_expression", "variable_name", "name", "attribute":
		return true
	default:
		return false
	}
}

func isDeclarationPosition(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "function_declaration", "function_definition", "method_definition", "method_declaration", "class_declaration", "class_definition":
		// Skip declaration names to avoid self-links.
		if p.ChildByFieldName("name") == n {
			return true
		}
	case "variable_declarator", "assignment", "simple_assignment_expression":
		if p.ChildByFieldName("name") == n || p.ChildByFieldName("left") == n {
			return true
		}
	}
	return false
}
