package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	cs "github.com/smacker/go-tree-sitter/csharp"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseCSharp extracts types, methods, using-directive import edges, and calls.
// Methods record their enclosing type in ParentID so symref resolution can use
// receiver-type disambiguation; type identifiers in method bodies emit reads
// for Unity/MonoBehaviour class inbound (who references this type).
// ASP.NET Core Controllers / Minimal APIs densify ctor+[FromServices] DI and
// MapGet/MapPost entrypoints onto the same C# graph.
func ParseCSharp(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(cs.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	content := string(buf)
	frameworks := DetectFrameworkPacks(relPath, nil, content)
	if looksLikeAspNetFile(relPath, content) {
		frameworks = withFramework(frameworks, string(FrameworkAspNetCore))
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "using_directive":
			if mod := csharpUsingName(n, buf); mod != "" {
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
		case "class_declaration", "interface_declaration", "struct_declaration", "record_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
				return
			}
			k := types.SymbolKindClass
			if n.Type() == "interface_declaration" {
				k = types.SymbolKindInterface
			}
			attrs := csharpAttributeNames(n, buf)
			role := aspnetRoleFromAttributes(attrs, name)
			// ControllerBase / *Controller naming without attrs still tags role.
			if role == "" && strings.HasSuffix(name, "Controller") &&
				(looksLikeAspNetFile(relPath, n.Content(buf)) || strings.Contains(n.Content(buf), "ControllerBase")) {
				role = "controller"
			}
			if role == "" && strings.HasSuffix(name, "Service") && looksLikeAspNetFile(relPath, content) {
				role = "service"
			}
			classFW := frameworks
			if role == "controller" || role == "service" || role == "repository" ||
				looksLikeAspNetFile(relPath, n.Content(buf)) {
				classFW = withFramework(frameworks, string(FrameworkAspNetCore))
			}
			sig := frameworkSignature(classFW, role)
			sym := symbol(repoID, relPath, name, k, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "csharp", sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			csharpEmitBaseList(n, buf, repoID, relPath, sym.ID, out)
			csharpEmitUnityComponentTypes(n, buf, repoID, relPath, sym.ID, out)
			extractAspNetDI(n, buf, repoID, relPath, sym.ID, out)

			fields := csharpCollectFieldTypes(n, buf)
			typeOf := csharpTypeOf(name, fields)
			body := n.ChildByFieldName("body")
			if body != nil {
				for i := 0; i < int(body.ChildCount()); i++ {
					walkMember(body.Child(i), name, typeOf, classFW, out, repoID, relPath, buf, walk)
				}
			} else {
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
			}
			return
		case "method_declaration", "constructor_declaration":
			// Top-level / file-scoped members outside a type (rare in C#).
			walkMember(n, "", nil, frameworks, out, repoID, relPath, buf, walk)
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(tree.RootNode())
	extractAspNetMinimalAPIs(tree.RootNode(), buf, repoID, relPath, frameworks, out)
	return out, nil
}

func walkMember(n *sitter.Node, parent string, typeOf func(string) string, frameworks []string, out *ParseResult, repoID, relPath string, buf []byte, walkTypes func(*sitter.Node)) {
	if n == nil {
		return
	}
	switch n.Type() {
	case "method_declaration", "constructor_declaration":
		name := ChildName(n, "name", buf)
		if name == "" && n.Type() == "constructor_declaration" {
			name = "ctor"
		}
		if name == "" {
			return
		}
		attrs := csharpAttributeNames(n, buf)
		role := aspnetMethodRole(attrs)
		sig := frameworkSignature(frameworks, role)
		sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "csharp", sig, parent)
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		if typeOf != nil {
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, typeOf)
		} else {
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
		}
		csharpEmitTypeReads(n, buf, repoID, relPath, sym.ID, out)
		csharpEmitUnityComponentTypes(n, buf, repoID, relPath, sym.ID, out)
		extractAspNetFromServicesMethod(n, buf, repoID, relPath, sym.ID, out)
	case "class_declaration", "interface_declaration", "struct_declaration", "record_declaration":
		walkTypes(n)
	default:
		for i := 0; i < int(n.ChildCount()); i++ {
			walkMember(n.Child(i), parent, typeOf, frameworks, out, repoID, relPath, buf, walkTypes)
		}
	}
}

func csharpUsingName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "qualified_name", "identifier", "name", "alias_qualified_name":
			mod := strings.TrimSpace(c.Content(buf))
			if mod != "" && mod != "static" && mod != "global" {
				return mod
			}
		}
	}
	return ""
}

