package parser

// Blade template extraction.
//
// A Laravel app's UI lives in `resources/views/**/*.blade.php`, and those files
// are a graph: pages `@extends` layouts, layouts `@yield` sections pages
// `@section`, partials are pulled in with `@include`, and components are used as
// `<x-forms.input>` / `@livewire('counter')`. Routed through the PHP tree-sitter
// grammar a Blade file yields almost nothing (its body is not valid PHP), so
// before this extractor `impact` on a shared layout or partial returned no
// dependents and `query` could not find a view at all.
//
// Views are indexed under their DOTTED Laravel name (`users.profile`), which is
// exactly the string `view('users.profile')` and `@extends('users.profile')`
// reference — so ResolveSymrefs binds controller → view → layout with no extra
// machinery. A file-level `imports` edge to the resolved `.blade.php` path gives
// the same wiring at file granularity.
//
// Densify extras (still pattern-safe, never a full PHP parse of the template):
//   - `@php` / `@php(…)` islands → Class::class / new Class / Model::static
//   - `@aware` / `@props` attribute declarations on (anonymous) components
//   - `@slot` / `<x-slot:…>` named slots (mirrors @section/@yield)
//   - Anonymous component gaps: package `<x-mail::msg>`, folder index views,
//     `<x-dynamic-component component="…">`

