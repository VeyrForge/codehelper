package parser

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Spring stereotype / DI densification shared by Java and Kotlin parsers.
// Mirrors Nest DI: emit call edges from bean classes to injected types so
// context/impact/trace see Controller→Service→Repository relationships.

var (
	springStereotypeRe = regexp.MustCompile(
		`@(?:RestController|Controller|Service|Repository|Component|Configuration|SpringBootApplication|ControllerAdvice|RestControllerAdvice|Bean)\b`)
	springAutowiredRe = regexp.MustCompile(
		`@(?:Autowired|Inject|Resource|Value)\b`)
	springRoleRe = regexp.MustCompile(
		`@(RestController|Controller|Service|Repository|Component|Configuration|SpringBootApplication|ControllerAdvice|RestControllerAdvice)\b`)
)

// springRoleFromAnnotations returns a signature role for Spring stereotypes.
// Prefer the last stereotype in src so class-local @RestController wins over a
// preceding @Service from an earlier type in a shared scan window.
func springRoleFromAnnotations(src string) string {
	matches := springRoleRe.FindAllStringSubmatch(src, -1)
	if len(matches) == 0 {
		return ""
	}
	m := matches[len(matches)-1]
	if len(m) < 2 {
		return ""
	}
	switch m[1] {
	case "RestController", "Controller", "ControllerAdvice", "RestControllerAdvice":
		return "controller"
	case "Service":
		return "service"
	case "Repository":
		return "repository"
	case "Component":
		return "component"
	case "Configuration", "Bean":
		return "configuration"
	case "SpringBootApplication":
		return "application"
	default:
		return strings.ToLower(m[1])
	}
}

// javaClassAnnotationWindow returns modifiers text immediately on the type node
// (not prior sibling types in the file).
func javaClassAnnotationWindow(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "modifiers" {
			return c.Content(buf)
		}
	}
	// Fallback: small prefix immediately before the type keyword.
	from := int(n.StartByte()) - 120
	if from < 0 {
		from = 0
	}
	return string(buf[from:int(n.StartByte())])
}

func looksLikeSpringFile(relPath, content string) bool {
	p := strings.ToLower(relPath)
	body := content
	if strings.Contains(body, "org.springframework") ||
		strings.Contains(body, "org.springframework.") ||
		springStereotypeRe.MatchString(body) ||
		springAutowiredRe.MatchString(body) ||
		strings.Contains(p, "application.properties") ||
		strings.Contains(p, "application.yml") ||
		strings.Contains(p, "application.yaml") {
		return true
	}
	return false
}

func springSkipInjectType(tok string) bool {
	switch tok {
	case "String", "Integer", "Long", "Boolean", "Double", "Float", "Short", "Byte",
		"Character", "Object", "Class", "Void", "Number", "Int",
		"List", "Map", "Set", "Collection", "Iterable", "Iterator", "Optional",
		"ArrayList", "HashMap", "HashSet", "LinkedList", "ConcurrentHashMap",
		"Page", "Pageable", "PageRequest", "Sort", "Slice",
		"Model", "ModelMap", "ModelAndView", "BindingResult", "Errors",
		"HttpServletRequest", "HttpServletResponse", "HttpSession",
		"RedirectAttributes", "UriComponentsBuilder", "Locale",
		"PathVariable", "RequestParam", "RequestBody", "ResponseBody",
		"Valid", "NotNull", "Nullable",
		"Unit", "Any", "Nothing",
		"Serializable", "Cloneable", "Comparable", "AutoCloseable",
		"Override", "Deprecated", "SuppressWarnings", "SafeVarargs":
		return true
	default:
		return false
	}
}

func emitSpringCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
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

