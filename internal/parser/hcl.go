package parser

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	hcl "github.com/smacker/go-tree-sitter/hcl"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseHCL extracts Terraform/OpenTofu-oriented symbols and edges from HCL.
//
// Symbols use address-style names so refs resolve across files:
//
//	resource "aws_instance" "web"  → aws_instance.web
//	data "aws_ami" "ubuntu"        → data.aws_ami.ubuntu
//	module "vpc"                   → module.vpc
//	variable "region"              → var.region
//	output "id"                    → output.id
//	locals { name = … }            → local.name
//
// Edges: module source → imports; var/module/data/resource/local addresses →
// reads; function_call → calls. Nested blocks (filter, lifecycle, …) are not
// indexed as symbols; their expressions attribute to the enclosing construct.
//
// Note: the bundled tree-sitter-hcl grammar does not expose block field names
// (type/labels); labels are read from positional identifier + string_lit kids.
func ParseHCL(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(hcl.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	root := tree.RootNode()
	if root == nil {
		return out, nil
	}
	// config_file → body → (block | attribute)*
	var body *sitter.Node
	for i := 0; i < int(root.ChildCount()); i++ {
		if c := root.Child(i); c != nil && c.Type() == "body" {
			body = c
			break
		}
	}
	if body == nil {
		body = root
	}
	hclWalkBody(body, buf, repoID, relPath, fid, "", true, out)
	return out, nil
}

func hclWalkBody(body *sitter.Node, buf []byte, repoID, relPath, fid, parentSym string, allowBareAttrs bool, out *ParseResult) {
	if body == nil {
		return
	}
	for i := 0; i < int(body.ChildCount()); i++ {
		n := body.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "block":
			hclHandleBlock(n, buf, repoID, relPath, fid, parentSym, out)
		case "attribute":
			// Top-level .tfvars / bare assigns — index as variables when allowed.
			if parentSym == "" && allowBareAttrs {
				if name := hclAttrName(n, buf); name != "" {
					sym := symbol(repoID, relPath, name, types.SymbolKindVariable,
						int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "hcl", "", "")
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					hclEmitFromNode(n, buf, repoID, relPath, fid, sym.ID, out)
				}
			} else if parentSym != "" {
				hclEmitFromNode(n, buf, repoID, relPath, fid, parentSym, out)
			}
		}
	}
}

func hclHandleBlock(n *sitter.Node, buf []byte, repoID, relPath, fid, parentSym string, out *ParseResult) {
	bt, labels := hclBlockTypeAndLabels(n, buf)
	if bt == "" {
		return
	}
	blockBody := hclBlockBody(n)

	// Nested non-construct blocks: attribute refs to enclosing symbol only.
	if parentSym != "" && !hclIsTopConstruct(bt) {
		hclEmitFromNode(n, buf, repoID, relPath, fid, parentSym, out)
		return
	}

	switch bt {
	case "locals":
		// Each locals attribute becomes local.<name>.
		if blockBody == nil {
			return
		}
		for i := 0; i < int(blockBody.ChildCount()); i++ {
			attr := blockBody.Child(i)
			if attr == nil || attr.Type() != "attribute" {
				continue
			}
			name := hclAttrName(attr, buf)
			if name == "" {
				continue
			}
			addr := "local." + name
			sym := symbol(repoID, relPath, addr, types.SymbolKindVariable,
				int(attr.StartPoint().Row)+1, int(attr.EndPoint().Row)+1, "hcl", "locals", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			hclEmitFromNode(attr, buf, repoID, relPath, fid, sym.ID, out)
		}
		return
	}

	addr, kind, sig := hclAddressForBlock(bt, labels)
	if addr == "" {
		// Unknown / terraform meta block — walk nested constructs but do not
		// promote required_providers object keys (aws = {…}) into symbols.
		if blockBody != nil {
			hclWalkBody(blockBody, buf, repoID, relPath, fid, parentSym, false, out)
		}
		return
	}

	sym := symbol(repoID, relPath, addr, kind,
		int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "hcl", sig, "")
	out.Symbols = append(out.Symbols, sym)
	out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))

	if bt == "module" && blockBody != nil {
		if src := hclModuleSource(blockBody, buf); src != "" {
			out.Imports = append(out.Imports, src)
			modID := moduleNodeID(repoID, src)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, fid, modID, "imports"),
				RepoID:     repoID,
				Kind:       types.RefKindImports,
				SourceID:   fid,
				TargetID:   modID,
				Confidence: 0.9,
			})
		}
	}

	if blockBody != nil {
		// Nested construct blocks are rare; walk attributes + nested bodies for refs.
		for i := 0; i < int(blockBody.ChildCount()); i++ {
			c := blockBody.Child(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "attribute":
				hclEmitFromNode(c, buf, repoID, relPath, fid, sym.ID, out)
			case "block":
				hclHandleBlock(c, buf, repoID, relPath, fid, sym.ID, out)
			}
		}
	}
}

func hclIsTopConstruct(bt string) bool {
	switch bt {
	case "resource", "data", "module", "variable", "output", "provider", "locals":
		return true
	}
	return false
}

