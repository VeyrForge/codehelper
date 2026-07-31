package parser

import (
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	nestModuleCallPattern     = regexp.MustCompile(`(?s)@Module\s*\(\s*\{(.*?)\}\s*\)`)
	nestArrayFieldStartRe     = regexp.MustCompile(`(?i)(controllers|providers|imports|exports)\s*:\s*\[`)
	nestIdentInList           = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)
	nestProvideBindPattern    = regexp.MustCompile(`(?s)\{\s*provide\s*:\s*([A-Z][A-Za-z0-9_]*|[\'"][^\'"]+[\'"])\s*,\s*useClass\s*:\s*([A-Z][A-Za-z0-9_]*)`)
	nestUseExistingPattern    = regexp.MustCompile(`(?s)\{\s*provide\s*:\s*([A-Z][A-Za-z0-9_]*|[\'"][^\'"]+[\'"])\s*,\s*useExisting\s*:\s*([A-Z][A-Za-z0-9_]*)`)
	nestInjectArrayPattern    = regexp.MustCompile(`(?i)inject\s*:\s*\[([^\]]*)\]`)
	nestUseFactoryPattern     = regexp.MustCompile(`(?s)\{\s*provide\s*:\s*([A-Z][A-Za-z0-9_]*|[\'"][^\'"]+[\'"])\s*,\s*useFactory\s*:\s*([A-Za-z_][A-Za-z0-9_]*)`)
	nestApplyMiddlewareRe     = regexp.MustCompile(`\.apply\s*\(\s*([^)]*)\)`)
	nestUseValueProvideRe     = regexp.MustCompile(`(?s)\{\s*provide\s*:\s*([A-Z][A-Za-z0-9_]*|[\'"][^\'"]+[\'"])\s*,\s*useValue\s*:`)
	nestUseDecoratorStartRe   = regexp.MustCompile(`@Use(?:Guards|Interceptors|Pipes|Filters)\s*\(`)
	nestCatchDecoratorStartRe = regexp.MustCompile(`@Catch\s*\(`)
	nestHTTPDecoratorRe       = regexp.MustCompile(`@(Get|Post|Put|Patch|Delete|Options|Head|All|Render|Redirect|Sse|MessagePattern|EventPattern)\b`)
)

// extractNestDI wires NestJS module metadata and constructor injection as call
// edges so context/impact/trace see provider↔module↔controller relationships.
// Also covers property injection, @Inject(...), and class-level @UseGuards /
// @UseInterceptors / @UsePipes / @UseFilters. Emits symref targets resolved
// later by same-dir / unique-name / non-fixture strategies.
func extractNestDI(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil || classSym == "" {
		return
	}
	meta := nestModuleMetadata(classNode, buf)
	for _, field := range []string{"controllers", "providers", "imports", "exports"} {
		for _, name := range meta[field] {
			emitNestCall(repoID, relPath, classSym, name, 0.85, out)
		}
	}
	for _, name := range nestCtorInjectTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.8, out)
	}
	for _, name := range nestPropertyInjectTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.78, out)
	}
	for _, name := range nestInjectDecoratorTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.82, out)
	}
	for _, name := range nestUseDecoratorTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.8, out)
	}
	for _, name := range nestCatchDecoratorTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.8, out)
	}
	for _, bind := range nestProviderBinds(classNode, buf) {
		bindName := "nest_bind_" + strings.ToLower(bind.provide) + "_" + strings.ToLower(bind.useClass)
		bindName = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
				return r
			}
			return '_'
		}, bindName)
		line := int(classNode.StartPoint().Row) + 1
		sym := symbol(repoID, relPath, bindName, types.SymbolKindVariable, line, line, "typescript", "framework=nestjs; role=provider_bind", classSym)
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitNestCall(repoID, relPath, classSym, bindName, 0.9, out)
		emitNestCall(repoID, relPath, sym.ID, bind.provide, 0.9, out)
		emitNestCall(repoID, relPath, sym.ID, bind.useClass, 0.9, out)
	}
	for _, name := range nestInjectArrayTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.82, out)
	}
	for _, name := range nestUseValueProvideTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.84, out)
	}
	for _, name := range nestMiddlewareApplyTypes(classNode, buf) {
		emitNestCall(repoID, relPath, classSym, name, 0.85, out)
	}
}

func emitNestCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
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

type nestProviderBind struct {
	provide  string
	useClass string
}