// javaLeafTypeName returns the simple type head (OwnerRepository, List→inner if generic).
func javaLeafTypeName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "type_identifier":
		return strings.TrimSpace(n.Content(buf))
	case "scoped_type_identifier":
		// org.example.Foo → Foo
		if c := n.ChildByFieldName("name"); c != nil {
			return strings.TrimSpace(c.Content(buf))
		}
		s := strings.TrimSpace(n.Content(buf))
		if i := strings.LastIndex(s, "."); i >= 0 {
			return s[i+1:]
		}
		return s
	case "generic_type", "type_arguments":
		// Prefer the type arguments' leaf when present (Page<Owner> → Owner),
		// else the raw type (List → List, skipped later for DI).
		if args := n.ChildByFieldName("arguments"); args != nil {
			for i := 0; i < int(args.NamedChildCount()); i++ {
				if leaf := javaLeafTypeName(args.NamedChild(i), buf); leaf != "" && !springSkipInjectType(leaf) {
					return leaf
				}
			}
		}
		// tree-sitter-java often exposes type_arguments as an unnamed child.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Type() == "type_arguments" || c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" || c.Type() == "generic_type" {
				if leaf := javaLeafTypeName(c, buf); leaf != "" && !springSkipInjectType(leaf) {
					return leaf
				}
			}
		}
		if t := n.ChildByFieldName("type"); t != nil {
			return javaLeafTypeName(t, buf)
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if leaf := javaLeafTypeName(n.NamedChild(i), buf); leaf != "" {
				return leaf
			}
		}
	case "array_type", "type":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if leaf := javaLeafTypeName(n.NamedChild(i), buf); leaf != "" {
				return leaf
			}
		}
	}
	return ""
}

// javaCollectFieldTypes maps field / ctor-param names → type leaf for typed calls.
func javaCollectFieldTypes(classNode *sitter.Node, buf []byte) map[string]string {
	out := map[string]string{}
	if classNode == nil {
		return out
	}
	body := classNode.ChildByFieldName("body")
	if body == nil {
		return out
	}
	Walk(body, func(n *sitter.Node) {
		switch n.Type() {
		case "field_declaration":
			typ := javaLeafTypeName(n.ChildByFieldName("type"), buf)
			if typ == "" {
				return
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c == nil || c.Type() != "variable_declarator" {
					continue
				}
				if nm := c.ChildByFieldName("name"); nm != nil {
					out[strings.TrimSpace(nm.Content(buf))] = typ
				}
			}
		case "constructor_declaration":
			params := n.ChildByFieldName("parameters")
			if params == nil {
				return
			}
			Walk(params, func(p *sitter.Node) {
				if p.Type() != "formal_parameter" {
					return
				}
				typ := javaLeafTypeName(p.ChildByFieldName("type"), buf)
				nm := ""
				if name := p.ChildByFieldName("name"); name != nil {
					nm = strings.TrimSpace(name.Content(buf))
				}
				if typ != "" && nm != "" {
					out[nm] = typ
				}
			})
		}
	})
	return out
}

func javaTypeOf(className string, fields map[string]string) func(string) string {
	if className == "" && len(fields) == 0 {
		return nil
	}
	return func(recv string) string {
		recv = strings.TrimSpace(recv)
		if recv == "" {
			return ""
		}
		if recv == "this" || recv == "super" {
			return className
		}
		if strings.HasPrefix(recv, "this.") {
			recv = strings.TrimPrefix(recv, "this.")
		} else if strings.HasPrefix(recv, "super.") {
			recv = strings.TrimPrefix(recv, "super.")
		}
		// Peel nested field access: this.owners → owners (already), owners.x → owners
		if i := strings.IndexByte(recv, '.'); i > 0 {
			recv = recv[:i]
		}
		if typ, ok := fields[recv]; ok {
			return typ
		}
		return ""
	}
}

