package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	tsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	tst "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseResult holds symbols and edges extracted from one source file.
type ParseResult struct {
	Symbols []types.Symbol
	Edges   []types.Reference
	Imports []string
}

// ParseTypeScript parses buf as TypeScript/TSX based on file extension.
func ParseTypeScript(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	_ = ctx
	ext := strings.ToLower(filepath.Ext(relPath))
	var lang *sitter.Language
	switch ext {
	case ".tsx":
		lang = tsx.GetLanguage()
	case ".ts", ".js", ".jsx", ".mjs", ".cjs":
		lang = tst.GetLanguage()
	default:
		lang = tst.GetLanguage()
	}
	p := sitter.NewParser()
	p.SetLanguage(lang)
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	root := tree.RootNode()
	out := &ParseResult{}
	langName := "typescript"
	if ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
		langName = "javascript"
	}
	frameworks := DetectFrameworkPacks(relPath, nil, string(buf))
	fid := FileNodeID(repoID, relPath)
	Walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "import_statement":
			if src := importSource(n, buf); src != "" {
				out.Imports = append(out.Imports, src)
				modID := moduleNodeID(repoID, src)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, modID, "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   modID,
					Confidence: 0.9,
				})
			}
		case "call_expression":
			// CommonJS require('…') / require("…") — Express and most Node libs.
			if mod := cjsRequireModule(n, buf); mod != "" {
				out.Imports = append(out.Imports, mod)
				modID := moduleNodeID(repoID, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, modID, "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   modID,
					Confidence: 0.85,
				})
			}
		case "function_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			role := tsFrameworkRole(relPath, name, frameworks)
			if role == "" {
				role = nextExportRole(relPath, name, frameworks, buf)
			}
			if role == "" && looksLikeRouteHandlerName(name) {
				role = "entrypoint"
			}
			fw := frameworks
			if role == "server_action" || role == "metadata" || role == "static_params" || role == "middleware" {
				fw = withFramework(fw, string(FrameworkNextJS))
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(fw, role), parentFromStack(n))
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, buildJSInstanceScope(n, buf))
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "method_definition":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			role := tsFrameworkRole(relPath, name, frameworks)
			fw := frameworks
			if looksLikeNestFile(relPath, buf) {
				fw = withFramework(fw, "nestjs")
				if nestRole := nestMethodHTTPRole(n, buf); nestRole != "" {
					role = nestRole
				}
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(fw, role), parentClassID(n, buf))
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			// Nest/Angular ctor DI + typed fields: this.svc.m() → Service.m
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, buildJSMethodTypeOf(n, buf))
			addReadEdgesFromNode(repoID, relPath, sym.ID, n, buf, out)
		case "class_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			classFW := frameworks
			classRole := ""
			if looksLikeNestFile(relPath, buf) {
				classFW = withFramework(classFW, "nestjs")
				classRole = nestClassRole(n, buf)
			}
			if looksLikeAngularFile(relPath, buf) {
				classFW = withFramework(classFW, string(FrameworkAngular))
				classRole = angularClassRole(n, buf)
			}
			embeds := tsCollectEmbedNames(n, buf)
			sig := appendEmbedsSig(frameworkSignature(classFW, classRole), embeds)
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			tsEmitHeritage(n, buf, repoID, relPath, sym.ID, out)
			if looksLikeNestFile(relPath, buf) {
				extractNestDI(n, buf, repoID, relPath, sym.ID, out)
			}
			if looksLikeAngularFile(relPath, buf) {
				extractAngularDI(n, buf, repoID, relPath, sym.ID, out)
			}
			if looksLikeTypeORMFile(relPath, buf) {
				extractTypeORMEntity(n, buf, repoID, relPath, sym.ID, out)
			}
		case "interface_declaration":
			name := ChildName(n, "name", buf)
			if name == "" {
				return
			}
			embeds := tsCollectEmbedNames(n, buf)
			sig := appendEmbedsSig("", embeds)
			sym := symbol(repoID, relPath, name, types.SymbolKindInterface, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			tsEmitHeritage(n, buf, repoID, relPath, sym.ID, out)
		case "variable_declarator":
			// Capture function-like variables used heavily in React/Next.js:
			// const Page = () => {}
			// const handler = async function() {}
			id := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if id == nil || val == nil {
				return
			}
			name := strings.TrimSpace(id.Content(buf))
			if name == "" {
				return
			}
			var body *sitter.Node
			role := ""
			if isFunctionLikeTSNode(val.Type()) {
				body = val
			} else if fnNode := wrappedFunctionNode(val, buf); fnNode != nil {
				body = fnNode
				role = "wrapped_component"
			}
			if body == nil {
				if isCapacitorPluginRegistration(val, buf) {
					sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(withFramework(frameworks, string(FrameworkCapacitor)), "plugin"), parentFromStack(n))
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					addReadEdgesFromNode(repoID, relPath, sym.ID, val, buf, out)
				} else if isDrizzleTableBuilder(val, buf) {
					fw := withFramework(frameworks, string(FrameworkDrizzle))
					sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(fw, "table"), parentFromStack(n))
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					addReadEdgesFromNode(repoID, relPath, sym.ID, val, buf, out)
				} else if isReactNativeNavigatorFactory(val, buf) {
					fw := withFramework(frameworks, string(FrameworkReactNative))
					fw = withFramework(fw, string(FrameworkReact))
					sym := symbol(repoID, relPath, name, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(fw, "navigator"), parentFromStack(n))
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					addReadEdgesFromNode(repoID, relPath, sym.ID, val, buf, out)
				}
				return
			}
			fw := frameworks
			if pathRole := tsFrameworkRole(relPath, name, frameworks); pathRole != "" {
				role = pathRole
			} else if nextRole := nextExportRole(relPath, name, frameworks, buf); nextRole != "" {
				role = nextRole
			} else if looksLikeRouteHandlerName(name) {
				role = "entrypoint"
			}
			if role == "server_action" || role == "metadata" || role == "static_params" || role == "middleware" {
				fw = withFramework(fw, string(FrameworkNextJS))
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(fw, role), parentFromStack(n))
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCallsScoped(body, buf, repoID, relPath, sym.ID, out, buildJSInstanceScope(body, buf))
			addReadEdgesFromNode(repoID, relPath, sym.ID, body, buf, out)
		case "export_statement":
			// Frameworks often use anonymous default exports for entrypoints.
			exportBody := strings.TrimSpace(n.Content(buf))
			if strings.HasPrefix(exportBody, "export default") && looksLikeAnonymousDefaultFunction(exportBody) {
				role := tsFrameworkRole(relPath, "default_export", frameworks)
				if role == "" {
					role = "entrypoint"
				}
				sym := symbol(repoID, relPath, "default_export", types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, frameworkSignature(frameworks, role), "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
		case "assignment_expression":
			// CommonJS / Express-style prototype APIs:
			//   app.use = function use(...) {}
			//   exports.Router = Router
			//   proto.send = function send(...) {}
			// Index under the dotted alias (app.use) so query/context("app.use")
			// resolve; bare property stays in signature as bare= for substring hits.
			// Skip TypeScript class field assigns (this.x = …) on Angular/Nest —
			// those are not CJS exports and flood the index with noise.
			if name, alias, body, kind, ok := cjsPrototypeAssign(n, buf); ok {
				if (looksLikeAngularFile(relPath, buf) || looksLikeNestFile(relPath, buf)) &&
					strings.HasPrefix(strings.ToLower(alias), "this.") {
					return
				}
				symName := name
				if alias != "" {
					symName = alias
				}
				sig := frameworkSignature(frameworks, "cjs_export")
				if name != "" && name != symName {
					if sig != "" {
						sig += "; "
					}
					sig += "bare=" + name
				}
				if alias != "" {
					if sig != "" {
						sig += "; "
					}
					sig += "alias=" + alias
				}
				sym := symbol(repoID, relPath, symName, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, langName, sig, "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				if body != nil {
					extractCallsScoped(body, buf, repoID, relPath, sym.ID, out, buildJSInstanceScope(body, buf))
					addReadEdgesFromNode(repoID, relPath, sym.ID, body, buf, out)
				}
			}
		}
	})
	extractExpressTopLevel(root, buf, repoID, relPath, frameworks, out)
	extractFastifyHonoRoutes(root, buf, repoID, relPath, frameworks, out)
	extractEdgeRuntimeServe(root, buf, repoID, relPath, frameworks, out)
	extractNuxtDensify(root, buf, repoID, relPath, frameworks, out)
	extractElectronIPC(root, buf, repoID, relPath, frameworks, out)
	extractORMClientUsage(repoID, relPath, buf, out)
	extractReactNativeScreenWires(repoID, relPath, buf, frameworks, out)
	extractIonicRouteWires(repoID, relPath, buf, frameworks, out)
	extractAngularRouteWires(repoID, relPath, buf, frameworks, out)
	return out, nil
}