func nestProviderBinds(classNode *sitter.Node, buf []byte) []nestProviderBind {
	text := nestDecoratorText(classNode, buf)
	var out []nestProviderBind
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
		out = append(out, nestProviderBind{provide: provide, useClass: impl})
	}
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
			// provide + factory function name (capitalized token providers still win).
			add(match[1], match[2])
		}
	}
	return out
}

// nestProviderNoiseTokens are non-class keywords inside Nest provider/module arrays.
func nestProviderNoiseToken(id string) bool {
	switch id {
	case "provide", "useClass", "useValue", "useFactory", "useExisting",
		"inject", "scope", "multi", "forwardRef", "TypeOrmModule",
		"Scope", "REQUEST", "TRANSIENT", "DEFAULT", "durable":
		return true
	default:
		return false
	}
}

// nestInjectArrayTypes collects inject: [DepA, DepB] tokens from provider objects.
func nestInjectArrayTypes(classNode *sitter.Node, buf []byte) []string {
	text := nestDecoratorText(classNode, buf)
	var out []string
	seen := map[string]bool{}
	for _, m := range nestInjectArrayPattern.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		for _, id := range nestIdentInList.FindAllString(m[1], -1) {
			if nestProviderNoiseToken(id) || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// nestUseValueProvideTypes collects provide tokens from { provide: T, useValue: … }
// objects so string/class tokens still appear in the module graph without an impl class.
func nestUseValueProvideTypes(classNode *sitter.Node, buf []byte) []string {
	text := nestDecoratorText(classNode, buf)
	var out []string
	seen := map[string]bool{}
	for _, m := range nestUseValueProvideRe.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		tok := strings.Trim(m[1], `'"`)
		tok = strings.TrimSpace(tok)
		if tok == "" || nestProviderNoiseToken(tok) || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// nestModuleMetadata returns controllers/providers/imports/exports class names
// from an adjacent @Module({...}) decorator. Nested arrays
// (TypeOrmModule.forFeature([Entity])) are balanced so trailing imports survive.
func nestModuleMetadata(classNode *sitter.Node, buf []byte) map[string][]string {
	out := map[string][]string{}
	text := nestDecoratorText(classNode, buf)
	if text == "" {
		return out
	}
	m := nestModuleCallPattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return out
	}
	body := m[1]
	for _, fm := range nestArrayFieldStartRe.FindAllStringSubmatchIndex(body, -1) {
		if len(fm) < 4 {
			continue
		}
		field := strings.ToLower(body[fm[2]:fm[3]])
		listBody, ok := nestBalancedBracketInner(body, fm[1]-1)
		if !ok {
			continue
		}
		for _, id := range nestIdentInList.FindAllString(listBody, -1) {
			if nestProviderNoiseToken(id) {
				continue
			}
			out[field] = append(out[field], id)
		}
	}
	return out
}

// nestBalancedBracketInner returns the content inside [...] starting at openIdx
// (index of '['). Nested brackets are respected.
func nestBalancedBracketInner(s string, openIdx int) (string, bool) {
	return nestBalancedDelimInner(s, openIdx, '[', ']')
}

// nestBalancedParenInner returns the content inside (...) starting at openIdx.
func nestBalancedParenInner(s string, openIdx int) (string, bool) {
	return nestBalancedDelimInner(s, openIdx, '(', ')')
}

func nestBalancedDelimInner(s string, openIdx int, open, close byte) (string, bool) {
	if openIdx < 0 || openIdx >= len(s) || s[openIdx] != open {
		return "", false
	}
	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[openIdx+1 : i], true
			}
		}
	}
	return "", false
}

