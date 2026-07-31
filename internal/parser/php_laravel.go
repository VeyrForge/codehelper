package parser

// Laravel application-graph densification.
//
// php.go already covers facades, `Route::verb`, `$this->app->bind` and the Laravel
// 11 bootstrap chain. What a real Laravel app is actually made of was still
// missing from the graph, which is why `impact` on a model or a view used to look
// deceptively cheap:
//
//   - Eloquent relations (`$this->hasMany(Order::class)`) — the model-to-model
//     graph, invisible because the target only appears as a `::class` constant.
//   - Model query statics (`User::where(...)`, `Invoice::create(...)`) — the real
//     call sites of every model; a bare `where` symref resolved nowhere.
//   - `view('users.profile')` / `route('users.index')` — string-keyed references
//     that are the ONLY link from a controller to a Blade view or a named route.
//   - `::class` references in `routes/`, `config/`, `bootstrap/` and providers —
//     middleware, policies, listeners and the `config/auth.php` model.
//   - Artisan command signatures — scheduled/CLI entrypoints.
//   - Container resolve (`app()->make(Foo::class)`, `resolve(Foo::class)`) —
//     the DI call sites that ctor type-hints miss when services are resolved
//     explicitly. String keys (`app()->make('mailer')`) stay thin on purpose.
//   - Macroable (`Str::macro('slugify', ...)`) — extension methods registered
//     at boot that otherwise have zero inbound callers.
//   - Provider wiring leftovers: `Event::listen` / `Gate::policy` /
//     `Schedule::job` / `Route::model` class args, and `$listen` map entries.
//
// Every edge here is additive and confidence-weighted; ambiguous or dynamic
// references (`view($name)`, `app()->make($key)`) are deliberately dropped
// rather than guessed.

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	eloquentRelationRe = regexp.MustCompile(
		`->\s*(hasMany|hasOne|belongsTo|belongsToMany|morphMany|morphOne|morphTo|morphToMany|morphedByMany|hasManyThrough|hasOneThrough)\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	// Model / job / event statics: `User::where(`, `SendInvoice::dispatch(`.
	laravelModelStaticRe = regexp.MustCompile(
		`\b([A-Z][A-Za-z0-9_]*)\s*::\s*([a-z][A-Za-z0-9_]*)\s*\(`)
	// `view('users.profile')`, `->markdown('mail.order')`, `View::make('x')`.
	// Blade's own `@extends`/`@include` are handled by ParseBlade, which also
	// resolves them to a file path.
	laravelViewRefRe = regexp.MustCompile(
		`\b(?:view|markdown|View\s*::\s*make)\s*\(\s*['"]([A-Za-z0-9_\-./:]+)['"]`)
	laravelRouteRefRe = regexp.MustCompile(
		`\b(?:route|to_route|signedRoute|temporarySignedRoute|hasRoute)\s*\(\s*['"]([A-Za-z0-9_\-.:]+)['"]`)
	laravelRouteNameRe    = regexp.MustCompile(`->\s*name\s*\(\s*['"]([A-Za-z0-9_\-.:]+)['"]\s*\)`)
	laravelClassConstRe   = regexp.MustCompile(`([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\b`)
	laravelSignatureRe    = regexp.MustCompile(`\$signature\s*=\s*['"]([^'"\s]+)`)
	laravelLivewireViewRe = regexp.MustCompile(`(?i)\bview\s*\(\s*['"]livewire\.([A-Za-z0-9_\-.]+)['"]`)
	// Container resolve — only `::class` (string aliases are opaque at index time).
	laravelContainerChainRe = regexp.MustCompile(
		`(?i)(?:\bapp\s*\(\s*\)\s*->\s*(?:make|get|makeWith)|\$this\s*->\s*app\s*->\s*(?:make|get|makeWith)|\$this\s*->\s*(?:make|get))\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	laravelResolveHelperRe = regexp.MustCompile(
		`(?i)\b(?:resolve|app)\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	// `app()->bind(A::class, B::class)` helper form (php.go covers `$this->app->bind`).
	laravelAppHelperBindRe = regexp.MustCompile(
		`(?i)\bapp\s*\(\s*\)\s*->\s*(?:bind|singleton|instance|scoped)\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*,\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	// Macroable: `Builder::macro('active', fn...)`, `Collection::mixin(...)`.
	laravelMacroRe = regexp.MustCompile(
		`\b([A-Z][A-Za-z0-9_]*)\s*::\s*(macro|mixin)\s*\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]`)
	// Provider / schedule / route-model wiring with class arguments.
	laravelEventListenRe = regexp.MustCompile(
		`(?i)\bEvent\s*::\s*listen\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*,\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	laravelGatePolicyRe = regexp.MustCompile(
		`(?i)\bGate\s*::\s*policy\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*,\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	laravelScheduleJobRe = regexp.MustCompile(
		`(?i)\bSchedule\s*::\s*(?:job|call)\s*\(\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	laravelScheduleCommandRe = regexp.MustCompile(
		`(?i)\bSchedule\s*::\s*command\s*\(\s*['"]([^'"\s]+)`)
	laravelRouteModelRe = regexp.MustCompile(
		`(?i)\bRoute\s*::\s*(?:model|bind)\s*\(\s*['"][^'"]+['"]\s*,\s*([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class`)
	// EventServiceProvider `$listen` map key: `OrderShipped::class =>`.
	laravelListenKeyRe = regexp.MustCompile(
		`([A-Za-z_\\][A-Za-z0-9_\\]*)\s*::\s*class\s*=>`)
)

// laravelModelStaticVerbs are Eloquent/Builder/Job entrypoints. Restricting the
// static-call densify to this list keeps `SomeEnum::from()` and unrelated static
// helpers from minting model edges.
var laravelModelStaticVerbs = map[string]bool{
	"all": true, "find": true, "findOrFail": true, "findMany": true, "findOr": true,
	"first": true, "firstWhere": true, "firstOrFail": true, "firstOrCreate": true,
	"firstOrNew": true, "create": true, "forceCreate": true, "updateOrCreate": true,
	"where": true, "whereIn": true, "whereNotIn": true, "whereNull": true,
	"whereNotNull": true, "whereHas": true, "whereDoesntHave": true, "whereBetween": true,
	"whereDate": true, "whereRaw": true, "orderBy": true, "orderByDesc": true,
	"latest": true, "oldest": true, "with": true, "withCount": true, "without": true,
	"withTrashed": true, "onlyTrashed": true, "query": true, "select": true,
	"pluck": true, "count": true, "sum": true, "avg": true, "max": true, "min": true,
	"exists": true, "doesntExist": true, "paginate": true, "simplePaginate": true,
	"cursorPaginate": true, "chunk": true, "chunkById": true, "cursor": true, "each": true,
	"destroy": true, "truncate": true, "insert": true, "upsert": true, "factory": true,
	// Jobs / events / notifications / mailables.
	"dispatch": true, "dispatchSync": true, "dispatchIf": true, "dispatchUnless": true,
	"dispatchAfterResponse": true, "send": true, "sendNow": true, "to": true, "fake": true,
}

// laravelAppPathRoles marks the directories whose `::class` references are wiring
// (middleware, policies, listeners, providers, config) rather than incidental.
func laravelWiringPath(relPath string) bool {
	p := strings.ToLower(filepathSlash(relPath))
	for _, seg := range []string{
		"routes/", "config/", "bootstrap/", "app/providers/", "database/seeders/",
		"app/console/", // Kernel schedule + command registration
	} {
		if strings.HasPrefix(p, seg) || strings.Contains(p, "/"+seg) {
			return true
		}
	}
	return false
}

// addLaravelAppSymbols densifies Eloquent relations, model statics, view/route
// helper references, wiring `::class` constants, Artisan signatures, container
// `::class` resolve, Macroable registrations, and provider Event/Gate/Schedule
// /Route-model wiring. It runs for PHP and Blade sources; frameworks carries
// the already-detected packs.
func addLaravelAppSymbols(repoID, relPath string, buf []byte, out *ParseResult, frameworks []string) {
	if out == nil {
		return
	}
	src := string(buf)
	if !looksLikeLaravelSource(relPath, src, frameworks) {
		return
	}
	fw := withFramework(frameworks, string(FrameworkLaravel))
	lines := strings.Split(src, "\n")
	wiring := laravelWiringPath(relPath)
	var wiringSite string // lazily created file-scope site for top-level references

	for i, line := range lines {
		lineNo := i + 1
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "*") {
			continue
		}
		enclosing := enclosingLaravelOwnerAtLine(out, lineNo)
		// Falls back to a single per-file site so `config/auth.php` and
		// `routes/web.php` top-level references still have a source node.
		ensureSite := func() string {
			if enclosing != "" {
				return enclosing
			}
			if wiringSite != "" {
				return wiringSite
			}
			name := laravelWiringSiteName(relPath)
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, 1, len(lines), "php",
				frameworkSignature(fw, "wiring"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			wiringSite = sym.ID
			return wiringSite
		}

		// Eloquent relations: bind both the relation method and the owning model,
		// so `impact` on Order reaches User (the model) and User::orders (the method).
		for _, m := range eloquentRelationRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 3 {
				continue
			}
			related := phpSimpleName(m[2])
			if related == "" {
				continue
			}
			if from := ensureSite(); from != "" {
				emitPHPCall(repoID, relPath, from, related, 0.9, out)
			}
			if owner := phpClassSymbolAtLine(out, lineNo); owner != "" {
				emitPHPCall(repoID, relPath, owner, related, 0.85, out)
			}
		}

		for _, m := range laravelModelStaticRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 3 {
				continue
			}
			cls, meth := m[1], m[2]
			if !laravelModelStaticVerbs[meth] || laravelFacadeConcrete[cls] != "" {
				continue // facades are handled in php.go with their concrete class
			}
			switch cls {
			case "self", "static", "parent", "Closure", "Carbon", "Str", "Arr", "Collection":
				continue
			}
			from := ensureSite()
			if from == "" {
				continue
			}
			emitPHPCall(repoID, relPath, from, cls, 0.85, out)
			emitPHPCall(repoID, relPath, from, cls+"."+meth, 0.8, out)
		}

		for _, m := range laravelViewRefRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 2 {
				continue
			}
			if view := normalizeBladeViewName(m[1]); view != "" {
				if from := ensureSite(); from != "" {
					emitPHPCall(repoID, relPath, from, view, 0.85, out)
				}
			}
		}
		for _, m := range laravelLivewireViewRe.FindAllStringSubmatch(line, -1) {
			if len(m) > 1 {
				if from := ensureSite(); from != "" {
					emitPHPCall(repoID, relPath, from, "livewire."+m[1], 0.8, out)
				}
			}
		}
		for _, m := range laravelRouteRefRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 2 {
				continue
			}
			if name := laravelRouteNameSymbol(m[1]); name != "" {
				if from := ensureSite(); from != "" {
					emitPHPCall(repoID, relPath, from, name, 0.85, out)
				}
			}
		}

		// Container resolve: app()->make(Foo::class) / resolve(Foo::class) / app(Foo::class).
		// String aliases stay dropped — they need runtime container knowledge.
		for _, re := range []*regexp.Regexp{laravelContainerChainRe, laravelResolveHelperRe} {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				if len(m) < 2 {
					continue
				}
				cls := phpSimpleName(m[1])
				if cls == "" || cls == "self" || cls == "static" || cls == "parent" {
					continue
				}
				if from := ensureSite(); from != "" {
					emitPHPCall(repoID, relPath, from, cls, 0.9, out)
				}
			}
		}
		if m := laravelAppHelperBindRe.FindStringSubmatch(line); len(m) > 2 {
			abstract, concrete := phpSimpleName(m[1]), phpSimpleName(m[2])
			if from := ensureSite(); from != "" && abstract != "" && concrete != "" {
				emitPHPCall(repoID, relPath, from, abstract, 0.9, out)
				emitPHPCall(repoID, relPath, from, concrete, 0.9, out)
			}
		}

		// Macroable registration: Str::macro('slugify', …) → Str + Str.slugify.
		for _, m := range laravelMacroRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 4 {
				continue
			}
			cls, kind, meth := m[1], m[2], m[3]
			from := ensureSite()
			if from == "" {
				continue
			}
			emitPHPCall(repoID, relPath, from, cls, 0.85, out)
			emitPHPCall(repoID, relPath, from, cls+"."+meth, 0.85, out)
			siteName := fmt.Sprintf("laravel_%s_%s_%s_%d", kind, strings.ToLower(cls), strings.ToLower(meth), lineNo)
			site := symbol(repoID, relPath, siteName, types.SymbolKindFunction, lineNo, lineNo, "php",
				frameworkSignature(fw, "macro"), "")
			out.Symbols = append(out.Symbols, site)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, site.ID))
			emitPHPCall(repoID, relPath, site.ID, cls, 0.9, out)
			emitPHPCall(repoID, relPath, site.ID, cls+"."+meth, 0.9, out)
		}

		// Event::listen / Gate::policy / Schedule::job / Route::model class args.
		if m := laravelEventListenRe.FindStringSubmatch(line); len(m) > 2 {
			if from := ensureSite(); from != "" {
				emitPHPCall(repoID, relPath, from, phpSimpleName(m[1]), 0.9, out)
				emitPHPCall(repoID, relPath, from, phpSimpleName(m[2]), 0.9, out)
			}
		}
		if m := laravelGatePolicyRe.FindStringSubmatch(line); len(m) > 2 {
			if from := ensureSite(); from != "" {
				emitPHPCall(repoID, relPath, from, phpSimpleName(m[1]), 0.9, out)
				emitPHPCall(repoID, relPath, from, phpSimpleName(m[2]), 0.9, out)
			}
		}
		if m := laravelScheduleJobRe.FindStringSubmatch(line); len(m) > 1 {
			if from := ensureSite(); from != "" {
				emitPHPCall(repoID, relPath, from, phpSimpleName(m[1]), 0.9, out)
			}
		}
		if m := laravelScheduleCommandRe.FindStringSubmatch(line); len(m) > 1 {
			cmd := sanitizeHookName(strings.ReplaceAll(m[1], ":", "_"))
			if cmd != "" && cmd != "hook" {
				if from := ensureSite(); from != "" {
					emitPHPCall(repoID, relPath, from, "artisan_"+cmd, 0.8, out)
				}
			}
		}
		if m := laravelRouteModelRe.FindStringSubmatch(line); len(m) > 1 {
			if from := ensureSite(); from != "" {
				emitPHPCall(repoID, relPath, from, phpSimpleName(m[1]), 0.85, out)
			}
		}
		// `$listen` map keys (EventServiceProvider): densify even when the
		// surrounding array isn't on a classic wiring `::class`-only scan line.
		for _, m := range laravelListenKeyRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 2 {
				continue
			}
			cls := phpSimpleName(m[1])
			if cls == "" || cls == "self" || cls == "static" || cls == "parent" {
				continue
			}
			if from := ensureSite(); from != "" {
				emitPHPCall(repoID, relPath, from, cls, 0.85, out)
			}
		}

		// Wiring `::class` constants: middleware, policies, listeners, the
		// `config/auth.php` model, seeder targets. Route lines are covered in
		// php.go; this adds the config/provider/bootstrap layer.
		if wiring {
			for _, m := range laravelClassConstRe.FindAllStringSubmatch(line, -1) {
				if len(m) < 2 {
					continue
				}
				cls := phpSimpleName(m[1])
				switch cls {
				case "", "self", "static", "parent":
					continue
				}
				if from := ensureSite(); from != "" {
					emitPHPCall(repoID, relPath, from, cls, 0.8, out)
				}
			}
		}

		// Artisan command: `protected $signature = 'app:sync {--force}'`.
		if m := laravelSignatureRe.FindStringSubmatch(line); len(m) > 1 {
			// `app:sync-invoices` → `app_sync_invoices` (sanitizeHookName drops
			// the namespace colon, which would fuse the two words).
			cmd := sanitizeHookName(strings.ReplaceAll(m[1], ":", "_"))
			if cmd != "" && cmd != "hook" {
				site := symbol(repoID, relPath, "artisan_"+cmd, types.SymbolKindFunction, lineNo, lineNo, "php",
					frameworkSignature(fw, "entrypoint"), "")
				out.Symbols = append(out.Symbols, site)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, site.ID))
				emitPHPCall(repoID, relPath, site.ID, "handle", 0.8, out)
				if owner := phpClassSymbolAtLine(out, lineNo); owner != "" {
					out.Edges = append(out.Edges, types.Reference{
						ID:         edgeID(repoID, site.ID, owner, "calls"),
						RepoID:     repoID,
						Kind:       types.RefKindCalls,
						SourceID:   site.ID,
						TargetID:   owner,
						Confidence: 0.85,
					})
				}
			}
		}
	}
}

