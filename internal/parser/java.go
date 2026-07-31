package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	java "github.com/smacker/go-tree-sitter/java"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseJava extracts classes/interfaces/enums, methods, constructors, import
// edges, extends/implements, Spring DI call edges, Hibernate/JPA entity +
// repository densify, @GetMapping entrypoints, and typed method calls
// (this.owners.findById → OwnerRepository.findById when the field type is known).
func ParseJava(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(java.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	content := string(buf)
	frameworks := DetectFrameworkPacks(relPath, nil, content)
	jpaish := looksLikeJPAFile(relPath, content)

	var walk func(n *sitter.Node)
	var walkMember func(n *sitter.Node, parent string, fields map[string]string)

	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "import_declaration":
			if mod := javaImportName(n, buf); mod != "" {
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
		case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
				return
			}
			kind := types.SymbolKindClass
			if n.Type() == "interface_declaration" {
				kind = types.SymbolKindInterface
			}
			annWindow := javaClassAnnotationWindow(n, buf)
			typeText := n.Content(buf)
			role := springRoleFromAnnotations(annWindow)
			if role == "" {
				role = jpaRoleFromAnnotations(annWindow + "\n" + typeText)
			}
			if jpaish || jpaEntityPattern.MatchString(annWindow) || strings.Contains(typeText, "JpaRepository") {
				frameworks = withFramework(frameworks, string(FrameworkJPA))
			}
			sig := frameworkSignature(frameworks, role)
			if embeds := javaEmbedNames(n, buf); len(embeds) > 0 {
				sig = appendEmbedsSig(sig, embeds)
			}
			sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "java", sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			javaEmitInheritance(n, buf, repoID, relPath, sym.ID, out)
			extractSpringJavaDI(n, buf, repoID, relPath, sym.ID, out)
			extractJPAEntity(n, buf, repoID, relPath, sym.ID, out)
			extractJpaRepository(n, buf, repoID, relPath, sym.ID, out)

			fields := javaCollectFieldTypes(n, buf)
			body := n.ChildByFieldName("body")
			if body != nil {
				for i := 0; i < int(body.ChildCount()); i++ {
					walkMember(body.Child(i), name, fields)
				}
			} else {
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
			}
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}

	walkMember = func(n *sitter.Node, parent string, fields map[string]string) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "method_declaration", "constructor_declaration":
			name := ChildName(n, "name", buf)
			if name == "" && n.Type() == "constructor_declaration" {
				name = parent
			}
			if name == "" {
				return
			}
			methAnn := javaMethodAnnotationWindow(n, buf)
			role := springMappingRole(methAnn)
			if role == "" && jpaQueryPattern.MatchString(methAnn) {
				role = "query"
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "java", frameworkSignature(frameworks, role), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, javaTypeOf(parent, fields))
			// @Query JPQL FROM Entity → method→entity call edge.
			if jpaQueryPattern.MatchString(methAnn) {
				for _, m := range jpaNamedQueryFrom.FindAllStringSubmatch(methAnn, -1) {
					if len(m) > 1 {
						emitJPACall(repoID, relPath, sym.ID, m[1], 0.84, out)
					}
				}
			}
		case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration":
			walk(n)
		default:
			for i := 0; i < int(n.ChildCount()); i++ {
				walkMember(n.Child(i), parent, fields)
			}
		}
	}

	walk(tree.RootNode())
	extractJPAEntityManagerUsage(repoID, relPath, buf, out)
	return out, nil
}

func javaEmbedNames(n *sitter.Node, buf []byte) []string {
	var out []string
	seen := map[string]bool{}
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] || springSkipInjectType(tok) {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	if sc := n.ChildByFieldName("superclass"); sc != nil {
		Walk(sc, func(c *sitter.Node) {
			if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" {
				add(javaLeafTypeName(c, buf))
			}
		})
	}
	if si := n.ChildByFieldName("interfaces"); si != nil {
		Walk(si, func(c *sitter.Node) {
			if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" {
				add(javaLeafTypeName(c, buf))
			}
		})
	}
	return out
}

func javaImportName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "scoped_identifier", "identifier":
			mod := strings.TrimSpace(c.Content(buf))
			mod = strings.TrimSuffix(mod, ".*")
			return mod
		}
	}
	return ""
}