// cjsRequireModule extracts the module path from require('x') / require("x").
func cjsRequireModule(n *sitter.Node, buf []byte) string {
	if n == nil || n.Type() != "call_expression" {
		return ""
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Type() != "identifier" {
		return ""
	}
	if strings.TrimSpace(fn.Content(buf)) != "require" {
		return ""
	}
	args := n.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "string", "string_fragment":
			s := strings.TrimSpace(c.Content(buf))
			s = strings.Trim(s, `"'`+"`")
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// extractExpressTopLevel densifies Express app|router.get/post/use/… sites
// (including nested), express.Router() factories, app.use mount targets, and
// named middleware-chain identifiers onto the JS/TS graph. Honest: cross-file
// router imports and dynamic app.use(path, require(…)) stay name-only.
func extractExpressTopLevel(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := string(buf)
	lower := strings.ToLower(src)
	looksExpress := containsFramework(frameworks, string(FrameworkExpress)) ||
		strings.Contains(src, "express()") || strings.Contains(src, "express.Router") ||
		strings.Contains(src, "require('express") || strings.Contains(src, `require("express`) ||
		strings.Contains(lower, "from 'express'") || strings.Contains(lower, `from "express"`) ||
		strings.Contains(src, "require('../../") || strings.Contains(src, `require("../..`)
	if !looksExpress {
		return
	}
	// Avoid React Router `Router()` false positives when no express.Router()/express() factory.
	bareRouterOK := strings.Contains(src, "express.Router") ||
		strings.Contains(src, "express()") ||
		strings.Contains(src, "require('express") || strings.Contains(src, `require("express`) ||
		strings.Contains(lower, "from 'express'") || strings.Contains(lower, `from "express"`) ||
		(!strings.Contains(lower, "react-router") && (strings.Contains(src, "require('../../") || strings.Contains(src, `require("../..`)))
	fw := withFramework(frameworks, string(FrameworkExpress))
	appVars, routerVars := collectExpressReceivers(root, buf, bareRouterOK)

	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return
		}
		line := int(n.StartPoint().Row) + 1

		// express.Router() / Router() factory → entrypoint + Router hub.
		if isExpressRouterFactory(fn, buf, bareRouterOK) {
			siteName := fmt.Sprintf("express_router_%d", line)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "javascript",
				frameworkSignature(fw, "router"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitTSCall(repoID, relPath, sym.ID, "Router", 0.7, out)
			if fn.Type() == "member_expression" {
				emitTSCall(repoID, relPath, sym.ID, "express.Router", 0.85, out)
			}
			return
		}

		if fn.Type() != "member_expression" {
			return
		}
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return
		}
		recv := strings.TrimSpace(obj.Content(buf))
		meth := strings.TrimSpace(prop.Content(buf))
		if !isExpressRouteMethod(meth) {
			return
		}
		_, isApp := appVars[recv]
		_, isRouter := routerVars[recv]
		if !isApp && !isRouter && !isExpressAPIReceiver(recv) {
			return
		}
		alias := recv + "." + meth
		siteName := fmt.Sprintf("express_%s_%d", meth, line)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "javascript", frameworkSignature(fw, "entrypoint"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitTSCall(repoID, relPath, sym.ID, alias, 0.85, out)
		emitTSCall(repoID, relPath, sym.ID, meth, 0.55, out)
		// Example apps → library Application/Router hubs (self-only inbound residual).
		hub := expressHubForReceiver(recv, isApp, isRouter)
		if hub != "" {
			emitTSCall(repoID, relPath, sym.ID, hub, 0.7, out)
		}
		// Named middleware / mount targets + inline handler bodies (Fastify-style).
		if args := n.ChildByFieldName("arguments"); args != nil {
			densifyExpressRouteArgs(args, buf, repoID, relPath, sym.ID, out)
		}
	})
}

// collectExpressReceivers finds identifiers bound to express() / express.Router()
// / Router() so routes on custom names (usersRouter.get) densify.
func collectExpressReceivers(root *sitter.Node, buf []byte, bareRouterOK bool) (appVars, routerVars map[string]struct{}) {
	appVars = map[string]struct{}{
		"app": {}, "application": {}, "server": {},
	}
	routerVars = map[string]struct{}{
		"router": {}, "route": {},
	}
	if root == nil {
		return appVars, routerVars
	}
	add := func(name string, router bool) {
		name = strings.TrimSpace(name)
		if name == "" || !plausibleJSIdent(name) {
			return
		}
		if router {
			routerVars[name] = struct{}{}
			return
		}
		appVars[name] = struct{}{}
	}
	Walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "variable_declarator":
			nameNode := n.ChildByFieldName("name")
			val := peelJSAssignToCall(n.ChildByFieldName("value"))
			if nameNode == nil || nameNode.Type() != "identifier" || val == nil {
				return
			}
			kind := expressFactoryKind(val.ChildByFieldName("function"), buf, bareRouterOK)
			switch kind {
			case "router":
				add(nameNode.Content(buf), true)
			case "app":
				add(nameNode.Content(buf), false)
			}
		case "assignment_expression":
			left := n.ChildByFieldName("left")
			val := peelJSAssignToCall(n.ChildByFieldName("right"))
			if left == nil || left.Type() != "identifier" || val == nil {
				return
			}
			kind := expressFactoryKind(val.ChildByFieldName("function"), buf, bareRouterOK)
			switch kind {
			case "router":
				add(left.Content(buf), true)
			case "app":
				add(left.Content(buf), false)
			}
		}
	})
	return appVars, routerVars
}

func peelJSAssignToCall(n *sitter.Node) *sitter.Node {
	for n != nil {
		switch n.Type() {
		case "call_expression":
			return n
		case "assignment_expression":
			n = n.ChildByFieldName("right")
		case "parenthesized_expression", "await_expression":
			if n.NamedChildCount() > 0 {
				n = n.NamedChild(0)
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	return nil
}

// expressFactoryKind returns "router", "app", or "" for express.Router()/express()/Router().
func expressFactoryKind(fn *sitter.Node, buf []byte, bareRouterOK bool) string {
	if isExpressRouterFactory(fn, buf, bareRouterOK) {
		return "router"
	}
	if fn == nil {
		return ""
	}
	if fn.Type() == "identifier" && strings.EqualFold(strings.TrimSpace(fn.Content(buf)), "express") {
		return "app"
	}
	return ""
}

func isExpressRouterFactory(fn *sitter.Node, buf []byte, bareRouterOK bool) bool {
	if fn == nil {
		return false
	}
	switch fn.Type() {
	case "identifier":
		return bareRouterOK && strings.EqualFold(strings.TrimSpace(fn.Content(buf)), "Router")
	case "member_expression":
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return false
		}
		return strings.EqualFold(strings.TrimSpace(obj.Content(buf)), "express") &&
			strings.EqualFold(strings.TrimSpace(prop.Content(buf)), "Router")
	default:
		return false
	}
}

func expressHubForReceiver(recv string, isApp, isRouter bool) string {
	switch strings.ToLower(strings.TrimSpace(recv)) {
	case "app", "application", "server":
		return "Application"
	case "router", "route":
		return "Router"
	}
	if isRouter {
		return "Router"
	}
	if isApp {
		return "Application"
	}
	return ""
}

// densifyExpressRouteArgs emits calls for named middleware / mount targets and
// scans inline handlers. String path args are skipped.
func densifyExpressRouteArgs(args *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if args == nil {
		return
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		arg := args.NamedChild(i)
		if arg == nil {
			continue
		}
		switch arg.Type() {
		case "identifier":
			emitTSCall(repoID, relPath, fromSym, strings.TrimSpace(arg.Content(buf)), 0.85, out)
		case "arrow_function", "function_expression", "function_declaration":
			extractCalls(arg, buf, repoID, relPath, fromSym, out)
			addReadEdgesFromNode(repoID, relPath, fromSym, arg, buf, out)
		case "call_expression":
			// Middleware factories: cors(), helmet(), express.json()
			if cfn := arg.ChildByFieldName("function"); cfn != nil {
				if cfn.Type() == "member_expression" {
					if alias := expressMemberAlias(cfn, buf); alias != "" {
						emitTSCall(repoID, relPath, fromSym, alias, 0.85, out)
					}
				}
				if nm := calleeName(cfn, buf); nm != "" {
					emitTSCall(repoID, relPath, fromSym, nm, 0.85, out)
				}
			}
		}
	}
}

func isExpressAPIReceiver(recv string) bool {
	switch strings.ToLower(strings.TrimSpace(recv)) {
	case "app", "router", "route", "server", "application":
		return true
	default:
		return false
	}
}

func isExpressRouteMethod(m string) bool {
	switch strings.ToLower(m) {
	case "use", "get", "post", "put", "patch", "delete", "all", "listen",
		"set", "engine", "param", "route", "render", "enable", "disable",
		"enabled", "disabled", "path", "handle", "init":
		return true
	default:
		return false
	}
}

// extractFastifyHonoRoutes densifies Fastify/Hono app|fastify.get/post/… sites
// (and named handler args) onto the TS graph. Honest Medium: plugin/encapsulate
// graphs and Hono middleware chains are not claimed.
func extractFastifyHonoRoutes(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := string(buf)
	lower := strings.ToLower(src)
	looksFastify := containsFramework(frameworks, string(FrameworkFastify)) ||
		strings.Contains(lower, "from 'fastify'") || strings.Contains(lower, `from "fastify"`) ||
		strings.Contains(lower, "require('fastify") || strings.Contains(lower, `require("fastify`) ||
		strings.Contains(lower, "fastify()") || strings.Contains(lower, "@fastify/")
	looksHono := containsFramework(frameworks, string(FrameworkHono)) ||
		strings.Contains(lower, "from 'hono'") || strings.Contains(lower, `from "hono"`) ||
		strings.Contains(lower, "from 'hono/") || strings.Contains(lower, `from "hono/`) ||
		strings.Contains(lower, "new hono(")
	if !looksFastify && !looksHono {
		return
	}
	pack := string(FrameworkFastify)
	prefix := "fastify"
	if looksHono && !looksFastify {
		pack = string(FrameworkHono)
		prefix = "hono"
	} else if looksHono && looksFastify {
		// Prefer Hono when both markers somehow appear (rare).
		pack = string(FrameworkHono)
		prefix = "hono"
	}
	fw := withFramework(frameworks, pack)
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		fn := n.ChildByFieldName("function")
		if fn == nil || fn.Type() != "member_expression" {
			return
		}
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return
		}
		recv := strings.TrimSpace(obj.Content(buf))
		meth := strings.TrimSpace(prop.Content(buf))
		if !isFastifyHonoReceiver(recv) || !isFastifyHonoRouteMethod(meth) {
			return
		}
		line := int(n.StartPoint().Row) + 1
		siteName := fmt.Sprintf("%s_%s_%d", prefix, strings.ToLower(meth), line)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "typescript",
			frameworkSignature(fw, "entrypoint"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		alias := recv + "." + meth
		emitTSCall(repoID, relPath, sym.ID, alias, 0.85, out)
		emitTSCall(repoID, relPath, sym.ID, meth, 0.55, out)
		if args := n.ChildByFieldName("arguments"); args != nil {
			for i := 0; i < int(args.NamedChildCount()); i++ {
				arg := args.NamedChild(i)
				if arg == nil {
					continue
				}
				switch arg.Type() {
				case "identifier":
					emitTSCall(repoID, relPath, sym.ID, strings.TrimSpace(arg.Content(buf)), 0.85, out)
				case "arrow_function", "function_expression":
					extractCalls(arg, buf, repoID, relPath, sym.ID, out)
					addReadEdgesFromNode(repoID, relPath, sym.ID, arg, buf, out)
				}
			}
		}
	})
}