// addLaravelRouteNames turns `->name('users.index')` into a symbol named
// `route:users.index` that points at the route site, so `route('users.index')`
// anywhere in the app (controller, Blade, mailable) reaches the controller action
// through one hop. Called from php.go with the route site it belongs to.
func addLaravelRouteNames(repoID, relPath string, lines []string, at int, routeSymID string, out *ParseResult, frameworks []string) {
	if out == nil || routeSymID == "" {
		return
	}
	// A fluent route definition spans several lines; the name may trail the chain.
	for j := 0; j <= 6 && at+j < len(lines); j++ {
		line := lines[at+j]
		if j > 0 && laravelRoutePattern.MatchString(line) {
			break // next route definition owns any following ->name()
		}
		for _, m := range laravelRouteNameRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 2 || strings.HasSuffix(m[1], ".") {
				continue // trailing dot = a group name prefix, not a route name
			}
			name := laravelRouteNameSymbol(m[1])
			if name == "" {
				continue
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, at+j+1, at+j+1, "php",
				frameworkSignature(withFramework(frameworks, string(FrameworkLaravel)), "route_name"), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, sym.ID, routeSymID, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   sym.ID,
				TargetID:   routeSymID,
				Confidence: 0.9,
			})
		}
	}
}

// laravelRouteNameSymbol namespaces a route name so `users.index` cannot collide
// with a Blade view of the same dotted name. Colons are folded to dots: a symref
// target id is `symref:repo:path:name`, and the resolver reads the name as
// everything after the LAST colon, so a name containing one would resolve wrong.
func laravelRouteNameSymbol(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasSuffix(s, ".") || strings.ContainsAny(s, "$ ") {
		return ""
	}
	return "route_name_" + collapseDots(strings.ReplaceAll(s, ":", "."))
}

