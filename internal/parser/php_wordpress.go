package parser

// WordPress densification beyond php.go's per-call hook sites.
//
// php.go mints line-numbered `wp_add_action_*` / `wp_doaction_*` sites and wires
// registration→callback. This file adds the rest of a real plugin/theme graph:
//
//   - Cross-file-friendly hook registry: each `add_action`/`add_filter` with a
//     literal tag also owns a stable hub `wp_hook_action_<tag>` /
//     `wp_hook_filter_<tag>` (hub→registration). `do_action`/`apply_filters`
//     fire sites call that hub by name so ResolveSymrefs binds fire→listener
//     when the hub is unique repo-wide; same-file fires also get concrete
//     fire→registration edges (always knowable). Dynamic tags are skipped.
//   - `register_rest_route` / `register_rest_field` — REST surface whose handlers
//     live only behind `'callback' => [$this, 'x']` arrays.
//   - Admin pages / settings / dashboard widgets whose render callback is a
//     POSITIONAL argument several lines down.
//   - `add_meta_box`, `register_setting` (`sanitize_callback`),
//     `register_block_type` (`render_callback`), `register_post_type`,
//     `register_taxonomy` — content-model entrypoints.
//   - `require_once __DIR__ . '/includes/class-foo.php'` and
//     `get_template_part('template-parts/content', 'page')`: WordPress wires files
//     by INCLUDE, not by autoloaded namespace, so without these edges a plugin's
//     file graph is a set of disconnected islands and `impact` on an included
//     class file finds no dependents.
//
// Callback arguments are read from the balanced call text, so multi-line
// registration arrays (the normal formatting) resolve.

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	// Registration calls that mint an entrypoint symbol: fn → name-arg index.
	wpRegistrarRe = regexp.MustCompile(
		`\b(register_rest_route|register_rest_field|register_post_type|register_taxonomy|register_block_type|register_block_pattern|register_setting|register_sidebar|register_nav_menu|add_meta_box|add_menu_page|add_submenu_page|add_options_page|add_theme_page|add_management_page|add_users_page|add_dashboard_page|add_media_page|add_posts_page|add_pages_page|add_comments_page|add_plugins_page|add_links_page|add_settings_section|add_settings_field|wp_add_dashboard_widget)\s*\(`)
	// 'callback' => 'fn' | [$this, 'm'] | [Cls::class, 'm'] | Closure
	// Array form must consume through ']' — a bare [^,\n]+ truncates at the
	// comma between class and method (the live WP formatting).
	wpCallbackKeyRe = regexp.MustCompile(
		`['"](callback|permission_callback|sanitize_callback|render_callback|update_callback|get_callback|validate_callback|auth_callback|display_callback|show_ui_callback|meta_box_cb)['"]\s*=>\s*(\[[^\]]*\]|array\s*\([^\)]*\)|'[^']+'|"[^"]+"|[A-Za-z_\\$][A-Za-z0-9_\\:]*)`)
	wpIncludeRe = regexp.MustCompile(
		`(?i)\b(require_once|include_once|require|include)\s*(?:\(\s*)?([^;]+)`)
	wpTemplatePartRe = regexp.MustCompile(
		`\b(get_template_part|locate_template|load_template|get_header|get_footer|get_sidebar)\s*\(\s*(.*)`)
	wpQuotedRe = regexp.MustCompile(`['"]([^'"]*)['"]`)
	// Literal-tag hook registration / fire (dynamic `$tag` skipped downstream).
	wpAddHookCallRe  = regexp.MustCompile(`\badd_(action|filter)\s*\(`)
	wpFireHookCallRe = regexp.MustCompile(`\b(do_action|apply_filters|do_action_ref_array|apply_filters_ref_array)\s*\(`)
)