func isFastifyHonoReceiver(recv string) bool {
	switch strings.ToLower(strings.TrimSpace(recv)) {
	case "app", "fastify", "server", "api", "router", "r", "route":
		return true
	default:
		return false
	}
}

func isFastifyHonoRouteMethod(m string) bool {
	// HTTP route verbs only. listen/register/use/on/addHook/basePath are
	// lifecycle/plugin/middleware APIs — claiming them as entrypoints would
	// contradict the Medium honesty note on extractFastifyHonoRoutes.
	switch strings.ToLower(m) {
	case "get", "post", "put", "patch", "delete", "all", "route", "head", "options":
		return true
	default:
		return false
	}
}

// expressMemberAlias returns "app.use" for member_expression callees whose
// receiver is an Express/CJS API object (same scope as cjsPrototypeAssign).
func expressMemberAlias(fn *sitter.Node, buf []byte) string {
	if fn == nil || fn.Type() != "member_expression" {
		return ""
	}
	obj := fn.ChildByFieldName("object")
	prop := fn.ChildByFieldName("property")
	if obj == nil || prop == nil {
		return ""
	}
	recv := strings.TrimSpace(obj.Content(buf))
	meth := strings.TrimSpace(prop.Content(buf))
	if meth == "" || !plausibleJSIdent(meth) {
		return ""
	}
	lower := strings.ToLower(recv)
	switch {
	case lower == "app", lower == "req", lower == "res", lower == "proto",
		lower == "router", lower == "route", lower == "server",
		lower == "request", lower == "response", lower == "application",
		lower == "exports",
		strings.HasSuffix(lower, ".prototype"),
		strings.HasPrefix(lower, "exports."):
		return recv + "." + meth
	default:
		return ""
	}
}

func emitTSCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
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

func containsFramework(list []string, want string) bool {
	for _, f := range list {
		if f == want {
			return true
		}
	}
	return false
}

// cjsPrototypeAssign extracts symbols from CommonJS/Express-style member assigns.
// Scoped to exports.*, module.exports.*, *.prototype.*, and app/req/res/proto
// receivers so we do not flood the index with arbitrary object mutations.
func cjsPrototypeAssign(n *sitter.Node, buf []byte) (name, alias string, body *sitter.Node, kind types.SymbolKind, ok bool) {
	left := n.ChildByFieldName("left")
	right := n.ChildByFieldName("right")
	if left == nil || right == nil || left.Type() != "member_expression" {
		return "", "", nil, "", false
	}
	prop := left.ChildByFieldName("property")
	obj := left.ChildByFieldName("object")
	if prop == nil {
		return "", "", nil, "", false
	}
	name = strings.TrimSpace(prop.Content(buf))
	if name == "" || !plausibleJSIdent(name) {
		return "", "", nil, "", false
	}
	objText := ""
	if obj != nil {
		objText = strings.TrimSpace(obj.Content(buf))
	}
	lower := strings.ToLower(objText)
	switch {
	case lower == "exports", lower == "module.exports",
		strings.HasSuffix(lower, ".prototype"),
		lower == "app", lower == "req", lower == "res", lower == "proto",
		lower == "router", lower == "route", lower == "server",
		lower == "request", lower == "response", lower == "application",
		lower == "this",
		strings.HasSuffix(lower, ".proto"),
		strings.HasSuffix(lower, ".prototype"),
		strings.HasPrefix(lower, "exports."),
		strings.HasPrefix(lower, "module.exports."):
		// ok
	default:
		return "", "", nil, "", false
	}
	alias = objText + "." + name
	if isFunctionLikeTSNode(right.Type()) {
		return name, alias, right, types.SymbolKindFunction, true
	}
	// Re-exports: exports.Router = Router
	if right.Type() == "identifier" || right.Type() == "member_expression" {
		return name, alias, nil, types.SymbolKindVariable, true
	}
	return "", "", nil, "", false
}

