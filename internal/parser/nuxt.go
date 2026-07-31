package parser

import (
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Nuxt / Nitro densify helpers.
//
// Path roles (composable/plugin/middleware/server_api/page) also live in
// tsFrameworkRole. This file owns wrapper densify:
//   - defineEventHandler / eventHandler → server_api|server_route|server_middleware
//     sites + body call edges (Nitro server/api, server/routes, server/middleware)
//   - defineNuxtRouteMiddleware → middleware site + body calls (navigateTo, …)
//   - defineNuxtPlugin → plugin site + body calls
//
// Composable wiring (useX → listUsers) comes from normal function-body call
// extraction once role=composable is tagged. Auto-import resolution stays
// conventional — no full Nitro runtime graph.

// extractNuxtDensify densifies Nuxt/Nitro convention wrappers into role-tagged
// entrypoints plus body call edges.
func extractNuxtDensify(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	p := strings.ToLower(filepath.ToSlash(relPath))
	src := strings.ToLower(string(buf))
	looksNuxt := containsFramework(frameworks, string(FrameworkNuxt)) ||
		looksLikeNuxtConventionPath(p) ||
		strings.Contains(src, "defineeventhandler") ||
		strings.Contains(src, "definenuxt") ||
		strings.Contains(src, "eventhandler(")
	if !looksNuxt {
		return
	}
	fw := withFramework(frameworks, string(FrameworkNuxt))
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		fn := n.ChildByFieldName("function")
		if fn == nil || fn.Type() != "identifier" {
			return
		}
		fnName := strings.TrimSpace(fn.Content(buf))
		role, sitePrefix := nuxtWrapperRole(fnName, p)
		if role == "" {
			return
		}
		line := int(n.StartPoint().Row) + 1
		siteName := fmt.Sprintf("%s_%d", sitePrefix, line)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "typescript",
			frameworkSignature(fw, role), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitTSCall(repoID, relPath, sym.ID, fnName, 0.9, out)
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

// extractNuxtServerHandlers is kept as a thin alias for older call sites / docs.
func extractNuxtServerHandlers(root *sitter.Node, buf []byte, repoID, relPath string, frameworks []string, out *ParseResult) {
	extractNuxtDensify(root, buf, repoID, relPath, frameworks, out)
}

// nuxtWrapperRole maps a Nuxt/Nitro wrapper call to a role + site name prefix.
func nuxtWrapperRole(fnName, relPath string) (role, sitePrefix string) {
	switch {
	case strings.EqualFold(fnName, "defineEventHandler"), strings.EqualFold(fnName, "eventHandler"):
		return nuxtServerHandlerRole(relPath), "nuxt_event"
	case strings.EqualFold(fnName, "defineNuxtRouteMiddleware"):
		return "middleware", "nuxt_mw"
	case strings.EqualFold(fnName, "defineNuxtPlugin"):
		return "plugin", "nuxt_plugin"
	default:
		return "", ""
	}
}

// nuxtServerHandlerRole picks Nitro path roles for defineEventHandler sites.
func nuxtServerHandlerRole(relPath string) string {
	p := strings.ToLower(filepath.ToSlash(relPath))
	switch {
	case strings.Contains(p, "/server/middleware/") || strings.HasPrefix(p, "server/middleware/"):
		return "server_middleware"
	case strings.Contains(p, "/server/routes/") || strings.HasPrefix(p, "server/routes/"):
		return "server_route"
	default:
		return "server_api"
	}
}

// nuxtPathRole returns a convention role for symbols under Nuxt dirs.
func nuxtPathRole(relPath string) string {
	p := strings.ToLower(filepath.ToSlash(relPath))
	switch {
	case isNuxtComposablesPath(p):
		return "composable"
	case isNuxtPluginsPath(p):
		return "plugin"
	case strings.Contains(p, "/server/middleware/") || strings.HasPrefix(p, "server/middleware/"):
		return "server_middleware"
	case strings.Contains(p, "/server/routes/") || strings.HasPrefix(p, "server/routes/"):
		return "server_route"
	case isNuxtServerAPIPath(p):
		return "server_api"
	case isNuxtMiddlewarePath(p):
		// Route middleware/ (not server/middleware — handled above).
		if strings.Contains(p, "/server/") || strings.HasPrefix(p, "server/") {
			return "server_middleware"
		}
		return "middleware"
	case strings.Contains(p, "/layouts/") || strings.HasPrefix(p, "layouts/"):
		return "layout"
	case strings.Contains(p, "/pages/") || strings.HasPrefix(p, "pages/") ||
		strings.HasSuffix(p, "app.vue") || strings.HasSuffix(p, "/app.vue"):
		return "page"
	default:
		return ""
	}
}

func looksLikeNuxtConventionPath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	return isNuxtComposablesPath(p) || isNuxtPluginsPath(p) || isNuxtServerAPIPath(p) ||
		isNuxtMiddlewarePath(p) || isNuxtPagesOrLayoutsPath(p) ||
		strings.Contains(p, "/server/routes/") || strings.HasPrefix(p, "server/routes/") ||
		strings.Contains(p, "/server/middleware/") || strings.HasPrefix(p, "server/middleware/") ||
		strings.HasSuffix(p, "app.vue") || strings.HasSuffix(p, "/app.vue")
}

func looksLikeNuxtComposableName(name string) bool {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "use") || len(name) < 4 {
		return false
	}
	r := name[3]
	return r >= 'A' && r <= 'Z'
}