// wpAdminPageCallbackArg maps an admin-page registrar to the 0-based index of its
// render-callback argument (WordPress fixes these positions).
var wpAdminPageCallbackArg = map[string]int{
	"add_menu_page":           4,
	"add_submenu_page":        5,
	"add_options_page":        4,
	"add_theme_page":          4,
	"add_management_page":     4,
	"add_users_page":          4,
	"add_dashboard_page":      4,
	"add_media_page":          4,
	"add_posts_page":          4,
	"add_pages_page":          4,
	"add_comments_page":       4,
	"add_plugins_page":        4,
	"add_links_page":          4,
	"add_meta_box":            2,
	"add_settings_section":    2,
	"add_settings_field":      2,
	"wp_add_dashboard_widget": 2,
}

// wpRegistrarSlugArg maps a registrar to the argument that best names it.
var wpRegistrarSlugArg = map[string]int{
	"register_rest_route": 0, "register_rest_field": 1, "register_post_type": 0,
	"register_taxonomy": 0, "register_block_type": 0, "register_block_pattern": 0,
	"register_setting": 1, "register_sidebar": -1, "register_nav_menu": 0,
	"add_meta_box": 0, "add_menu_page": 3, "add_submenu_page": 4,
	"add_options_page": 3, "add_theme_page": 3, "add_management_page": 3,
	"add_users_page": 3, "add_dashboard_page": 3, "add_media_page": 3,
	"add_posts_page": 3, "add_pages_page": 3, "add_comments_page": 3,
	"add_plugins_page": 3, "add_links_page": 3, "add_settings_section": 0,
	"add_settings_field": 0, "wp_add_dashboard_widget": 0,
}

// wpSite is a registration call's synthetic entrypoint symbol and the source
// line span it owns, so callbacks written inside its argument array attach to it.
type wpSite struct {
	id        string
	from, to  int
	registrar string
}

// addWordPressAppSymbols densifies REST routes, admin pages, content-model
// registrations, their callbacks, the include/template file graph, and the
// cross-file-friendly hook registry (fire→listener hubs).
func addWordPressAppSymbols(repoID, relPath string, buf []byte, out *ParseResult, frameworks []string) {
	if out == nil {
		return
	}
	src := string(buf)
	if !looksLikeWordPressSource(relPath, src, frameworks) {
		return
	}
	fw := withFramework(frameworks, string(FrameworkWordPress))
	lines := strings.Split(src, "\n")
	offsets := lineStartOffsets(src)

	var sites []wpSite
	// Pass 1: registration calls → entrypoint symbols spanning their whole call.
	for _, loc := range wpRegistrarRe.FindAllStringSubmatchIndex(src, -1) {
		fn := src[loc[2]:loc[3]]
		args, endOff := phpBalancedArgs(src, loc[1])
		if args == "" && endOff == 0 {
			continue
		}
		startLine := offsetLine(offsets, loc[0])
		endLine := offsetLine(offsets, endOff)
		// Resolved BEFORE the site symbol is appended, or the site (a narrower
		// span) would be returned as its own enclosing symbol.
		enclosing := enclosingSymbolAtLine(out, startLine)
		parts := phpSplitArgs(args)
		name := wpSiteName(fn, parts, startLine)
		role := "entrypoint"
		if fn == "register_post_type" || fn == "register_taxonomy" || fn == "register_setting" {
			role = "registration"
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindFunction, startLine, endLine, "php",
			frameworkSignature(fw, role), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		emitPHPCall(repoID, relPath, sym.ID, fn, 0.85, out)
		sites = append(sites, wpSite{id: sym.ID, from: startLine, to: endLine, registrar: fn})

		// Positional render callbacks (admin pages, meta boxes, settings fields).
		if idx, ok := wpAdminPageCallbackArg[fn]; ok && idx < len(parts) {
			cb := strings.TrimSpace(parts[idx])
			// Null/false placeholders (submenu separators) are not callables.
			if cb != "" && !strings.EqualFold(cb, "null") && !strings.EqualFold(cb, "false") {
				emitWPHookCallbackEdges(repoID, relPath, sym.ID, cb, out)
			}
		}
		// The registering function owns the site, so impact on a handler climbs
		// back through register_routes() / probe_admin_menu() to their callers.
		if enclosing != "" && enclosing != sym.ID {
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, enclosing, sym.ID, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   enclosing,
				TargetID:   sym.ID,
				Confidence: 0.9,
			})
		}
	}

	// Pass 2: keyed callbacks anywhere — attributed to the innermost registration
	// site covering the line, else the enclosing function/method.
	for i, line := range lines {
		lineNo := i + 1
		for _, m := range wpCallbackKeyRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 3 {
				continue
			}
			from := innermostWPSite(sites, lineNo)
			if from == "" {
				from = enclosingSymbolAtLine(out, lineNo)
			}
			if from == "" {
				continue
			}
			emitWPHookCallbackEdges(repoID, relPath, from, strings.TrimSpace(m[2]), out)
		}
	}

	addWPHookRegistry(repoID, relPath, src, out, fw)
	addPHPIncludeEdges(repoID, relPath, src, out)
	addWPTemplatePartEdges(repoID, relPath, lines, out, fw)
}

