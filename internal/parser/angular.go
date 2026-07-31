package parser

import (
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	angularNgModulePattern   = regexp.MustCompile(`(?s)@NgModule\s*\(\s*\{(.*?)\}\s*\)`)
	angularComponentPattern  = regexp.MustCompile(`(?s)@Component\s*\(\s*\{(.*?)\}\s*\)`)
	angularArrayFieldStart   = regexp.MustCompile(`(?i)(declarations|imports|providers|exports|bootstrap|schemas|viewProviders)\s*:\s*\[`)
	angularIdentInList       = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)
	angularProvidedInRe      = regexp.MustCompile(`(?s)@Injectable\s*\(\s*\{[^}]*?\bprovidedIn\s*:\s*([A-Z][A-Za-z0-9_]*|[\'"][^\'"]+[\'"])`)
	angularInjectArrayRe     = regexp.MustCompile(`(?i)inject\s*:\s*\[([^\]]*)\]`)
	angularDepsArrayRe       = regexp.MustCompile(`(?i)deps\s*:\s*\[([^\]]*)\]`)
	angularRouteComponentRe  = regexp.MustCompile(`(?i)\bcomponent\s*:\s*([A-Z][A-Za-z0-9_]*)`)
	angularRouteLoadThenRe   = regexp.MustCompile(`(?is)load(?:Component|Children)\s*:\s*[^;\n]*?\.then\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\s*=>\s*[A-Za-z_][A-Za-z0-9_]*\.([A-Z][A-Za-z0-9_]*)`)
	angularRoutesConstRe     = regexp.MustCompile(`(?m)(?:export\s+)?const\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?::\s*Routes\b)?\s*=`)
	angularProvideRouterRe   = regexp.MustCompile(`\bprovideRouter\s*\(`)
	angularRouterModuleForRe = regexp.MustCompile(`\bRouterModule\.for(?:Root|Child)\s*\(`)
)

// extractAngularDI wires @NgModule / @Component / @Injectable metadata and
// constructor injection as call edges so context/impact/trace see
// module↔component↔service relationships (Nest-style densify on the same TS
// graph). Also emits provide/useClass|useExisting|useFactory bind leaves.
func extractAngularDI(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil || classSym == "" || out == nil {
		return
	}
	meta := angularNgModuleMetadata(classNode, buf)
	for _, field := range []string{"declarations", "imports", "providers", "exports", "bootstrap"} {
		for _, name := range meta[field] {
			emitNestCall(repoID, relPath, classSym, name, 0.85, out)
		}
	}
	for _, name := range angularComponentProviders(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.82, out)
	}
	for _, name := range nestCtorInjectTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.8, out)
	}
	for _, name := range angularInjectableProvidedIn(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.84, out)
	}
	for _, name := range angularProviderInjectDeps(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.82, out)
	}
	for _, bind := range angularProviderBinds(classNode, buf) {
		bindName := "angular_bind_" + strings.ToLower(bind.provide) + "_" + strings.ToLower(bind.useClass)
		bindName = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
				return r
			}
			return '_'
		}, bindName)
		line := int(classNode.StartPoint().Row) + 1
		sym := symbol(repoID, relPath, bindName, types.SymbolKindVariable, line, line, "typescript",
			"frameworks=angular; role=provider_bind", classSym)
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitNestCall(repoID, relPath, classSym, bindName, 0.9, out)
		emitNestCall(repoID, relPath, sym.ID, bind.provide, 0.9, out)
		emitNestCall(repoID, relPath, sym.ID, bind.useClass, 0.9, out)
	}
}

func angularNgModuleMetadata(classNode *sitter.Node, buf []byte) map[string][]string {
	out := map[string][]string{}
	text := angularDecoratorText(classNode, buf)
	if text == "" {
		return out
	}
	m := angularNgModulePattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return out
	}
	body := m[1]
	for _, fm := range angularArrayFieldStart.FindAllStringSubmatchIndex(body, -1) {
		if len(fm) < 4 {
			continue
		}
		field := strings.ToLower(body[fm[2]:fm[3]])
		listBody, ok := nestBalancedBracketInner(body, fm[1]-1)
		if !ok {
			continue
		}
		for _, id := range angularIdentInList.FindAllString(listBody, -1) {
			if angularProviderNoiseToken(id) {
				continue
			}
			out[field] = append(out[field], id)
		}
	}
	return out
}

