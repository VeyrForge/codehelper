package parser

import (
	"context"
	"strings"
	"testing"
)

func TestParsePHP_WordPressRestRouteCallbacks(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
/**
 * Plugin Name: Probe REST
 */

class Probe_REST_Controller
{
    public function register_routes()
    {
        register_rest_route(
            'probe/v1',
            '/things',
            [
                'methods'             => 'GET',
                'callback'            => [ $this, 'get_things' ],
                'permission_callback' => [ $this, 'can_read' ],
            ]
        );
    }

    public function get_things( $request ) { return []; }

    public function can_read() { return true; }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "wp-content/plugins/probe/class-rest.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	var site string
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "wp_rest_route_probe_v1_things_") {
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("REST site signature %q missing role=entrypoint", s.Signature)
			}
			site = s.ID
		}
	}
	if site == "" {
		t.Fatalf("expected wp_rest_route_probe_v1_things_* entrypoint; symbols=%#v", res.Symbols)
	}
	calls := callTargetsFrom(res, site)
	for _, want := range []string{"get_things", "can_read", "register_rest_route"} {
		if !calls[want] {
			t.Errorf("REST site missing call to %q; got %#v", want, calls)
		}
	}
	// The registering method reaches the route site, so impact on a handler
	// climbs back to register_routes().
	var registerID string
	for _, s := range res.Symbols {
		if s.Name == "register_routes" {
			registerID = s.ID
		}
	}
	if registerID == "" {
		t.Fatal("missing register_routes method")
	}
	linked := false
	for _, e := range res.Edges {
		if e.SourceID == registerID && e.TargetID == site {
			linked = true
		}
	}
	if !linked {
		t.Errorf("register_routes should reach its REST site; edges=%#v", res.Edges)
	}
}

func TestParsePHP_WordPressAdminPagesAndMetaBoxes(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
add_action( 'admin_menu', 'probe_admin_menu' );

function probe_admin_menu() {
    add_menu_page(
        'Probe Settings',
        'Probe',
        'manage_options',
        'probe-settings',
        'probe_render_settings'
    );
    add_submenu_page(
        'probe-settings',
        'Probe Tools',
        'Tools',
        'manage_options',
        'probe-tools',
        'probe_render_tools'
    );
}

function probe_render_settings() {}
function probe_render_tools() {}

add_action( 'add_meta_boxes', function () {
    add_meta_box( 'probe_box', 'Probe', 'probe_render_box', 'post' );
} );

function probe_render_box( $post ) {}

register_setting( 'probe_group', 'probe_option', [
    'sanitize_callback' => 'probe_sanitize_option',
] );

function probe_sanitize_option( $value ) { return $value; }
`)
	res, err := ParsePHP(context.Background(), "repo", "wp-content/plugins/probe/admin.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	for _, prefix := range []string{
		"wp_menu_page_probe_settings_", "wp_submenu_page_probe_tools_",
		"wp_meta_box_probe_box_", "wp_setting_probe_option_",
	} {
		if !hasPrefixName(names, prefix) {
			t.Errorf("missing %s*; got %#v", prefix, names)
		}
	}
	calls := allCallTargets(res)
	for _, want := range []string{
		"probe_render_settings", // positional add_menu_page callback (arg 5)
		"probe_render_tools",    // positional add_submenu_page callback (arg 6)
		"probe_render_box",      // positional add_meta_box callback (arg 3)
		"probe_sanitize_option", // keyed sanitize_callback
	} {
		if !calls[want] {
			t.Errorf("missing WordPress callback edge to %q; got %#v", want, calls)
		}
	}
	// A capability string in the same call must not be mistaken for a callback.
	if calls["manage_options"] {
		t.Errorf("capability string was treated as a callback; got %#v", calls)
	}
}

func TestParsePHP_WordPressIncludeGraph(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
/**
 * Plugin Name: Probe
 */
require_once __DIR__ . '/includes/class-probe-loader.php';
require_once plugin_dir_path( __FILE__ ) . 'includes/helpers.php';
include ABSPATH . 'wp-admin/includes/file.php';
require_once $dynamic_path;
`)
	res, err := ParsePHP(context.Background(), "repo", "wp-content/plugins/probe/probe.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	imports := importTargets(res)
	for _, want := range []string{
		"wp-content/plugins/probe/includes/class-probe-loader.php",
		"wp-content/plugins/probe/includes/helpers.php",
		"wp-admin/includes/file.php",
	} {
		if !imports[want] {
			t.Errorf("missing include edge to %q; got %#v", want, imports)
		}
	}
	// A variable include path is unknowable — no guessed edge.
	for got := range imports {
		if strings.Contains(got, "dynamic") {
			t.Errorf("dynamic include should not produce an edge; got %q", got)
		}
	}
}

