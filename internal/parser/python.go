package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	py "github.com/smacker/go-tree-sitter/python"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	fastAPIDecoratorPattern = regexp.MustCompile(`(?i)^\s*@(?:\w+\.)?(get|post|put|patch|delete|options|head)\s*\(`)
	flaskRoutePattern       = regexp.MustCompile(`(?i)^\s*@(\w+)\.route\s*\(`)
	djangoPathPattern       = regexp.MustCompile(`(?i)\b(?:path|re_path)\s*\(\s*['"][^'"]*['"]\s*,\s*([A-Za-z_][A-Za-z0-9_\.]*)`)
	drfRouterRegister       = regexp.MustCompile(`(?i)\b(\w+)\.register\s*\(\s*['"][^'"]*['"]\s*,\s*([A-Za-z_][A-Za-z0-9_\.]*)`)
	drfAPIViewDecorator     = regexp.MustCompile(`(?i)^\s*@api_view\s*\(`)
	pyDefNamePattern        = regexp.MustCompile(`(?i)^(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`)
)

// ParsePython extracts symbols from Python source.
func ParsePython(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(py.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	frameworks := DetectFrameworkPacks(relPath, nil, string(buf))
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "import_statement", "import_from_statement":
			emitPythonImports(n, buf, repoID, relPath, out)
		case "function_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			parent := pythonParentClass(n, buf)
			kind := types.SymbolKindFunction
			if parent != "" {
				kind = types.SymbolKindMethod
			}
			sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "python", frameworkSignature(frameworks, ""), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
			// Decorators sit on the parent decorated_definition, not inside the
			// function body — walk them so Depends/app.get appear as call edges.
			if p := n.Parent(); p != nil && p.Type() == "decorated_definition" {
				extractPythonDecoratorCalls(p, buf, repoID, relPath, sym.ID, out)
			}
		case "class_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "python", frameworkSignature(frameworks, ""), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPythonBaseEdges(n, buf, repoID, relPath, sym.ID, out)
		case "assignment":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left == nil {
				return
			}
			name := strings.TrimSpace(left.Content(buf))
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "python", frameworkSignature(frameworks, "state"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if right != nil {
				addReadEdgesFromNode(repoID, relPath, sym.ID, right, buf, out)
			}
		}
	})
	addPythonFrameworkSymbols(repoID, relPath, buf, out, frameworks)
	addPythonDICallEdges(tree.RootNode(), buf, repoID, relPath, out)
	return out, nil
}

func emitPythonImports(n *sitter.Node, buf []byte, repoID, relPath string, out *ParseResult) {
	if src := pyImportModule(n, buf); src != "" {
		out.Imports = append(out.Imports, src)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, FileNodeID(repoID, relPath), moduleNodeID(repoID, src), "imports"),
			RepoID:     repoID,
			Kind:       types.RefKindImports,
			SourceID:   FileNodeID(repoID, relPath),
			TargetID:   moduleNodeID(repoID, src),
			Confidence: 0.85,
		})
	}
	// Densify from-import names: `from fastapi import Depends` → imports edge to Depends.
	for _, name := range pyImportNames(n, buf) {
		if name == "" || !isCallableName(name) {
			continue
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, FileNodeID(repoID, relPath), tgt, "imports"),
			RepoID:     repoID,
			Kind:       types.RefKindImports,
			SourceID:   FileNodeID(repoID, relPath),
			TargetID:   tgt,
			Confidence: 0.8,
		})
	}
}

func pyImportModule(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if mod := n.ChildByFieldName("module_name"); mod != nil {
		return strings.TrimSpace(mod.Content(buf))
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "dotted_name", "relative_import":
			return strings.TrimSpace(c.Content(buf))
		}
	}
	return ""
}