// extractSpringJavaDI emits Controller/Service/Repository injection call edges
// from constructor params, @Autowired fields, and typed bean fields.
func extractSpringJavaDI(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil || classSym == "" || out == nil {
		return
	}
	// Window: annotations preceding the class + class body.
	from := int(classNode.StartByte()) - 400
	if from < 0 {
		from = 0
	}
	window := string(buf[from:int(classNode.EndByte())])
	if !looksLikeSpringFile(relPath, window) && !springStereotypeRe.MatchString(window) && !springAutowiredRe.MatchString(window) {
		// Still densify constructor param types on any Java class — cheap and useful.
	}
	seen := map[string]bool{}
	add := func(tok string, conf float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] || springSkipInjectType(tok) {
			return
		}
		seen[tok] = true
		emitSpringCall(repoID, relPath, classSym, tok, conf, out)
	}

	body := classNode.ChildByFieldName("body")
	if body != nil {
		Walk(body, func(n *sitter.Node) {
			switch n.Type() {
			case "constructor_declaration":
				params := n.ChildByFieldName("parameters")
				if params == nil {
					return
				}
				Walk(params, func(p *sitter.Node) {
					if p.Type() != "formal_parameter" {
						return
					}
					if typ := javaLeafTypeName(p.ChildByFieldName("type"), buf); typ != "" {
						add(typ, 0.85)
					}
				})
			case "field_declaration":
				mods := ""
				for i := 0; i < int(n.ChildCount()); i++ {
					c := n.Child(i)
					if c != nil && c.Type() == "modifiers" {
						mods = c.Content(buf)
						break
					}
				}
				typ := javaLeafTypeName(n.ChildByFieldName("type"), buf)
				if typ == "" {
					return
				}
				if springAutowiredRe.MatchString(mods) {
					add(typ, 0.82)
					return
				}
				// Constructor-injection style: private final Dep dep; on a Spring bean.
				if springStereotypeRe.MatchString(window) &&
					(strings.Contains(mods, "final") || strings.Contains(mods, "private")) {
					add(typ, 0.78)
				}
			case "method_declaration":
				// Setter injection: @Autowired void setOwners(OwnerRepository owners)
				mods := ""
				for i := 0; i < int(n.ChildCount()); i++ {
					c := n.Child(i)
					if c != nil && c.Type() == "modifiers" {
						mods = c.Content(buf)
						break
					}
				}
				if !springAutowiredRe.MatchString(mods) {
					return
				}
				name := ChildName(n, "name", buf)
				if !strings.HasPrefix(name, "set") {
					return
				}
				params := n.ChildByFieldName("parameters")
				if params == nil {
					return
				}
				Walk(params, func(p *sitter.Node) {
					if p.Type() != "formal_parameter" {
						return
					}
					if typ := javaLeafTypeName(p.ChildByFieldName("type"), buf); typ != "" {
						add(typ, 0.8)
					}
				})
			}
		})
	}
}

func javaEmitInheritance(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if n == nil || out == nil {
		return
	}
	emit := func(tok string, kind types.ReferenceKind, conf float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" || springSkipInjectType(tok) {
			return
		}
		if r := []rune(tok); len(r) == 0 || !unicode.IsUpper(r[0]) {
			return
		}
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
	if sc := n.ChildByFieldName("superclass"); sc != nil {
		Walk(sc, func(c *sitter.Node) {
			if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" {
				emit(javaLeafTypeName(c, buf), types.RefKindInherits, 0.9)
			}
		})
	}
	if si := n.ChildByFieldName("interfaces"); si != nil {
		Walk(si, func(c *sitter.Node) {
			if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" {
				emit(javaLeafTypeName(c, buf), types.RefKindImplements, 0.9)
			}
		})
	}
}