import (
	"context"
	"path"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	// @extends('layouts.app') / @include('partials.nav', [...]) / @includeWhen($c, 'x')
	bladeViewDirectiveRe = regexp.MustCompile(
		`@(extends|include|includeIf|includeUnless|includeWhen|includeFirst|each|component|livewire|slot)\s*\(\s*(.+)`)
	// <x-alert>, <x-forms.input />, <x-mail::message> (package namespace).
	bladeComponentTagRe = regexp.MustCompile(`<x-([A-Za-z0-9_.\-]+(?:::[A-Za-z0-9_.\-/]+)?)`)
	// <livewire:counter />
	bladeLivewireTagRe = regexp.MustCompile(`<livewire:([A-Za-z0-9_.\-]+)`)
	// @section('content') / @push('scripts') define; @yield('content') / @stack consume.
	bladeSectionDefineRe = regexp.MustCompile(`@(section|push|prepend)\s*\(\s*['"]([^'"]+)['"]`)
	bladeSectionUseRe    = regexp.MustCompile(`@(yield|stack)\s*\(\s*['"]([^'"]+)['"]`)
	// Quoted view names inside a directive's argument list.
	bladeQuotedRe = regexp.MustCompile(`['"]([A-Za-z0-9_\-./:]+)['"]`)
	// @aware(['user', 'theme' => 'light']) / @props(['type' => 'info', 'message'])
	bladeAwareRe = regexp.MustCompile(`@aware\s*\(\s*\[([^\]]*)\]`)
	bladePropsRe = regexp.MustCompile(`@props\s*\(\s*\[([^\]]*)\]`)
	// Quoted identifiers in @props/@aware arrays; bladeArrayKeys drops values after =>.
	bladeArrayQuotedRe = regexp.MustCompile(`['"]([A-Za-z_][A-Za-z0-9_]*)['"]`)
	// <x-slot:header> / <x-slot name="header">
	bladeSlotTagRe = regexp.MustCompile(`<x-slot(?::([A-Za-z0-9_\-]+)|\b[^>]*\bname\s*=\s*['"]([^'"]+)['"])`)
	// Default slot render site inside an anonymous component body.
	bladeDefaultSlotRe = regexp.MustCompile(`\{\{\s*\$slot\b`)
	// <x-dynamic-component component="forms.input" /> / :component="'alert'"
	bladeDynamicComponentRe = regexp.MustCompile(
		`(?i)<x-dynamic-component\b[^>]*(?:(?::component)|(?:\bcomponent))\s*=\s*['"]([A-Za-z0-9_.\-/]+)['"]`)
	// @php(…) single-line inline form.
	bladePhpInlineRe = regexp.MustCompile(`(?m)@php\s*\((.+)\)\s*$`)
	// Cautious densify inside @php islands only (no full PHP parse).
	bladePhpClassConstRe = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\s*::\s*class\b`)
	bladePhpNewRe        = regexp.MustCompile(`\bnew\s+\\?([A-Z][A-Za-z0-9_\\]*)\s*[\(;]`)
)

// IsBladePath reports a Laravel Blade template (`*.blade.php`).
func IsBladePath(relPath string) bool {
	return strings.HasSuffix(strings.ToLower(filepathSlash(relPath)), ".blade.php")
}

// ParseBlade extracts the view graph from a Blade template: the view itself, the
// layouts/partials/components it pulls in, and the sections it defines or yields.
func ParseBlade(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	if ctx != nil && ctx.Err() != nil {
		return out, ctx.Err()
	}
	viewName, viewsRoot := bladeViewName(relPath)
	if viewName == "" {
		return out, nil
	}
	fw := withFramework(DetectFrameworkPacks(relPath, nil, ""), string(FrameworkLaravel))
	src := string(buf)
	lines := strings.Split(src, "\n")

	// The view symbol: named `users.profile` so `view('users.profile')` and
	// `@extends('users.profile')` resolve straight to it.
	view := symbol(repoID, relPath, viewName, types.SymbolKindFunction, 1, len(lines), "blade",
		frameworkSignature(fw, "view"), "")
	out.Symbols = append(out.Symbols, view)
	out.Edges = append(out.Edges, containsEdge(repoID, relPath, view.ID))

	seenRef := map[string]bool{}
	// refView links this view to another view name and, when the views root is
	// known, to that view's file so the file-level import graph matches too.
	refView := func(target string, conf float64) {
		target = normalizeBladeViewName(target)
		if target == "" || target == viewName || seenRef[target] {
			return
		}
		seenRef[target] = true
		emitPHPCall(repoID, relPath, view.ID, target, conf, out)
		if viewsRoot == "" {
			return
		}
		mod := path.Join(viewsRoot, strings.ReplaceAll(target, ".", "/")+".blade.php")
		out.Imports = append(out.Imports, mod)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, FileNodeID(repoID, relPath), moduleNodeID(repoID, mod), "imports"),
			RepoID:     repoID,
			Kind:       types.RefKindImports,
			SourceID:   FileNodeID(repoID, relPath),
			TargetID:   moduleNodeID(repoID, mod),
			Confidence: conf,
		})
	}

	anonComponent := strings.HasPrefix(viewName, "components.")

	for i, line := range lines {
		lineNo := i + 1
		for _, m := range bladeViewDirectiveRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 3 {
				continue
			}
			directive, args := m[1], m[2]
			switch directive {
			case "livewire":
				for _, q := range bladeQuotedRe.FindAllStringSubmatch(args, 1) {
					emitBladeLivewire(repoID, relPath, view.ID, q[1], out)
				}
			case "slot":
				// `@slot('header')` names a component slot (define site).
				if q := bladeQuotedRe.FindStringSubmatch(args); len(q) > 1 {
					emitBladeSlotDefine(repoID, relPath, view.ID, q[1], lineNo, fw, out)
				}
			case "each":
				// @each('view.row', $rows, 'row') — first argument is the view.
				if q := bladeQuotedRe.FindStringSubmatch(args); len(q) > 1 {
					refView(q[1], 0.85)
				}
			case "includeWhen", "includeUnless":
				// @includeWhen($cond, 'partials.x') — the view is the 2nd argument.
				qs := bladeQuotedRe.FindAllStringSubmatch(args, -1)
				if len(qs) > 0 {
					refView(qs[len(qs)-1][1], 0.8)
				}
			case "includeFirst":
				for _, q := range bladeQuotedRe.FindAllStringSubmatch(args, -1) {
					refView(q[1], 0.7) // only one of the candidates renders
				}
			case "component":
				if q := bladeQuotedRe.FindStringSubmatch(args); len(q) > 1 {
					refView(q[1], 0.85)
				}
			default: // extends, include, includeIf
				if q := bladeQuotedRe.FindStringSubmatch(args); len(q) > 1 {
					conf := 0.9
					if directive != "extends" {
						conf = 0.85
					}
					refView(q[1], conf)
				}
			}
		}
		for _, m := range bladeAwareRe.FindAllStringSubmatch(line, -1) {
			if len(m) > 1 {
				for _, key := range bladeArrayKeys(m[1]) {
					emitBladeAttrSymbol(repoID, relPath, view.ID, bladeAwareSymbol(key), "view_aware", lineNo, fw, out)
				}
			}
		}
		for _, m := range bladePropsRe.FindAllStringSubmatch(line, -1) {
			if len(m) > 1 {
				for _, key := range bladeArrayKeys(m[1]) {
					emitBladeAttrSymbol(repoID, relPath, view.ID, bladePropSymbol(key), "view_prop", lineNo, fw, out)
				}
			}
		}
		for _, m := range bladeSlotTagRe.FindAllStringSubmatch(line, -1) {
			name := ""
			if len(m) > 1 && m[1] != "" {
				name = m[1]
			} else if len(m) > 2 {
				name = m[2]
			}
			if name != "" {
				emitPHPCall(repoID, relPath, view.ID, bladeSlotSymbol(name), 0.8, out)
			}
		}
		if anonComponent && bladeDefaultSlotRe.MatchString(line) {
			emitBladeSlotDefine(repoID, relPath, view.ID, "default", lineNo, fw, out)
		}
		for _, m := range bladeDynamicComponentRe.FindAllStringSubmatch(line, -1) {
			if len(m) > 1 {
				emitBladeComponent(repoID, relPath, view.ID, m[1], refView, out)
			}
		}
		for _, m := range bladeComponentTagRe.FindAllStringSubmatch(line, -1) {
			if len(m) > 1 {
				emitBladeComponent(repoID, relPath, view.ID, m[1], refView, out)
			}
		}
		for _, m := range bladeLivewireTagRe.FindAllStringSubmatch(line, -1) {
			if len(m) > 1 {
				emitBladeLivewire(repoID, relPath, view.ID, m[1], out)
			}
		}
		for _, m := range bladeSectionDefineRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 3 {
				continue
			}
			name := bladeSectionSymbol(m[2])
			if name == "" {
				continue
			}
			sec := symbol(repoID, relPath, name, types.SymbolKindVariable, lineNo, lineNo, "blade",
				frameworkSignature(fw, "view_section"), "")
			out.Symbols = append(out.Symbols, sec)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sec.ID))
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, view.ID, sec.ID, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   view.ID,
				TargetID:   sec.ID,
				Confidence: 0.8,
			})
		}
		for _, m := range bladeSectionUseRe.FindAllStringSubmatch(line, -1) {
			if len(m) > 2 {
				emitPHPCall(repoID, relPath, view.ID, bladeSectionSymbol(m[2]), 0.75, out)
			}
		}
	}
	// Cautious densify of @php islands (Class::class / new X / Model::static).
	// Whole-file Laravel helpers still cover {{ route() }} / @if(Model::…).
	densifyBladePhpIslands(repoID, relPath, view.ID, src, out)
	// PHP expressions inside {{ }} / directive args: route()/view() helper targets
	// and model statics, so a view that queries or links is not a dead end.
	addLaravelAppSymbols(repoID, relPath, buf, out, fw)
	return out, nil
}

// densifyBladePhpIslands extracts `@php(…)` and `@php`…`@endphp` bodies and
// pattern-matches high-confidence class references only — never a full PHP parse.
func densifyBladePhpIslands(repoID, relPath, fromSym, src string, out *ParseResult) {
	if fromSym == "" || src == "" {
		return
	}
	for _, m := range bladePhpInlineRe.FindAllStringSubmatch(src, -1) {
		if len(m) > 1 {
			densifyBladePhpSnippet(repoID, relPath, fromSym, m[1], out)
		}
	}
	// Block form: @php … @endphp (not the inline @php(…) form).
	rest := src
	for {
		idx := strings.Index(rest, "@php")
		if idx < 0 {
			break
		}
		after := rest[idx+len("@php"):]
		// Skip inline `@php(` — already handled above.
		trimStart := strings.TrimLeft(after, " \t")
		if strings.HasPrefix(trimStart, "(") {
			rest = after
			continue
		}
		end := strings.Index(after, "@endphp")
		if end < 0 {
			break
		}
		densifyBladePhpSnippet(repoID, relPath, fromSym, after[:end], out)
		rest = after[end+len("@endphp"):]
	}
}

func densifyBladePhpSnippet(repoID, relPath, fromSym, snippet string, out *ParseResult) {
	if strings.TrimSpace(snippet) == "" {
		return
	}
	for _, m := range bladePhpClassConstRe.FindAllStringSubmatch(snippet, -1) {
		if len(m) > 1 {
			switch m[1] {
			case "self", "static", "parent":
				continue
			}
			emitPHPCall(repoID, relPath, fromSym, m[1], 0.8, out)
		}
	}
	for _, m := range bladePhpNewRe.FindAllStringSubmatch(snippet, -1) {
		if len(m) > 1 {
			if cls := phpSimpleName(m[1]); cls != "" {
				emitPHPCall(repoID, relPath, fromSym, cls, 0.75, out)
			}
		}
	}
	// Model/job statics inside @php — same allowlist as php_laravel densify.
	for _, m := range laravelModelStaticRe.FindAllStringSubmatch(snippet, -1) {
		if len(m) < 3 {
			continue
		}
		cls, meth := m[1], m[2]
		if !laravelModelStaticVerbs[meth] || laravelFacadeConcrete[cls] != "" {
			continue
		}
		switch cls {
		case "self", "static", "parent", "Closure", "Carbon", "Str", "Arr", "Collection":
			continue
		}
		emitPHPCall(repoID, relPath, fromSym, cls, 0.85, out)
		emitPHPCall(repoID, relPath, fromSym, cls+"."+meth, 0.8, out)
	}
	for _, m := range laravelViewRefRe.FindAllStringSubmatch(snippet, -1) {
		if len(m) > 1 {
			if view := normalizeBladeViewName(m[1]); view != "" {
				emitPHPCall(repoID, relPath, fromSym, view, 0.85, out)
			}
		}
	}
	for _, m := range laravelRouteRefRe.FindAllStringSubmatch(snippet, -1) {
		if len(m) > 1 {
			if name := laravelRouteNameSymbol(m[1]); name != "" {
				emitPHPCall(repoID, relPath, fromSym, name, 0.85, out)
			}
		}
	}
}

// emitBladeComponent links a `<x-forms.input>` usage to the anonymous component
// view (`components.forms.input`) and to the class-based component (`Input`).
// Package tags (`<x-mail::message>`), folder-index anonymous components, and
// dynamic-component targets are handled here too.
func emitBladeComponent(repoID, relPath, fromSym, tag string, refView func(string, float64), out *ParseResult) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return
	}
	lower := strings.ToLower(tag)
	if lower == "slot" || strings.HasPrefix(lower, "slot:") || strings.HasPrefix(lower, "slot.") {
		return
	}
	if lower == "dynamic-component" || strings.HasPrefix(lower, "dynamic-component.") {
		return // resolved via bladeDynamicComponentRe
	}

	// Package / namespace component: <x-mail::message>
	if i := strings.Index(tag, "::"); i >= 0 {
		pkg, name := tag[:i], tag[i+2:]
		dotted := normalizeBladeViewName(pkg + "." + strings.ReplaceAll(name, "/", "."))
		if dotted != "" {
			refView(dotted, 0.8)
		}
		if cls := bladeComponentClass(strings.ReplaceAll(name, "/", ".")); cls != "" {
			emitPHPCall(repoID, relPath, fromSym, cls, 0.75, out)
		}
		return
	}

	tag = strings.Trim(tag, ".-")
	if tag == "" {
		return
	}
	dotted := strings.ReplaceAll(tag, "/", ".")
	refView("components."+dotted, 0.8)
	// Anonymous folder components often live at components/foo/index.blade.php.
	if dotted != "" && !strings.Contains(dotted, ".") {
		refView("components."+dotted+".index", 0.55)
	}
	if cls := bladeComponentClass(dotted); cls != "" {
		emitPHPCall(repoID, relPath, fromSym, cls, 0.75, out)
	}
}

// emitBladeLivewire links a Livewire usage to its component class (`counter` →
// `Counter`, `admin.user-table` → `UserTable`) and to its own Blade view.
func emitBladeLivewire(repoID, relPath, fromSym, name string, out *ParseResult) {
	name = strings.Trim(strings.TrimSpace(name), ".-")
	if name == "" {
		return
	}
	if cls := bladeComponentClass(strings.ReplaceAll(name, "/", ".")); cls != "" {
		emitPHPCall(repoID, relPath, fromSym, cls, 0.8, out)
	}
	emitPHPCall(repoID, relPath, fromSym, "livewire."+strings.ReplaceAll(name, "/", "."), 0.7, out)
}

// bladeComponentClass converts a component tag's last segment into its PHP class
// name (`forms.date-picker` → `DatePicker`).
func bladeComponentClass(dotted string) string {
	leaf := dotted
	if i := strings.LastIndexByte(leaf, '.'); i >= 0 {
		leaf = leaf[i+1:]
	}
	var b strings.Builder
	upper := true
	for _, r := range leaf {
		switch {
		case r == '-' || r == '_':
			upper = true
		case upper:
			b.WriteString(strings.ToUpper(string(r)))
			upper = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bladeSectionSymbol namespaces a section/stack name so `content` does not
// collide with an application symbol called `content`.
func bladeSectionSymbol(name string) string {
	s := sanitizeHookName(name)
	if s == "" || s == "hook" {
		return ""
	}
	return "view_section_" + s
}

func bladeSlotSymbol(name string) string {
	s := sanitizeHookName(name)
	if s == "" || s == "hook" {
		return ""
	}
	return "view_slot_" + s
}

func bladeAwareSymbol(name string) string {
	s := sanitizeHookName(name)
	if s == "" || s == "hook" {
		return ""
	}
	return "view_aware_" + s
}

func bladePropSymbol(name string) string {
	s := sanitizeHookName(name)
	if s == "" || s == "hook" {
		return ""
	}
	return "view_prop_" + s
}

func emitBladeSlotDefine(repoID, relPath, fromSym, rawName string, lineNo int, fw []string, out *ParseResult) {
	name := bladeSlotSymbol(rawName)
	if name == "" {
		return
	}
	emitBladeAttrSymbol(repoID, relPath, fromSym, name, "view_slot", lineNo, fw, out)
}

func emitBladeAttrSymbol(repoID, relPath, fromSym, name, role string, lineNo int, fw []string, out *ParseResult) {
	if name == "" || fromSym == "" {
		return
	}
	for _, s := range out.Symbols {
		if s.Name == name {
			emitPHPCall(repoID, relPath, fromSym, name, 0.8, out)
			return
		}
	}
	sym := symbol(repoID, relPath, name, types.SymbolKindVariable, lineNo, lineNo, "blade",
		frameworkSignature(fw, role), "")
	out.Symbols = append(out.Symbols, sym)
	out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, fromSym, sym.ID, "calls"),
		RepoID:     repoID,
		Kind:       types.RefKindCalls,
		SourceID:   fromSym,
		TargetID:   sym.ID,
		Confidence: 0.8,
	})
}

// bladeArrayKeys extracts quoted keys from a Blade `@props`/`@aware` array body,
// skipping string values that follow `=>`.
func bladeArrayKeys(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range bladeArrayQuotedRe.FindAllStringSubmatchIndex(body, -1) {
		if len(m) < 4 {
			continue
		}
		key := body[m[2]:m[3]]
		before := strings.TrimSpace(body[:m[0]])
		if strings.HasSuffix(before, "=>") {
			continue // value, not a key
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// normalizeBladeViewName converts a view reference to its canonical dotted form,
// tolerating slash paths (`partials/nav`) and namespaced views (`mail::layout`).
// Colons fold to dots because a symref target id is `symref:repo:path:name` and
// the resolver reads the name as everything after the LAST colon.
func normalizeBladeViewName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".blade.php")
	s = strings.ReplaceAll(s, "/", ".")
	s = strings.ReplaceAll(s, ":", ".")
	s = collapseDots(s)
	if s == "" || strings.ContainsAny(s, "$ ") {
		return "" // dynamic view name — unknowable, prefer no edge over a wrong one
	}
	return s
}

// bladeViewName derives the dotted Laravel view name and the repo-relative views
// root from a template path. `resources/views/users/profile.blade.php` yields
// ("users.profile", "resources/views"); a package/theme template outside a views
// root still yields a name so the view is at least searchable.
func bladeViewName(relPath string) (name, viewsRoot string) {
	p := filepathSlash(strings.TrimSpace(relPath))
	if p == "" {
		return "", ""
	}
	stem := strings.TrimSuffix(p, ".blade.php")
	if stem == p {
		stem = strings.TrimSuffix(p, path.Ext(p))
	}
	segs := strings.Split(stem, "/")
	// Deepest `views` (or `templates`) segment is the view root — handles both
	// `resources/views/...` and package roots like `packages/foo/resources/views/...`.
	rootIdx := -1
	for i := 0; i < len(segs)-1; i++ {
		switch strings.ToLower(segs[i]) {
		case "views", "templates":
			rootIdx = i
		}
	}
	if rootIdx >= 0 {
		return strings.Join(segs[rootIdx+1:], "."), strings.Join(segs[:rootIdx+1], "/")
	}
	return strings.Join(segs, "."), ""
}
