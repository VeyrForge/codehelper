package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	php "github.com/smacker/go-tree-sitter/php"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	laravelRoutePattern   = regexp.MustCompile(`(?i)Route::(get|post|put|patch|delete|options|any|match|resource|apiResource)\s*\(`)
	laravelRouteAction    = regexp.MustCompile(`(?i)\[\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*,\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]\s*\]`)
	laravelRouteInvokable = regexp.MustCompile(`(?i)([A-Za-z_\\][A-Za-z0-9_\\]*Controller)\s*::\s*class`)
	laravelRouteString    = regexp.MustCompile(`(?i)['"]([A-Za-z_\\][A-Za-z0-9_\\]*)@([A-Za-z_][A-Za-z0-9_]*)['"]`)
	laravelFacadeCall     = regexp.MustCompile(`\b(Route|Hash|Schema|DB|Cache|Auth|Storage|Log|Gate|Event|Mail|Queue|Redis|Http|Cookie|Session|Validator|View|File|Artisan|Blade|Config|Crypt|Notification|Password|Redirect|Response|URL|Str|Arr)\s*::\s*([A-Za-z_][A-Za-z0-9_]*)`)
	laravelBootstrapCall  = regexp.MustCompile(`(?i)\b(Application|Middleware|Exceptions)\s*::\s*([A-Za-z_][A-Za-z0-9_]*)`)
	laravelWithMethod     = regexp.MustCompile(`(?i)->\s*(withRouting|withMiddleware|withExceptions|withProviders|withCommands|withSchedule|create)\s*\(`)
	laravelExtendsForm    = regexp.MustCompile(`(?i)class\s+(\w+)\s+extends\s+FormRequest\b`)
	laravelAppBind        = regexp.MustCompile(`(?i)\$this\s*->\s*app\s*->\s*bind\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*,\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	wpHookPattern         = regexp.MustCompile(`(?i)add_(action|filter)\s*\(\s*['"]([^'"]+)['"]\s*,\s*([^\),]+)`)
)

var laravelFacadeConcrete = map[string]string{
	"Auth": "AuthManager", "Cache": "CacheManager", "DB": "DatabaseManager",
	"Event": "Dispatcher", "Hash": "HashManager", "Mail": "MailManager",
	"Queue": "QueueManager", "Redis": "RedisManager", "Route": "Router",
	"Storage": "FilesystemManager", "Validator": "Factory", "View": "Factory",
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
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "namespace_use_declaration", "use_statement":
			// tree-sitter-php nests imports under namespace_use_clause →
			// qualified_name (not a direct "name" child). Segment-only "name"
			// nodes under the FQCN must not become import edges.
			for _, mod := range phpUseImportNames(n, buf) {
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
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "class_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "php", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
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
	return out, nil
}

// phpUseImportNames returns FQCNs from a namespace_use_declaration / use_statement.
func phpUseImportNames(n *sitter.Node, buf []byte) []string {
	if n == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(mod string) {
		mod = strings.TrimSpace(mod)
		mod = strings.TrimPrefix(mod, `\`)
		if mod == "" || seen[mod] {
			return
		}
		seen[mod] = true
		out = append(out, mod)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "namespace_use_clause":
			if q := childOfType(c, "qualified_name"); q != nil {
				add(q.Content(buf))
				continue
			}
			if nm := childOfType(c, "name"); nm != nil {
				add(nm.Content(buf))
			}
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
				if prefix != "" {
					add(prefix + `\` + strings.TrimPrefix(leaf, `\`))
				} else {
					add(leaf)
				}
			}
		}
	}
	return out
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
			// Route::verb(...) is a call to the Route facade.
			emitPHPCall(repoID, relPath, sym.ID, "Route", 0.9, out)
			emitPHPCall(repoID, relPath, sym.ID, laravelFacadeConcrete["Route"], 0.9, out)
			emitLaravelRouteActionEdges(repoID, relPath, sym.ID, trim, out)
			// Multi-line actions: peek a few following lines.
			for j := 1; j <= 3 && i+j < len(lines); j++ {
				emitLaravelRouteActionEdges(repoID, relPath, sym.ID, lines[i+j], out)
			}
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
		if m := wpHookPattern.FindStringSubmatch(trim); len(m) > 3 {
			cb := sanitizeCallbackName(m[3])
			if cb == "" {
				cb = fmt.Sprintf("wp_hook_callback_%d", i+1)
			}
			sym := symbol(repoID, relPath, cb, types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(withFramework(frameworks, string(FrameworkWordPress)), "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
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

func emitPHPCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) {
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