func plausibleJSIdent(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func isFunctionLikeTSNode(nodeType string) bool {
	switch nodeType {
	case "arrow_function", "function_expression", "generator_function":
		return true
	default:
		return false
	}
}

func wrappedFunctionNode(val *sitter.Node, buf []byte) *sitter.Node {
	if val == nil || val.Type() != "call_expression" {
		return nil
	}
	// Many wrappers (memo/forwardRef/defineComponent) contain function args
	// nested under an arguments node instead of direct children.
	if strings.Contains(val.Content(buf), "=>") || strings.Contains(strings.ToLower(val.Content(buf)), "function(") {
		return val
	}
	for i := 0; i < int(val.ChildCount()); i++ {
		c := val.Child(i)
		if c == nil {
			continue
		}
		if isFunctionLikeTSNode(c.Type()) {
			return c
		}
	}
	return nil
}

func isCapacitorPluginRegistration(val *sitter.Node, buf []byte) bool {
	if val == nil || val.Type() != "call_expression" {
		return false
	}
	return strings.Contains(strings.ToLower(val.Content(buf)), "registerplugin(")
}

func looksLikeAnonymousDefaultFunction(s string) bool {
	ls := strings.ToLower(s)
	return strings.Contains(ls, "=>") || strings.Contains(ls, "function(") || strings.Contains(ls, "function (")
}

func looksLikeRouteHandlerName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

// tsFrameworkRole tags Next.js App/Pages Router files and Nuxt composables /
// pages with a stable role so query/signature filters see page|layout|route_handler|composable.
// Also tags Deno/Bun/Workers/edge fetch handlers, SvelteKit loaders, Remix
// loader/action, and Electron main/preload/renderer (does not rewrite Next role rules).
func tsFrameworkRole(relPath, name string, frameworks []string) string {
	p := strings.ToLower(filepath.ToSlash(relPath))
	base := strings.ToLower(filepath.Base(p))
	isNext := containsFramework(frameworks, string(FrameworkNextJS))
	isNuxt := containsFramework(frameworks, string(FrameworkNuxt))

	if isNext || looksLikeNextAppRouterPath(p) || looksLikeNextMiddlewarePath(p) {
		// Named App Router exports beat path roles (generateMetadata in layout.tsx).
		switch name {
		case "generateMetadata", "generateViewport", "generateImageMetadata":
			return "metadata"
		case "generateStaticParams":
			return "static_params"
		case "middleware":
			return "middleware"
		}
		if looksLikeNextMiddlewarePath(p) {
			return "middleware"
		}
		switch {
		case strings.HasPrefix(base, "page."):
			return "page"
		case strings.HasPrefix(base, "layout."):
			return "layout"
		case strings.HasPrefix(base, "route."):
			return "route_handler"
		case strings.HasPrefix(base, "loading."):
			return "loading"
		case strings.HasPrefix(base, "error."):
			return "error"
		case strings.HasPrefix(base, "template."):
			return "template"
		case strings.HasPrefix(base, "default."):
			return "default"
		case strings.HasPrefix(base, "not-found."):
			return "not_found"
		case strings.Contains(p, "/pages/") || strings.HasPrefix(p, "pages/"):
			return "page"
		}
	}
	if isNuxt || looksLikeNuxtConventionPath(p) {
		if role := nuxtPathRole(p); role != "" {
			return role
		}
		if looksLikeNuxtComposableName(name) {
			return "composable"
		}
	}
	isSK := containsFramework(frameworks, string(FrameworkSvelteKit)) || looksLikeSvelteKitPath(p)
	if isSK {
		if role := svelteKitExportRole(name); role != "" {
			return role
		}
		switch {
		case strings.Contains(base, "+page.server."):
			return "page_server"
		case strings.Contains(base, "+layout.server."):
			return "layout_server"
		case strings.HasPrefix(base, "+page."):
			return "page"
		case strings.HasPrefix(base, "+layout."):
			return "layout"
		case strings.HasPrefix(base, "+server."):
			return "server"
		case strings.HasPrefix(base, "+error."):
			return "error"
		case strings.HasPrefix(base, "hooks.server."):
			return "hooks_server"
		case strings.HasPrefix(base, "hooks.client."):
			return "hooks_client"
		case looksLikeRouteHandlerName(name):
			return "route_handler"
		}
	}
	isRemix := containsFramework(frameworks, string(FrameworkRemix)) || looksLikeRemixRoutePath(p)
	if isRemix {
		if role := remixExportRole(name); role != "" {
			return role
		}
		if looksLikeRemixRoutePath(p) {
			return "route"
		}
	}
	// Edge/Deno/Bun/Workers roles beat Electron path heuristics (host bleed on main.ts).
	if role := edgeRuntimeRole(name, frameworks); role != "" {
		return role
	}
	isElectron := containsFramework(frameworks, string(FrameworkElectron))
	if !isElectron && looksLikeElectronPath(p) &&
		!containsFramework(frameworks, string(FrameworkDeno)) &&
		!containsFramework(frameworks, string(FrameworkBun)) &&
		!containsFramework(frameworks, string(FrameworkCloudflareWorkers)) &&
		!containsFramework(frameworks, string(FrameworkEdge)) {
		isElectron = true
	}
	if isElectron {
		if role := electronPathRole(p, name); role != "" {
			return role
		}
	}
	isRN := containsFramework(frameworks, string(FrameworkReactNative))
	if isRN || looksLikeReactNativePath(p) {
		if strings.Contains(p, "/navigation/") || strings.HasPrefix(p, "navigation/") ||
			looksLikeRNNavigatorName(name) {
			return "navigator"
		}
		if strings.Contains(p, "/screens/") || strings.HasPrefix(p, "screens/") ||
			strings.HasSuffix(name, "Screen") || strings.HasSuffix(base, "screen.tsx") ||
			strings.HasSuffix(base, "screen.ts") || strings.HasSuffix(base, "screen.jsx") ||
			strings.HasSuffix(base, "screen.js") {
			return "screen"
		}
		if strings.Contains(p, "/components/") || strings.HasPrefix(p, "components/") ||
			name == "App" || strings.HasSuffix(name, "View") {
			return "component"
		}
	}
	isIonic := containsFramework(frameworks, string(FrameworkIonic))
	isCap := containsFramework(frameworks, string(FrameworkCapacitor))
	if isIonic || isCap || looksLikeIonicPath(p) {
		if strings.Contains(p, "/pages/") || strings.HasPrefix(p, "pages/") ||
			strings.HasSuffix(name, "Page") || strings.HasSuffix(base, "page.tsx") ||
			strings.HasSuffix(base, "page.ts") || strings.HasSuffix(base, "page.jsx") ||
			strings.HasSuffix(base, "page.js") {
			return "page"
		}
		if looksLikeIonicRouterName(name) || strings.Contains(p, "/routes/") ||
			strings.HasPrefix(p, "routes/") {
			return "router"
		}
		if strings.HasSuffix(name, "Plugin") || strings.Contains(strings.ToLower(name), "plugin") {
			return "plugin"
		}
	}
	return ""
}

func looksLikeReactNativePath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	return strings.Contains(p, "/screens/") || strings.HasPrefix(p, "screens/") ||
		strings.Contains(p, "/navigation/") || strings.HasPrefix(p, "navigation/")
}

func looksLikeRNNavigatorName(name string) bool {
	switch name {
	case "RootNavigator", "AppNavigator", "Stack", "Tab", "Drawer",
		"NavigationContainer", "RootStack", "MainTabs":
		return true
	default:
		return strings.HasSuffix(name, "Navigator")
	}
}

func isReactNativeNavigatorFactory(val *sitter.Node, buf []byte) bool {
	if val == nil {
		return false
	}
	ls := strings.ToLower(val.Content(buf))
	return strings.Contains(ls, "createnativestacknavigator") ||
		strings.Contains(ls, "createbottomtabnavigator") ||
		strings.Contains(ls, "createdrawernavigator") ||
		strings.Contains(ls, "createstacknavigator") ||
		strings.Contains(ls, "creatematerialtoptabnavigator") ||
		strings.Contains(ls, "createnativematerialtoptabnavigator")
}

// Stack.Screen / Tab.Screen component={HomeScreen} — wire navigator → screen leaves.
var rnScreenComponentPattern = regexp.MustCompile(
	`(?i)\.(?:Screen)\b[^>/\n]*\bcomponent\s*=\s*\{\s*([A-Z][A-Za-z0-9_]*)\s*\}`)

// extractReactNativeScreenWires densifies React Navigation Screen component props
// into calls edges from the enclosing navigator function (RootNavigator → HomeScreen).
func extractReactNativeScreenWires(repoID, relPath string, buf []byte, frameworks []string, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	if !containsFramework(frameworks, string(FrameworkReactNative)) &&
		!looksLikeReactNativePath(relPath) &&
		!strings.Contains(src, "@react-navigation") &&
		!strings.Contains(src, ".Screen") {
		return
	}
	matches := rnScreenComponentPattern.FindAllStringSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return
	}
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		screen := src[m[2]:m[3]]
		if screen == "" {
			continue
		}
		lineNo := 1 + strings.Count(src[:m[0]], "\n")
		from := enclosingSymbolAtLine(out, lineNo)
		if from == "" {
			// Prefer a navigator-role symbol in this file when JSX sits in return.
			for _, s := range out.Symbols {
				if strings.Contains(s.Signature, "role=navigator") {
					from = s.ID
					break
				}
			}
		}
		if from == "" {
			continue
		}
		emitNestCall(repoID, relPath, from, screen, 0.88, out)
	}
}

func looksLikeIonicPath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	return strings.Contains(p, "/pages/") || strings.HasPrefix(p, "pages/") ||
		strings.Contains(p, "/routes/") || strings.HasPrefix(p, "routes/")
}

func looksLikeIonicRouterName(name string) bool {
	switch name {
	case "AppRoutes", "AppRouter", "IonRouterOutlet", "Tabs", "RootTabs", "App":
		return true
	default:
		return strings.HasSuffix(name, "Routes") || strings.HasSuffix(name, "Router")
	}
}

// Ionic React Router: <Route component={HomePage} /> / element={<HomePage />}
var ionicRouteComponentPattern = regexp.MustCompile(
	`(?i)<Route\b[^>]*\bcomponent\s*=\s*\{\s*([A-Z][A-Za-z0-9_]*)\s*\}`)
var ionicRouteElementPattern = regexp.MustCompile(
	`(?i)<Route\b[^>]*\belement\s*=\s*\{\s*<\s*([A-Z][A-Za-z0-9_]*)\b`)

