package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Hibernate/JPA densification on the Java graph: @Entity relations,
// JpaRepository<Entity,Id>, @Query methods, and EntityManager find/persist —
// so context/impact reach Owner from OwnerRepository beyond Spring stereotype DI
// (mirrors TypeORM @Entity/@ManyToOne densify).

var (
	jpaRelationPattern = regexp.MustCompile(
		`@(ManyToOne|OneToMany|OneToOne|ManyToMany)\b`)
	jpaEntityPattern = regexp.MustCompile(`@Entity\b`)
	jpaQueryPattern  = regexp.MustCompile(`@Query\b`)
	jpaRepoExtends   = regexp.MustCompile(
		`(?i)\b(?:extends|implements)\s+(?:[\w.]+\.)?(JpaRepository|CrudRepository|PagingAndSortingRepository|ListCrudRepository|ListPagingAndSortingRepository)\s*<\s*([A-Z][A-Za-z0-9_]*)\s*,`)
	jpaEntityManagerCall = regexp.MustCompile(
		`(?i)\.(?:find|getReference|getReferenceById|persist|merge|remove|refresh)\s*\(\s*([A-Z][A-Za-z0-9_]*)\s*(?:\.class\b|,)`)
	jpaNamedQueryFrom = regexp.MustCompile(
		`(?i)\bFROM\s+([A-Z][A-Za-z0-9_]*)\b`)
	springMappingAttrRe = regexp.MustCompile(
		`@(GetMapping|PostMapping|PutMapping|DeleteMapping|PatchMapping|RequestMapping)\b`)
)

func looksLikeJPAFile(relPath, content string) bool {
	p := strings.ToLower(filepath.ToSlash(relPath))
	body := content
	lower := strings.ToLower(body)
	if strings.Contains(lower, "jakarta.persistence") ||
		strings.Contains(lower, "javax.persistence") ||
		strings.Contains(lower, "org.hibernate") ||
		strings.Contains(body, "@Entity") ||
		strings.Contains(body, "@Table") ||
		strings.Contains(body, "@Query") ||
		strings.Contains(body, "JpaRepository") ||
		strings.Contains(body, "EntityManager") ||
		strings.Contains(body, "Hibernate") ||
		strings.Contains(p, "/entity/") || strings.Contains(p, "/entities/") ||
		strings.Contains(p, "/model/") || strings.Contains(p, "/models/") ||
		strings.Contains(p, "/repository/") || strings.Contains(p, "/repositories/") {
		return true
	}
	return false
}

func jpaRoleFromAnnotations(src string) string {
	switch {
	case jpaEntityPattern.MatchString(src):
		return "entity"
	case jpaQueryPattern.MatchString(src):
		return "query"
	case strings.Contains(src, "@Repository") || strings.Contains(src, "JpaRepository") ||
		strings.Contains(src, "CrudRepository"):
		return "repository"
	default:
		return ""
	}
}

func springMappingRole(src string) string {
	if springMappingAttrRe.MatchString(src) {
		return "entrypoint"
	}
	return ""
}

func emitJPACall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) || springSkipInjectType(name) {
		return
	}
	if name[0] < 'A' || name[0] > 'Z' {
		return
	}
	tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, fromSym, tgt, "calls"),
		RepoID:     repoID,
		Kind:       types.RefKindCalls,
		SourceID:   fromSym,
		TargetID:   tgt,
		Confidence: conf,
	})
}

// extractJPAEntity densifies @Entity classes: relation annotations → target
// type call edges (Owner → Pet / Account).
func extractJPAEntity(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil || classSym == "" || out == nil {
		return
	}
	text := classNode.Content(buf)
	if !jpaEntityPattern.MatchString(text) && !jpaRelationPattern.MatchString(text) {
		return
	}
	if !looksLikeJPAFile(relPath, text) && !jpaEntityPattern.MatchString(text) {
		return
	}
	seen := map[string]bool{}
	body := classNode.ChildByFieldName("body")
	if body == nil {
		return
	}
	Walk(body, func(n *sitter.Node) {
		if n.Type() != "field_declaration" {
			return
		}
		mods := ""
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "modifiers" {
				mods = c.Content(buf)
				break
			}
		}
		if !jpaRelationPattern.MatchString(mods) {
			return
		}
		typ := javaRelationTargetType(n, buf)
		if typ == "" || seen[typ] || springSkipInjectType(typ) {
			return
		}
		seen[typ] = true
		emitJPACall(repoID, relPath, classSym, typ, 0.86, out)
	})
}

