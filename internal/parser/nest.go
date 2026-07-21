package parser

import (
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	nestModuleCallPattern  = regexp.MustCompile(`(?s)@Module\s*\(\s*\{(.*?)\}\s*\)`)
	nestArrayFieldPattern  = regexp.MustCompile(`(?i)(controllers|providers|imports|exports)\s*:\s*\[([^\]]*)\]`)
	nestIdentInList        = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)
	nestProvideBindPattern = regexp.MustCompile(`(?s)\{\s*provide\s*:\s*([A-Z][A-Za-z0-9_]*|[\'"][^\'"]+[\'"])\s*,\s*useClass\s*:\s*([A-Z][A-Za-z0-9_]*)`)
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
	for _, match := range nestProvideBindPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 3 {
			continue
		}
		provide := strings.Trim(match[1], `'"`)
		if provide != "" && match[2] != "" {
			out = append(out, nestProviderBind{provide: provide, useClass: match[2]})
		}
	}
	return out
}

// nestModuleMetadata returns controllers/providers/imports/exports class names
// from an adjacent @Module({...}) decorator.
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
	for _, fm := range nestArrayFieldPattern.FindAllStringSubmatch(body, -1) {
		if len(fm) < 3 {
			continue
		}
		field := strings.ToLower(fm[1])
		for _, id := range nestIdentInList.FindAllString(fm[2], -1) {
			// Skip common non-class tokens inside provider objects.
			switch id {
			case "provide", "useClass", "useValue", "useFactory", "useExisting",
				"inject", "scope", "multi", "forwardRef":
				continue
			}
			out[field] = append(out[field], id)
		}
	}
	return out
}

// nestDecoratorText finds decorator source preceding a class (export + @Module).
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
		if joined := strings.Join(parts, "\n"); strings.Contains(joined, "@Module") || strings.Contains(joined, "Module(") {
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
	if strings.Contains(window, "@Module") {
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

var nestInjectCallRe = regexp.MustCompile(`@Inject\s*\(\s*(?:forwardRef\s*\(\s*\(\s*\)\s*=>\s*)?([A-Z][A-Za-z0-9_]*)`)

// nestInjectDecoratorTypes finds @Inject(Token) / @Inject(forwardRef(() => T)).
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
		if len(m) < 2 {
			continue
		}
		t := m[1]
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

var nestUseDecoratorRe = regexp.MustCompile(`@Use(?:Guards|Interceptors|Pipes|Filters)\s*\(\s*([^)]*)\)`)
var nestCatchDecoratorRe = regexp.MustCompile(`@Catch\s*\(\s*([^)]*)\)`)

// nestUseDecoratorTypes collects class/method @UseGuards(X) / @UsePipes(Y) targets.
func nestUseDecoratorTypes(classNode *sitter.Node, buf []byte) []string {
	start := int(classNode.StartByte())
	end := int(classNode.EndByte())
	if start < 0 || end > len(buf) || start >= end {
		return nil
	}
	// Include a short window before the class for class-level decorators.
	from := start - 400
	if from < 0 {
		from = 0
	}
	src := string(buf[from:end])
	var out []string
	seen := map[string]bool{}
	for _, m := range nestUseDecoratorRe.FindAllStringSubmatch(src, -1) {
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

// nestCatchDecoratorTypes collects @Catch(HttpException) filter targets.
func nestCatchDecoratorTypes(classNode *sitter.Node, buf []byte) []string {
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
	for _, m := range nestCatchDecoratorRe.FindAllStringSubmatch(src, -1) {
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

// looksLikeNestFile reports NestJS markers in path or source.
func looksLikeNestFile(relPath string, buf []byte) bool {
	p := strings.ToLower(relPath)
	if strings.Contains(p, ".module.") || strings.Contains(p, ".controller.") ||
		strings.Contains(p, ".service.") || strings.Contains(p, ".provider.") ||
		strings.Contains(p, ".guard.") || strings.Contains(p, ".pipe.") ||
		strings.Contains(p, ".interceptor.") {
		return true
	}
	s := string(buf)
	return strings.Contains(s, "@nestjs/") || strings.Contains(s, "@Module(") ||
		strings.Contains(s, "@Injectable(") || strings.Contains(s, "@Controller(") ||
		strings.Contains(s, "@Injectable()") || strings.Contains(s, "@Controller()")
}
