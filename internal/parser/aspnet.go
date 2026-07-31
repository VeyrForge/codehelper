package parser

import (
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ASP.NET Core densification on the C# graph: Controllers / Minimal APIs /
// ctor + [FromServices] DI + MapGet/MapPost entrypoints so context/impact/trace
// see Controller→Service and route→handler relationships (mirrors Spring/Express).

var (
	aspnetHTTPAttrRe = regexp.MustCompile(
		`\[\s*(HttpGet|HttpPost|HttpPut|HttpDelete|HttpPatch|HttpHead|HttpOptions|Route|AcceptVerbs)\b`)
	aspnetControllerAttrRe = regexp.MustCompile(
		`\[\s*(ApiController|Controller|Route)\b`)
	aspnetFromServicesRe = regexp.MustCompile(
		`\[\s*FromServices\s*\]`)
	aspnetDIRegRe = regexp.MustCompile(
		`\bAdd(?:Scoped|Singleton|Transient|KeyedScoped|KeyedSingleton|KeyedTransient)\s*<\s*([A-Z][A-Za-z0-9_]*)\s*>`)
	aspnetMinimalMapMethods = map[string]bool{
		"MapGet": true, "MapPost": true, "MapPut": true, "MapDelete": true,
		"MapPatch": true, "MapMethods": true, "Map": true, "MapGroup": true,
	}
)

func looksLikeAspNetFile(relPath, content string) bool {
	p := strings.ToLower(relPath)
	body := content
	lower := strings.ToLower(body)
	if strings.Contains(lower, "microsoft.aspnetcore") ||
		strings.Contains(body, "WebApplication") ||
		strings.Contains(body, "ControllerBase") ||
		strings.Contains(body, "IEndpointRouteBuilder") ||
		aspnetControllerAttrRe.MatchString(body) ||
		aspnetHTTPAttrRe.MatchString(body) ||
		aspnetFromServicesRe.MatchString(body) ||
		strings.Contains(body, "MapGet(") || strings.Contains(body, "MapPost(") ||
		strings.Contains(body, "MapPut(") || strings.Contains(body, "MapDelete(") ||
		strings.Contains(p, "/controllers/") || strings.Contains(p, "\\controllers\\") ||
		strings.HasSuffix(p, "controller.cs") ||
		((strings.Contains(p, "startup.cs") || strings.Contains(p, "program.cs")) &&
			(strings.Contains(body, "MapGet") || strings.Contains(body, "MapPost") ||
				strings.Contains(body, "UseEndpoints") || strings.Contains(body, "AddControllers") ||
				strings.Contains(body, "WebApplication"))) {
		return true
	}
	return false
}

func aspnetSkipInjectType(tok string) bool {
	if csharpSkipType(tok) {
		return true
	}
	switch tok {
	case "ControllerBase", "Controller", "PageModel", "Hub", "ViewComponent",
		"ApiController", "Route", "HttpGet", "HttpPost", "HttpPut", "HttpDelete",
		"HttpPatch", "FromServices", "FromBody", "FromQuery", "FromRoute",
		"FromHeader", "FromForm", "BindRequired", "Required",
		"IActionResult", "ActionResult", "IResult", "Results",
		"CancellationToken", "HttpContext", "HttpRequest", "HttpResponse",
		"IFormFile", "IFormCollection", "Stream", "PipeReader",
		"JsonResult", "ContentResult", "FileResult", "RedirectResult",
		"StatusCodeResult", "ObjectResult", "OkObjectResult", "NotFoundResult",
		"BadRequestResult", "UnauthorizedResult", "ForbidResult",
		"ILogger", "IConfiguration", "IHostEnvironment", "IWebHostEnvironment",
		"IServiceProvider", "IServiceCollection", "IApplicationBuilder",
		"IEndpointRouteBuilder", "WebApplication", "WebApplicationBuilder",
		"IHostBuilder", "IHost", "Host", "Program",
		"String", "Int32", "Int64", "Boolean", "Decimal", "Double", "Single",
		"DateTime", "DateTimeOffset", "Guid", "Byte", "Char", "Object",
		"ValueTask", "IAsyncEnumerable", "IEnumerable", "ICollection",
		"IList", "IReadOnlyList", "IDictionary", "Nullable":
		return true
	default:
		return false
	}
}

func emitAspNetCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) || aspnetSkipInjectType(name) {
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

func csharpAttributeNames(n *sitter.Node, buf []byte) []string {
	var out []string
	if n == nil {
		return out
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil || c.Type() != "attribute_list" {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			attr := c.NamedChild(j)
			if attr == nil || attr.Type() != "attribute" {
				continue
			}
			if name := attr.ChildByFieldName("name"); name != nil {
				tok := strings.TrimSpace(name.Content(buf))
				if i := strings.LastIndex(tok, "."); i >= 0 {
					tok = tok[i+1:]
				}
				if tok != "" {
					out = append(out, tok)
				}
			}
		}
	}
	return out
}

func aspnetRoleFromAttributes(attrs []string, typeName string) string {
	joined := strings.Join(attrs, ",")
	has := func(name string) bool {
		for _, a := range attrs {
			if a == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("ApiController") || has("Controller"):
		return "controller"
	case strings.HasSuffix(typeName, "Controller") && (has("Route") || aspnetHTTPAttrRe.MatchString("["+joined)):
		return "controller"
	case strings.HasSuffix(typeName, "Controller"):
		return "controller"
	case strings.HasSuffix(typeName, "Service"):
		return "service"
	case strings.HasSuffix(typeName, "Repository"):
		return "repository"
	default:
		return ""
	}
}

func aspnetMethodRole(attrs []string) string {
	for _, a := range attrs {
		switch a {
		case "HttpGet", "HttpPost", "HttpPut", "HttpDelete", "HttpPatch",
			"HttpHead", "HttpOptions", "AcceptVerbs", "Route":
			return "entrypoint"
		}
	}
	return ""
}

func csharpLeafTypeName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "identifier", "type_identifier":
		return strings.TrimSpace(n.Content(buf))
	case "generic_name", "qualified_name":
		s := strings.TrimSpace(n.Content(buf))
		if i := strings.IndexAny(s, "<["); i > 0 {
			s = s[:i]
		}
		if i := strings.LastIndex(s, "."); i >= 0 {
			s = s[i+1:]
		}
		return s
	case "nullable_type", "array_type", "pointer_type", "ref_type":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if leaf := csharpLeafTypeName(n.NamedChild(i), buf); leaf != "" {
				return leaf
			}
		}
	case "predefined_type":
		return ""
	}
	return ""
}