func TestParsePHP_WordPressTemplateParts(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
get_header();
while ( have_posts() ) : the_post();
    get_template_part( 'template-parts/content', 'page' );
endwhile;
get_footer();
`)
	res, err := ParsePHP(context.Background(), "repo", "wp-content/themes/probe/page.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	imports := importTargets(res)
	for _, want := range []string{
		"template-parts/content-page.php", // slug + name variant renders first
		"template-parts/content.php",      // fallback
		"header.php", "footer.php",
	} {
		if !imports[want] {
			t.Errorf("missing template part edge to %q; got %#v", want, imports)
		}
	}
}

func TestWordPressFrameworkDetectionOnRealLayout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, body string }{
		{"wp-content/plugins/probe/class-rest.php", "<?php\nclass X {}\n"},
		{"wp-content/themes/probe/page.php", "<?php\nget_header();\n"},
		{"includes/rest.php", "<?php\nregister_rest_route( 'a/v1', '/b', [] );\n"},
		{"src/blocks.php", "<?php\nregister_block_type( 'probe/card' );\n"},
		{"probe.php", "<?php\n/**\n * Plugin Name: Probe\n */\n"},
	} {
		packs := DetectFrameworkPacks(tc.path, nil, tc.body)
		if !containsFramework(packs, string(FrameworkWordPress)) {
			t.Errorf("expected wordpress pack for %s; got %v", tc.path, packs)
		}
	}
}

func TestPHPSplitArgsAndBalancedArgs(t *testing.T) {
	t.Parallel()
	src := `foo( 'a', [ $this, 'm' ], bar( 1, 2 ), "x,y" )`
	open := strings.IndexByte(src, '(') + 1
	args, end := phpBalancedArgs(src, open)
	if end != len(src) {
		t.Fatalf("balanced end=%d want %d (args=%q)", end, len(src), args)
	}
	parts := phpSplitArgs(args)
	if len(parts) != 4 {
		t.Fatalf("split gave %d parts: %#v", len(parts), parts)
	}
	if parts[1] != "[ $this, 'm' ]" {
		t.Errorf("nested array arg mangled: %q", parts[1])
	}
	if parts[3] != `"x,y"` {
		t.Errorf("quoted comma split: %q", parts[3])
	}
	if got := phpStringLiteral(parts[0]); got != "a" {
		t.Errorf("phpStringLiteral(%q) = %q want a", parts[0], got)
	}
	if got := phpStringLiteral(parts[2]); got != "" {
		t.Errorf("expression arg should have no literal; got %q", got)
	}
}

func TestParsePHP_WordPressHookRegistryFireToListener(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
/**
 * Plugin Name: Probe Hooks
 */
add_action( 'probe_activated', 'probe_on_activate' );
add_filter( 'probe_box_html', 'probe_filter_box', 10, 1 );

function probe_on_activate() {
    do_action( 'probe_activated' );
}

function probe_filter_box( $html ) {
    return apply_filters( 'probe_box_html', $html );
}

function probe_fire_elsewhere() {
    do_action( 'probe_activated' );
    apply_filters( 'probe_box_html', 'x' );
}

// Dynamic tags are not statically knowable — no hub / fire edge.
do_action( $dynamic_hook );
`)
	res, err := ParsePHP(context.Background(), "repo", "wp-content/plugins/probe/hooks.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	actionHub := symbolByName(res, "wp_hook_action_probe_activated")
	filterHub := symbolByName(res, "wp_hook_filter_probe_box_html")
	if actionHub == nil {
		t.Fatalf("missing action hub; symbols=%#v", res.Symbols)
	}
	if filterHub == nil {
		t.Fatalf("missing filter hub; symbols=%#v", res.Symbols)
	}
	if !strings.Contains(actionHub.Signature, "role=hook_hub") {
		t.Errorf("action hub signature %q missing role=hook_hub", actionHub.Signature)
	}
	// Hub owns the registration site (hub → add_*).
	actionHubCalls := callTargetsFrom(res, actionHub.ID)
	if !hasAnyPrefix(actionHubCalls, "wp_add_action_probe_activated_") {
		t.Errorf("action hub should reach add_action site; got %#v", actionHubCalls)
	}
	filterHubCalls := callTargetsFrom(res, filterHub.ID)
	if !hasAnyPrefix(filterHubCalls, "wp_add_filter_probe_box_html_") {
		t.Errorf("filter hub should reach add_filter site; got %#v", filterHubCalls)
	}
	// Same-file fire sites call the matching registration (concrete) and the hub (cross-file name).
	var fireAction, fireFilter string
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "wp_doaction_probe_activated_") {
			fireAction = s.ID
		}
		if strings.HasPrefix(s.Name, "wp_applyfilters_probe_box_html_") {
			fireFilter = s.ID
		}
	}
	if fireAction == "" || fireFilter == "" {
		t.Fatalf("missing fire sites; symbols=%#v", res.Symbols)
	}
	// Collect every fire site for the action tag (probe_on_activate + probe_fire_elsewhere).
	for _, s := range res.Symbols {
		if !strings.HasPrefix(s.Name, "wp_doaction_probe_activated_") {
			continue
		}
		calls := callTargetsFrom(res, s.ID)
		if !calls["wp_hook_action_probe_activated"] {
			t.Errorf("%s missing hub call; got %#v", s.Name, calls)
		}
		if !hasAnyPrefix(calls, "wp_add_action_probe_activated_") {
			t.Errorf("%s missing same-file registration edge; got %#v", s.Name, calls)
		}
	}
	for _, s := range res.Symbols {
		if !strings.HasPrefix(s.Name, "wp_applyfilters_probe_box_html_") {
			continue
		}
		calls := callTargetsFrom(res, s.ID)
		if !calls["wp_hook_filter_probe_box_html"] {
			t.Errorf("%s missing filter hub call; got %#v", s.Name, calls)
		}
	}
	// Dynamic do_action($var) must not invent a hub.
	for _, s := range res.Symbols {
		if strings.Contains(s.Name, "dynamic") {
			t.Errorf("dynamic hook should not mint a symbol; got %q", s.Name)
		}
	}
}

