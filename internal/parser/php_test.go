package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

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
	for _, want := range []string{"baz", "helper", "stat", "maybe"} {
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
        $this->app->bind(LoggerContract::class, FileLogger::class);
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
	for _, want := range []string{"HashManager", "LoggerContract", "FileLogger"} {
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
