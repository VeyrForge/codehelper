package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	php "github.com/smacker/go-tree-sitter/php"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	// Verb + common live-app wrappers (middleware/group/prefix/view/…). Without
	// these, Route::middleware/group are dropped — the facade loop skips Route.
	laravelRoutePattern   = regexp.MustCompile(`(?i)Route::(get|post|put|patch|delete|options|any|match|resource|apiResource|middleware|group|prefix|name|controller|view|redirect|fallback)\s*\(`)
	laravelRouteAction    = regexp.MustCompile(`(?i)\[\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*,\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]\s*\]`)
	laravelRouteInvokable = regexp.MustCompile(`(?i)([A-Za-z_\\][A-Za-z0-9_\\]*Controller)\s*::\s*class`)
	laravelRouteString    = regexp.MustCompile(`(?i)['"]([A-Za-z_\\][A-Za-z0-9_\\]*)@([A-Za-z_][A-Za-z0-9_]*)['"]`)
	laravelFacadeCall     = regexp.MustCompile(`\b(Route|Hash|Schema|DB|Cache|Auth|Storage|Log|Gate|Event|Mail|Queue|Redis|Http|Cookie|Session|Validator|View|File|Artisan|Blade|Config|Crypt|Notification|Password|Redirect|Response|URL|Str|Arr)\s*::\s*([A-Za-z_][A-Za-z0-9_]*)`)
	laravelBootstrapCall  = regexp.MustCompile(`(?i)\b(Application|Middleware|Exceptions)\s*::\s*([A-Za-z_][A-Za-z0-9_]*)`)
	laravelWithMethod     = regexp.MustCompile(`(?i)->\s*(withRouting|withMiddleware|withExceptions|withProviders|withCommands|withSchedule|create)\s*\(`)
	laravelExtendsForm    = regexp.MustCompile(`(?i)class\s+(\w+)\s+extends\s+FormRequest\b`)
	laravelAppBind        = regexp.MustCompile(`(?i)\$this\s*->\s*app\s*->\s*(?:bind|singleton|instance)\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*,\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	symfonyRouteAttr      = regexp.MustCompile(`(?i)#\[\s*Route\s*\(`)
	symfonyRouteMethod    = regexp.MustCompile(`(?i)(?:public|protected|private)\s+function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	symfonyCtorOpen       = regexp.MustCompile(`(?i)function\s+__construct\s*\(`)
	symfonyTypedParam     = regexp.MustCompile(`(?i)(?:private|protected|public|readonly)\s+(?:readonly\s+)?([A-Z][A-Za-z0-9_\\]*)\s+\$|([A-Z][A-Za-z0-9_\\]*)\s+\$[A-Za-z_]`)
	symfonyClassDecl      = regexp.MustCompile(`(?i)class\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	wpHookOpen            = regexp.MustCompile(`(?i)^add_(action|filter)\s*\(`)
	wpHookPattern         = regexp.MustCompile(`(?i)add_(action|filter)\s*\(\s*['"]([^'"]+)['"]\s*,\s*(.+)\)\s*;?\s*$`)
	wpFirePattern         = regexp.MustCompile(`(?i)\b(do_action|apply_filters|do_action_ref_array|apply_filters_ref_array)\s*\(\s*['"]([^'"]+)['"]`)
	wpRegisterHookPattern = regexp.MustCompile(`(?i)register_(activation|deactivation)_hook\s*\(\s*[^,]+,\s*([^\)]+)\)`)
	wpShortcodePattern    = regexp.MustCompile(`(?i)add_shortcode\s*\(\s*['"]([^'"]+)['"]\s*,\s*([^\)]+)\)`)
	wpArrayCallback       = regexp.MustCompile(`(?i)(?:array\s*\(|\[)\s*([^\],]+)\s*,\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]\s*(?:\)|\])`)
)

var laravelFacadeConcrete = map[string]string{
	"Artisan": "Artisan", "Auth": "AuthManager", "Blade": "Blade",
	"Cache": "CacheManager", "Config": "Repository", "Cookie": "CookieJar",
	"Crypt": "Encrypter", "DB": "DatabaseManager", "Event": "Dispatcher",
	"File": "Filesystem", "Gate": "Gate", "Hash": "HashManager",
	"Http": "Factory", "Log": "LogManager", "Mail": "MailManager",
	"Notification": "ChannelManager", "Password": "PasswordBrokerManager",
	"Queue": "QueueManager", "Redirect": "Redirector", "Redis": "RedisManager",
	"Response": "ResponseFactory", "Route": "Router", "Schema": "Builder",
	"Session": "SessionManager", "Storage": "FilesystemManager",
	"URL": "UrlGenerator", "Validator": "Factory", "View": "Factory",
	// Helpers (same leaf as facade — still densifies Facade::m → leaf.m).
	"Arr": "Arr", "Str": "Str",
}

// ParsePHP extracts classes, methods, and functions.
func ParsePHP(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(php.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	frameworks := DetectFrameworkPacks(relPath, nil, string(buf))
	phpAliases := map[string]string{} // use-alias → FQCN leaf (Authenticatable → User)
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "namespace_use_declaration", "use_statement":
			// tree-sitter-php nests imports under namespace_use_clause →
			// qualified_name (not a direct "name" child). Segment-only "name"
			// nodes under the FQCN must not become import edges. Aliases
			// (`use Foo\Bar as Baz`) map Baz → Bar for extends/implements.
			for _, cl := range phpUseClauses(n, buf) {
				mod := cl.FQCN
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.85,
				})
				if cl.Alias != "" {
					phpAliases[cl.Alias] = phpFQCNLeaf(mod)
				}
			}
		case "use_declaration":
			// Trait use inside a class: `use HasFactory, Notifiable;`
			for _, mod := range phpTraitUseNames(n, buf) {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.8,
				})
			}
		case "function_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "method_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			parent := phpEnclosingTypeName(n, buf)
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, ""), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			// $this->m() / self::m() → Parent.m; $this->dep->m() → Dep.m when typed.
			typeOf := phpMethodTypeOf(n, buf, parent)
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, typeOf)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "class_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			embeds := phpCollectEmbedNames(n, buf, phpAliases)
			sig := appendEmbedsSig(frameworkSignature(frameworks, ""), embeds)
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			phpEmitInheritance(n, buf, repoID, relPath, sym.ID, out, phpAliases)
			phpEmitTraitUses(n, buf, repoID, relPath, sym.ID, out, phpAliases)
		case "trait_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, "trait"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		case "interface_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			embeds := phpCollectEmbedNames(n, buf, phpAliases)
			sig := appendEmbedsSig(frameworkSignature(frameworks, ""), embeds)
			sym := symbol(repoID, relPath, name, types.SymbolKindInterface, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			phpEmitInheritance(n, buf, repoID, relPath, sym.ID, out, phpAliases)
		case "simple_assignment_expression":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left == nil {
				return
			}
			name := sanitizeCallbackName(left.Content(buf))
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, "state"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if right != nil {
				addReadEdgesFromNode(repoID, relPath, sym.ID, right, buf, out)
			}
		}
	})
	addPHPFrameworkSymbols(repoID, relPath, buf, out, frameworks)
	addSymfonyFrameworkSymbols(repoID, relPath, buf, out, frameworks)
	// Laravel/WordPress app densify runs last: both attribute references to the
	// nearest enclosing symbol, including the route/hook sites minted above.
	addLaravelAppSymbols(repoID, relPath, buf, out, frameworks)
	addWordPressAppSymbols(repoID, relPath, buf, out, frameworks)
	return out, nil
}