// nestMiddlewareApplyTypes collects MiddlewareConsumer.apply(A, B) targets.
func nestMiddlewareApplyTypes(classNode *sitter.Node, buf []byte) []string {
	body := classNode.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	src := body.Content(buf)
	var out []string
	seen := map[string]bool{}
	for _, m := range nestApplyMiddlewareRe.FindAllStringSubmatch(src, -1) {
		if len(m) < 2 {
			continue
		}
		for _, id := range nestIdentInList.FindAllString(m[1], -1) {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// nestClassRole tags Nest classes from decorators / Nest interfaces / name suffixes.
func nestClassRole(classNode *sitter.Node, buf []byte) string {
	text := nestClassWindow(classNode, buf)
	lowerName := strings.ToLower(ChildName(classNode, "name", buf))
	switch {
	case strings.Contains(text, "@Module"):
		return "module"
	case strings.Contains(text, "@Controller"):
		return "controller"
	case strings.Contains(text, "@Catch") || strings.Contains(text, "ExceptionFilter"):
		return "filter"
	case strings.Contains(text, "NestMiddleware"):
		return "middleware"
	case strings.Contains(text, "CanActivate") || strings.HasSuffix(lowerName, "guard"):
		return "guard"
	case strings.Contains(text, "NestInterceptor") || strings.HasSuffix(lowerName, "interceptor"):
		return "interceptor"
	case strings.Contains(text, "PipeTransform") || strings.HasSuffix(lowerName, "pipe"):
		return "pipe"
	case strings.Contains(text, "@Injectable"):
		return "service"
	default:
		return ""
	}
}

// nestMethodHTTPRole tags @Get/@Post/... handler methods as entrypoints.
func nestMethodHTTPRole(methodNode *sitter.Node, buf []byte) string {
	if methodNode == nil {
		return ""
	}
	start := int(methodNode.StartByte())
	from := start - 350
	if from < 0 {
		from = 0
	}
	window := string(buf[from:start])
	// Prefer explicit decorator siblings when present.
	if p := methodNode.Parent(); p != nil {
		var parts []string
		for i := 0; i < int(p.ChildCount()); i++ {
			c := p.Child(i)
			if c == nil {
				continue
			}
			if c == methodNode {
				break
			}
			if c.Type() == "decorator" {
				parts = append(parts, c.Content(buf))
			}
		}
		if joined := strings.Join(parts, "\n"); joined != "" {
			window = joined
		}
	}
	if nestHTTPDecoratorRe.MatchString(window) {
		return "entrypoint"
	}
	return ""
}

func nestClassWindow(classNode *sitter.Node, buf []byte) string {
	if classNode == nil {
		return ""
	}
	start := int(classNode.StartByte())
	end := int(classNode.EndByte())
	if start < 0 || end > len(buf) || start >= end {
		return ""
	}
	from := start - 600
	if from < 0 {
		from = 0
	}
	return string(buf[from:end])
}

// nestDecoratorText finds decorator source preceding a class (export + @Module/@Controller/…).
func nestDecoratorText(classNode *sitter.Node, buf []byte) string {
	if classNode == nil {
		return ""
	}
	// Prefer explicit decorator siblings under the same parent.
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
		if joined := strings.Join(parts, "\n"); joined != "" {
			return joined
		}
	}
	// Fallback: look back a short window before the class keyword.
	start := int(classNode.StartByte())
	if start <= 0 {
		return ""
	}
	from := start - 800
	if from < 0 {
		from = 0
	}
	window := string(buf[from:start])
	if strings.Contains(window, "@Module") || strings.Contains(window, "@Controller") ||
		strings.Contains(window, "@Injectable") || strings.Contains(window, "@Catch") {
		return window
	}
	return ""
}

// nestCtorInjectTypes collects constructor parameter type identifiers
// (e.g. `constructor(private readonly catsService: CatsService)`).
func nestCtorInjectTypes(classNode *sitter.Node, buf []byte) []string {
	body := classNode.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	Walk(body, func(n *sitter.Node) {
		if n.Type() != "method_definition" && n.Type() != "public_field_definition" {
			return
		}
		name := ChildName(n, "name", buf)
		if name != "constructor" {
			return
		}
		params := n.ChildByFieldName("parameters")
		if params == nil {
			// Some grammars nest parameters without a field name.
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c != nil && (c.Type() == "formal_parameters" || c.Type() == "parameters") {
					params = c
					break
				}
			}
		}
		if params == nil {
			return
		}
		Walk(params, func(p *sitter.Node) {
			switch p.Type() {
			case "type_identifier", "identifier":
				// Only accept capitalized type names (classes/tokens).
				t := strings.TrimSpace(p.Content(buf))
				if t == "" || t[0] < 'A' || t[0] > 'Z' {
					return
				}
				// Skip the parameter identifier itself when it is camelCase
				// without a separate type node nearby — prefer type_identifier.
				if p.Type() == "identifier" {
					return
				}
				if seen[t] {
					return
				}
				seen[t] = true
				out = append(out, t)
			case "type_annotation":
				// Walk type_annotation children via nested Walk above.
			}
		})
	})
	return out
}

// nestPropertyInjectTypes collects typed class fields used for property DI
// (e.g. `private readonly catsService: CatsService;` outside the constructor).
func nestPropertyInjectTypes(classNode *sitter.Node, buf []byte) []string {
	body := classNode.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	Walk(body, func(n *sitter.Node) {
		if n.Type() != "public_field_definition" && n.Type() != "field_definition" &&
			n.Type() != "property_definition" {
			return
		}
		// Skip methods / constructors — those are handled elsewhere.
		if n.ChildByFieldName("value") != nil {
			val := n.ChildByFieldName("value")
			if val != nil && (val.Type() == "arrow_function" || val.Type() == "function" ||
				val.Type() == "function_expression") {
				return
			}
		}
		Walk(n, func(p *sitter.Node) {
			if p.Type() != "type_identifier" {
				return
			}
			t := strings.TrimSpace(p.Content(buf))
			if t == "" || t[0] < 'A' || t[0] > 'Z' || seen[t] {
				return
			}
			seen[t] = true
			out = append(out, t)
		})
	})
	return out
}

// nestInjectCallRe matches @Inject(Token), @Inject('TOKEN'), and forwardRef forms.
// Group 1 = class/const ident; group 2 = string token (identifier-shaped).
var nestInjectCallRe = regexp.MustCompile(`@Inject\s*\(\s*(?:forwardRef\s*\(\s*\(\s*\)\s*=>\s*)?(?:([A-Z][A-Za-z0-9_]*)|[\'"]([A-Za-z_][A-Za-z0-9_]*)[\'"])`)

// nestInjectDecoratorTypes finds @Inject(Token) / @Inject('tok') / forwardRef forms
// on ctor params and class fields.
func nestInjectDecoratorTypes(classNode *sitter.Node, buf []byte) []string {
	text := nestDecoratorText(classNode, buf)
	body := ""
	if b := classNode.ChildByFieldName("body"); b != nil {
		body = b.Content(buf)
	}
	src := text + "\n" + body
	var out []string
	seen := map[string]bool{}
	for _, m := range nestInjectCallRe.FindAllStringSubmatch(src, -1) {
		t := ""
		if len(m) >= 2 {
			t = m[1]
		}
		if t == "" && len(m) >= 3 {
			t = m[2]
		}
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// nestUseDecoratorTypes collects class/method @UseGuards/@UseInterceptors/@UsePipes/
// @UseFilters targets, including nested calls like new ValidationPipe({…}).
func nestUseDecoratorTypes(classNode *sitter.Node, buf []byte) []string {
	return nestDecoratorArgTypes(classNode, buf, nestUseDecoratorStartRe)
}

// nestCatchDecoratorTypes collects @Catch(HttpException) filter targets.
func nestCatchDecoratorTypes(classNode *sitter.Node, buf []byte) []string {
	return nestDecoratorArgTypes(classNode, buf, nestCatchDecoratorStartRe)
}

func nestDecoratorArgTypes(classNode *sitter.Node, buf []byte, startRe *regexp.Regexp) []string {
	start := int(classNode.StartByte())
	end := int(classNode.EndByte())
	if start < 0 || end > len(buf) || start >= end {
		return nil
	}
	from := start - 400
	if from < 0 {
		from = 0
	}
	src := string(buf[from:end])
	var out []string
	seen := map[string]bool{}
	for _, loc := range startRe.FindAllStringIndex(src, -1) {
		if len(loc) < 2 {
			continue
		}
		openIdx := loc[1] - 1
		inner, ok := nestBalancedParenInner(src, openIdx)
		if !ok {
			continue
		}
		for _, id := range nestIdentInList.FindAllString(inner, -1) {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// looksLikeNestFile reports NestJS markers in path or source.
// Angular files that share .module.ts / @Injectable are excluded.
func looksLikeNestFile(relPath string, buf []byte) bool {
	if looksLikeAngularFile(relPath, buf) {
		return false
	}
	p := strings.ToLower(relPath)
	if strings.Contains(p, ".module.") || strings.Contains(p, ".controller.") ||
		strings.Contains(p, ".service.") || strings.Contains(p, ".provider.") ||
		strings.Contains(p, ".guard.") || strings.Contains(p, ".pipe.") ||
		strings.Contains(p, ".interceptor.") || strings.Contains(p, ".middleware.") ||
		strings.Contains(p, ".filter.") || strings.Contains(p, ".gateway.") {
		return true
	}
	s := string(buf)
	return strings.Contains(s, "@nestjs/") || strings.Contains(s, "@Module(") ||
		strings.Contains(s, "@Injectable(") || strings.Contains(s, "@Controller(") ||
		strings.Contains(s, "@Injectable()") || strings.Contains(s, "@Controller()") ||
		strings.Contains(s, "NestMiddleware") || strings.Contains(s, "MiddlewareConsumer")
}