// wpSiteName builds a stable, readable symbol name for a registration call.
func wpSiteName(fn string, args []string, line int) string {
	slug := ""
	if idx, ok := wpRegistrarSlugArg[fn]; ok && idx >= 0 && idx < len(args) {
		slug = wpSlug(phpStringLiteral(args[idx]))
	}
	if fn == "register_rest_route" && len(args) > 1 {
		if route := wpSlug(phpStringLiteral(args[1])); route != "" && route != "hook" {
			if slug != "" && slug != "hook" {
				slug += "_" + route
			} else {
				slug = route
			}
		}
	}
	if fn == "register_rest_field" && len(args) > 0 {
		// Prefer object_type_field when both literals are present.
		if obj := wpSlug(phpStringLiteral(args[0])); obj != "" && obj != "hook" {
			if slug != "" && slug != "hook" {
				slug = obj + "_" + slug
			} else {
				slug = obj
			}
		}
	}
	base := "wp_" + strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(fn, "register_"), "wp_add_"), "add_")
	if slug != "" && slug != "hook" {
		base += "_" + slug
	}
	if len(base) > 60 {
		base = base[:60]
	}
	return fmt.Sprintf("%s_%d", base, line)
}

// wpSlug turns a namespace/route literal into a symbol-name fragment. Separators
// become underscores; sanitizeHookName alone would drop them and fuse the words
// ("probe/v1" → "probev1").
func wpSlug(raw string) string {
	s := strings.NewReplacer("/", "_", ":", "_", `\`, "_", ".", "_").Replace(strings.TrimSpace(raw))
	s = strings.Trim(s, "_")
	return sanitizeHookName(s)
}

// innermostWPSite returns the narrowest registration site whose span covers line.
func innermostWPSite(sites []wpSite, line int) string {
	best, bestSpan := "", -1
	for _, s := range sites {
		if line < s.from || line > s.to {
			continue
		}
		span := s.to - s.from
		if bestSpan < 0 || span < bestSpan {
			best, bestSpan = s.id, span
		}
	}
	return best
}

// wpHookReg is one literal-tag add_action/add_filter site in the current file.
type wpHookReg struct {
	kind, hook, siteID string
}

// addWPHookRegistry densifies fire→listener where the hook tag is a string
// literal. Same-file: concrete fire→registration edges. Cross-file: a stable
// hub symbol per (kind, tag) that registrations own and fires call by name —
// ResolveSymrefs binds when that hub name is unique repo-wide (custom plugin
// tags); crowded core tags like `init` stay honest-ambiguous.
func addWPHookRegistry(repoID, relPath, src string, out *ParseResult, fw []string) {
	if out == nil || src == "" {
		return
	}
	offsets := lineStartOffsets(src)
	hubs := map[string]string{} // hub name → symbol id (once per file)
	seenEdge := map[string]bool{}
	addConcreteCall := func(from, to string, conf float64) {
		if from == "" || to == "" || from == to {
			return
		}
		id := edgeID(repoID, from, to, "calls")
		if seenEdge[id] {
			return
		}
		seenEdge[id] = true
		out.Edges = append(out.Edges, types.Reference{
			ID:         id,
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   from,
			TargetID:   to,
			Confidence: conf,
		})
	}
	ensureHub := func(kind, hook string, line int) (string, string) {
		name := wpHookHubName(kind, hook)
		if name == "" {
			return "", ""
		}
		if id := hubs[name]; id != "" {
			return name, id
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "php",
			frameworkSignature(fw, "hook_hub"), "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		hubs[name] = sym.ID
		return name, sym.ID
	}

	var regs []wpHookReg
	for _, loc := range wpAddHookCallRe.FindAllStringSubmatchIndex(src, -1) {
		kind := strings.ToLower(src[loc[2]:loc[3]])
		args, endOff := phpBalancedArgs(src, loc[1])
		if endOff == 0 {
			continue
		}
		parts := phpSplitArgs(args)
		if len(parts) < 2 {
			continue
		}
		hookLit := phpStringLiteral(parts[0])
		if hookLit == "" {
			continue // dynamic tag — not statically knowable
		}
		hook := sanitizeHookName(hookLit)
		if hook == "" || hook == "hook" {
			continue
		}
		startLine := offsetLine(offsets, loc[0])
		prefix := fmt.Sprintf("wp_add_%s_%s_", kind, hook)
		siteID := findSymbolWithPrefixAtLine(out, prefix, startLine)
		if siteID == "" {
			// php.go may have missed an unusual shape — mint a registration site.
			siteName := fmt.Sprintf("wp_add_%s_%s_%d", kind, hook, startLine)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, startLine, startLine, "php",
				frameworkSignature(fw, "entrypoint"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPHPCall(repoID, relPath, sym.ID, "add_"+kind, 0.85, out)
			emitWPHookCallbackEdges(repoID, relPath, sym.ID, parts[1], out)
			siteID = sym.ID
		}
		_, hubID := ensureHub(kind, hook, startLine)
		addConcreteCall(hubID, siteID, 0.9)
		regs = append(regs, wpHookReg{kind: kind, hook: hook, siteID: siteID})
	}

	for _, loc := range wpFireHookCallRe.FindAllStringSubmatchIndex(src, -1) {
		fn := strings.ToLower(src[loc[2]:loc[3]])
		args, endOff := phpBalancedArgs(src, loc[1])
		if endOff == 0 {
			continue
		}
		parts := phpSplitArgs(args)
		if len(parts) == 0 {
			continue
		}
		hookLit := phpStringLiteral(parts[0])
		if hookLit == "" {
			continue
		}
		hook := sanitizeHookName(hookLit)
		if hook == "" || hook == "hook" {
			continue
		}
		kind := "action"
		if strings.Contains(fn, "filter") {
			kind = "filter"
		}
		startLine := offsetLine(offsets, loc[0])
		firePrefix := fmt.Sprintf("wp_%s_%s_", strings.ReplaceAll(fn, "_", ""), hook)
		fireID := findSymbolWithPrefixAtLine(out, firePrefix, startLine)
		if fireID == "" {
			siteName := fmt.Sprintf("wp_%s_%s_%d", strings.ReplaceAll(fn, "_", ""), hook, startLine)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, startLine, startLine, "php",
				frameworkSignature(fw, "hook-fire"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitPHPCall(repoID, relPath, sym.ID, fn, 0.9, out)
			fireID = sym.ID
		}
		// Cross-file: call the stable hub by name (resolves when unique).
		if hubName := wpHookHubName(kind, hook); hubName != "" {
			emitPHPCall(repoID, relPath, fireID, hubName, 0.85, out)
		}
		// Same-file: concrete fire → matching registration sites.
		for _, r := range regs {
			if r.kind == kind && r.hook == hook {
				addConcreteCall(fireID, r.siteID, 0.9)
			}
		}
	}
}

// wpHookHubName is the stable, cross-file symbol name for a literal hook tag.
// Mirrors Laravel's route_name_* hubs: fire sites call this name; registrations
// own a same-named symbol that points at the add_* site.
func wpHookHubName(kind, hook string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	hook = strings.TrimSpace(hook)
	if hook == "" || hook == "hook" {
		return ""
	}
	if kind != "action" && kind != "filter" {
		return ""
	}
	return "wp_hook_" + kind + "_" + hook
}

// findSymbolWithPrefixAtLine returns the id of a symbol whose name starts with
// prefix and whose LineStart matches line (php.go's line-numbered hook sites).
func findSymbolWithPrefixAtLine(out *ParseResult, prefix string, line int) string {
	if out == nil || prefix == "" {
		return ""
	}
	for _, s := range out.Symbols {
		if s.LineStart == line && strings.HasPrefix(s.Name, prefix) {
			return s.ID
		}
	}
	return ""
}

// addPHPIncludeEdges emits file-level imports for `require`/`include` of literal
// paths. WordPress plugins and themes (and plenty of plain-PHP apps) wire their
// files this way, so without these edges the file graph has no connections at all.
func addPHPIncludeEdges(repoID, relPath, src string, out *ParseResult) {
	fid := FileNodeID(repoID, relPath)
	dir := path.Dir(filepathSlash(relPath))
	if dir == "." {
		dir = ""
	}
	seen := map[string]bool{}
	for _, m := range wpIncludeRe.FindAllStringSubmatch(src, -1) {
		if len(m) < 3 {
			continue
		}
		target := resolvePHPIncludePath(m[2], dir)
		if target == "" || target == filepathSlash(relPath) || seen[target] {
			continue
		}
		seen[target] = true
		out.Imports = append(out.Imports, target)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fid, moduleNodeID(repoID, target), "imports"),
			RepoID:     repoID,
			Kind:       types.RefKindImports,
			SourceID:   fid,
			TargetID:   moduleNodeID(repoID, target),
			Confidence: 0.85,
		})
	}
}

// resolvePHPIncludePath turns an include expression into a repo-relative path.
// `__DIR__ . '/includes/x.php'` and `plugin_dir_path(__FILE__) . 'includes/x.php'`
// are file-relative; `ABSPATH`/`get_template_directory()` are root-relative. A
// fully dynamic expression (a variable path) yields "" — no guessed edge.
func resolvePHPIncludePath(expr, dir string) string {
	e := strings.TrimSpace(expr)
	quoted := wpQuotedRe.FindAllStringSubmatch(e, -1)
	if len(quoted) == 0 {
		return ""
	}
	// The literal that carries the path is the one ending in .php.
	lit := ""
	for _, q := range quoted {
		if strings.HasSuffix(strings.ToLower(q[1]), ".php") {
			lit = q[1]
			break
		}
	}
	if lit == "" {
		return ""
	}
	lit = strings.TrimPrefix(filepathSlash(strings.TrimSpace(lit)), "./")
	if lit == "" || strings.ContainsAny(lit, "$*") {
		return ""
	}
	rootRelative := false
	for _, marker := range []string{
		"ABSPATH", "WP_CONTENT_DIR", "WP_PLUGIN_DIR", "get_template_directory",
		"get_stylesheet_directory", "get_theme_file_path",
	} {
		if strings.Contains(e, marker) {
			rootRelative = true
			break
		}
	}
	base := dir
	if rootRelative {
		base = ""
	}
	lit = strings.TrimPrefix(lit, "/")
	joined := lit
	if base != "" {
		joined = path.Join(base, lit)
	}
	joined = path.Clean(joined)
	if joined == "" || joined == "." || strings.HasPrefix(joined, "..") {
		return ""
	}
	return joined
}

// addWPTemplatePartEdges links a theme template to the parts it renders.
// `get_template_part('template-parts/content', 'page')` loads
// `template-parts/content-page.php`, falling back to `content.php`.
func addWPTemplatePartEdges(repoID, relPath string, lines []string, out *ParseResult, fw []string) {
	fid := FileNodeID(repoID, relPath)
	seen := map[string]bool{}
	addImport := func(target string, conf float64) {
		if target == "" || seen[target] {
			return
		}
		seen[target] = true
		out.Imports = append(out.Imports, target)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fid, moduleNodeID(repoID, target), "imports"),
			RepoID:     repoID,
			Kind:       types.RefKindImports,
			SourceID:   fid,
			TargetID:   moduleNodeID(repoID, target),
			Confidence: conf,
		})
	}
	for i, line := range lines {
		for _, m := range wpTemplatePartRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 3 {
				continue
			}
			fn, args := m[1], m[2]
			quoted := wpQuotedRe.FindAllStringSubmatch(args, -1)
			switch fn {
			case "get_header", "get_footer", "get_sidebar":
				stem := strings.TrimPrefix(fn, "get_")
				if len(quoted) > 0 && quoted[0][1] != "" {
					stem += "-" + quoted[0][1]
				}
				addImport(stem+".php", 0.8)
			default:
				if len(quoted) == 0 || quoted[0][1] == "" {
					continue
				}
				slug := strings.TrimSuffix(filepathSlash(quoted[0][1]), ".php")
				if len(quoted) > 1 && quoted[1][1] != "" {
					addImport(slug+"-"+quoted[1][1]+".php", 0.85)
				}
				addImport(slug+".php", 0.8)
			}
			// A synthetic site keeps template loads visible in symbol search.
			site := symbol(repoID, relPath, fmt.Sprintf("wp_%s_%d", fn, i+1),
				types.SymbolKindFunction, i+1, i+1, "php", frameworkSignature(fw, "template_load"), "")
			out.Symbols = append(out.Symbols, site)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, site.ID))
			emitPHPCall(repoID, relPath, site.ID, fn, 0.85, out)
		}
	}
}

// looksLikeWordPressSource gates the densify to WordPress files.
func looksLikeWordPressSource(relPath, src string, frameworks []string) bool {
	if containsFramework(frameworks, string(FrameworkWordPress)) {
		return true
	}
	p := strings.ToLower(filepathSlash(relPath))
	if strings.Contains(p, "wp-content/") || strings.Contains(p, "wp-includes/") ||
		strings.Contains(p, "wp-admin/") || strings.HasSuffix(p, "functions.php") {
		return true
	}
	lower := strings.ToLower(src)
	for _, marker := range []string{
		"add_action(", "add_filter(", "register_rest_route(", "wp_enqueue_",
		"plugin_dir_path(", "get_template_part(", "$wpdb", "absp" + "ath",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// phpBalancedArgs returns the argument text of a call whose '(' has just been
// consumed at openIdx, plus the source offset just past the matching ')'.
// Quotes and nesting are tracked so a callback array or a nested call does not
// truncate the argument list.
func phpBalancedArgs(src string, openIdx int) (args string, end int) {
	depth := 1
	inSq, inDq := false, false
	for i := openIdx; i < len(src); i++ {
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
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return src[openIdx:i], i + 1
			}
		}
	}
	return "", 0
}

// phpSplitArgs splits an argument list on top-level commas.
func phpSplitArgs(args string) []string {
	var out []string
	depth := 0
	inSq, inDq := false, false
	start := 0
	for i := 0; i < len(args); i++ {
		c := args[i]
		if inSq {
			if c == '\\' && i+1 < len(args) {
				i++
				continue
			}
			if c == '\'' {
				inSq = false
			}
			continue
		}
		if inDq {
			if c == '\\' && i+1 < len(args) {
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
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(args[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(args[start:]))
	return out
}

// phpStringLiteral returns the content of a quoted argument, or "" when the
// argument is an expression.
func phpStringLiteral(arg string) string {
	a := strings.TrimSpace(arg)
	if len(a) < 2 {
		return ""
	}
	q := a[0]
	if (q != '\'' && q != '"') || a[len(a)-1] != q {
		return ""
	}
	return a[1 : len(a)-1]
}

// lineStartOffsets indexes byte offsets of each line start for offset→line lookups.
func lineStartOffsets(src string) []int {
	offs := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			offs = append(offs, i+1)
		}
	}
	return offs
}

// offsetLine converts a byte offset to a 1-based line number.
func offsetLine(offsets []int, off int) int {
	lo, hi := 0, len(offsets)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if offsets[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}
