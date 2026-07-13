package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParsePHP_FrameworkPatterns(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
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
	if !names["route_get_2"] {
		t.Fatalf("expected Laravel route symbol, got %#v", res.Symbols)
	}
	if !names["boot_plugin"] {
		t.Fatalf("expected WordPress callback symbol, got %#v", res.Symbols)
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