// collapseDots trims and de-duplicates dot separators.
func collapseDots(s string) string {
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	return strings.Trim(s, ".")
}

// laravelWiringSiteName names the per-file site used for top-level references.
func laravelWiringSiteName(relPath string) string {
	base := path.Base(filepathSlash(relPath))
	base = strings.TrimSuffix(base, ".blade.php")
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	name := sanitizeHookName(base)
	if name == "" || name == "hook" {
		name = "file"
	}
	return "laravel_wiring_" + name
}

// phpClassSymbolAtLine returns the id of the class/interface/trait symbol whose
// body contains the line — the owner of a relation method or command signature.
func phpClassSymbolAtLine(out *ParseResult, line int) string {
	if out == nil {
		return ""
	}
	best := ""
	bestSpan := -1
	for _, s := range out.Symbols {
		if s.Kind != types.SymbolKindClass && s.Kind != types.SymbolKindInterface {
			continue
		}
		if s.LineStart > line || line > s.LineEnd {
			continue
		}
		// Innermost enclosing type wins (anonymous classes nest).
		span := s.LineEnd - s.LineStart
		if bestSpan < 0 || span < bestSpan {
			best, bestSpan = s.ID, span
		}
	}
	return best
}

// enclosingLaravelOwnerAtLine prefers a real method over synthetic densify sites
// that addPHPFrameworkSymbols mints on the same line (span-0 `role=facade-call`
// / `route_*` symbols). Without this, `Event::listen` / `Str::macro` edges would
// attach to the synthetic site instead of `boot()` / `register()`, and impact on
// the provider method would miss the wired classes.
func enclosingLaravelOwnerAtLine(out *ParseResult, line int) string {
	if out == nil {
		return ""
	}
	best := ""
	bestSpan := -1
	for _, s := range out.Symbols {
		if s.Kind != types.SymbolKindMethod {
			continue
		}
		if s.LineStart > line || line > s.LineEnd {
			continue
		}
		span := s.LineEnd - s.LineStart
		if bestSpan < 0 || span < bestSpan {
			best, bestSpan = s.ID, span
		}
	}
	if best != "" {
		return best
	}
	bestSpan = -1
	for _, s := range out.Symbols {
		if s.Kind != types.SymbolKindFunction {
			continue
		}
		if s.LineStart > line || line > s.LineEnd {
			continue
		}
		if laravelSyntheticDensifySymbol(s) {
			continue
		}
		span := s.LineEnd - s.LineStart
		if bestSpan < 0 || span < bestSpan {
			best, bestSpan = s.ID, span
		}
	}
	return best
}