// javaRelationTargetType prefers the entity leaf inside List/Set/Optional generics
// for @OneToMany/@ManyToOne fields (List<Pet> → Pet).
func javaRelationTargetType(field *sitter.Node, buf []byte) string {
	if field == nil {
		return ""
	}
	typNode := field.ChildByFieldName("type")
	if typNode == nil {
		return ""
	}
	var ids []string
	Walk(typNode, func(c *sitter.Node) {
		if c.Type() == "type_identifier" {
			ids = append(ids, strings.TrimSpace(c.Content(buf)))
		}
	})
	for i := len(ids) - 1; i >= 0; i-- {
		if ids[i] != "" && !springSkipInjectType(ids[i]) {
			return ids[i]
		}
	}
	return javaLeafTypeName(typNode, buf)
}

// extractJpaRepository densifies interfaces extending JpaRepository<Entity,Id>
// and @Query methods → entity leaves (+ JPQL FROM Entity).
func extractJpaRepository(typeNode *sitter.Node, buf []byte, repoID, relPath, typeSym string, out *ParseResult) {
	if typeNode == nil || typeSym == "" || out == nil {
		return
	}
	text := typeNode.Content(buf)
	window := text
	from := int(typeNode.StartByte()) - 200
	if from < 0 {
		from = 0
	}
	window = string(buf[from:int(typeNode.EndByte())])
	if !looksLikeJPAFile(relPath, window) &&
		!strings.Contains(text, "JpaRepository") &&
		!strings.Contains(text, "CrudRepository") &&
		!jpaQueryPattern.MatchString(text) {
		return
	}
	seen := map[string]bool{}
	add := func(tok string, conf float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] || springSkipInjectType(tok) {
			return
		}
		seen[tok] = true
		emitJPACall(repoID, relPath, typeSym, tok, conf, out)
	}
	for _, m := range jpaRepoExtends.FindAllStringSubmatch(text, -1) {
		if len(m) > 2 {
			add(m[2], 0.9)
		}
	}
	// Also walk type_parameters / superclass_interfaces for generic type args.
	if si := typeNode.ChildByFieldName("interfaces"); si != nil {
		Walk(si, func(c *sitter.Node) {
			if c.Type() != "generic_type" && c.Type() != "type_identifier" {
				return
			}
			s := c.Content(buf)
			if !strings.Contains(s, "Repository") {
				return
			}
			if args := c.ChildByFieldName("arguments"); args != nil {
				for i := 0; i < int(args.NamedChildCount()); i++ {
					if leaf := javaLeafTypeName(args.NamedChild(i), buf); leaf != "" && !springSkipInjectType(leaf) {
						add(leaf, 0.9)
						break // first type arg is the entity
					}
				}
			}
		})
	}
	for _, m := range jpaNamedQueryFrom.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			add(m[1], 0.82)
		}
	}
}

// extractJPAEntityManagerUsage wires EntityManager.find(Owner.class) style
// calls from enclosing methods to entity leaves.
func extractJPAEntityManagerUsage(repoID, relPath string, buf []byte, out *ParseResult) {
	if out == nil || !looksLikeJPAFile(relPath, string(buf)) {
		return
	}
	src := string(buf)
	if !strings.Contains(src, "EntityManager") && !jpaEntityManagerCall.MatchString(src) {
		return
	}
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		for _, m := range jpaEntityManagerCall.FindAllStringSubmatch(line, -1) {
			if len(m) < 2 {
				continue
			}
			from := enclosingSymbolAtLine(out, i+1)
			if from == "" {
				continue
			}
			emitJPACall(repoID, relPath, from, m[1], 0.88, out)
		}
	}
}

// javaMethodAnnotationWindow returns modifiers text on a method node.
func javaMethodAnnotationWindow(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "modifiers" {
			return c.Content(buf)
		}
	}
	from := int(n.StartByte()) - 160
	if from < 0 {
		from = 0
	}
	return string(buf[from:int(n.StartByte())])
}