func csharpCollectFieldTypes(classNode *sitter.Node, buf []byte) map[string]string {
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
			varDecl := (*sitter.Node)(nil)
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c != nil && c.Type() == "variable_declaration" {
					varDecl = c
					break
				}
			}
			if varDecl == nil {
				return
			}
			typ := csharpLeafTypeName(varDecl.ChildByFieldName("type"), buf)
			if typ == "" {
				return
			}
			for i := 0; i < int(varDecl.NamedChildCount()); i++ {
				c := varDecl.NamedChild(i)
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
				if p.Type() != "parameter" {
					return
				}
				typ := csharpLeafTypeName(p.ChildByFieldName("type"), buf)
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

func csharpTypeOf(className string, fields map[string]string) func(string) string {
	if className == "" && len(fields) == 0 {
		return nil
	}
	return func(recv string) string {
		recv = strings.TrimSpace(recv)
		if recv == "" {
			return ""
		}
		if recv == "this" || recv == "base" {
			return className
		}
		if strings.HasPrefix(recv, "this.") {
			recv = strings.TrimPrefix(recv, "this.")
		} else if strings.HasPrefix(recv, "base.") {
			recv = strings.TrimPrefix(recv, "base.")
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

// extractAspNetDI emits Controller/Service injection call edges from ctor
// params, [FromServices] method params, and typed service fields.
func extractAspNetDI(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil || classSym == "" || out == nil {
		return
	}
	seen := map[string]bool{}
	add := func(tok string, conf float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] || aspnetSkipInjectType(tok) {
			return
		}
		seen[tok] = true
		emitAspNetCall(repoID, relPath, classSym, tok, conf, out)
	}

	body := classNode.ChildByFieldName("body")
	if body == nil {
		return
	}
	aspish := looksLikeAspNetFile(relPath, classNode.Content(buf)) ||
		len(csharpAttributeNames(classNode, buf)) > 0 ||
		strings.Contains(classNode.Content(buf), "ControllerBase")

	Walk(body, func(n *sitter.Node) {
		switch n.Type() {
		case "constructor_declaration":
			params := n.ChildByFieldName("parameters")
			if params == nil {
				return
			}
			Walk(params, func(p *sitter.Node) {
				if p.Type() != "parameter" {
					return
				}
				if typ := csharpLeafTypeName(p.ChildByFieldName("type"), buf); typ != "" {
					add(typ, 0.85)
				}
			})
		case "field_declaration":
			if !aspish {
				return
			}
			mods := n.Content(buf)
			varDecl := (*sitter.Node)(nil)
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c != nil && c.Type() == "variable_declaration" {
					varDecl = c
					break
				}
			}
			if varDecl == nil {
				return
			}
			typ := csharpLeafTypeName(varDecl.ChildByFieldName("type"), buf)
			if typ == "" {
				return
			}
			if strings.Contains(mods, "readonly") || strings.Contains(mods, "private") {
				add(typ, 0.78)
			}
		case "method_declaration":
			params := n.ChildByFieldName("parameters")
			if params == nil {
				return
			}
			Walk(params, func(p *sitter.Node) {
				if p.Type() != "parameter" {
					return
				}
				attrs := csharpAttributeNames(p, buf)
				fromSvc := false
				for _, a := range attrs {
					if a == "FromServices" {
						fromSvc = true
						break
					}
				}
				if !fromSvc && !aspnetFromServicesRe.MatchString(p.Content(buf)) {
					return
				}
				if typ := csharpLeafTypeName(p.ChildByFieldName("type"), buf); typ != "" {
					add(typ, 0.88)
					// Also edge from the action method itself when we know its sym later —
					// class-level edge covers Controller→Service for impact.
				}
			})
		}
	})
}

// extractAspNetFromServicesMethod emits method→service call edges for
// [FromServices] parameters on a known method symbol.
func extractAspNetFromServicesMethod(methodNode *sitter.Node, buf []byte, repoID, relPath, methodSym string, out *ParseResult) {
	if methodNode == nil || methodSym == "" || out == nil {
		return
	}
	params := methodNode.ChildByFieldName("parameters")
	if params == nil {
		return
	}
	seen := map[string]bool{}
	Walk(params, func(p *sitter.Node) {
		if p.Type() != "parameter" {
			return
		}
		attrs := csharpAttributeNames(p, buf)
		fromSvc := false
		for _, a := range attrs {
			if a == "FromServices" {
				fromSvc = true
				break
			}
		}
		if !fromSvc && !aspnetFromServicesRe.MatchString(p.Content(buf)) {
			return
		}
		typ := csharpLeafTypeName(p.ChildByFieldName("type"), buf)
		typ = strings.TrimSpace(typ)
		if typ == "" || seen[typ] || aspnetSkipInjectType(typ) {
			return
		}
		seen[typ] = true
		emitAspNetCall(repoID, relPath, methodSym, typ, 0.88, out)
	})
}

// extractAspNetMinimalAPIs indexes app.MapGet/MapPost/… call sites as
// entrypoint symbols with call edges to Map* aliases and DI lambda param types.
func extractAspNetMinimalAPIs(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := string(buf)
	if !looksLikeAspNetFile(relPath, src) &&
		!strings.Contains(src, "MapGet") && !strings.Contains(src, "MapPost") {
		return
	}
	fw := withFramework(frameworks, string(FrameworkAspNetCore))
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "invocation_expression" {
			return
		}
		fn := n.ChildByFieldName("function")
		if fn == nil || fn.Type() != "member_access_expression" {
			return
		}
		prop := fn.ChildByFieldName("name")
		if prop == nil {
			return
		}
		meth := strings.TrimSpace(prop.Content(buf))
		if !aspnetMinimalMapMethods[meth] {
			return
		}
		recv := ""
		for i := 0; i < int(fn.NamedChildCount()); i++ {
			c := fn.NamedChild(i)
			if c != nil && c != prop {
				recv = strings.TrimSpace(c.Content(buf))
				break
			}
		}
		line := int(n.StartPoint().Row) + 1
		alias := meth
		if recv != "" {
			// Peel nested calls: builder.Build().MapGet → MapGet still primary.
			if i := strings.LastIndex(recv, "."); i >= 0 {
				recv = recv[i+1:]
			}
			alias = recv + "." + meth
		}
		siteName := fmt.Sprintf("aspnet_%s_%d", meth, line)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "csharp", frameworkSignature(fw, "entrypoint"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitAspNetCall(repoID, relPath, sym.ID, meth, 0.55, out)
		// Alias-style target for query (app.MapGet) — keep capitalized leaf too.
		if alias != meth {
			tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, alias)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, sym.ID, tgt, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   sym.ID,
				TargetID:   tgt,
				Confidence: 0.85,
			})
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
			body := arg
			if arg.Type() == "argument" && arg.NamedChildCount() > 0 {
				body = arg.NamedChild(0)
			}
			if body == nil {
				continue
			}
			switch body.Type() {
			case "lambda_expression", "anonymous_method_expression":
				params := body.ChildByFieldName("parameters")
				if params != nil {
					Walk(params, func(p *sitter.Node) {
						if p.Type() != "parameter" {
							return
						}
						if typ := csharpLeafTypeName(p.ChildByFieldName("type"), buf); typ != "" {
							emitAspNetCall(repoID, relPath, sym.ID, typ, 0.86, out)
						}
					})
				}
				if lb := body.ChildByFieldName("body"); lb != nil {
					extractCalls(lb, buf, repoID, relPath, sym.ID, out)
					csharpEmitTypeReads(lb, buf, repoID, relPath, sym.ID, out)
				}
			case "identifier":
				// Named handler method reference: app.MapGet("/", GetHealth)
				name := strings.TrimSpace(body.Content(buf))
				if name != "" && name[0] >= 'A' && name[0] <= 'Z' {
					emitAspNetCall(repoID, relPath, sym.ID, name, 0.8, out)
				}
			}
		}
	})

	// DI registration: services.AddScoped<UserService>()
	if enclosing := findCSharpEnclosingMethodSym(root, buf, repoID, relPath, out); enclosing != "" {
		for _, m := range aspnetDIRegRe.FindAllStringSubmatch(src, -1) {
			emitAspNetCall(repoID, relPath, enclosing, m[1], 0.82, out)
		}
	} else {
		for _, m := range aspnetDIRegRe.FindAllStringSubmatch(src, -1) {
			// File-level fallback: attach to Program class when present.
			for _, s := range out.Symbols {
				if s.Name == "Program" && s.Kind == types.SymbolKindClass {
					emitAspNetCall(repoID, relPath, s.ID, m[1], 0.75, out)
					break
				}
			}
		}
	}
}

func findCSharpEnclosingMethodSym(root *sitter.Node, buf []byte, repoID, relPath string, out *ParseResult) string {
	// Prefer Main when present (typical Program.cs Minimal API host).
	for _, s := range out.Symbols {
		if s.Name == "Main" && s.Kind == types.SymbolKindMethod {
			return s.ID
		}
	}
	_ = root
	_ = buf
	_ = repoID
	_ = relPath
	return ""
}