// extractIonicRouteWires densifies Ionic/React Router Route → page leaves
// (AppRoutes → HomePage), mirroring React Native Screen densify.
func extractIonicRouteWires(repoID, relPath string, buf []byte, frameworks []string, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	if !containsFramework(frameworks, string(FrameworkIonic)) &&
		!containsFramework(frameworks, string(FrameworkCapacitor)) &&
		!strings.Contains(src, "@ionic/") &&
		!strings.Contains(src, "IonRouterOutlet") &&
		!strings.Contains(src, "IonPage") {
		return
	}
	var matches [][]int
	matches = append(matches, ionicRouteComponentPattern.FindAllStringSubmatchIndex(src, -1)...)
	matches = append(matches, ionicRouteElementPattern.FindAllStringSubmatchIndex(src, -1)...)
	if len(matches) == 0 {
		return
	}
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		page := src[m[2]:m[3]]
		if page == "" {
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

func edgeRuntimeRole(name string, frameworks []string) string {
	raw := strings.TrimSpace(name)
	n := strings.ToLower(raw)
	isEdge := containsFramework(frameworks, string(FrameworkDeno)) ||
		containsFramework(frameworks, string(FrameworkBun)) ||
		containsFramework(frameworks, string(FrameworkCloudflareWorkers)) ||
		containsFramework(frameworks, string(FrameworkEdge))
	if !isEdge {
		return ""
	}
	switch n {
	case "fetch", "handler", "default_export", "scheduled", "queue", "email", "tail",
		"fetchhandler", "requesthandler", "route":
		return "edge_handler"
	default:
		// healthHandler / greetHandler / *Handler → edge_handler
		if strings.HasSuffix(raw, "Handler") || strings.HasSuffix(n, "handler") {
			return "edge_handler"
		}
		return ""
	}
}

// extractEdgeRuntimeServe densifies Deno.serve / Bun.serve call sites into
// entrypoint symbols plus calls edges to named handler identifiers.
func extractEdgeRuntimeServe(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := strings.ToLower(string(buf))
	looksDeno := containsFramework(frameworks, string(FrameworkDeno)) || strings.Contains(src, "deno.serve")
	looksBun := containsFramework(frameworks, string(FrameworkBun)) || strings.Contains(src, "bun.serve")
	if !looksDeno && !looksBun {
		return
	}
	fw := frameworks
	if looksDeno {
		fw = withFramework(fw, string(FrameworkDeno))
	}
	if looksBun {
		fw = withFramework(fw, string(FrameworkBun))
	}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		fn := n.ChildByFieldName("function")
		if fn == nil || fn.Type() != "member_expression" {
			return
		}
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return
		}
		recv := strings.TrimSpace(obj.Content(buf))
		meth := strings.TrimSpace(prop.Content(buf))
		runtime := ""
		switch {
		case strings.EqualFold(recv, "Deno") && strings.EqualFold(meth, "serve"):
			runtime = string(FrameworkDeno)
		case strings.EqualFold(recv, "Bun") && strings.EqualFold(meth, "serve"):
			runtime = string(FrameworkBun)
		default:
			return
		}
		line := int(n.StartPoint().Row) + 1
		siteName := fmt.Sprintf("%s_serve_%d", runtime, line)
		siteFW := withFramework(fw, runtime)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "typescript", frameworkSignature(siteFW, "entrypoint"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitTSCall(repoID, relPath, sym.ID, recv+"."+meth, 0.9, out)
		if args := n.ChildByFieldName("arguments"); args != nil {
			for i := 0; i < int(args.NamedChildCount()); i++ {
				arg := args.NamedChild(i)
				if arg == nil {
					continue
				}
				switch arg.Type() {
				case "identifier":
					emitTSCall(repoID, relPath, sym.ID, strings.TrimSpace(arg.Content(buf)), 0.85, out)
				case "object":
					extractEdgeServeObjectHandlers(arg, buf, repoID, relPath, sym.ID, out)
				case "arrow_function", "function_expression":
					extractCalls(arg, buf, repoID, relPath, sym.ID, out)
				}
			}
			extractCalls(args, buf, repoID, relPath, sym.ID, out)
			addReadEdgesFromNode(repoID, relPath, sym.ID, args, buf, out)
		}
	})
}

func extractEdgeServeObjectHandlers(obj *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if obj == nil {
		return
	}
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		pair := obj.NamedChild(i)
		if pair == nil || pair.Type() != "pair" {
			continue
		}
		key := pair.ChildByFieldName("key")
		val := pair.ChildByFieldName("value")
		if key == nil || val == nil {
			continue
		}
		keyName := strings.Trim(strings.TrimSpace(key.Content(buf)), `"'`)
		switch strings.ToLower(keyName) {
		case "fetch", "handler", "error", "websocket":
			if val.Type() == "identifier" {
				emitTSCall(repoID, relPath, fromSym, strings.TrimSpace(val.Content(buf)), 0.85, out)
			} else if val.Type() == "arrow_function" || val.Type() == "function_expression" {
				extractCalls(val, buf, repoID, relPath, fromSym, out)
			}
		}
	}
}

func looksLikeNextAppRouterPath(p string) bool {
	base := filepath.Base(p)
	switch {
	case strings.HasPrefix(base, "page."), strings.HasPrefix(base, "layout."),
		strings.HasPrefix(base, "route."), strings.HasPrefix(base, "loading."),
		strings.HasPrefix(base, "error."), strings.HasPrefix(base, "template."),
		strings.HasPrefix(base, "default."), strings.HasPrefix(base, "not-found."):
		return strings.Contains(p, "/app/") || strings.HasPrefix(p, "app/")
	default:
		return false
	}
}

func looksLikeSvelteKitPath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	base := filepath.Base(p)
	return strings.Contains(base, "+page.") || strings.Contains(base, "+layout.") ||
		strings.Contains(base, "+server.") || strings.Contains(base, "+error.") ||
		strings.HasPrefix(base, "hooks.server.") || strings.HasPrefix(base, "hooks.client.")
}

func svelteKitExportRole(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "load":
		return "loader"
	case "actions":
		return "actions"
	case "prerender", "ssr", "csr", "trailingslash", "config":
		return "page_config"
	default:
		return ""
	}
}

func looksLikeRemixRoutePath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	return strings.Contains(p, "/app/routes/") || strings.HasPrefix(p, "app/routes/")
}

func remixExportRole(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "loader", "clientloader":
		return "loader"
	case "action", "clientaction":
		return "action"
	case "headers", "meta", "links", "shouldrevalidate":
		return "route_export"
	case "default_export", "default":
		return "route"
	default:
		return ""
	}
}

func looksLikeElectronPath(p string) bool {
	return electronConventionPath(p)
}

func electronPathRole(p, name string) string {
	p = strings.ToLower(filepath.ToSlash(p))
	base := strings.ToLower(baseNameNoExt(p))
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(p, "/preload/") || strings.HasPrefix(p, "preload/") ||
		base == "preload" || strings.HasSuffix(base, "preload") ||
		n == "preload" || strings.Contains(n, "preload"):
		return "preload"
	case strings.Contains(p, "/renderer/") || strings.HasPrefix(p, "renderer/") ||
		base == "renderer" || n == "renderer":
		return "renderer"
	case strings.Contains(p, "/main/") || strings.HasPrefix(p, "main/") ||
		base == "main" || strings.HasSuffix(base, "-main") || n == "main" ||
		n == "createwindow" || n == "whenready":
		return "main"
	default:
		if n == "default_export" {
			if strings.Contains(p, "preload") {
				return "preload"
			}
			if strings.Contains(p, "renderer") {
				return "renderer"
			}
			return "main"
		}
		return ""
	}
}

// extractElectronIPC densifies ipcMain.handle/on and contextBridge.exposeInMainWorld
// into entrypoint symbols + calls edges to named handler identifiers.
func extractElectronIPC(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := strings.ToLower(string(buf))
	looks := containsFramework(frameworks, string(FrameworkElectron)) ||
		looksLikeElectronPath(relPath) ||
		strings.Contains(src, "ipcmain.") || strings.Contains(src, "ipcrenderer.") ||
		strings.Contains(src, "contextbridge.") || strings.Contains(src, "from \"electron\"") ||
		strings.Contains(src, "from 'electron'") || strings.Contains(src, "require(\"electron\")") ||
		strings.Contains(src, "require('electron')")
	if !looks {
		return
	}
	fw := withFramework(frameworks, string(FrameworkElectron))
	role := electronPathRole(strings.ToLower(filepath.ToSlash(relPath)), "")
	if role == "" {
		role = "main"
	}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return
		}
		fnText := strings.ToLower(strings.TrimSpace(fn.Content(buf)))
		isIPC := strings.HasSuffix(fnText, "ipcmain.handle") || strings.HasSuffix(fnText, "ipcmain.on") ||
			fnText == "ipcmain.handle" || fnText == "ipcmain.on" ||
			strings.HasSuffix(fnText, "ipcrenderer.invoke") || strings.HasSuffix(fnText, "ipcrenderer.on") ||
			strings.HasSuffix(fnText, "contextbridge.exposeinmainworld")
		if !isIPC {
			return
		}
		line := int(n.StartPoint().Row) + 1
		siteRole := role
		// Prefer path role (main/preload/renderer). Only invent from callee when path is unknown.
		if siteRole == "" {
			switch {
			case strings.Contains(fnText, "contextbridge"):
				siteRole = "preload"
			case strings.Contains(fnText, "ipcrenderer"):
				siteRole = "renderer"
			case strings.Contains(fnText, "ipcmain"):
				siteRole = "main"
			default:
				siteRole = "main"
			}
		}
		siteName := fmt.Sprintf("electron_ipc_%d", line)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "typescript",
			frameworkSignature(fw, siteRole), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		// Channel string + handler identifier / inline body.
		if args := n.ChildByFieldName("arguments"); args != nil {
			for i := 0; i < int(args.NamedChildCount()); i++ {
				arg := args.NamedChild(i)
				if arg == nil {
					continue
				}
				switch arg.Type() {
				case "string", "string_fragment":
					ch := strings.Trim(strings.TrimSpace(arg.Content(buf)), `"'`+"`")
					if ch != "" {
						emitTSCall(repoID, relPath, sym.ID, "ipc:"+ch, 0.75, out)
					}
				case "identifier":
					emitTSCall(repoID, relPath, sym.ID, strings.TrimSpace(arg.Content(buf)), 0.85, out)
				case "arrow_function", "function_expression":
					extractCalls(arg, buf, repoID, relPath, sym.ID, out)
					addReadEdgesFromNode(repoID, relPath, sym.ID, arg, buf, out)
				}
			}
		}
	})
}