func laravelSyntheticDensifySymbol(s types.Symbol) bool {
	sig := s.Signature
	if strings.Contains(sig, "role=facade-call") ||
		strings.Contains(sig, "role=macro") ||
		strings.Contains(sig, "role=container_bind") ||
		strings.Contains(sig, "role=wiring") ||
		strings.Contains(sig, "role=route_name") {
		return true
	}
	if strings.Contains(sig, "role=entrypoint") {
		name := s.Name
		if strings.HasPrefix(name, "route_") || strings.HasPrefix(name, "boot_") ||
			strings.HasPrefix(name, "artisan_") || strings.HasPrefix(name, "laravel_") {
			return true
		}
	}
	return false
}

// looksLikeLaravelSource gates the densify so a plain-PHP or WordPress file does
// not pay for (or get polluted by) Laravel heuristics.
func looksLikeLaravelSource(relPath, src string, frameworks []string) bool {
	if containsFramework(frameworks, string(FrameworkLaravel)) {
		return true
	}
	if IsBladePath(relPath) {
		return true
	}
	p := strings.ToLower(filepathSlash(relPath))
	for _, seg := range []string{
		"app/http/", "app/models/", "app/providers/", "app/console/", "app/jobs/",
		"app/events/", "app/listeners/", "app/policies/", "app/services/",
		"app/livewire/", "app/filament/", "routes/", "config/", "bootstrap/",
		"database/seeders/", "database/migrations/",
	} {
		if strings.HasPrefix(p, seg) || strings.Contains(p, "/"+seg) {
			return true
		}
	}
	lower := strings.ToLower(src)
	for _, marker := range []string{
		"illuminate\\", "use illuminate", "extends model", "extends controller",
		"extends formrequest", "extends serviceprovider", "extends command",
		"laravel\\", "livewire\\", "filament\\",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