// csharpEmitTypeReads emits reads for capitalized type identifiers in a method
// body (Unity scripts referencing other MonoBehaviours / ScriptableObjects).
func csharpEmitTypeReads(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	seen := map[string]bool{}
	Walk(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "identifier", "type_identifier", "generic_name":
		default:
			return
		}
		tok := strings.TrimSpace(n.Content(buf))
		// generic_name may be "List<Foo>" — take the simple type head.
		if i := strings.IndexAny(tok, "<["); i > 0 {
			tok = tok[:i]
		}
		if tok == "" || tok[0] < 'A' || tok[0] > 'Z' || seen[tok] {
			return
		}
		if csharpSkipType(tok) {
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
			Confidence: 0.55,
		})
	})
}

func csharpSkipType(tok string) bool {
	switch tok {
	case "String", "Int32", "Boolean", "Object", "Void", "Task", "List",
		"Dictionary", "IEnumerator", "MonoBehaviour", "ScriptableObject",
		"GameObject", "Transform", "Vector2", "Vector3", "Quaternion",
		"Debug", "Mathf", "Time", "Input", "Coroutine", "Component",
		"GetComponent", "AddComponent", "RequireComponent", "TryGetComponent",
		"GetComponentInChildren", "GetComponentInParent", "GetComponents",
		"FindObjectOfType", "FindObjectsOfType", "FindFirstObjectByType",
		"FindAnyObjectByType", "FindObjectsByType", "Rigidbody",
		// ASP.NET / BCL framework bases — skip as inherit hubs.
		"ControllerBase", "Controller", "PageModel", "Hub", "ViewComponent":
		return true
	default:
		return false
	}
}

// csharpEmitBaseList emits inherits/implements for `: Base, IFoo` lists.
// Engine bases (MonoBehaviour) are skipped to avoid drowning inbound hubs.
func csharpEmitBaseList(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if n == nil || out == nil {
		return
	}
	var bl *sitter.Node
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "base_list" {
			bl = c
			break
		}
	}
	if bl == nil {
		return
	}
	idx := 0
	for i := 0; i < int(bl.NamedChildCount()); i++ {
		ch := bl.NamedChild(i)
		if ch == nil {
			continue
		}
		tok := strings.TrimSpace(ch.Content(buf))
		if j := strings.IndexAny(tok, "<["); j > 0 {
			tok = tok[:j]
		}
		if tok == "" || tok[0] < 'A' || tok[0] > 'Z' || csharpSkipType(tok) {
			continue
		}
		kind := types.RefKindInherits
		conf := 0.9
		if (len(tok) > 1 && tok[0] == 'I' && tok[1] >= 'A' && tok[1] <= 'Z') || idx > 0 {
			kind = types.RefKindImplements
			conf = 0.85
		}
		idx++
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, string(kind)),
			RepoID:     repoID,
			Kind:       kind,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
}

// Unity GetComponent<T> / AddComponent<T> / FindObjectOfType<T> /
// [RequireComponent(typeof(T))].
var (
	csharpGetAddComponentRe = regexp.MustCompile(
		`\b(?:GetComponent|AddComponent|TryGetComponent|GetComponentInChildren|GetComponentInParent|GetComponents|GetComponentsInChildren|GetComponentsInParent|FindObjectOfType|FindObjectsOfType|FindFirstObjectByType|FindAnyObjectByType|FindObjectsByType)\s*<\s*([A-Z][A-Za-z0-9_]*)\s*>`)
	csharpRequireComponentRe = regexp.MustCompile(`\[RequireComponent\s*\(\s*typeof\s*\(\s*([A-Z][A-Za-z0-9_]*)\s*\)`)
)

func csharpEmitUnityComponentTypes(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := root.Content(buf)
	seen := map[string]bool{}
	emit := func(tok string, conf float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" || csharpSkipType(tok) || seen[tok] {
			return
		}
		seen[tok] = true
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
	for _, m := range csharpGetAddComponentRe.FindAllStringSubmatch(src, -1) {
		emit(m[1], 0.8)
	}
	for _, m := range csharpRequireComponentRe.FindAllStringSubmatch(src, -1) {
		emit(m[1], 0.85)
	}
	if p := root.Parent(); p != nil {
		from := int(root.StartByte()) - 200
		if from < 0 {
			from = 0
		}
		window := string(buf[from:int(root.StartByte())])
		for _, m := range csharpRequireComponentRe.FindAllStringSubmatch(window, -1) {
			emit(m[1], 0.85)
		}
	}
}
