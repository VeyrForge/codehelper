package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParsePHP_UseAliasExtendsAndThisRecv(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Models;
use Illuminate\Foundation\Auth\User as Authenticatable;
class User extends Authenticatable {
    public function id() { return $this->load(); }
    public function load() { return 1; }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Models/User.php", src)
	if err != nil {
		t.Fatal(err)
	}
	var userID, idID string
	for _, s := range res.Symbols {
		switch s.Name {
		case "User":
			userID = s.ID
		case "id":
			idID = s.ID
		}
	}
	if userID == "" || idID == "" {
		t.Fatalf("missing symbols: %#v", res.Symbols)
	}
	sawInheritsUser := false
	for _, e := range res.Edges {
		if e.SourceID == userID && e.Kind == types.RefKindInherits && symrefName(e.TargetID) == "User" {
			sawInheritsUser = true
		}
	}
	if !sawInheritsUser {
		t.Fatalf("expected extends Authenticatable → User leaf; edges=%#v", res.Edges)
	}
	sawThis := false
	for _, e := range res.Edges {
		if e.SourceID == idID && e.Kind == types.RefKindCalls && symrefName(e.TargetID) == "User.load" {
			sawThis = true
		}
	}
	if !sawThis {
		t.Fatalf("expected $this->load → User.load; edges=%#v", res.Edges)
	}
}

func TestParsePHP_UseImportEdges(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Models;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Foundation\Auth\User as Authenticatable;
use App\Http\Controllers\Controller;
class User extends Authenticatable {
    use HasFactory;
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Models/User.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		// TargetID is mod:repo:<name>
		id := e.TargetID
		if i := strings.LastIndex(id, ":"); i >= 0 {
			imports[id[i+1:]] = true
		}
	}
	for _, want := range []string{
		`Illuminate\Database\Eloquent\Factories\HasFactory`,
		`Illuminate\Foundation\Auth\User`,
		`App\Http\Controllers\Controller`,
		`HasFactory`,
	} {
		if !imports[want] {
			t.Errorf("missing imports edge for %q; got %#v", want, imports)
		}
	}
	// Segment-only names must not appear as imports.
	for _, bad := range []string{"Illuminate", "Database", "Eloquent", "Factories", "Foundation", "Auth"} {
		if imports[bad] {
			t.Errorf("segment %q should not be an import edge", bad)
		}
	}
}

func TestParsePHP_FrameworkPatterns(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
use App\Http\Controllers\UserController;
Route::get('/users', [UserController::class, 'index']);
add_action('init', 'boot_plugin');
function boot_plugin() {}
`)
	res, err := ParsePHP(context.Background(), "repo", "routes/web.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	if len(res.Symbols) == 0 {
		t.Fatal("expected symbols")
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["route_get_3"] {
		t.Fatalf("expected Laravel route symbol, got %#v", res.Symbols)
	}
	if !names["Route"] {
		t.Fatalf("expected Route facade symbol, got %#v", res.Symbols)
	}
	if !names["boot_plugin"] {
		t.Fatalf("expected WordPress callback symbol, got %#v", res.Symbols)
	}
	var wpSite string
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "wp_add_action_init_") {
			wpSite = s.ID
			break
		}
	}
	if wpSite == "" {
		t.Fatalf("expected wp_add_action_init_* entrypoint, got %#v", res.Symbols)
	}
	wpCalls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == wpSite {
			wpCalls[symrefName(e.TargetID)] = true
		}
	}
	if !wpCalls["boot_plugin"] {
		t.Errorf("WP hook site missing call to boot_plugin; got %#v", wpCalls)
	}
	var routeID string
	for _, s := range res.Symbols {
		if s.Name == "route_get_3" {
			routeID = s.ID
		}
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == routeID {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Route", "UserController", "index"} {
		if !calls[want] {
			t.Errorf("route missing call to %q; got %#v", want, calls)
		}
	}
}

func TestParsePHP_LaravelBootstrapAndFormRequest(t *testing.T) {
	t.Parallel()
	boot := []byte(`<?php
use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Middleware;
return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__.'/../routes/web.php',
    )
    ->withMiddleware(function (Middleware $middleware): void {
    })->create();
`)
	res, err := ParsePHP(context.Background(), "repo", "bootstrap/app.php", boot)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["Application"] {
		t.Fatalf("expected Application bootstrap card, got %#v", names)
	}
	var sawWithRouting bool
	for n := range names {
		if strings.HasPrefix(n, "boot_withrouting_") {
			sawWithRouting = true
		}
	}
	if !sawWithRouting {
		t.Fatalf("expected withRouting entrypoint, got %#v", names)
	}

	form := []byte(`<?php
namespace App\Http\Requests;
use Illuminate\Foundation\Http\FormRequest;
class StoreUserRequest extends FormRequest {
    public function rules() { return []; }
}
`)
	fres, err := ParsePHP(context.Background(), "repo", "app/Http/Requests/StoreUserRequest.php", form)
	if err != nil {
		t.Fatal(err)
	}
	var reqID string
	for _, s := range fres.Symbols {
		if s.Name == "StoreUserRequest" {
			reqID = s.ID
		}
	}
	if reqID == "" {
		t.Fatal("missing StoreUserRequest")
	}
	found := false
	for _, e := range fres.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == reqID && symrefName(e.TargetID) == "FormRequest" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected StoreUserRequest→FormRequest edge; edges=%#v", fres.Edges)
	}
}

func TestParsePHP_InheritanceEdges(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App;
interface Auditable {}
class Base {}
class User extends Base implements Auditable {
    public function id() { return 1; }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/User.php", src)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	for _, s := range res.Symbols {
		if s.Name == "User" {
			userID = s.ID
		}
		if s.Name == "id" && s.ParentID != "User" {
			t.Fatalf("method id ParentID=%q want User", s.ParentID)
		}
	}
	if userID == "" {
		t.Fatal("missing User class")
	}
	var sawInherits, sawImplements bool
	for _, e := range res.Edges {
		if e.SourceID != userID {
			continue
		}
		if e.Kind == types.RefKindInherits && strings.HasSuffix(e.TargetID, ":Base") {
			sawInherits = true
		}
		if e.Kind == types.RefKindImplements && strings.HasSuffix(e.TargetID, ":Auditable") {
			sawImplements = true
		}
	}
	if !sawInherits || !sawImplements {
		t.Fatalf("inherits=%v implements=%v edges=%#v", sawInherits, sawImplements, res.Edges)
	}
}

func TestParsePHP_TraitUsesAndParentID(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
trait HasFactory {}
trait Notifiable {}
class User {
    use HasFactory, Notifiable;
    public function id() { return 1; }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/User.php", src)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	var sawTrait bool
	for _, s := range res.Symbols {
		if s.Name == "User" {
			userID = s.ID
		}
		if s.Name == "HasFactory" {
			sawTrait = true
		}
		if s.Name == "id" && s.ParentID != "User" {
			t.Fatalf("id ParentID=%q want User", s.ParentID)
		}
	}
	if userID == "" || !sawTrait {
		t.Fatalf("missing User/HasFactory; symbols=%#v", res.Symbols)
	}
	traits := map[string]bool{}
	for _, e := range res.Edges {
		if e.SourceID == userID && e.Kind == types.RefKindImplements {
			traits[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"HasFactory", "Notifiable"} {
		if !traits[want] {
			t.Errorf("missing trait implements %q; got %#v", want, traits)
		}
	}
	for _, s := range res.Symbols {
		if s.Name == "User" && !strings.Contains(s.Signature, "embeds=") {
			t.Fatalf("User signature should include embeds=, got %q", s.Signature)
		}
		if s.Name == "User" {
			if !strings.Contains(s.Signature, "HasFactory") || !strings.Contains(s.Signature, "Notifiable") {
				t.Fatalf("User embeds missing traits: %q", s.Signature)
			}
		}
	}
}

func TestParsePHP_TraitAliasAndEmbeds(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Models;
use Illuminate\Database\Eloquent\Factories\HasFactory as Factory;
class User {
    use Factory;
    public function boot() { $this->newFactory(); }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Models/User.php", src)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	for _, s := range res.Symbols {
		if s.Name == "User" {
			userID = s.ID
			if !strings.Contains(s.Signature, "embeds=HasFactory") && !strings.Contains(s.Signature, "HasFactory") {
				t.Fatalf("expected embeds remapped to HasFactory, got %q", s.Signature)
			}
		}
	}
	if userID == "" {
		t.Fatal("missing User")
	}
	saw := false
	for _, e := range res.Edges {
		if e.SourceID == userID && e.Kind == types.RefKindImplements && symrefName(e.TargetID) == "HasFactory" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected implements HasFactory via alias Factory; edges=%#v", res.Edges)
	}
}

func TestParsePHP_CallEdges(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App;
function helper($x) { return strlen($x); }
class Foo {
    public function bar() {
        $this->baz();
        helper(1);
        Other::stat();
        $obj?->maybe();
        \App\helper(2);
    }
    public function baz() {}
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Foo.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Foo.baz", "helper", "stat", "maybe"} {
		if !calls[want] {
			t.Fatalf("expected PHP calls edge to %q, got call targets %#v", want, calls)
		}
	}
}

// symrefName extracts the trailing identifier from a `symref:repo:path:name`.
func symrefName(target string) string {
	i := strings.LastIndex(target, ":")
	if i < 0 {
		return target
	}
	return target[i+1:]
}

func TestParsePHP_FacadeConcreteAndBinds(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
class AppServiceProvider {
    public function register() {
        Hash::make('secret');
        Crypt::encrypt('x');
        Cache::get('k');
        $this->app->bind(LoggerContract::class, FileLogger::class);
        $this->app->singleton(MailerContract::class, SmtpMailer::class);
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Providers/AppServiceProvider.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	seen := map[string]bool{}
	for _, edge := range res.Edges {
		if edge.Kind == types.RefKindCalls {
			seen[symrefName(edge.TargetID)] = true
		}
	}
	for _, want := range []string{
		"HashManager", "HashManager.make",
		"Encrypter", "Encrypter.encrypt",
		"CacheManager", "CacheManager.get",
		"LoggerContract", "FileLogger",
		"MailerContract", "SmtpMailer",
	} {
		if !seen[want] {
			t.Errorf("missing call to %q: %#v", want, seen)
		}
	}
	var bind bool
	for _, sym := range res.Symbols {
		if strings.HasPrefix(sym.Name, "laravel_bind_") {
			bind = true
		}
	}
	if !bind {
		t.Fatalf("missing Laravel bind symbol: %#v", res.Symbols)
	}
}

func TestParsePHP_TypedPropertyMethodCall(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
class OrderController {
    public function __construct(private Logger $logger) {}

    public function store() {
        $this->logger->info('ok');
    }

    public function create(OrderService $orders) {
        $orders->persist();
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Http/Controllers/OrderController.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	var storeID, createID string
	for _, s := range res.Symbols {
		if s.ParentID != "OrderController" {
			continue
		}
		switch s.Name {
		case "store":
			storeID = s.ID
		case "create":
			createID = s.ID
		}
	}
	if storeID == "" || createID == "" {
		t.Fatalf("missing store/create; symbols=%#v", res.Symbols)
	}
	seen := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == storeID {
			seen[symrefName(e.TargetID)] = true
		}
	}
	if !seen["Logger.info"] {
		t.Errorf("missing typed Logger.info call; got %#v", seen)
	}
	createSeen := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == createID {
			createSeen[symrefName(e.TargetID)] = true
		}
	}
	if !createSeen["OrderService.persist"] {
		t.Errorf("missing typed OrderService.persist call; got %#v", createSeen)
	}
}

func TestParsePHP_WordPressHooksDensify(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
/**
 * Plugin Name: Probe Plugin
 * Version: 1.0.0
 */
if (!defined('ABSPATH')) exit;

class ProbePlugin {
    public function boot() {}
    public function render_box($atts) { return ''; }
    public function get_item($req) { return []; }
}

function probe_activate() {}

add_action('init', [ProbePlugin::class, 'boot']);
add_filter('the_content', 'probe_filter_content', 10, 1);
function probe_filter_content($c) { return $c; }
do_action('init');
apply_filters('the_content', 'x');
register_activation_hook(__FILE__, 'probe_activate');
add_shortcode('probe_box', [ProbePlugin::class, 'render_box']);

// Multi-line live shape + REST route densify.
add_action(
    'rest_api_init',
    [ProbePlugin::class, 'boot']
);
register_rest_route('probe/v1', '/item', [
    'methods' => 'GET',
    'callback' => [ProbePlugin::class, 'get_item'],
]);
`)
	res, err := ParsePHP(context.Background(), "repo", "wp-content/plugins/probe/probe.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	for _, prefix := range []string{"wp_add_action_init_", "wp_add_filter_the_content_", "wp_shortcode_probe_box_", "wp_register_activation_", "wp_add_action_rest_api_init_", "wp_rest_route_"} {
		if !hasPrefixName(names, prefix) {
			t.Errorf("missing %s*; got %#v", prefix, names)
		}
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"boot", "ProbePlugin", "probe_filter_content", "probe_activate", "render_box", "add_shortcode", "do_action", "apply_filters", "register_rest_route", "get_item"} {
		if !calls[want] {
			t.Errorf("missing WP call to %q; got %#v", want, calls)
		}
	}
	fw := DetectFrameworkPacks("wp-content/plugins/probe/probe.php", nil, string(src))
	found := false
	for _, f := range fw {
		if f == "wordpress" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected wordpress framework pack, got %v", fw)
	}
}