func parentFromStack(n *sitter.Node) string {
	_ = n
	return ""
}

func parentClassID(n *sitter.Node, buf []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_declaration" {
			return ChildName(p, "name", buf)
		}
	}
	return ""
}

// tsSkipHeritageType drops ubiquitous DOM/React/Node bases so they do not
// drown CallersOf/impact hubs the way MonoBehaviour / AActor are skipped.
func tsSkipHeritageType(tok string) bool {
	switch tok {
	case "Object", "Array", "Error", "Function", "Promise", "Map", "Set", "Date",
		"EventEmitter", "EventTarget", "HTMLElement", "Element", "Node",
		"Component", "PureComponent", "React", "ReactNode",
		"Controller", "Injectable", "Module", "Pipe", "Directive",
		"Serializable", "Cloneable", "Comparable":
		return true
	default:
		return false
	}
}

// tsTypeLeaf returns the rightmost Capitalized identifier in a heritage type
// expression (Base, React.Component → Component, pkg.Foo → Foo).
func tsTypeLeaf(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "type_identifier", "identifier", "property_identifier":
		return strings.TrimSpace(n.Content(buf))
	case "member_expression", "nested_type_identifier", "generic_type",
		"type_query", "qualified_name":
		// Prefer the deepest named type child; fall back to last Capitalized token.
		var leaf string
		Walk(n, func(c *sitter.Node) {
			switch c.Type() {
			case "type_identifier", "identifier", "property_identifier":
				tok := strings.TrimSpace(c.Content(buf))
				if tok != "" {
					leaf = tok
				}
			}
		})
		if leaf != "" {
			return leaf
		}
		txt := strings.TrimSpace(n.Content(buf))
		if i := strings.IndexAny(txt, "<[{("); i > 0 {
			txt = txt[:i]
		}
		if j := strings.LastIndex(txt, "."); j >= 0 {
			txt = txt[j+1:]
		}
		return strings.TrimSpace(txt)
	default:
		return ""
	}
}

// tsCollectEmbedNames gathers extends/implements leaf names for embeds= densify
// (recv_type method promotion via ResolveSymrefs).
func tsCollectEmbedNames(n *sitter.Node, buf []byte) []string {
	if n == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] || tsSkipHeritageType(tok) {
			return
		}
		if r := []rune(tok); len(r) == 0 || r[0] < 'A' || r[0] > 'Z' {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "class_heritage", "extends_clause", "implements_clause", "extends_type_clause":
			Walk(c, func(ch *sitter.Node) {
				switch ch.Type() {
				case "type_identifier", "identifier":
					add(ch.Content(buf))
				case "member_expression", "nested_type_identifier", "generic_type":
					add(tsTypeLeaf(ch, buf))
				}
			})
		}
	}
	return out
}

// tsEmitHeritage emits inherits (extends) and implements edges for class /
// interface heritage so CallersOf/impact densify Nest/Angular base classes and
// shared interfaces (previously missing on the TS/JS graph).
func tsEmitHeritage(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if n == nil || out == nil {
		return
	}
	emit := func(tok string, kind types.ReferenceKind, conf float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" || tsSkipHeritageType(tok) {
			return
		}
		if r := []rune(tok); len(r) == 0 || r[0] < 'A' || r[0] > 'Z' {
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
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "class_heritage":
			for j := 0; j < int(c.NamedChildCount()); j++ {
				ch := c.NamedChild(j)
				if ch == nil {
					continue
				}
				switch ch.Type() {
				case "extends_clause":
					for k := 0; k < int(ch.NamedChildCount()); k++ {
						t := ch.NamedChild(k)
						if t == nil {
							continue
						}
						emit(tsTypeLeaf(t, buf), types.RefKindInherits, 0.9)
					}
				case "implements_clause":
					for k := 0; k < int(ch.NamedChildCount()); k++ {
						t := ch.NamedChild(k)
						if t == nil {
							continue
						}
						emit(tsTypeLeaf(t, buf), types.RefKindImplements, 0.85)
					}
				}
			}
		case "extends_type_clause":
			// interface Foo extends Bar, Baz
			for j := 0; j < int(c.NamedChildCount()); j++ {
				t := c.NamedChild(j)
				if t == nil {
					continue
				}
				emit(tsTypeLeaf(t, buf), types.RefKindInherits, 0.9)
			}
		}
	}
}

// buildJSInstanceScope maps local instances to class names from new expressions
// and explicit TypeScript annotations.
func buildJSInstanceScope(root *sitter.Node, buf []byte) func(string) string {
	scope := jsCollectLocalTypes(root, buf)
	return func(name string) string { return scope[strings.TrimSpace(name)] }
}

// buildJSMethodTypeOf merges enclosing-class field/ctor-param types with locals
// so Nest/Angular `this.catsService.findAll()` resolves to CatsService.findAll.
func buildJSMethodTypeOf(method *sitter.Node, buf []byte) func(string) string {
	className := parentClassID(method, buf)
	fields := map[string]string{}
	if cls := tsEnclosingClassNode(method); cls != nil {
		fields = tsCollectFieldTypes(cls, buf)
	}
	locals := jsCollectLocalTypes(method, buf)
	return jsTypeOf(className, fields, locals)
}

func tsEnclosingClassNode(n *sitter.Node) *sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_declaration" || p.Type() == "class" {
			return p
		}
	}
	return nil
}

// tsCollectFieldTypes maps injected field / ctor-param property names → type
// leaf (catsService → CatsService) for typed CallersOf/impact edges.
func tsCollectFieldTypes(classNode *sitter.Node, buf []byte) map[string]string {
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
		typ = strings.TrimSpace(typ)
		if name == "" || typ == "" {
			return
		}
		if typ[0] < 'A' || typ[0] > 'Z' {
			return
		}
		if _, ok := out[name]; !ok {
			out[name] = typ
		}
	}
	tsParamNameType := func(p *sitter.Node) (string, string) {
		if p == nil {
			return "", ""
		}
		switch p.Type() {
		case "required_parameter", "optional_parameter", "rest_parameter":
		default:
			return "", ""
		}
		nm := ""
		if name := p.ChildByFieldName("pattern"); name != nil {
			nm = FirstIdentifier(name, buf)
		}
		if nm == "" {
			if name := p.ChildByFieldName("name"); name != nil {
				nm = strings.TrimSpace(name.Content(buf))
			}
		}
		if nm == "" {
			nm = FirstIdentifier(p, buf)
		}
		typ := ""
		if ta := p.ChildByFieldName("type"); ta != nil {
			Walk(ta, func(c *sitter.Node) {
				if typ != "" || c.Type() != "type_identifier" {
					return
				}
				t := strings.TrimSpace(c.Content(buf))
				if t != "" && t[0] >= 'A' && t[0] <= 'Z' {
					typ = t
				}
			})
		}
		if typ == "" {
			Walk(p, func(c *sitter.Node) {
				if typ != "" || c.Type() != "type_identifier" {
					return
				}
				t := strings.TrimSpace(c.Content(buf))
				if t != "" && t[0] >= 'A' && t[0] <= 'Z' {
					typ = t
				}
			})
		}
		// Nest/Angular: @Inject(CatsService) private cats — type only in decorator.
		if typ == "" {
			if inj := tsInjectDecoratorType(p, buf); inj != "" {
				typ = inj
			}
		}
		return nm, typ
	}
	Walk(body, func(n *sitter.Node) {
		switch n.Type() {
		case "public_field_definition", "field_definition", "property_definition":
			if val := n.ChildByFieldName("value"); val != nil &&
				(val.Type() == "arrow_function" || val.Type() == "function" || val.Type() == "function_expression") {
				return
			}
			nm := ""
			if name := n.ChildByFieldName("name"); name != nil {
				nm = strings.TrimSpace(name.Content(buf))
			}
			if nm == "" {
				nm = FirstIdentifier(n, buf)
			}
			typ := ""
			if ta := n.ChildByFieldName("type"); ta != nil {
				Walk(ta, func(c *sitter.Node) {
					if typ != "" || c.Type() != "type_identifier" {
						return
					}
					t := strings.TrimSpace(c.Content(buf))
					if t != "" && t[0] >= 'A' && t[0] <= 'Z' {
						typ = t
					}
				})
			}
			if typ == "" {
				Walk(n, func(c *sitter.Node) {
					if typ != "" || c.Type() != "type_identifier" {
						return
					}
					t := strings.TrimSpace(c.Content(buf))
					if t != "" && t[0] >= 'A' && t[0] <= 'Z' {
						typ = t
					}
				})
			}
			// Angular inject(HeroService) / Nest @Inject(Svc) field initializers.
			if typ == "" {
				if val := n.ChildByFieldName("value"); val != nil {
					if inj := tsInjectCallType(val, buf); inj != "" {
						typ = inj
					}
				}
				if typ == "" {
					if inj := tsInjectDecoratorType(n, buf); inj != "" {
						typ = inj
					}
				}
			}
			add(nm, typ)
		case "method_definition":
			if ChildName(n, "name", buf) != "constructor" {
				return
			}
			params := n.ChildByFieldName("parameters")
			if params == nil {
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
				nm, typ := tsParamNameType(p)
				add(nm, typ)
			})
		}
	})
	return out
}