// phpUseClause is one `use FQCN [as Alias]` binding.
type phpUseClause struct {
	FQCN  string
	Alias string // empty when no `as` clause
}

// phpUseClauses returns FQCN (+ optional alias) from a namespace_use_declaration.
func phpUseClauses(n *sitter.Node, buf []byte) []phpUseClause {
	if n == nil {
		return nil
	}
	var out []phpUseClause
	seen := map[string]bool{}
	add := func(fqcn, alias string) {
		fqcn = strings.TrimSpace(strings.TrimPrefix(fqcn, `\`))
		alias = strings.TrimSpace(alias)
		if fqcn == "" || seen[fqcn+"\x00"+alias] {
			return
		}
		seen[fqcn+"\x00"+alias] = true
		out = append(out, phpUseClause{FQCN: fqcn, Alias: alias})
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "namespace_use_clause":
			fqcn := ""
			if q := childOfType(c, "qualified_name"); q != nil {
				fqcn = q.Content(buf)
			} else if nm := childOfType(c, "name"); nm != nil {
				fqcn = nm.Content(buf)
			}
			alias := ""
			if al := childOfType(c, "namespace_aliasing_clause"); al != nil {
				if nm := childOfType(al, "name"); nm != nil {
					alias = nm.Content(buf)
				}
			}
			add(fqcn, alias)
		case "namespace_use_group":
			// use Foo\{Bar, Baz as Qux};
			prefix := ""
			if q := childOfType(c, "namespace_name"); q != nil {
				prefix = strings.TrimSpace(q.Content(buf))
			}
			for j := 0; j < int(c.ChildCount()); j++ {
				cl := c.Child(j)
				if cl == nil || cl.Type() != "namespace_use_group_clause" {
					continue
				}
				leaf := ""
				if q := childOfType(cl, "qualified_name"); q != nil {
					leaf = q.Content(buf)
				} else if nm := childOfType(cl, "name"); nm != nil {
					leaf = nm.Content(buf)
				}
				leaf = strings.TrimSpace(leaf)
				if leaf == "" {
					continue
				}
				alias := ""
				if al := childOfType(cl, "namespace_aliasing_clause"); al != nil {
					if nm := childOfType(al, "name"); nm != nil {
						alias = nm.Content(buf)
					}
				}
				if prefix != "" {
					add(prefix+`\`+strings.TrimPrefix(leaf, `\`), alias)
				} else {
					add(leaf, alias)
				}
			}
		}
	}
	return out
}

// phpUseImportNames returns FQCNs from a namespace_use_declaration / use_statement.
func phpUseImportNames(n *sitter.Node, buf []byte) []string {
	clauses := phpUseClauses(n, buf)
	out := make([]string, 0, len(clauses))
	for _, cl := range clauses {
		out = append(out, cl.FQCN)
	}
	return out
}

func phpFQCNLeaf(fqcn string) string {
	fqcn = strings.TrimSpace(strings.TrimPrefix(fqcn, `\`))
	if i := strings.LastIndex(fqcn, `\`); i >= 0 {
		return fqcn[i+1:]
	}
	return fqcn
}

// phpResolveTypeLeaf strips FQCN to leaf and remaps use-aliases (Authenticatable→User).
func phpResolveTypeLeaf(name string, aliases map[string]string) string {
	name = phpFQCNLeaf(name)
	if aliases != nil {
		if leaf, ok := aliases[name]; ok && leaf != "" {
			name = leaf
		}
	}
	return name
}

// appendEmbedsSig appends embeds=Parent,Trait to a symbol Signature (Go/PHP/Ruby).
func appendEmbedsSig(sig string, embeds []string) string {
	var cleaned []string
	seen := map[string]bool{}
	for _, e := range embeds {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		cleaned = append(cleaned, e)
	}
	if len(cleaned) == 0 {
		return sig
	}
	part := "embeds=" + strings.Join(cleaned, ",")
	if sig == "" {
		return part
	}
	if strings.Contains(sig, "embeds=") {
		// Merge into existing embeds= fragment.
		i := strings.Index(sig, "embeds=")
		rest := sig[i+len("embeds="):]
		end := len(rest)
		for j := 0; j < len(rest); j++ {
			if rest[j] == ' ' || rest[j] == ';' {
				end = j
				break
			}
		}
		existing := strings.Split(rest[:end], ",")
		for _, e := range existing {
			e = strings.TrimSpace(e)
			if e == "" || seen[e] {
				continue
			}
			seen[e] = true
			cleaned = append([]string{e}, cleaned...)
		}
		prefix := sig[:i]
		suffix := rest[end:]
		return prefix + "embeds=" + strings.Join(cleaned, ",") + suffix
	}
	return sig + " " + part
}

// phpCollectEmbedNames returns parent/interface/trait leaf names for embeds= densification.
func phpCollectEmbedNames(n *sitter.Node, buf []byte, aliases map[string]string) []string {
	if n == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		name := phpResolveTypeLeaf(raw, aliases)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "base_clause", "class_interface_clause":
			for j := 0; j < int(c.NamedChildCount()); j++ {
				ch := c.NamedChild(j)
				if ch == nil {
					continue
				}
				if ch.Type() == "name" || ch.Type() == "qualified_name" {
					add(ch.Content(buf))
				}
			}
		}
	}
	body := n.ChildByFieldName("body")
	if body == nil {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "declaration_list" {
				body = c
				break
			}
		}
	}
	if body != nil {
		for i := 0; i < int(body.ChildCount()); i++ {
			c := body.Child(i)
			if c == nil || c.Type() != "use_declaration" {
				continue
			}
			for _, name := range phpTraitUseNames(c, buf) {
				add(name)
			}
		}
	}
	return out
}

// phpThisTypeOf maps $this / self / static receivers to the enclosing type name.
func phpThisTypeOf(parent string) func(string) string {
	return phpTypeOf(parent, nil)
}

// phpMethodTypeOf merges enclosing-class typed properties / ctor params and
// the method's own typed parameters so `$this->logger->info()` / `$orders->create()`
// resolve to Logger.info / OrderService.create when types are local.
func phpMethodTypeOf(method *sitter.Node, buf []byte, parent string) func(string) string {
	fields := map[string]string{}
	if cls := phpEnclosingClassNode(method); cls != nil {
		fields = phpCollectFieldTypes(cls, buf)
	}
	for name, typ := range phpCollectMethodParamTypes(method, buf) {
		if _, ok := fields[name]; !ok {
			fields[name] = typ
		}
	}
	return phpTypeOf(parent, fields)
}

// phpCollectMethodParamTypes maps $param → type leaf for typed receivers
// inside a method body (OrderService $orders → $orders->create()).
func phpCollectMethodParamTypes(method *sitter.Node, buf []byte) map[string]string {
	out := map[string]string{}
	if method == nil {
		return out
	}
	params := method.ChildByFieldName("parameters")
	if params == nil {
		return out
	}
	add := func(name, typ string) {
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "$")
		typ = phpSimpleName(strings.TrimSpace(typ))
		if name == "" || typ == "" || phpSkipTypedField(typ) {
			return
		}
		if _, ok := out[name]; !ok {
			out[name] = typ
		}
	}
	Walk(params, func(p *sitter.Node) {
		switch p.Type() {
		case "simple_parameter", "property_promotion_parameter", "variadic_parameter":
		default:
			return
		}
		nm := ""
		if name := p.ChildByFieldName("name"); name != nil {
			nm = strings.TrimSpace(name.Content(buf))
		}
		if nm == "" {
			for i := 0; i < int(p.NamedChildCount()); i++ {
				c := p.NamedChild(i)
				if c != nil && c.Type() == "variable_name" {
					nm = strings.TrimSpace(c.Content(buf))
					break
				}
			}
		}
		typ := ""
		if t := p.ChildByFieldName("type"); t != nil {
			typ = t.Content(buf)
		}
		if typ == "" {
			Walk(p, func(c *sitter.Node) {
				if typ != "" {
					return
				}
				switch c.Type() {
				case "named_type", "primitive_type", "name", "qualified_name":
					cand := strings.TrimSpace(c.Content(buf))
					if cand != "" && cand[0] >= 'A' && cand[0] <= 'Z' {
						typ = cand
					}
				}
			})
		}
		add(nm, typ)
	})
	return out
}

func phpEnclosingClassNode(n *sitter.Node) *sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "class_declaration", "interface_declaration", "trait_declaration":
			return p
		}
	}
	return nil
}

// phpCollectFieldTypes maps $prop / promoted ctor params → type leaf.
func phpCollectFieldTypes(classNode *sitter.Node, buf []byte) map[string]string {
	out := map[string]string{}
	if classNode == nil {
		return out
	}
	body := classNode.ChildByFieldName("body")
	if body == nil {
		return out
	}
	add := func(name, typ string) {
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "$")
		typ = phpSimpleName(strings.TrimSpace(typ))
		if name == "" || typ == "" || phpSkipTypedField(typ) {
			return
		}
		if _, ok := out[name]; !ok {
			out[name] = typ
		}
	}
	phpParamNameType := func(p *sitter.Node) {
		if p == nil {
			return
		}
		switch p.Type() {
		case "simple_parameter", "property_promotion_parameter", "variadic_parameter":
		default:
			return
		}
		nm := ""
		if name := p.ChildByFieldName("name"); name != nil {
			nm = strings.TrimSpace(name.Content(buf))
		}
		if nm == "" {
			for i := 0; i < int(p.NamedChildCount()); i++ {
				c := p.NamedChild(i)
				if c != nil && c.Type() == "variable_name" {
					nm = strings.TrimSpace(c.Content(buf))
					break
				}
			}
		}
		typ := ""
		if t := p.ChildByFieldName("type"); t != nil {
			typ = t.Content(buf)
		}
		if typ == "" {
			Walk(p, func(c *sitter.Node) {
				if typ != "" {
					return
				}
				switch c.Type() {
				case "named_type", "primitive_type", "name", "qualified_name":
					cand := strings.TrimSpace(c.Content(buf))
					if cand != "" && cand[0] >= 'A' && cand[0] <= 'Z' {
						typ = cand
					}
				}
			})
		}
		add(nm, typ)
	}
	Walk(body, func(n *sitter.Node) {
		switch n.Type() {
		case "property_declaration":
			typ := ""
			if t := n.ChildByFieldName("type"); t != nil {
				typ = t.Content(buf)
			}
			if typ == "" {
				Walk(n, func(c *sitter.Node) {
					if typ != "" {
						return
					}
					switch c.Type() {
					case "named_type", "name", "qualified_name":
						cand := strings.TrimSpace(c.Content(buf))
						if cand != "" && cand[0] >= 'A' && cand[0] <= 'Z' {
							typ = cand
						}
					}
				})
			}
			Walk(n, func(c *sitter.Node) {
				if c.Type() != "property_element" && c.Type() != "variable_name" {
					return
				}
				nm := strings.TrimSpace(c.Content(buf))
				if c.Type() == "property_element" {
					if vn := c.ChildByFieldName("name"); vn != nil {
						nm = strings.TrimSpace(vn.Content(buf))
					} else {
						for i := 0; i < int(c.NamedChildCount()); i++ {
							ch := c.NamedChild(i)
							if ch != nil && ch.Type() == "variable_name" {
								nm = strings.TrimSpace(ch.Content(buf))
								break
							}
						}
					}
				}
				add(nm, typ)
			})
		case "method_declaration":
			if ChildName(n, "name", buf) != "__construct" {
				return
			}
			params := n.ChildByFieldName("parameters")
			if params == nil {
				return
			}
			Walk(params, func(p *sitter.Node) {
				phpParamNameType(p)
			})
		}
	})
	return out
}

func phpSkipTypedField(tok string) bool {
	switch strings.ToLower(tok) {
	case "string", "int", "float", "bool", "array", "object", "mixed", "iterable",
		"callable", "void", "never", "true", "false", "null", "self", "static", "parent":
		return true
	default:
		return false
	}
}

// phpTypeOf resolves $this/self/static, typed $this->dep, and bare $dep receivers.
func phpTypeOf(parent string, fields map[string]string) func(string) string {
	if parent == "" && len(fields) == 0 {
		return nil
	}
	return func(recv string) string {
		recv = strings.TrimSpace(recv)
		switch recv {
		case "$this", "this", "self", "static", "$self":
			return parent
		}
		// Peel $this->dep / $this->dep->x → dep
		for _, pref := range []string{"$this->", "this->", "self::", "static::"} {
			if strings.HasPrefix(recv, pref) {
				recv = strings.TrimPrefix(recv, pref)
				break
			}
		}
		// Nullsafe / optional chaining peel: $logger?->info → $logger
		recv = strings.ReplaceAll(recv, "?->", "->")
		recv = strings.TrimPrefix(recv, "$")
		if i := strings.Index(recv, "->"); i > 0 {
			recv = recv[:i]
		}
		if i := strings.IndexByte(recv, '.'); i > 0 {
			recv = recv[:i]
		}
		if i := strings.Index(recv, "::"); i > 0 {
			recv = recv[:i]
		}
		if typ, ok := fields[recv]; ok {
			return typ
		}
		return ""
	}
}

func phpTraitUseNames(n *sitter.Node, buf []byte) []string {
	if n == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		var name string
		switch c.Type() {
		case "name", "qualified_name":
			name = strings.TrimSpace(c.Content(buf))
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func childOfType(n *sitter.Node, typ string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == typ {
			return c
		}
	}
	return nil
}

// phpEmitInheritance emits inherits/implements edges from class/interface
// base_clause and class_interface_clause children (direct only — not nested types).
// aliases maps use-alias → FQCN leaf so `extends Authenticatable` resolves to User.
func phpEmitInheritance(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult, aliases map[string]string) {
	if n == nil || out == nil {
		return
	}
	emit := func(name string, kind types.ReferenceKind, conf float64) {
		name = phpResolveTypeLeaf(name, aliases)
		if name == "" {
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, string(kind)),
			RepoID:     repoID,
			Kind:       kind,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "base_clause":
			for j := 0; j < int(c.NamedChildCount()); j++ {
				ch := c.NamedChild(j)
				if ch == nil {
					continue
				}
				if ch.Type() == "name" || ch.Type() == "qualified_name" {
					emit(ch.Content(buf), types.RefKindInherits, 0.9)
				}
			}
		case "class_interface_clause":
			for j := 0; j < int(c.NamedChildCount()); j++ {
				ch := c.NamedChild(j)
				if ch == nil {
					continue
				}
				if ch.Type() == "name" || ch.Type() == "qualified_name" {
					emit(ch.Content(buf), types.RefKindImplements, 0.9)
				}
			}
		}
	}
}

// phpEmitTraitUses emits implements edges for `use TraitA, TraitB;` inside a class
// so traits get inbound callers via CallersOf/impact (imports alone are file-level).
// aliases remaps `use Factory` when `use …\HasFactory as Factory` is imported.
func phpEmitTraitUses(classNode *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult, aliases map[string]string) {
	if classNode == nil || out == nil {
		return
	}
	body := classNode.ChildByFieldName("body")
	if body == nil {
		for i := 0; i < int(classNode.ChildCount()); i++ {
			c := classNode.Child(i)
			if c != nil && c.Type() == "declaration_list" {
				body = c
				break
			}
		}
	}
	if body == nil {
		return
	}
	for i := 0; i < int(body.ChildCount()); i++ {
		c := body.Child(i)
		if c == nil || c.Type() != "use_declaration" {
			continue
		}
		for _, name := range phpTraitUseNames(c, buf) {
			name = phpResolveTypeLeaf(name, aliases)
			if name == "" {
				continue
			}
			tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, fromSym, tgt, "implements"),
				RepoID:     repoID,
				Kind:       types.RefKindImplements,
				SourceID:   fromSym,
				TargetID:   tgt,
				Confidence: 0.85,
			})
		}
	}
}

// phpEnclosingTypeName returns the nearest enclosing class/interface/trait name.
func phpEnclosingTypeName(n *sitter.Node, buf []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "class_declaration", "interface_declaration", "trait_declaration":
			return ChildName(p, "name", buf)
		}
	}
	return ""
}

func addPHPFrameworkSymbols(repoID, relPath string, buf []byte, out *ParseResult, frameworks []string) {
	src := string(buf)
	lines := strings.Split(src, "\n")
	laravelFW := withFramework(frameworks, string(FrameworkLaravel))
	facadeEmitted := map[string]bool{}
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if m := laravelRoutePattern.FindStringSubmatch(trim); len(m) > 1 {
			name := fmt.Sprintf("route_%s_%d", strings.ToLower(m[1]), i+1)
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(laravelFW, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if !facadeEmitted["Route"] {
				facade := symbol(repoID, relPath, "Route", types.SymbolKindClass, i+1, i+1, "php", frameworkSignature(laravelFW, "facade"), "")
				out.Symbols = append(out.Symbols, facade)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, facade.ID))
				facadeEmitted["Route"] = true
			}
			// Route::verb(...) is a call to the Route facade / Router concrete.
			emitPHPCall(repoID, relPath, sym.ID, "Route", 0.9, out)
			emitPHPCall(repoID, relPath, sym.ID, laravelFacadeConcrete["Route"], 0.9, out)
			emitPHPCall(repoID, relPath, sym.ID, laravelFacadeConcrete["Route"]+"."+m[1], 0.9, out)
			emitLaravelRouteActionEdges(repoID, relPath, sym.ID, trim, out)
			// Multi-line actions: peek a few following lines.
			for j := 1; j <= 3 && i+j < len(lines); j++ {
				emitLaravelRouteActionEdges(repoID, relPath, sym.ID, lines[i+j], out)
			}
			// `->name('users.index')` becomes its own symbol pointing at this
			// route, so `route('users.index')` reaches the controller action.
			addLaravelRouteNames(repoID, relPath, lines, i, sym.ID, out, frameworks)
		}
		// Other Laravel facades (Hash::make, Schema::create, …): card + call edge.
		for _, fm := range laravelFacadeCall.FindAllStringSubmatch(trim, -1) {
			if len(fm) < 3 {
				continue
			}
			facade, method := fm[1], fm[2]
			if facade == "Route" {
				continue // handled above with route_* symbols
			}
			if !facadeEmitted[facade] {
				fsym := symbol(repoID, relPath, facade, types.SymbolKindClass, i+1, i+1, "php", frameworkSignature(laravelFW, "facade"), "")
				out.Symbols = append(out.Symbols, fsym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, fsym.ID))
				facadeEmitted[facade] = true
			}
			// Synthetic call site so the facade gets inbound callers.
			siteName := fmt.Sprintf("%s_%s_%d", strings.ToLower(facade), strings.ToLower(method), i+1)
			site := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(laravelFW, "facade-call"), "")
			out.Symbols = append(out.Symbols, site)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, site.ID))
			emitPHPCall(repoID, relPath, site.ID, facade, 0.85, out)
			if concrete := laravelFacadeConcrete[facade]; concrete != "" {
				emitPHPCall(repoID, relPath, site.ID, concrete, 0.9, out)
				// Hash::make → HashManager.make for typed CallersOf/impact.
				emitPHPCall(repoID, relPath, site.ID, concrete+"."+method, 0.9, out)
			}
			emitPHPCall(repoID, relPath, site.ID, method, 0.55, out)
		}
		if m := laravelAppBind.FindStringSubmatch(trim); len(m) > 2 {
			abstract, concrete := phpSimpleName(m[1]), phpSimpleName(m[2])
			name := fmt.Sprintf("laravel_bind_%s_%s_%d", strings.ToLower(abstract), strings.ToLower(concrete), i+1)
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, i+1, i+1, "php", frameworkSignature(laravelFW, "container_bind"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPHPCall(repoID, relPath, sym.ID, abstract, 0.9, out)
			emitPHPCall(repoID, relPath, sym.ID, concrete, 0.9, out)
		}
		wpFW := withFramework(frameworks, string(FrameworkWordPress))
		// Multi-line add_action/add_filter: join until paren balance closes.
		if wpHookOpen.MatchString(trim) {
			window := phpJoinCallWindow(lines, i, 8)
			if m := wpHookPattern.FindStringSubmatch(window); len(m) > 3 {
				kind, hook := strings.ToLower(m[1]), sanitizeHookName(m[2])
				siteName := fmt.Sprintf("wp_add_%s_%s_%d", kind, hook, i+1)
				sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(wpFW, "entrypoint"), "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				emitPHPCall(repoID, relPath, sym.ID, "add_"+kind, 0.85, out)
				emitWPHookCallbackEdges(repoID, relPath, sym.ID, m[3], out)
			}
		}
		if m := wpFirePattern.FindStringSubmatch(trim); len(m) > 2 {
			fn, hook := strings.ToLower(m[1]), sanitizeHookName(m[2])
			siteName := fmt.Sprintf("wp_%s_%s_%d", strings.ReplaceAll(fn, "_", ""), hook, i+1)
			// Keep fire names short: wp_doaction_init_N / wp_applyfilters_content_N
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(wpFW, "hook-fire"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPHPCall(repoID, relPath, sym.ID, fn, 0.9, out)
		}
		if m := wpRegisterHookPattern.FindStringSubmatch(trim); len(m) > 2 {
			kind := strings.ToLower(m[1])
			siteName := fmt.Sprintf("wp_register_%s_%d", kind, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(wpFW, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPHPCall(repoID, relPath, sym.ID, "register_"+kind+"_hook", 0.85, out)
			emitWPHookCallbackEdges(repoID, relPath, sym.ID, m[2], out)
		}
		if m := wpShortcodePattern.FindStringSubmatch(trim); len(m) > 2 {
			tag := sanitizeHookName(m[1])
			siteName := fmt.Sprintf("wp_shortcode_%s_%d", tag, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(wpFW, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPHPCall(repoID, relPath, sym.ID, "add_shortcode", 0.85, out)
			emitWPHookCallbackEdges(repoID, relPath, sym.ID, m[2], out)
		}
		// Laravel 11+ bootstrap/app.php: Application::configure → withRouting/…
		if m := laravelBootstrapCall.FindStringSubmatch(trim); len(m) > 2 {
			cls, meth := m[1], m[2]
			if !facadeEmitted[cls] {
				fsym := symbol(repoID, relPath, cls, types.SymbolKindClass, i+1, i+1, "php", frameworkSignature(laravelFW, "bootstrap"), "")
				out.Symbols = append(out.Symbols, fsym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, fsym.ID))
				facadeEmitted[cls] = true
			}
			siteName := fmt.Sprintf("boot_%s_%s_%d", strings.ToLower(cls), strings.ToLower(meth), i+1)
			site := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(laravelFW, "entrypoint"), "")
			out.Symbols = append(out.Symbols, site)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, site.ID))
			emitPHPCall(repoID, relPath, site.ID, cls, 0.9, out)
			emitPHPCall(repoID, relPath, site.ID, meth, 0.8, out)
		}
		for _, wm := range laravelWithMethod.FindAllStringSubmatch(trim, -1) {
			if len(wm) < 2 {
				continue
			}
			meth := wm[1]
			siteName := fmt.Sprintf("boot_%s_%d", strings.ToLower(meth), i+1)
			site := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(laravelFW, "entrypoint"), "")
			out.Symbols = append(out.Symbols, site)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, site.ID))
			emitPHPCall(repoID, relPath, site.ID, meth, 0.85, out)
			if strings.EqualFold(meth, "withRouting") {
				emitPHPCall(repoID, relPath, site.ID, "Route", 0.7, out)
			}
			if strings.EqualFold(meth, "withMiddleware") {
				emitPHPCall(repoID, relPath, site.ID, "Middleware", 0.75, out)
			}
			if strings.EqualFold(meth, "withExceptions") {
				emitPHPCall(repoID, relPath, site.ID, "Exceptions", 0.75, out)
			}
		}
		if m := laravelExtendsForm.FindStringSubmatch(trim); len(m) > 1 {
			reqName := m[1]
			for _, s := range out.Symbols {
				if s.Name == reqName && s.Kind == types.SymbolKindClass {
					emitPHPCall(repoID, relPath, s.ID, "FormRequest", 0.85, out)
					break
				}
			}
		}
	}
}

func emitLaravelRouteActionEdges(repoID, relPath, fromSym, line string, out *ParseResult) {
	// Every `X::class` on a route line is wiring: the controller, but also
	// `->middleware([EnsureTokenIsValid::class])` and `->can(..., Post::class)`.
	for _, cm := range laravelClassConstRe.FindAllStringSubmatch(line, -1) {
		if len(cm) > 1 {
			emitPHPCall(repoID, relPath, fromSym, phpSimpleName(cm[1]), 0.8, out)
		}
	}
	if m := laravelRouteAction.FindStringSubmatch(line); len(m) > 2 {
		ctrl := phpSimpleName(m[1])
		emitPHPCall(repoID, relPath, fromSym, ctrl, 0.85, out)
		emitPHPCall(repoID, relPath, fromSym, m[2], 0.75, out)
		return
	}
	if m := laravelRouteInvokable.FindStringSubmatch(line); len(m) > 1 {
		emitPHPCall(repoID, relPath, fromSym, phpSimpleName(m[1]), 0.85, out)
		return
	}
	if m := laravelRouteString.FindStringSubmatch(line); len(m) > 2 {
		emitPHPCall(repoID, relPath, fromSym, phpSimpleName(m[1]), 0.85, out)
		emitPHPCall(repoID, relPath, fromSym, m[2], 0.75, out)
	}
	if strings.Contains(line, "view(") {
		emitPHPCall(repoID, relPath, fromSym, "view", 0.7, out)
	}
}

// phpJoinCallWindow joins line i with following lines until paren balance
// closes (or maxExtra exhausted). Used for multi-line WP hooks / REST routes.
func phpJoinCallWindow(lines []string, i, maxExtra int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	joined := strings.TrimSpace(lines[i])
	bal := 0
	for _, r := range joined {
		switch r {
		case '(':
			bal++
		case ')':
			bal--
		}
	}
	if bal <= 0 && strings.Contains(joined, ")") {
		return joined
	}
	for j := 1; j <= maxExtra && i+j < len(lines); j++ {
		next := strings.TrimSpace(lines[i+j])
		if next == "" {
			continue
		}
		joined += " " + next
		for _, r := range next {
			switch r {
			case '(':
				bal++
			case ')':
				bal--
			}
		}
		if bal <= 0 {
			break
		}
	}
	return joined
}

// looksLikeSymfonyFile reports attribute-route / AbstractController / Symfony\* markers.
func looksLikeSymfonyFile(relPath, content string) bool {
	p := strings.ToLower(filepath.ToSlash(relPath))
	body := strings.ToLower(content)
	if strings.Contains(body, "symfony\\") || strings.Contains(body, "abstractcontroller") ||
		strings.Contains(body, "#[route") || strings.Contains(body, "ascontroller") ||
		strings.Contains(body, "frameworkbundle") {
		return true
	}
	if (strings.Contains(p, "/controller/") || strings.Contains(p, "/controllers/")) &&
		(strings.Contains(body, "#[route") || strings.Contains(body, "symfony\\") ||
			strings.Contains(body, "abstractcontroller")) {
		return true
	}
	return false
}

// addSymfonyFrameworkSymbols densifies #[Route] entrypoints + ctor/promoted DI
// onto the PHP graph (mirrors Laravel routes + ASP.NET ctor DI — Medium honesty).
func addSymfonyFrameworkSymbols(repoID, relPath string, buf []byte, out *ParseResult, frameworks []string) {
	if out == nil {
		return
	}
	src := string(buf)
	if !looksLikeSymfonyFile(relPath, src) && !containsFramework(frameworks, string(FrameworkSymfony)) {
		return
	}
	fw := withFramework(frameworks, string(FrameworkSymfony))
	lines := strings.Split(src, "\n")

	// Tag *Controller classes with role=controller when Symfony-shaped.
	for i := range out.Symbols {
		s := &out.Symbols[i]
		if s.Kind != types.SymbolKindClass {
			continue
		}
		if strings.HasSuffix(s.Name, "Controller") || strings.Contains(strings.ToLower(s.Signature), "frameworks=symfony") {
			s.Signature = frameworkSignature(fw, "controller")
		}
		if strings.HasSuffix(s.Name, "Service") {
			s.Signature = frameworkSignature(fw, "service")
		}
	}

	// #[Route] … public function action → entrypoint site + call to action (+ controller leaf).
	// Class-level #[Route] is a path prefix only — skip (do not bind the next method).
	for i, line := range lines {
		if !symfonyRouteAttr.MatchString(line) {
			continue
		}
		action := ""
		classLevel := false
		for j := 0; j <= 6 && i+j < len(lines); j++ {
			lj := lines[i+j]
			if j > 0 && symfonyClassDecl.MatchString(lj) {
				classLevel = true
				break
			}
			if m := symfonyRouteMethod.FindStringSubmatch(lj); len(m) > 1 {
				action = m[1]
				break
			}
		}
		if classLevel {
			continue
		}
		name := fmt.Sprintf("symfony_route_%d", i+1)
		if action != "" {
			name = fmt.Sprintf("symfony_route_%s_%d", strings.ToLower(action), i+1)
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(fw, "entrypoint"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitPHPCall(repoID, relPath, sym.ID, "Route", 0.8, out)
		if action != "" {
			emitPHPCall(repoID, relPath, sym.ID, action, 0.85, out)
			// Promote the action method to entrypoint role when present.
			for k := range out.Symbols {
				s := &out.Symbols[k]
				if s.Name == action && s.Kind == types.SymbolKindMethod {
					s.Signature = frameworkSignature(fw, "entrypoint")
					break
				}
			}
		}
		if cls := phpEnclosingClassFromLines(lines, i); cls != "" {
			emitPHPCall(repoID, relPath, sym.ID, cls, 0.75, out)
		}
	}

	// Ctor / promoted-property DI: class → injected service types.
	// Balanced parens so #[Autowire(...)] inside the parameter list does not truncate.
	if params := phpConstructParams(src); params != "" {
		cls := ""
		if cm := symfonyClassDecl.FindStringSubmatch(src); len(cm) > 1 {
			cls = cm[1]
		}
		var classID string
		for _, s := range out.Symbols {
			if s.Kind == types.SymbolKindClass && (s.Name == cls || (cls == "" && strings.HasSuffix(s.Name, "Controller"))) {
				classID = s.ID
				if s.Name == cls {
					break
				}
			}
		}
		if classID == "" {
			return
		}
		seen := map[string]bool{}
		for _, pm := range symfonyTypedParam.FindAllStringSubmatch(params, -1) {
			tok := pm[1]
			if tok == "" {
				tok = pm[2]
			}
			tok = phpSimpleName(tok)
			if tok == "" || seen[tok] || symfonySkipInjectType(tok) {
				continue
			}
			seen[tok] = true
			emitPHPCall(repoID, relPath, classID, tok, 0.85, out)
		}
	}
}

// phpConstructParams returns the raw parameter list of the first __construct(
// in src, with nested () (e.g. #[Autowire("%x%")]) preserved via depth counting.
func phpConstructParams(src string) string {
	loc := symfonyCtorOpen.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	start := loc[1] // byte offset after '('
	depth := 1
	inSq, inDq := false, false
	for i := start; i < len(src); i++ {
		c := src[i]
		if inSq {
			if c == '\\' && i+1 < len(src) {
				i++
				continue
			}
			if c == '\'' {
				inSq = false
			}
			continue
		}
		if inDq {
			if c == '\\' && i+1 < len(src) {
				i++
				continue
			}
			if c == '"' {
				inDq = false
			}
			continue
		}
		switch c {
		case '\'':
			inSq = true
		case '"':
			inDq = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return src[start:i]
			}
		}
	}
	return ""
}

func phpEnclosingClassFromLines(lines []string, at int) string {
	for i := at; i >= 0; i-- {
		if m := symfonyClassDecl.FindStringSubmatch(lines[i]); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func symfonySkipInjectType(tok string) bool {
	switch tok {
	case "string", "int", "float", "bool", "array", "object", "mixed", "iterable",
		"callable", "void", "never", "true", "false", "null", "self", "static", "parent",
		"Request", "Response", "JsonResponse", "RedirectResponse", "BinaryFileResponse",
		"StreamedResponse", "ParameterBag", "InputBag", "ContainerInterface",
		"EntityManagerInterface", "ManagerRegistry", "LoggerInterface",
		"TranslatorInterface", "UrlGeneratorInterface", "RouterInterface",
		"EventDispatcherInterface", "Security", "TokenStorageInterface",
		"UserInterface", "AbstractController", "Controller", "FormInterface":
		return true
	default:
		return false
	}
}

func emitPHPCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) {
		return
	}
	// A symref id is `symref:repo:path:name` and the resolver reads the name as
	// everything after the last colon — a colon here would silently resolve to
	// the wrong symbol, so drop it rather than emit a misleading edge.
	if strings.ContainsRune(name, ':') {
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

func phpSimpleName(fqcn string) string {
	fqcn = strings.TrimSpace(fqcn)
	fqcn = strings.TrimPrefix(fqcn, `\`)
	if i := strings.LastIndex(fqcn, `\`); i >= 0 {
		return fqcn[i+1:]
	}
	return fqcn
}

// emitWPHookCallbackEdges links a hook registration site to its callback
// function/method leaf names (string or array($this|'Class', 'method')).
func emitWPHookCallbackEdges(repoID, relPath, fromSym, raw string, out *ParseResult) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if am := wpArrayCallback.FindStringSubmatch(raw); len(am) > 2 {
		emitWPArrayCallback(repoID, relPath, fromSym, am[1], am[2], out)
		return
	}
	// Strip priority / accepted_args after a plain string or identifier callback.
	cbRaw := raw
	if i := strings.Index(raw, ","); i >= 0 {
		cbRaw = strings.TrimSpace(raw[:i])
	}
	if strings.HasPrefix(cbRaw, "'") || strings.HasPrefix(cbRaw, `"`) {
		q := cbRaw[0]
		end := strings.IndexRune(cbRaw[1:], rune(q))
		if end >= 0 {
			name := cbRaw[1 : 1+end]
			if name != "" {
				emitPHPCall(repoID, relPath, fromSym, name, 0.9, out)
			}
			return
		}
	}
	cb := sanitizeCallbackName(cbRaw)
	if cb != "" {
		emitPHPCall(repoID, relPath, fromSym, cb, 0.8, out)
	}
}

func emitWPArrayCallback(repoID, relPath, fromSym, recv, method string, out *ParseResult) {
	method = strings.TrimSpace(method)
	recv = strings.TrimSpace(recv)
	recv = strings.Trim(recv, `"'`)
	recv = strings.ReplaceAll(recv, "::class", "")
	recv = strings.TrimPrefix(recv, `\`)
	if i := strings.LastIndex(recv, `\`); i >= 0 {
		recv = recv[i+1:]
	}
	if method != "" {
		emitPHPCall(repoID, relPath, fromSym, method, 0.9, out)
	}
	if recv != "" && !strings.EqualFold(recv, "$this") && !strings.EqualFold(recv, "this") {
		emitPHPCall(repoID, relPath, fromSym, recv, 0.8, out)
	}
}

func sanitizeHookName(hook string) string {
	s := strings.TrimSpace(hook)
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "hook"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func sanitizeCallbackName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	s = strings.ReplaceAll(s, "::class", "")
	s = strings.ReplaceAll(s, "::", "_")
	s = strings.ReplaceAll(s, "->", "_")
	s = strings.ReplaceAll(s, "$this", "this")
	s = strings.ReplaceAll(s, "[", "")
	s = strings.ReplaceAll(s, "]", "")
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