func angularComponentProviders(classNode *sitter.Node, buf []byte) []string {
	text := angularDecoratorText(classNode, buf)
	if text == "" || !strings.Contains(text, "@Component") {
		return nil
	}
	m := angularComponentPattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil
	}
	body := m[1]
	var out []string
	seen := map[string]bool{}
	for _, fm := range angularArrayFieldStart.FindAllStringSubmatchIndex(body, -1) {
		if len(fm) < 4 {
			continue
		}
		field := strings.ToLower(body[fm[2]:fm[3]])
		if field != "providers" && field != "imports" && field != "viewproviders" {
			continue
		}
		listBody, ok := nestBalancedBracketInner(body, fm[1]-1)
		if !ok {
			continue
		}
		for _, id := range angularIdentInList.FindAllString(listBody, -1) {
			if angularProviderNoiseToken(id) || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

type angularProviderBind struct {
	provide  string
	useClass string
}

func angularProviderBinds(classNode *sitter.Node, buf []byte) []angularProviderBind {
	text := angularDecoratorText(classNode, buf)
	var out []angularProviderBind
	seen := map[string]bool{}
	add := func(provide, impl string) {
		provide = strings.Trim(provide, `'"`)
		impl = strings.TrimSpace(impl)
		if provide == "" || impl == "" {
			return
		}
		key := provide + "->" + impl
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, angularProviderBind{provide: provide, useClass: impl})
	}
	// Reuse Nest regexes — Angular DI object literals share provide/useClass shape.
	for _, match := range nestProvideBindPattern.FindAllStringSubmatch(text, -1) {
		if len(match) >= 3 {
			add(match[1], match[2])
		}
	}
	for _, match := range nestUseExistingPattern.FindAllStringSubmatch(text, -1) {
		if len(match) >= 3 {
			add(match[1], match[2])
		}
	}
	for _, match := range nestUseFactoryPattern.FindAllStringSubmatch(text, -1) {
		if len(match) >= 3 {
			add(match[1], match[2])
		}
	}
	return out
}

// angularInjectableProvidedIn returns providedIn class tokens
// (@Injectable({ providedIn: X })). String scopes ('root' / 'platform' / 'any')
// are ignored — they are not call targets.
func angularInjectableProvidedIn(classNode *sitter.Node, buf []byte) []string {
	text := angularDecoratorText(classNode, buf)
	if text == "" || !strings.Contains(text, "@Injectable") {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range angularProvidedInRe.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		tok := strings.Trim(m[1], `'"`)
		tok = strings.TrimSpace(tok)
		if tok == "" || !isCallableName(tok) {
			continue
		}
		switch strings.ToLower(tok) {
		case "root", "platform", "any":
			continue
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// angularProviderInjectDeps collects inject:/deps: tokens from provider objects.
func angularProviderInjectDeps(classNode *sitter.Node, buf []byte) []string {
	text := angularDecoratorText(classNode, buf)
	if text == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	addList := func(inner string) {
		for _, id := range angularIdentInList.FindAllString(inner, -1) {
			if angularProviderNoiseToken(id) || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, m := range angularInjectArrayRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			addList(m[1])
		}
	}
	for _, m := range angularDepsArrayRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			addList(m[1])
		}
	}
	return out
}

func angularProviderNoiseToken(id string) bool {
	switch id {
	case "provide", "useClass", "useValue", "useFactory", "useExisting",
		"deps", "multi", "forwardRef", "inject", "providedIn",
		"RouterModule", "CommonModule", "BrowserModule", "HttpClientModule",
		"FormsModule", "ReactiveFormsModule", "NgModule", "Component",
		"Injectable", "Directive", "Pipe", "Routes", "Route":
		return true
	default:
		return false
	}
}

// extractAngularRouteWires densifies Angular Router `component:` / lazy
// loadComponent|loadChildren `.then(m => m.X)` leaves onto a routes/router
// symbol (const Routes / provideRouter / RouterModule.forRoot|forChild).
func extractAngularRouteWires(repoID, relPath string, buf []byte, frameworks []string, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	if !containsFramework(frameworks, string(FrameworkAngular)) &&
		!looksLikeAngularFile(relPath, buf) &&
		!strings.Contains(src, "@angular/router") &&
		!strings.Contains(src, "provideRouter") &&
		!strings.Contains(src, "RouterModule.for") {
		return
	}
	if !strings.Contains(src, "component:") &&
		!strings.Contains(src, "loadComponent") &&
		!strings.Contains(src, "loadChildren") {
		return
	}

	ensureAngularRouterSymbols(repoID, relPath, src, out)

	var matches [][]int
	matches = append(matches, angularRouteComponentRe.FindAllStringSubmatchIndex(src, -1)...)
	matches = append(matches, angularRouteLoadThenRe.FindAllStringSubmatchIndex(src, -1)...)
	if len(matches) == 0 {
		return
	}
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		page := src[m[2]:m[3]]
		if page == "" || angularProviderNoiseToken(page) {
			continue
		}
		lineNo := 1 + strings.Count(src[:m[0]], "\n")
		from := enclosingSymbolAtLine(out, lineNo)
		if from == "" {
			for _, s := range out.Symbols {
				if strings.Contains(s.Signature, "role=router") {
					from = s.ID
					break
				}
			}
		}
		if from == "" {
			continue
		}
		emitNestCall(repoID, relPath, from, page, 0.88, out)
	}
}

func ensureAngularRouterSymbols(repoID, relPath, src string, out *ParseResult) {
	seenName := map[string]bool{}
	for _, s := range out.Symbols {
		seenName[s.Name] = true
	}
	addRouter := func(name string, line int) {
		if name == "" || seenName[name] {
			return
		}
		seenName[name] = true
		sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "typescript",
			"frameworks=angular; role=router", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	}
	for _, m := range angularRoutesConstRe.FindAllStringSubmatchIndex(src, -1) {
		if len(m) < 4 {
			continue
		}
		name := src[m[2]:m[3]]
		windowEnd := m[1] + 240
		if windowEnd > len(src) {
			windowEnd = len(src)
		}
		window := src[m[0]:windowEnd]
		if !strings.Contains(window, "component:") &&
			!strings.Contains(window, "loadComponent") &&
			!strings.Contains(window, "loadChildren") &&
			!strings.Contains(window, "path:") {
			continue
		}
		lineNo := 1 + strings.Count(src[:m[0]], "\n")
		addRouter(name, lineNo)
	}
	if angularProvideRouterRe.MatchString(src) || angularRouterModuleForRe.MatchString(src) {
		hasRouter := false
		for _, s := range out.Symbols {
			if strings.Contains(s.Signature, "role=router") {
				hasRouter = true
				break
			}
		}
		if !hasRouter {
			line := 1
			if loc := angularProvideRouterRe.FindStringIndex(src); loc != nil {
				line = 1 + strings.Count(src[:loc[0]], "\n")
			} else if loc := angularRouterModuleForRe.FindStringIndex(src); loc != nil {
				line = 1 + strings.Count(src[:loc[0]], "\n")
			}
			addRouter("angular_routes", line)
		}
	}
}

// angularDecoratorText finds decorator source preceding a class
// (@NgModule / @Component / @Injectable / @Directive / @Pipe).
func angularDecoratorText(classNode *sitter.Node, buf []byte) string {
	if classNode == nil {
		return ""
	}
	if p := classNode.Parent(); p != nil {
		var parts []string
		for i := 0; i < int(p.ChildCount()); i++ {
			c := p.Child(i)
			if c == nil {
				continue
			}
			if c == classNode {
				break
			}
			switch c.Type() {
			case "decorator", "call_expression", "identifier":
				parts = append(parts, c.Content(buf))
			}
		}
		if joined := strings.Join(parts, "\n"); angularDecoratorWindow(joined) {
			return joined
		}
	}
	start := int(classNode.StartByte())
	if start <= 0 {
		return ""
	}
	from := start - 1200
	if from < 0 {
		from = 0
	}
	window := string(buf[from:start])
	if angularDecoratorWindow(window) {
		return window
	}
	return ""
}

func angularDecoratorWindow(s string) bool {
	return strings.Contains(s, "@NgModule") || strings.Contains(s, "@Component") ||
		strings.Contains(s, "@Injectable") || strings.Contains(s, "@Directive") ||
		strings.Contains(s, "@Pipe")
}

// angularClassRole returns a role tag from nearby Angular decorators.
func angularClassRole(classNode *sitter.Node, buf []byte) string {
	text := angularDecoratorText(classNode, buf)
	switch {
	case strings.Contains(text, "@NgModule"):
		return "module"
	case strings.Contains(text, "@Component"):
		return "component"
	case strings.Contains(text, "@Directive"):
		return "directive"
	case strings.Contains(text, "@Pipe"):
		return "pipe"
	case strings.Contains(text, "@Injectable"):
		return "injectable"
	default:
		return ""
	}
}

// looksLikeAngularFile reports Angular markers in path or source.
// Distinguishes from Nest: requires @angular/, @NgModule, @Component, or
// *.component.ts — not bare @Injectable / *.module.ts.
func looksLikeAngularFile(relPath string, buf []byte) bool {
	p := strings.ToLower(strings.ReplaceAll(relPath, "\\", "/"))
	if strings.HasSuffix(p, ".component.ts") || strings.HasSuffix(p, ".component.tsx") ||
		strings.HasSuffix(p, ".routes.ts") || strings.HasSuffix(p, ".routing.ts") ||
		strings.Contains(p, "angular.json") {
		return true
	}
	s := string(buf)
	if strings.Contains(s, "@angular/") || strings.Contains(s, "@NgModule(") {
		return true
	}
	if strings.Contains(s, "@Component(") && (strings.Contains(s, "selector:") ||
		strings.Contains(s, "templateUrl:") || strings.Contains(s, "standalone:")) {
		return true
	}
	return false
}