// tsInjectDecoratorType returns the Capitalized token from @Inject(Token) /
// @Inject(forwardRef(() => Token)) on a parameter or field node.
func tsInjectDecoratorType(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	text := n.Content(buf)
	if !strings.Contains(text, "@Inject") {
		return ""
	}
	m := nestInjectCallRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// tsInjectCallType returns the Capitalized arg of inject(Token) / Inject(Token)
// field initializers (Angular 14+ functional DI).
func tsInjectCallType(val *sitter.Node, buf []byte) string {
	if val == nil {
		return ""
	}
	if val.Type() != "call_expression" {
		return ""
	}
	fn := val.ChildByFieldName("function")
	if fn == nil {
		return ""
	}
	callee := strings.TrimSpace(calleeName(fn, buf))
	if !strings.EqualFold(callee, "inject") {
		return ""
	}
	args := val.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	found := ""
	Walk(args, func(c *sitter.Node) {
		if found != "" {
			return
		}
		switch c.Type() {
		case "identifier", "type_identifier":
			t := strings.TrimSpace(c.Content(buf))
			if t != "" && t[0] >= 'A' && t[0] <= 'Z' {
				found = t
			}
		}
	})
	return found
}

func jsCollectLocalTypes(root *sitter.Node, buf []byte) map[string]string {
	scope := map[string]string{}
	if root == nil {
		return scope
	}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "variable_declarator" {
			return
		}
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := strings.TrimSpace(nameNode.Content(buf))
		if i := strings.IndexByte(name, ':'); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name == "" {
			return
		}
		if value := n.ChildByFieldName("value"); value != nil && value.Type() == "new_expression" {
			if ctor := value.ChildByFieldName("constructor"); ctor != nil {
				if typ := calleeName(ctor, buf); typ != "" {
					scope[name] = typ
					return
				}
			}
		}
		Walk(n, func(c *sitter.Node) {
			if _, ok := scope[name]; ok || c.Type() != "type_identifier" {
				return
			}
			if typ := strings.TrimSpace(c.Content(buf)); typ != "" {
				scope[name] = typ
			}
		})
	})
	return scope
}

// jsTypeOf peels this./super. receivers and resolves field/local names to types
// (mirrors javaTypeOf for Nest/Angular constructor DI).
func jsTypeOf(className string, fields, locals map[string]string) func(string) string {
	if className == "" && len(fields) == 0 && len(locals) == 0 {
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
		if i := strings.IndexByte(recv, '.'); i > 0 {
			recv = recv[:i]
		}
		if typ, ok := fields[recv]; ok {
			return typ
		}
		if typ, ok := locals[recv]; ok {
			return typ
		}
		return ""
	}
}

func importSource(n *sitter.Node, buf []byte) string {
	src := n.ChildByFieldName("source")
	if src == nil {
		return ""
	}
	t := src.Content(buf)
	t = strings.Trim(t, `"'`)
	return t
}

// FileNodeID returns stable id for a file vertex.
func FileNodeID(repoID, relPath string) string {
	return fmt.Sprintf("file:%s:%s", repoID, relPath)
}

// extractCalls walks a definition body and emits a `calls` symref edge per
// invocation. It is language-agnostic across the tree-sitter grammars used by
// this package (JS/TS, Go, Python, Rust, Java, C#): different grammars name the
// call and callee nodes differently, so callee resolution probes the common
// field names rather than assuming one grammar.
func extractCalls(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	extractCallsScoped(root, buf, repoID, relPath, fromSym, out, nil)
}

// extractCallsScoped is extractCalls with optional receiver-type inference. When
// typeOf is non-nil (Go), a method call `x.Foo()` whose receiver `x` has a known
// type T is emitted as a type-qualified symref `T.Foo`, letting the resolver pick
// (*T).Foo over an unrelated type's Foo. typeOf nil reproduces the bare-name
// behaviour for every other language.
func extractCallsScoped(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult, typeOf func(string) string) {
	emit := func(name string, confidence float64) {
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
			Confidence: confidence,
		})
	}
	Walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "call_expression", "call", "invocation_expression":
			if typeOf != nil {
				if fn := n.ChildByFieldName("function"); fn != nil {
					var recv, member *sitter.Node
					switch fn.Type() {
					case "selector_expression":
						recv, member = fn.ChildByFieldName("operand"), fn.ChildByFieldName("field")
					case "member_expression":
						recv, member = fn.ChildByFieldName("object"), fn.ChildByFieldName("property")
					case "member_access_expression":
						// C#: _users.Find — field "name" is the member; other named child is receiver.
						member = fn.ChildByFieldName("name")
						for i := 0; i < int(fn.NamedChildCount()); i++ {
							c := fn.NamedChild(i)
							if c != nil && c != member {
								recv = c
								break
							}
						}
					case "field_expression":
						// C++/Rust: obj.m() / this->m() — field is the method name.
						recv, member = fn.ChildByFieldName("argument"), fn.ChildByFieldName("field")
					}
					if recv != nil && member != nil {
						if typ := typeOf(callReceiverName(recv, buf)); typ != "" {
							emit(typ+"."+strings.TrimSpace(member.Content(buf)), 0.9)
							return
						}
					}
				}
			}
			// JS/TS/Go/Rust/Python/C#: callee is the "function" field.
			if fn := n.ChildByFieldName("function"); fn != nil {
				if nm := calleeName(fn, buf); nm != "" {
					emit(nm, 0.5)
				}
				// Express/CJS member calls: also emit app.use so aliases resolve.
				if fn.Type() == "member_expression" {
					if alias := expressMemberAlias(fn, buf); alias != "" {
						emit(alias, 0.5)
					}
				}
				// Python attribute calls: app.get / UserService.list_users.
				if fn.Type() == "attribute" {
					if alias := pythonMemberAlias(fn, buf); alias != "" {
						emit(alias, 0.85)
					}
					if typed := pythonTypedCallee(fn, buf); typed != "" {
						emit(typed, 0.8)
					}
				}
				if calleeName(fn, buf) != "" || (fn.Type() == "member_expression" && expressMemberAlias(fn, buf) != "") ||
					(fn.Type() == "attribute" && (pythonMemberAlias(fn, buf) != "" || pythonTypedCallee(fn, buf) != "")) {
					return
				}
			}
			// Kotlin: no function field — simple_identifier or trailing name on
			// navigation_expression before call_suffix.
			if typeOf != nil {
				if recv, meth, ok := kotlinCallReceiver(n, buf); ok {
					if typ := typeOf(recv); typ != "" {
						emit(typ+"."+meth, 0.9)
						return
					}
				}
			}
			if nm := kotlinCallCallee(n, buf); nm != "" {
				emit(nm, 0.5)
				return
			}
			// Ruby / Elixir: call nodes have no "function" field — method is the
			// leading identifier/constant (route(path), Foo.bar → bar).
			if typeOf != nil {
				if recv, meth, ok := rubyCallReceiver(n, buf); ok {
					if typ := typeOf(recv); typ != "" {
						emit(typ+"."+meth, 0.9)
						return
					}
				}
			}
			emit(rubyCallCallee(n, buf), 0.5)
			return
		case "command":
			// Bash: `deploy_app "$1"` — command_name field (skip builtins/CLIs).
			nm := ""
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				nm = strings.TrimSpace(nameNode.Content(buf))
				if nm == "" {
					nm = FirstIdentifier(nameNode, buf)
				}
			}
			if nm != "" && !bashSkipCommand(nm) {
				emit(nm, 0.45)
			}
			return
		case "function_call":
			// Lua: foo() / t:bar() / require("x") — name field (trailing ident).
			nm := ""
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				nm = strings.TrimSpace(nameNode.Content(buf))
				if i := strings.LastIndexAny(nm, ":."); i >= 0 && i+1 < len(nm) {
					nm = strings.TrimSpace(nm[i+1:])
				}
				if nm == "" {
					nm = FirstIdentifier(nameNode, buf)
				}
			}
			if nm == "" {
				nm = FirstIdentifier(n, buf)
			}
			emit(nm, 0.5)
		case "method_invocation":
			// Java: method name is its own field; object may be this / field_access.
			meth := ""
			if nm := n.ChildByFieldName("name"); nm != nil {
				meth = strings.TrimSpace(nm.Content(buf))
			}
			if typeOf != nil && meth != "" {
				if obj := n.ChildByFieldName("object"); obj != nil {
					if typ := typeOf(callReceiverName(obj, buf)); typ != "" {
						emit(typ+"."+meth, 0.9)
						return
					}
				} else if typ := typeOf("this"); typ != "" {
					// Bare m() inside instance method → Class.m
					emit(typ+"."+meth, 0.85)
					return
				}
			}
			if meth != "" {
				emit(meth, 0.5)
			} else {
				emit(calleeName(n.ChildByFieldName("function"), buf), 0.5)
			}
		case "function_call_expression":
			// PHP: foo() / \Ns\foo() — callee is the "function" field.
			emit(calleeName(n.ChildByFieldName("function"), buf), 0.5)
		case "member_call_expression", "nullsafe_member_call_expression", "scoped_call_expression":
			// PHP: $obj->m() / $obj?->m() / Class::m() — method name is its own field.
			// When typeOf knows $this/self/static, emit Type.method for recv_type.
			if typeOf != nil {
				var recvNode *sitter.Node
				if obj := n.ChildByFieldName("object"); obj != nil {
					recvNode = obj
				} else if sc := n.ChildByFieldName("scope"); sc != nil {
					recvNode = sc
				} else if n.NamedChildCount() > 0 {
					recvNode = n.NamedChild(0)
				}
				meth := ""
				if nm := n.ChildByFieldName("name"); nm != nil {
					meth = nm.Content(buf)
				}
				if recvNode != nil && meth != "" {
					recv := strings.TrimSpace(recvNode.Content(buf))
					if typ := typeOf(recv); typ != "" {
						emit(typ+"."+meth, 0.9)
						return
					}
				}
			}
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(buf), 0.5)
			}
		case "object_creation_expression", "new_expression":
			// Constructor calls (Java/C#/JS): record the type being constructed.
			emit(calleeName(firstNonNull(n.ChildByFieldName("type"), n.ChildByFieldName("constructor")), buf), 0.5)
		}
	})
}