// extractSpringKotlinDI densifies primary-constructor / property injection for Kotlin Spring.
func extractSpringKotlinDI(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil || classSym == "" || out == nil {
		return
	}
	from := int(classNode.StartByte()) - 400
	if from < 0 {
		from = 0
	}
	window := string(buf[from:int(classNode.EndByte())])
	seen := map[string]bool{}
	add := func(tok string, conf float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] || springSkipInjectType(tok) {
			return
		}
		// Kotlin type may be Foo? or Foo<Bar>
		tok = strings.TrimSuffix(tok, "?")
		if i := strings.IndexAny(tok, "<["); i > 0 {
			tok = tok[:i]
		}
		if i := strings.LastIndex(tok, "."); i >= 0 {
			tok = tok[i+1:]
		}
		if tok == "" || seen[tok] || springSkipInjectType(tok) {
			return
		}
		seen[tok] = true
		emitSpringCall(repoID, relPath, classSym, tok, conf, out)
	}

	isSpring := springStereotypeRe.MatchString(window) || springAutowiredRe.MatchString(window) ||
		strings.Contains(window, "org.springframework")

	Walk(classNode, func(n *sitter.Node) {
		switch n.Type() {
		case "class_parameter", "parameter":
			// primary ctor: class OwnerController(private val pets: PetService)
			if t := kotlinParamTypeName(n, buf); t != "" {
				add(t, 0.85)
			}
		case "property_declaration":
			mods := n.Content(buf)
			t := kotlinPropertyTypeName(n, buf)
			if t == "" {
				return
			}
			if springAutowiredRe.MatchString(mods) {
				add(t, 0.82)
				return
			}
			if isSpring && (strings.Contains(mods, "val ") || strings.Contains(mods, "lateinit")) {
				add(t, 0.78)
			}
		}
	})
}

func kotlinParamTypeName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "user_type", "nullable_type", "type_identifier":
			return kotlinTypeLeaf(c, buf)
		}
	}
	return ""
}

func kotlinPropertyTypeName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "user_type", "nullable_type", "type_identifier":
			return kotlinTypeLeaf(c, buf)
		}
	}
	return ""
}

func kotlinTypeLeaf(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "type_identifier", "simple_identifier":
		return strings.TrimSpace(n.Content(buf))
	case "nullable_type":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if leaf := kotlinTypeLeaf(n.NamedChild(i), buf); leaf != "" {
				return leaf
			}
		}
	case "user_type":
		var last string
		Walk(n, func(c *sitter.Node) {
			if c.Type() == "type_identifier" || c.Type() == "simple_identifier" {
				last = strings.TrimSpace(c.Content(buf))
			}
		})
		return last
	}
	s := strings.TrimSpace(n.Content(buf))
	s = strings.TrimSuffix(s, "?")
	if i := strings.IndexAny(s, "<["); i > 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func kotlinCollectFieldTypes(classNode *sitter.Node, buf []byte) map[string]string {
	out := map[string]string{}
	if classNode == nil {
		return out
	}
	Walk(classNode, func(n *sitter.Node) {
		switch n.Type() {
		case "class_parameter", "parameter":
			name := ""
			typ := ""
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c == nil {
					continue
				}
				switch c.Type() {
				case "simple_identifier":
					if name == "" {
						name = strings.TrimSpace(c.Content(buf))
					}
				case "user_type", "nullable_type", "type_identifier":
					typ = kotlinTypeLeaf(c, buf)
				}
			}
			if name != "" && typ != "" {
				out[name] = typ
			}
		case "property_declaration":
			name := ""
			typ := kotlinPropertyTypeName(n, buf)
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c != nil && c.Type() == "variable_declaration" {
					for j := 0; j < int(c.ChildCount()); j++ {
						cc := c.Child(j)
						if cc != nil && cc.Type() == "simple_identifier" {
							name = strings.TrimSpace(cc.Content(buf))
							break
						}
					}
				}
				if c != nil && c.Type() == "simple_identifier" && name == "" {
					name = strings.TrimSpace(c.Content(buf))
				}
			}
			if name != "" && typ != "" {
				out[name] = typ
			}
		}
	})
	return out
}

func kotlinTypeOf(className string, fields map[string]string) func(string) string {
	if className == "" && len(fields) == 0 {
		return nil
	}
	return func(recv string) string {
		recv = strings.TrimSpace(recv)
		if recv == "" {
			return ""
		}
		if recv == "this" || recv == "super" {
			return className
		}
		if strings.HasPrefix(recv, "this.") {
			recv = strings.TrimPrefix(recv, "this.")
		}
		if i := strings.IndexByte(recv, '.'); i > 0 {
			recv = recv[:i]
		}
		if typ, ok := fields[recv]; ok {
			return typ
		}
		return ""
	}
}
