package parser

import (
	"context"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	swift "github.com/smacker/go-tree-sitter/swift"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseSwift extracts types, functions, import edges, and basic call edges.
// Call resolution is heuristic (name-only via extractCalls / kotlin-style
// navigation_expression) — treat empty fanout as unknown, not isolation.
// SwiftUI densify: View-conforming structs get frameworks=swiftui;role=view
// (+ screen when *Screen), and NavigationLink / TabView destination views
// become calls edges from the enclosing view.
func ParseSwift(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(swift.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	frameworks := DetectFrameworkPacks(relPath, nil, string(buf))
	var typeStack []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "import_declaration":
			if mod := swiftImportName(n, buf); mod != "" {
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
		case "function_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n.ChildByFieldName("name"), buf)
			}
			if name == "" {
				name = FirstIdentifier(n, buf)
			}
			if name == "" {
				return
			}
			parent := ""
			if len(typeStack) > 0 {
				parent = typeStack[len(typeStack)-1]
			}
			role := ""
			if parent != "" && name == "body" && containsFramework(frameworks, string(FrameworkSwiftUI)) {
				role = "view_body"
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "swift", frameworkSignature(frameworks, role), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			return
		case "class_declaration", "struct_declaration", "enum_declaration", "protocol_declaration", "actor_declaration", "extension_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				name = FirstIdentifier(n.ChildByFieldName("name"), buf)
			}
			if name == "" {
				name = FirstIdentifier(n, buf)
			}
			if name == "" {
				// extension without explicit type name — still walk body
				for i := 0; i < int(n.NamedChildCount()); i++ {
					walk(n.NamedChild(i))
				}
				return
			}
			// tree-sitter-swift often types `extension Greeter {…}` as
			// class_declaration (not extension_declaration). Detect via text.
			isExt := n.Type() == "extension_declaration" ||
				strings.HasPrefix(strings.TrimSpace(n.Content(buf)), "extension")
			if !isExt {
				k := types.SymbolKindClass
				switch n.Type() {
				case "protocol_declaration":
					k = types.SymbolKindInterface
				case "enum_declaration":
					k = types.SymbolKindEnum
				}
				sig := ""
				fw := frameworks
				// tree-sitter-swift often reports structs as class_declaration.
				isStruct := n.Type() == "struct_declaration" ||
					strings.HasPrefix(strings.TrimSpace(n.Content(buf)), "struct")
				if isStruct && (looksLikeSwiftUIView(n, buf) ||
					(containsFramework(frameworks, string(FrameworkSwiftUI)) &&
						(strings.HasSuffix(name, "View") || strings.HasSuffix(name, "Screen")))) {
					fw = withFramework(fw, string(FrameworkSwiftUI))
					role := "view"
					if strings.HasSuffix(name, "Screen") {
						role = "screen"
					}
					sig = frameworkSignature(fw, role)
				}
				sym := symbol(repoID, relPath, name, k, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "swift", sig, "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
			typeStack = append(typeStack, name)
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
			typeStack = typeStack[:len(typeStack)-1]
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	extractSwiftUINavWires(repoID, relPath, buf, frameworks, out)
	return out, nil
}

func looksLikeSwiftUIView(n *sitter.Node, buf []byte) bool {
	if n == nil {
		return false
	}
	text := n.Content(buf)
	// struct HomeView: View { … } / : View, … /
	lower := strings.ToLower(text)
	if strings.Contains(lower, ": view") || strings.Contains(lower, ":view") {
		return true
	}
	if strings.Contains(lower, "some view") {
		return true
	}
	return false
}

var (
	swiftUINavLinkDestNamed = regexp.MustCompile(
		`(?i)NavigationLink\s*\([^)]*destination\s*:\s*([A-Z][A-Za-z0-9_]*)`)
	swiftUINavLinkTrailingView = regexp.MustCompile(
		`(?i)NavigationLink[^\{\n]*\{\s*([A-Z][A-Za-z0-9_]*)\s*\(`)
	swiftUIViewCallPattern = regexp.MustCompile(
		`\b([A-Z][A-Za-z0-9_]*(?:View|Screen))\s*\(`)
)

// extractSwiftUINavWires densifies NavigationLink destinations + sibling View()
// calls inside a View body into calls edges from the enclosing view struct.
func extractSwiftUINavWires(repoID, relPath string, buf []byte, frameworks []string, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	if !containsFramework(frameworks, string(FrameworkSwiftUI)) &&
		!strings.Contains(src, "import SwiftUI") &&
		!strings.Contains(src, "NavigationLink") &&
		!strings.Contains(src, "NavigationStack") {
		return
	}
	builtins := map[string]bool{
		"View": true, "Text": true, "Image": true, "Button": true,
		"VStack": true, "HStack": true, "ZStack": true, "List": true, "Form": true,
		"NavigationStack": true, "NavigationView": true, "NavigationLink": true,
		"TabView": true, "Spacer": true, "ScrollView": true, "LazyVStack": true,
		"Color": true, "Font": true, "AnyView": true, "EmptyView": true,
	}
	emitFromLine := func(lineNo int, target string) {
		target = strings.TrimSpace(target)
		if target == "" || builtins[target] {
			return
		}
		from := ""
		fromName := ""
		bestSpan := int(^uint(0) >> 1)
		for _, s := range out.Symbols {
			if !(strings.Contains(s.Signature, "role=view") || strings.Contains(s.Signature, "role=screen")) {
				continue
			}
			if s.LineStart <= lineNo && lineNo <= s.LineEnd {
				span := s.LineEnd - s.LineStart
				if span < bestSpan {
					bestSpan = span
					from = s.ID
					fromName = s.Name
				}
			}
		}
		if from == "" {
			from = enclosingSymbolAtLine(out, lineNo)
		}
		if from == "" || fromName == target {
			return
		}
		emitNestCall(repoID, relPath, from, target, 0.86, out)
	}
	for _, m := range swiftUINavLinkDestNamed.FindAllStringSubmatchIndex(src, -1) {
		if len(m) < 4 {
			continue
		}
		lineNo := 1 + strings.Count(src[:m[0]], "\n")
		emitFromLine(lineNo, src[m[2]:m[3]])
	}
	for _, m := range swiftUINavLinkTrailingView.FindAllStringSubmatchIndex(src, -1) {
		if len(m) < 4 {
			continue
		}
		lineNo := 1 + strings.Count(src[:m[0]], "\n")
		emitFromLine(lineNo, src[m[2]:m[3]])
	}
	for _, m := range swiftUIViewCallPattern.FindAllStringSubmatchIndex(src, -1) {
		if len(m) < 4 {
			continue
		}
		lineNo := 1 + strings.Count(src[:m[0]], "\n")
		emitFromLine(lineNo, src[m[2]:m[3]])
	}
}

func swiftImportName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	// Prefer the longest identifier / navigation path under the import.
	var best string
	var walk func(*sitter.Node)
	walk = func(x *sitter.Node) {
		if x == nil {
			return
		}
		switch x.Type() {
		case "simple_identifier", "identifier", "type_identifier":
			s := strings.TrimSpace(x.Content(buf))
			if s != "" && (best == "" || len(s) > len(best)) {
				best = s
			}
		case "navigation_expression", "user_type", "scoped_type_identifier":
			s := strings.TrimSpace(x.Content(buf))
			s = strings.ReplaceAll(s, " ", "")
			if s != "" && len(s) > len(best) {
				best = s
			}
		}
		for i := 0; i < int(x.NamedChildCount()); i++ {
			walk(x.NamedChild(i))
		}
	}
	walk(n)
	best = strings.TrimSpace(best)
	if best == "" || best == "import" {
		return ""
	}
	return best
}
