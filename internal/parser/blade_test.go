package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// callTargetsFrom collects symref call target names emitted from one symbol.
func callTargetsFrom(res *ParseResult, fromID string) map[string]bool {
	out := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == fromID {
			out[symrefName(e.TargetID)] = true
		}
	}
	return out
}

func importTargets(res *ParseResult) map[string]bool {
	out := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		id := e.TargetID
		if i := strings.Index(id, ":"); i >= 0 {
			// mod:<repo>:<path> — path may itself contain no colons.
			parts := strings.SplitN(id, ":", 3)
			if len(parts) == 3 {
				out[parts[2]] = true
				continue
			}
		}
		out[id] = true
	}
	return out
}

func symbolByName(res *ParseResult, name string) *types.Symbol {
	for i := range res.Symbols {
		if res.Symbols[i].Name == name {
			return &res.Symbols[i]
		}
	}
	return nil
}

func TestParseBlade_ViewGraph(t *testing.T) {
	t.Parallel()
	src := []byte(`@extends('layouts.app')

@section('title', 'Profile')

@section('content')
    @include('partials.nav')
    @includeWhen($showTips, 'partials.tips')
    <x-forms.input name="email" />
    <x-alert type="error" />
    <livewire:user-counter />
    <a href="{{ route('users.index') }}">All users</a>
@endsection
`)
	res, err := ParseBlade(context.Background(), "repo", "resources/views/users/profile.blade.php", src)
	if err != nil {
		t.Fatalf("parse blade: %v", err)
	}
	view := symbolByName(res, "users.profile")
	if view == nil {
		t.Fatalf("expected view symbol users.profile; symbols=%#v", res.Symbols)
	}
	if !strings.Contains(view.Signature, "role=view") {
		t.Errorf("view signature %q missing role=view", view.Signature)
	}
	if !strings.Contains(view.Signature, "laravel") {
		t.Errorf("view signature %q missing laravel framework pack", view.Signature)
	}
	if view.Language != "blade" {
		t.Errorf("view language=%q want blade", view.Language)
	}

	calls := callTargetsFrom(res, view.ID)
	for _, want := range []string{
		"layouts.app",            // @extends
		"partials.nav",           // @include
		"partials.tips",          // @includeWhen (view is the 2nd arg)
		"components.forms.input", // <x-forms.input>
		"Input",                  // class-based component
		"components.alert",       // <x-alert>
		"Alert",                  //
		"UserCounter",            // <livewire:user-counter>
		"route_name_users.index", // {{ route(...) }} inside the template
		"view_section_content",   // @section define edge
	} {
		if !calls[want] {
			t.Errorf("view missing call to %q; got %#v", want, calls)
		}
	}

	// Included views resolve to real file paths so the file graph connects too.
	imports := importTargets(res)
	for _, want := range []string{
		"resources/views/layouts/app.blade.php",
		"resources/views/partials/nav.blade.php",
		"resources/views/components/forms/input.blade.php",
	} {
		if !imports[want] {
			t.Errorf("missing blade import %q; got %#v", want, imports)
		}
	}

	if sec := symbolByName(res, "view_section_content"); sec == nil {
		t.Errorf("expected view_section_content symbol; symbols=%#v", res.Symbols)
	}
}

func TestParseBlade_LayoutYieldsSections(t *testing.T) {
	t.Parallel()
	src := []byte(`<html>
<body>
    @yield('content')
    @stack('scripts')
</body>
</html>
`)
	res, err := ParseBlade(context.Background(), "repo", "resources/views/layouts/app.blade.php", src)
	if err != nil {
		t.Fatalf("parse blade: %v", err)
	}
	view := symbolByName(res, "layouts.app")
	if view == nil {
		t.Fatalf("expected layouts.app view; symbols=%#v", res.Symbols)
	}
	calls := callTargetsFrom(res, view.ID)
	for _, want := range []string{"view_section_content", "view_section_scripts"} {
		if !calls[want] {
			t.Errorf("layout missing yield of %q; got %#v", want, calls)
		}
	}
}

func TestParseBlade_DynamicViewNameNotGuessed(t *testing.T) {
	t.Parallel()
	src := []byte("@include($partial)\n@extends($layout)\n")
	res, err := ParseBlade(context.Background(), "repo", "resources/views/dyn.blade.php", src)
	if err != nil {
		t.Fatalf("parse blade: %v", err)
	}
	view := symbolByName(res, "dyn")
	if view == nil {
		t.Fatalf("expected dyn view; symbols=%#v", res.Symbols)
	}
	if got := callTargetsFrom(res, view.ID); len(got) != 0 {
		t.Errorf("dynamic view names must not produce edges; got %#v", got)
	}
	if got := importTargets(res); len(got) != 0 {
		t.Errorf("dynamic view names must not produce imports; got %#v", got)
	}
}

func TestBladeViewName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path, name, root string
	}{
		{"resources/views/users/profile.blade.php", "users.profile", "resources/views"},
		{"resources/views/layouts/app.blade.php", "layouts.app", "resources/views"},
		{"packages/billing/resources/views/invoice.blade.php", "invoice", "packages/billing/resources/views"},
		{"themes/site/templates/home.blade.php", "home", "themes/site/templates"},
		{"welcome.blade.php", "welcome", ""},
	} {
		name, root := bladeViewName(tc.path)
		if name != tc.name || root != tc.root {
			t.Errorf("bladeViewName(%q) = (%q,%q) want (%q,%q)", tc.path, name, root, tc.name, tc.root)
		}
	}
}