func hclAddressForBlock(bt string, labels []string) (addr string, kind types.SymbolKind, sig string) {
	switch bt {
	case "resource":
		if len(labels) >= 2 {
			return labels[0] + "." + labels[1], types.SymbolKindClass, "resource"
		}
	case "data":
		if len(labels) >= 2 {
			return "data." + labels[0] + "." + labels[1], types.SymbolKindClass, "data"
		}
	case "module":
		if len(labels) >= 1 {
			return "module." + labels[0], types.SymbolKindClass, "module"
		}
	case "variable":
		if len(labels) >= 1 {
			return "var." + labels[0], types.SymbolKindVariable, "variable"
		}
	case "output":
		if len(labels) >= 1 {
			return "output." + labels[0], types.SymbolKindVariable, "output"
		}
	case "provider":
		if len(labels) >= 1 {
			name := "provider." + labels[0]
			if len(labels) >= 2 {
				name += "." + labels[1]
			}
			return name, types.SymbolKindNamespace, "provider"
		}
	}
	return "", "", ""
}

func hclBlockTypeAndLabels(n *sitter.Node, buf []byte) (bt string, labels []string) {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "identifier":
			if bt == "" {
				bt = c.Content(buf)
			}
		case "string_lit":
			if s := hclStringLit(c, buf); s != "" {
				labels = append(labels, s)
			}
		case "block_start", "body", "block_end":
			// stop scanning labels once body starts
			if c.Type() == "block_start" || c.Type() == "body" {
				return bt, labels
			}
		}
	}
	return bt, labels
}

func hclBlockBody(n *sitter.Node) *sitter.Node {
	for i := 0; i < int(n.ChildCount()); i++ {
		if c := n.Child(i); c != nil && c.Type() == "body" {
			return c
		}
	}
	return nil
}

func hclAttrName(n *sitter.Node, buf []byte) string {
	for i := 0; i < int(n.ChildCount()); i++ {
		if c := n.Child(i); c != nil && c.Type() == "identifier" {
			return c.Content(buf)
		}
	}
	return ""
}

func hclStringLit(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "template_literal" {
			return c.Content(buf)
		}
	}
	// Fallback: strip surrounding quotes from raw content.
	s := strings.TrimSpace(n.Content(buf))
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func hclModuleSource(body *sitter.Node, buf []byte) string {
	for i := 0; i < int(body.ChildCount()); i++ {
		attr := body.Child(i)
		if attr == nil || attr.Type() != "attribute" {
			continue
		}
		if hclAttrName(attr, buf) != "source" {
			continue
		}
		for j := 0; j < int(attr.ChildCount()); j++ {
			c := attr.Child(j)
			if c == nil || c.Type() != "expression" {
				continue
			}
			if s := hclExprStringLit(c, buf); s != "" {
				return s
			}
		}
	}
	return ""
}

func hclExprStringLit(expr *sitter.Node, buf []byte) string {
	if expr == nil {
		return ""
	}
	var found string
	Walk(expr, func(n *sitter.Node) {
		if found != "" || n.Type() != "string_lit" {
			return
		}
		found = hclStringLit(n, buf)
	})
	return found
}

func hclEmitFromNode(n *sitter.Node, buf []byte, repoID, relPath, fid, fromSym string, out *ParseResult) {
	if n == nil || fromSym == "" {
		return
	}
	Walk(n, func(node *sitter.Node) {
		switch node.Type() {
		case "function_call":
			name := FirstIdentifier(node, buf)
			if name == "" || hclSkipCall[name] {
				return
			}
			tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, fromSym, tgt, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   fromSym,
				TargetID:   tgt,
				Confidence: 0.75,
			})
		case "expression":
			if addr := hclExprAddress(node, buf); addr != "" {
				tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, addr)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fromSym, tgt, "reads"),
					RepoID:     repoID,
					Kind:       types.RefKindReads,
					SourceID:   fromSym,
					TargetID:   tgt,
					Confidence: 0.85,
				})
			}
		}
	})
}

// hclExprAddress turns a Terraform traversal expression into an address that
// matches symbol names (strips trailing attribute/output segments).
func hclExprAddress(expr *sitter.Node, buf []byte) string {
	if expr == nil {
		return ""
	}
	// Only direct variable_expr + get_attr chains (not function_call wrappers).
	var root string
	var attrs []string
	for i := 0; i < int(expr.ChildCount()); i++ {
		c := expr.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "variable_expr":
			if root != "" {
				return "" // unexpected second root
			}
			root = FirstIdentifier(c, buf)
		case "get_attr":
			if id := FirstIdentifier(c, buf); id != "" {
				attrs = append(attrs, id)
			}
		case "function_call", "literal_value", "collection_value",
			"for_expr", "operation", "unary_operation", "binary_operation",
			"conditional", "template_expr", "index", "splat", "full_splat", "attr_splat":
			// Not a plain address traversal.
			return ""
		}
	}
	if root == "" {
		return ""
	}
	return hclNormalizeAddress(root, attrs)
}

func hclNormalizeAddress(root string, attrs []string) string {
	switch root {
	case "var":
		if len(attrs) >= 1 {
			return "var." + attrs[0]
		}
	case "local":
		if len(attrs) >= 1 {
			return "local." + attrs[0]
		}
	case "module":
		if len(attrs) >= 1 {
			return "module." + attrs[0]
		}
	case "data":
		if len(attrs) >= 2 {
			return "data." + attrs[0] + "." + attrs[1]
		}
	case "path", "terraform", "count", "each", "self", "true", "false", "null":
		return ""
	default:
		// Resource: TYPE.NAME[.attr…]
		if len(attrs) >= 1 {
			return root + "." + attrs[0]
		}
	}
	return ""
}

// hclSkipCall drops HCL/Terraform keywords mistaken for calls (none today) and
// extremely noisy type constructors that are not useful as graph targets.
var hclSkipCall = map[string]bool{
	"true": true, "false": true, "null": true,
}
