package parser

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	kt "github.com/smacker/go-tree-sitter/kotlin"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseKotlin extracts declarations, imports, inheritance, Spring DI call edges,
// and typed calls. The tree-sitter Kotlin grammar used here does not expose a
// "name" field on function_declaration / class_declaration — names live as
// sibling simple_identifier / type_identifier nodes (extension receivers appear
// as user_type "." simple_identifier before the parameter list).
func ParseKotlin(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(kt.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	content := string(buf)
	frameworks := DetectFrameworkPacks(relPath, nil, content)

	var typeStack []string
	var fieldStack []map[string]string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "import_header":
			if mod := kotlinImportName(n, buf); mod != "" {
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
			return
		case "property_declaration":
			name := kotlinPropertyName(n, buf)
			if name == "" {
				return
			}
			parent := ""
			if len(typeStack) > 0 {
				parent = typeStack[len(typeStack)-1]
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "kotlin", frameworkSignature(frameworks, ""), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			return
		case "function_declaration":
			name := kotlinDeclName(n, buf)
			if name == "" {
				return
			}
			parent := ""
			if len(typeStack) > 0 {
				parent = typeStack[len(typeStack)-1]
			}
			kind := types.SymbolKindFunction
			if parent != "" {
				kind = types.SymbolKindMethod
			}
			sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "kotlin", frameworkSignature(frameworks, ""), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			var typeOf fnTypeOf
			if parent != "" && len(fieldStack) > 0 {
				typeOf = kotlinTypeOf(parent, fieldStack[len(fieldStack)-1])
			}
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, typeOf)
			return
		case "class_declaration", "object_declaration":
			name := kotlinDeclName(n, buf)
			if name == "" {
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
				return
			}
			kind := types.SymbolKindClass
			if kotlinIsInterface(n) {
				kind = types.SymbolKindInterface
			}
			annWindow := kotlinClassAnnotationWindow(n, buf)
			role := springRoleFromAnnotations(annWindow)
			sig := frameworkSignature(frameworks, role)
			if embeds := kotlinEmbedNames(n, buf); len(embeds) > 0 {
				sig = appendEmbedsSig(sig, embeds)
			}
			sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "kotlin", sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			kotlinEmitInheritance(n, buf, repoID, relPath, sym.ID, out)
			extractSpringKotlinDI(n, buf, repoID, relPath, sym.ID, out)

			fields := kotlinCollectFieldTypes(n, buf)
			typeStack = append(typeStack, name)
			fieldStack = append(fieldStack, fields)
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
			fieldStack = fieldStack[:len(fieldStack)-1]
			typeStack = typeStack[:len(typeStack)-1]
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(tree.RootNode())
	return out, nil
}

type fnTypeOf = func(string) string

func kotlinClassAnnotationWindow(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "modifiers", "annotation", "annotated":
			return c.Content(buf)
		}
	}
	from := int(n.StartByte()) - 120
	if from < 0 {
		from = 0
	}
	return string(buf[from:int(n.StartByte())])
}

func kotlinPropertyName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if s := ChildName(n, "name", buf); s != "" {
		return s
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "variable_declaration" {
			for j := 0; j < int(c.ChildCount()); j++ {
				cc := c.Child(j)
				if cc != nil && cc.Type() == "simple_identifier" {
					return strings.TrimSpace(cc.Content(buf))
				}
			}
		}
		if c.Type() == "simple_identifier" {
			return strings.TrimSpace(c.Content(buf))
		}
	}
	return ""
}

// kotlinDeclName reads the declaration name from siblings when the grammar has
// no name field. For extension functions (`fun Route.route(...)`) the last
// simple_identifier before function_value_parameters is the function name.
func kotlinDeclName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if s := ChildName(n, "name", buf); s != "" {
		return s
	}
	switch n.Type() {
	case "function_declaration":
		var lastSimple string
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if c.Type() == "simple_identifier" {
				lastSimple = c.Content(buf)
			}
			if c.Type() == "function_value_parameters" {
				break
			}
		}
		return lastSimple
	case "class_declaration", "object_declaration":
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "type_identifier" {
				return c.Content(buf)
			}
		}
	}
	return ""
}

func kotlinIsInterface(n *sitter.Node) bool {
	if n == nil {
		return false
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && !c.IsNamed() && c.Type() == "interface" {
			return true
		}
	}
	return false
}

func kotlinImportName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	s := strings.TrimSpace(n.Content(buf))
	s = strings.TrimPrefix(s, "import ")
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".*")
	if i := strings.Index(s, " as "); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func kotlinEmbedNames(n *sitter.Node, buf []byte) []string {
	var out []string
	seen := map[string]bool{}
	self := kotlinDeclName(n, buf)
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		tok = strings.TrimSuffix(tok, "?")
		if i := strings.IndexAny(tok, "<("); i > 0 {
			tok = tok[:i]
		}
		if i := strings.LastIndex(tok, "."); i >= 0 {
			tok = tok[i+1:]
		}
		if tok == "" || tok == self || seen[tok] || springSkipInjectType(tok) {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "class_body" {
			break
		}
		if c.Type() == "delegation_specifier" || c.Type() == "delegation_specifiers" {
			Walk(c, func(x *sitter.Node) {
				if x.Type() == "user_type" || x.Type() == "type_identifier" {
					add(kotlinTypeLeaf(x, buf))
				}
			})
		}
	}
	return out
}

func kotlinEmitInheritance(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	self := kotlinDeclName(n, buf)
	seen := map[string]bool{}
	emit := func(leaf string, kind types.ReferenceKind) {
		leaf = strings.TrimSpace(leaf)
		leaf = strings.TrimSuffix(leaf, "?")
		if i := strings.IndexAny(leaf, "<("); i > 0 {
			leaf = leaf[:i]
		}
		if i := strings.LastIndex(leaf, "."); i >= 0 {
			leaf = leaf[i+1:]
		}
		if leaf == "" || leaf == self || seen[leaf] || springSkipInjectType(leaf) {
			return
		}
		seen[leaf] = true
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, leaf)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, string(kind)),
			RepoID:     repoID,
			Kind:       kind,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.85,
		})
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "class_body" {
			break
		}
		if c.Type() != "delegation_specifier" && c.Type() != "delegation_specifiers" {
			continue
		}
		Walk(c, func(x *sitter.Node) {
			switch x.Type() {
			case "constructor_invocation":
				// Base() → inherits
				emit(kotlinTypeLeaf(x, buf), types.RefKindInherits)
			case "user_type":
				// Bare Auditable (no constructor call) → implements
				// Skip if this user_type is nested under constructor_invocation.
				p := x.Parent()
				for p != nil && p != c {
					if p.Type() == "constructor_invocation" {
						return
					}
					p = p.Parent()
				}
				emit(kotlinTypeLeaf(x, buf), types.RefKindImplements)
			}
		})
	}
}