func TestParsePHP_WordPressRestFieldAndAdminSettings(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
register_rest_field( 'post', 'probe_meta', [
    'get_callback'    => [ $this, 'get_probe_meta' ],
    'update_callback' => 'probe_update_meta',
] );

add_settings_section(
    'probe_section',
    'Probe',
    'probe_render_section',
    'probe'
);
add_settings_field(
    'probe_field',
    'Field',
    'probe_render_field',
    'probe',
    'probe_section'
);
wp_add_dashboard_widget( 'probe_dash', 'Probe', 'probe_render_dash' );

function get_probe_meta() { return []; }
function probe_update_meta() {}
function probe_render_section() {}
function probe_render_field() {}
function probe_render_dash() {}

add_submenu_page( 'tools.php', 'Sep', 'Sep', 'manage_options', 'probe-sep', null );
`)
	res, err := ParsePHP(context.Background(), "repo", "wp-content/plugins/probe/admin-rest.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	for _, prefix := range []string{
		"wp_rest_field_post_probe_meta_",
		"wp_settings_section_probe_section_",
		"wp_settings_field_probe_field_",
		"wp_dashboard_widget_probe_dash_",
	} {
		if !hasPrefixName(names, prefix) {
			t.Errorf("missing %s*; got %#v", prefix, names)
		}
	}
	calls := allCallTargets(res)
	for _, want := range []string{
		"get_probe_meta", "probe_update_meta",
		"probe_render_section", "probe_render_field", "probe_render_dash",
	} {
		if !calls[want] {
			t.Errorf("missing callback edge to %q; got %#v", want, calls)
		}
	}
	// Null submenu callback must not become a call target named "null".
	if calls["null"] {
		t.Errorf("null submenu callback was treated as a callable; got %#v", calls)
	}
}

func hasAnyPrefix(names map[string]bool, prefix string) bool {
	for n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}