// callReceiverName peels this / *this / pointer_expression(this) / bare idents
// used as method-call receivers (C++ field_expression, JS member_expression).
func callReceiverName(recv *sitter.Node, buf []byte) string {
	if recv == nil {
		return ""
	}
	switch recv.Type() {
	case "this":
		return "this"
	case "identifier", "field_identifier", "simple_identifier":
		return strings.TrimSpace(recv.Content(buf))
	case "field_access":
		// Java: this.owners / owners — keep dotted form for javaTypeOf peeling.
		return strings.TrimSpace(recv.Content(buf))
	case "pointer_expression", "unary_expression", "parenthesized_expression":
		for i := 0; i < int(recv.NamedChildCount()); i++ {
			if c := recv.NamedChild(i); c != nil {
				if nm := callReceiverName(c, buf); nm != "" {
					return nm
				}
			}
		}
		s := strings.TrimSpace(recv.Content(buf))
		if s == "this" || s == "*this" {
			return "this"
		}
	}
	s := strings.TrimSpace(recv.Content(buf))
	if s == "this" || s == "*this" {
		return "this"
	}
	return s
}

// calleeName resolves the trailing simple name of a callee node, handling the
// member/selector/attribute/scoped forms used across grammars.
func calleeName(fn *sitter.Node, buf []byte) string {
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier", "field_identifier", "type_identifier",
		"property_identifier", "shorthand_property_identifier",
		"simple_identifier", // Kotlin
		"name":              // PHP simple callee name (function_call_expression.function)
		return fn.Content(buf)
	case "qualified_name":
		// PHP \Ns\sub\func -> trailing simple name.
		for i := int(fn.NamedChildCount()) - 1; i >= 0; i-- {
			if c := fn.NamedChild(i); c != nil && c.Type() == "name" {
				return c.Content(buf)
			}
		}
		return fn.Content(buf)
	}
	// member_expression(JS).property, selector_expression(Go).field,
	// attribute(Python).attribute, scoped_identifier(Rust/Java).name,
	// field_expression(Rust).field, member_access_expression(C#).name.
	for _, field := range []string{"property", "field", "attribute", "name"} {
		if c := fn.ChildByFieldName(field); c != nil {
			if nm := calleeName(c, buf); nm != "" {
				return nm
			}
		}
	}
	// Fallback: last identifier-like named child.
	for i := int(fn.NamedChildCount()) - 1; i >= 0; i-- {
		c := fn.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "identifier", "field_identifier", "type_identifier", "property_identifier", "simple_identifier":
			return c.Content(buf)
		}
	}
	return ""
}

func firstNonNull(nodes ...*sitter.Node) *sitter.Node {
	for _, n := range nodes {
		if n != nil {
			return n
		}
	}
	return nil
}

// rubyCallCallee resolves the method name on a tree-sitter-ruby `call` node.
// Bare calls (`route(path)`) use a leading identifier; receiver calls
// (`Foo.bar` / `obj.bar`) use the trailing identifier after `.`.
func rubyCallCallee(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	var firstIdent, lastIdent string
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "identifier", "simple_identifier", "constant":
			name := strings.TrimSpace(c.Content(buf))
			if firstIdent == "" {
				firstIdent = name
			}
			lastIdent = name
		case "dot":
			// Elixir: Format.apply(name) — call → dot(alias, identifier).
			var last string
			var walkDot func(*sitter.Node)
			walkDot = func(x *sitter.Node) {
				if x == nil {
					return
				}
				if x.Type() == "identifier" || x.Type() == "simple_identifier" {
					last = strings.TrimSpace(x.Content(buf))
				}
				for j := 0; j < int(x.NamedChildCount()); j++ {
					walkDot(x.NamedChild(j))
				}
			}
			walkDot(c)
			if last != "" {
				return last
			}
		case "argument_list", "call_suffix", "arguments", "block", "do_block":
			// Stop before arguments so we don't pick arg identifiers.
			if lastIdent != "" {
				return lastIdent
			}
			return firstIdent
		}
	}
	if lastIdent != "" && lastIdent != firstIdent {
		return lastIdent
	}
	return firstIdent
}

// rubyCallReceiver returns (receiver, method) for `self.foo` / `Foo.bar` style calls.
func rubyCallReceiver(n *sitter.Node, buf []byte) (recv, meth string, ok bool) {
	if n == nil {
		return "", "", false
	}
	var idents []string
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "identifier", "simple_identifier", "constant", "self":
			idents = append(idents, strings.TrimSpace(c.Content(buf)))
		case "argument_list", "call_suffix", "arguments", "block", "do_block":
			if len(idents) >= 2 {
				return idents[0], idents[len(idents)-1], true
			}
			return "", "", false
		}
	}
	if len(idents) >= 2 {
		return idents[0], idents[len(idents)-1], true
	}
	return "", "", false
}

// kotlinCallCallee resolves the callee on a tree-sitter-kotlin call_expression,
// which has no "function" field: bare calls use a leading simple_identifier;
// member calls use the trailing simple_identifier on navigation_expression.
func kotlinCallCallee(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "simple_identifier":
			return strings.TrimSpace(c.Content(buf))
		case "navigation_expression":
			var last string
			var walkNav func(*sitter.Node)
			walkNav = func(x *sitter.Node) {
				if x == nil {
					return
				}
				if x.Type() == "simple_identifier" {
					last = strings.TrimSpace(x.Content(buf))
				}
				for j := 0; j < int(x.ChildCount()); j++ {
					walkNav(x.Child(j))
				}
			}
			walkNav(c)
			return last
		case "call_suffix":
			return ""
		}
	}
	return ""
}

// kotlinCallReceiver returns (receiver, method) for pets.save() / this.owners.find().
func kotlinCallReceiver(n *sitter.Node, buf []byte) (recv, meth string, ok bool) {
	if n == nil {
		return "", "", false
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() != "navigation_expression" {
			continue
		}
		var idents []string
		var walkNav func(*sitter.Node)
		walkNav = func(x *sitter.Node) {
			if x == nil {
				return
			}
			switch x.Type() {
			case "simple_identifier", "this_expression":
				idents = append(idents, strings.TrimSpace(x.Content(buf)))
			}
			for j := 0; j < int(x.ChildCount()); j++ {
				walkNav(x.Child(j))
			}
		}
		walkNav(c)
		if len(idents) >= 2 {
			return idents[0], idents[len(idents)-1], true
		}
	}
	return "", "", false
}

// isCallableName filters out names that are never user-defined symbols worth a
// graph edge (language builtins and the like keep symref noise out of the
// resolver without affecting precision).
func isCallableName(name string) bool {
	if name == "" {
		return false
	}
	return !builtinCallNames[name]
}

// builtinCallNames are ubiquitous language builtins/keywords that would only add
// unresolvable symref noise. Kept intentionally small and high-confidence.
var builtinCallNames = map[string]bool{
	// Go builtins
	"len": true, "cap": true, "make": true, "new": true, "append": true,
	"copy": true, "delete": true, "panic": true, "recover": true, "print": true,
	"println": true, "close": true, "complex": true, "real": true, "imag": true,
	// JS/TS ubiquitous
	"require": true, "parseInt": true, "parseFloat": true,
	// Ruby loaders (imports edges cover these; calls would be noise)
	"require_relative": true, "load": true,
	// Python builtins
	"super": true, "isinstance": true,
	// Unreal / C++ macro helpers (type reads come from Cast<T>/CreateDefaultSubobject<T>)
	"TEXT": true, "LOCTEXT": true, "NSLOCTEXT": true,
}