func TestParsePHP_LaravelAppEloquentAndView(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Models;
use Illuminate\Database\Eloquent\Model;
class User extends Model {
    public function orders() {
        return $this->hasMany(Order::class);
    }
    public static function active() {
        return User::where('active', 1)->get();
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Models/User.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Order", "User.where", "where"} {
		if !calls[want] {
			t.Errorf("missing eloquent densify call %q; got %#v", want, calls)
		}
	}

	ctrl := []byte(`<?php
namespace App\Http\Controllers;
class ProfileController {
    public function show() {
        return view('users.profile');
    }
}
`)
	cres, err := ParsePHP(context.Background(), "repo", "app/Http/Controllers/ProfileController.php", ctrl)
	if err != nil {
		t.Fatal(err)
	}
	ccalls := map[string]bool{}
	for _, e := range cres.Edges {
		if e.Kind == types.RefKindCalls {
			ccalls[symrefName(e.TargetID)] = true
		}
	}
	if !ccalls["users.profile"] {
		t.Errorf("missing view densify to users.profile; got %#v", ccalls)
	}
}

func TestParsePHP_LaravelRouteMiddlewareGroup(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
use App\Http\Controllers\UserController;
Route::middleware('auth')->group(function () {
    Route::get('/users', [UserController::class, 'index']);
});
Route::prefix('api')->group(function () {
    Route::post('/users', [UserController::class, 'store']);
});
`)
	res, err := ParsePHP(context.Background(), "repo", "routes/web.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	for _, want := range []string{"route_middleware_3", "route_get_4", "route_prefix_6", "route_post_7", "Route"} {
		if !names[want] {
			t.Errorf("missing %s; got %#v", want, names)
		}
	}
	var mwID string
	for _, s := range res.Symbols {
		if s.Name == "route_middleware_3" {
			mwID = s.ID
		}
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == mwID {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Route", "Router", "Router.middleware"} {
		if !calls[want] {
			t.Errorf("middleware site missing call to %q; got %#v", want, calls)
		}
	}
}

func hasPrefixName(names map[string]bool, prefix string) bool {
	for n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}