// Blade files must not be routed through the PHP tree-sitter grammar, which
// yields no view symbols for them.
func TestExtractRoutesBladeToBladeParser(t *testing.T) {
	t.Parallel()
	res, err := Extract(context.Background(), "repo", "resources/views/home.blade.php",
		[]byte("@extends('layouts.app')\n"))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if symbolByName(res, "home") == nil {
		t.Fatalf("Extract did not dispatch *.blade.php to ParseBlade; symbols=%#v", res.Symbols)
	}
	if !IsBladePath("resources/views/home.blade.php") || IsBladePath("app/Models/User.php") {
		t.Fatal("IsBladePath misclassified a path")
	}
}

func TestParseBlade_PhpCautious(t *testing.T) {
	t.Parallel()
	src := []byte(`@php($user = User::where('id', 1)->first())

@php
    $svc = new InvoiceService();
    $cls = Order::class;
@endphp
`)
	res, err := ParseBlade(context.Background(), "repo", "resources/views/billing/show.blade.php", src)
	if err != nil {
		t.Fatalf("parse blade: %v", err)
	}
	view := symbolByName(res, "billing.show")
	if view == nil {
		t.Fatalf("expected billing.show; symbols=%#v", res.Symbols)
	}
	calls := callTargetsFrom(res, view.ID)
	for _, want := range []string{
		"User", "User.where", // inline @php(…)
		"InvoiceService", // new Class inside @php…@endphp
		"Order",          // Class::class — not covered by whole-file wiring densify
	} {
		if !calls[want] {
			t.Errorf("cautious @php missing %q; got %#v", want, calls)
		}
	}
}

func TestParseBlade_AwarePropsAndSlots(t *testing.T) {
	t.Parallel()
	// Anonymous component definition: props/aware + default slot render.
	def := []byte(`@props(['type' => 'info', 'message'])
@aware(['user', 'theme' => 'light'])
<div class="alert alert-{{ $type }}">
    {{ $slot }}
</div>
`)
	res, err := ParseBlade(context.Background(), "repo", "resources/views/components/alert.blade.php", def)
	if err != nil {
		t.Fatalf("parse component: %v", err)
	}
	view := symbolByName(res, "components.alert")
	if view == nil {
		t.Fatalf("expected components.alert; symbols=%#v", res.Symbols)
	}
	for _, want := range []string{"view_prop_type", "view_prop_message", "view_aware_user", "view_aware_theme", "view_slot_default"} {
		if symbolByName(res, want) == nil {
			t.Errorf("missing %s symbol; symbols=%#v", want, res.Symbols)
		}
	}
	// Value string 'light' / 'info' must not become prop/aware keys.
	if symbolByName(res, "view_prop_info") != nil || symbolByName(res, "view_aware_light") != nil {
		t.Errorf("array values must not become prop/aware symbols; symbols=%#v", res.Symbols)
	}

	// Consumer fills named slots.
	use := []byte(`<x-card>
    <x-slot:header>Title</x-slot>
    <x-slot name="footer">Bye</x-slot>
    @slot('body')
        Content
    @endslot
</x-card>
`)
	ures, err := ParseBlade(context.Background(), "repo", "resources/views/pages/home.blade.php", use)
	if err != nil {
		t.Fatalf("parse consumer: %v", err)
	}
	page := symbolByName(ures, "pages.home")
	if page == nil {
		t.Fatalf("expected pages.home; symbols=%#v", ures.Symbols)
	}
	calls := callTargetsFrom(ures, page.ID)
	for _, want := range []string{"view_slot_header", "view_slot_footer", "view_slot_body", "components.card", "components.card.index"} {
		if !calls[want] {
			t.Errorf("slot/anon consumer missing %q; got %#v", want, calls)
		}
	}
	if symbolByName(ures, "view_slot_body") == nil {
		t.Errorf("expected @slot define symbol view_slot_body")
	}
}

func TestParseBlade_AnonymousComponentGaps(t *testing.T) {
	t.Parallel()
	src := []byte(`<x-mail::message>
    Hello
</x-mail::message>
<x-dynamic-component component="forms.input" />
<x-panel />
`)
	res, err := ParseBlade(context.Background(), "repo", "resources/views/mail/welcome.blade.php", src)
	if err != nil {
		t.Fatalf("parse blade: %v", err)
	}
	view := symbolByName(res, "mail.welcome")
	if view == nil {
		t.Fatalf("expected mail.welcome; symbols=%#v", res.Symbols)
	}
	calls := callTargetsFrom(res, view.ID)
	for _, want := range []string{
		"mail.message",           // package namespace — no components. prefix
		"Message",                // class leaf
		"components.forms.input", // dynamic-component target
		"Input",
		"components.panel",       // anonymous tag
		"components.panel.index", // folder-index fallback
		"Panel",
	} {
		if !calls[want] {
			t.Errorf("anon component gap missing %q; got %#v", want, calls)
		}
	}
	imports := importTargets(res)
	for _, want := range []string{
		"resources/views/mail/message.blade.php",
		"resources/views/components/forms/input.blade.php",
		"resources/views/components/panel.blade.php",
		"resources/views/components/panel/index.blade.php",
	} {
		if !imports[want] {
			t.Errorf("missing import %q; got %#v", want, imports)
		}
	}
}