func pyImportNames(n *sitter.Node, buf []byte) []string {
	if n == nil || n.Type() != "import_from_statement" {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	add := func(raw string) {
		name := strings.TrimSpace(raw)
		if i := strings.Index(name, " as "); i >= 0 {
			name = strings.TrimSpace(name[i+4:])
		}
		if name == "" || name == "*" || seen[name] || !isCallableName(name) {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "dotted_name":
			// Skip the module name field; named imports are sibling dotted_names /
			// aliased_imports after module_name in tree-sitter-python.
			if n.ChildByFieldName("module_name") != nil && c == n.ChildByFieldName("module_name") {
				continue
			}
			add(c.Content(buf))
		case "aliased_import":
			if name := c.ChildByFieldName("name"); name != nil {
				if alias := c.ChildByFieldName("alias"); alias != nil {
					add(alias.Content(buf))
				} else {
					add(name.Content(buf))
				}
			} else {
				add(c.Content(buf))
			}
		}
	}
	return names
}

func extractPythonDecoratorCalls(decorated *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if decorated == nil {
		return
	}
	for i := 0; i < int(decorated.ChildCount()); i++ {
		c := decorated.Child(i)
		if c == nil || c.Type() != "decorator" {
			continue
		}
		extractCalls(c, buf, repoID, relPath, fromSym, out)
		// Also pull Depends(dep) → dep edges from decorator/default-arg territory
		// that sits on the decorator node itself (rare but cheap).
		emitPythonDependsArgEdges(c, buf, repoID, relPath, fromSym, out)
	}
}

func addPythonFrameworkSymbols(repoID, relPath string, buf []byte, out *ParseResult, frameworks []string) {
	lines := strings.Split(string(buf), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if m := fastAPIDecoratorPattern.FindStringSubmatch(trim); len(m) > 1 {
			meth := strings.ToLower(m[1])
			name := fmt.Sprintf("fastapi_%s_%d", meth, i+1)
			fw := withFramework(frameworks, string(FrameworkFastAPI))
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "python", frameworkSignature(fw, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPythonCall(repoID, relPath, sym.ID, meth, 0.85, out)
			if recv := pythonDecoratorReceiver(trim); recv != "" {
				emitPythonCall(repoID, relPath, sym.ID, recv, 0.7, out)
				emitPythonCall(repoID, relPath, sym.ID, recv+"."+meth, 0.9, out)
			}
			if handler := nextPythonDefName(lines, i); handler != "" {
				emitPythonCall(repoID, relPath, sym.ID, handler, 0.9, out)
			}
		}
		if m := flaskRoutePattern.FindStringSubmatch(trim); len(m) > 1 {
			recv := m[1]
			name := fmt.Sprintf("flask_route_%d", i+1)
			fw := withFramework(frameworks, string(FrameworkFlask))
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "python", frameworkSignature(fw, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPythonCall(repoID, relPath, sym.ID, "route", 0.85, out)
			emitPythonCall(repoID, relPath, sym.ID, recv, 0.75, out)
			emitPythonCall(repoID, relPath, sym.ID, recv+".route", 0.9, out)
			if handler := nextPythonDefName(lines, i); handler != "" {
				emitPythonCall(repoID, relPath, sym.ID, handler, 0.9, out)
			}
		}
		if drfAPIViewDecorator.MatchString(trim) {
			name := fmt.Sprintf("drf_api_view_%d", i+1)
			fw := withFramework(withFramework(frameworks, string(FrameworkDjango)), string(FrameworkDjangoREST))
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "python", frameworkSignature(fw, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPythonCall(repoID, relPath, sym.ID, "api_view", 0.85, out)
			if handler := nextPythonDefName(lines, i); handler != "" {
				emitPythonCall(repoID, relPath, sym.ID, handler, 0.9, out)
			}
		}
		if m := djangoPathPattern.FindStringSubmatch(trim); len(m) > 1 {
			viewRaw := strings.TrimSpace(m[1])
			viewRaw = strings.TrimSuffix(viewRaw, ".as_view")
			view := strings.ReplaceAll(viewRaw, ".", "_")
			name := fmt.Sprintf("django_path_%s_%d", view, i+1)
			fw := withFramework(frameworks, string(FrameworkDjango))
			if strings.Contains(trim, "as_view") || strings.Contains(strings.ToLower(viewRaw), "viewset") {
				fw = withFramework(fw, string(FrameworkDjangoREST))
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "python", frameworkSignature(fw, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPythonCall(repoID, relPath, sym.ID, "path", 0.7, out)
			emitPythonViewTarget(repoID, relPath, sym.ID, viewRaw, out)
		}
		if m := drfRouterRegister.FindStringSubmatch(trim); len(m) > 2 {
			recv, viewRaw := m[1], strings.TrimSpace(m[2])
			view := strings.ReplaceAll(viewRaw, ".", "_")
			name := fmt.Sprintf("drf_register_%s_%d", view, i+1)
			fw := withFramework(withFramework(frameworks, string(FrameworkDjango)), string(FrameworkDjangoREST))
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, i+1, i+1, "python", frameworkSignature(fw, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPythonCall(repoID, relPath, sym.ID, "register", 0.85, out)
			emitPythonCall(repoID, relPath, sym.ID, recv, 0.7, out)
			emitPythonCall(repoID, relPath, sym.ID, recv+".register", 0.9, out)
			emitPythonViewTarget(repoID, relPath, sym.ID, viewRaw, out)
		}
	}
}

func emitPythonViewTarget(repoID, relPath, fromSym, viewRaw string, out *ParseResult) {
	viewRaw = strings.TrimSpace(viewRaw)
	if viewRaw == "" {
		return
	}
	parts := strings.Split(viewRaw, ".")
	leaf := parts[len(parts)-1]
	emitPythonCall(repoID, relPath, fromSym, leaf, 0.9, out)
	if len(parts) > 1 {
		emitPythonCall(repoID, relPath, fromSym, viewRaw, 0.75, out)
		emitPythonCall(repoID, relPath, fromSym, parts[0], 0.55, out)
	}
}

func nextPythonDefName(lines []string, decoratorLine int) string {
	for j := decoratorLine + 1; j < len(lines) && j <= decoratorLine+8; j++ {
		trim := strings.TrimSpace(lines[j])
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "@") {
			continue
		}
		if m := pyDefNamePattern.FindStringSubmatch(trim); len(m) > 1 {
			return m[1]
		}
		return ""
	}
	return ""
}

func pythonDecoratorReceiver(trim string) string {
	trim = strings.TrimSpace(trim)
	if !strings.HasPrefix(trim, "@") {
		return ""
	}
	body := strings.TrimPrefix(trim, "@")
	if i := strings.IndexByte(body, '.'); i > 0 {
		recv := body[:i]
		if isCallableName(recv) {
			return recv
		}
	}
	return ""
}

// addPythonDICallEdges attaches Depends / include_router calls that live at
// module scope (common in FastAPI tutorials) to the local `app`/`router`
// symbol so they participate in the call graph after symref resolution.
func addPythonDICallEdges(root *sitter.Node, buf []byte, repoID, relPath string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	fallback := ""
	for _, s := range out.Symbols {
		switch s.Name {
		case "app", "router":
			if fallback == "" {
				fallback = s.ID
			}
		}
	}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call" {
			return
		}
		name := calleeName(n.ChildByFieldName("function"), buf)
		switch name {
		case "Depends", "include_router", "register":
		default:
			// app.include_router / router.register via attribute
			if fn := n.ChildByFieldName("function"); fn != nil && fn.Type() == "attribute" {
				name = calleeName(fn, buf)
			}
			switch name {
			case "Depends", "include_router", "register":
			default:
				return
			}
		}
		from := enclosingPythonFunctionSym(n, buf, repoID, relPath, out)
		if from == "" {
			from = fallback
		}
		if from == "" {
			return
		}
		emitPythonCallOnce(repoID, relPath, from, name, 0.85, out)
		emitPythonDependsArgEdges(n, buf, repoID, relPath, from, out)
		emitPythonCallArgEdges(n, buf, repoID, relPath, from, name, out)
	})
}

func emitPythonDependsArgEdges(call *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if call == nil {
		return
	}
	// Walk nested calls named Depends and emit edges to their first positional arg.
	Walk(call, func(n *sitter.Node) {
		if n.Type() != "call" {
			return
		}
		if calleeName(n.ChildByFieldName("function"), buf) != "Depends" {
			return
		}
		args := n.ChildByFieldName("arguments")
		if args == nil {
			return
		}
		for i := 0; i < int(args.NamedChildCount()); i++ {
			arg := args.NamedChild(i)
			if arg == nil {
				continue
			}
			if arg.Type() == "keyword_argument" {
				if val := arg.ChildByFieldName("value"); val != nil {
					emitPythonCall(repoID, relPath, fromSym, strings.TrimSpace(val.Content(buf)), 0.9, out)
				}
				continue
			}
			emitPythonCall(repoID, relPath, fromSym, strings.TrimSpace(arg.Content(buf)), 0.9, out)
			break
		}
	})
}

func emitPythonCallArgEdges(call *sitter.Node, buf []byte, repoID, relPath, fromSym, callee string, out *ParseResult) {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return
	}
	switch callee {
	case "include_router", "register":
	default:
		return
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		arg := args.NamedChild(i)
		if arg == nil {
			continue
		}
		if arg.Type() == "keyword_argument" {
			continue
		}
		raw := strings.TrimSpace(arg.Content(buf))
		// register("users", UserViewSet) — second positional is the view.
		if callee == "register" && i == 0 {
			continue
		}
		emitPythonViewTarget(repoID, relPath, fromSym, raw, out)
		break
	}
}

func emitPythonBaseEdges(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil {
		return
	}
	supers := classNode.ChildByFieldName("superclasses")
	if supers == nil {
		return
	}
	Walk(supers, func(n *sitter.Node) {
		switch n.Type() {
		case "identifier":
			emitPythonCall(repoID, relPath, classSym, strings.TrimSpace(n.Content(buf)), 0.85, out)
		case "attribute":
			raw := strings.TrimSpace(n.Content(buf))
			emitPythonViewTarget(repoID, relPath, classSym, raw, out)
		}
	})
}

func pythonParentClass(n *sitter.Node, buf []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_definition" {
			return ChildName(p, "name", buf)
		}
	}
	return ""
}

func emitPythonCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) {
		// Allow dotted aliases like app.get / UserService.list_users.
		if !pythonCallableAlias(name) {
			return
		}
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

func emitPythonCallOnce(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
	for _, e := range out.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == fromSym && e.TargetID == tgt {
			return
		}
	}
	emitPythonCall(repoID, relPath, fromSym, name, conf, out)
}

func pythonCallableAlias(name string) bool {
	if name == "" || strings.ContainsAny(name, " \t()[]{}\"'") {
		return false
	}
	parts := strings.Split(name, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if !isCallableName(p) {
			return false
		}
	}
	return true
}

// pythonMemberAlias returns app.get / router.post / bp.route for attribute callees.
func pythonMemberAlias(fn *sitter.Node, buf []byte) string {
	if fn == nil || fn.Type() != "attribute" {
		return ""
	}
	obj := fn.ChildByFieldName("object")
	attr := fn.ChildByFieldName("attribute")
	if obj == nil || attr == nil {
		return ""
	}
	recv := strings.TrimSpace(obj.Content(buf))
	meth := strings.TrimSpace(attr.Content(buf))
	if !isPythonFrameworkReceiver(recv) || !isPythonRouteMethod(meth) {
		return ""
	}
	return recv + "." + meth
}

// pythonTypedCallee returns UserService.list_users for Capitalized.receiver calls.
func pythonTypedCallee(fn *sitter.Node, buf []byte) string {
	if fn == nil || fn.Type() != "attribute" {
		return ""
	}
	obj := fn.ChildByFieldName("object")
	attr := fn.ChildByFieldName("attribute")
	if obj == nil || attr == nil || obj.Type() != "identifier" {
		return ""
	}
	recv := strings.TrimSpace(obj.Content(buf))
	meth := strings.TrimSpace(attr.Content(buf))
	if recv == "" || meth == "" || !isCallableName(recv) || !isCallableName(meth) {
		return ""
	}
	if recv[0] < 'A' || recv[0] > 'Z' {
		return ""
	}
	return recv + "." + meth
}

func isPythonFrameworkReceiver(recv string) bool {
	switch strings.ToLower(strings.TrimSpace(recv)) {
	case "app", "api", "router", "bp", "blueprint", "application", "server":
		return true
	default:
		return false
	}
}

func isPythonRouteMethod(meth string) bool {
	switch strings.ToLower(strings.TrimSpace(meth)) {
	case "get", "post", "put", "patch", "delete", "options", "head",
		"route", "api_route", "websocket", "include_router", "register",
		"add_url_rule", "before_request", "after_request":
		return true
	default:
		return false
	}
}

func enclosingPythonFunctionSym(n *sitter.Node, buf []byte, repoID, relPath string, out *ParseResult) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "function_definition" {
			continue
		}
		name := ChildName(p, "name", buf)
		if name == "" {
			return ""
		}
		ls := int(p.StartPoint().Row) + 1
		want := fmt.Sprintf("sym:%s:%s:%d:%s", repoID, relPath, ls, name)
		for _, s := range out.Symbols {
			if s.ID == want {
				return s.ID
			}
		}
		// Fallback: first symbol with this name in the file.
		for _, s := range out.Symbols {
			if s.Name == name {
				return s.ID
			}
		}
		return ""
	}
	return ""
}
