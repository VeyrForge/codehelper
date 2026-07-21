package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseKotlinNamesAndCalls(t *testing.T) {
	src := []byte(`
package demo

public interface ApplicationCall {
    public val application: Application
}

public class RoutingNode {
    public fun createChild(selector: RouteSelector): RoutingNode {
        return RoutingNode()
    }
}

public fun Route.route(path: String, build: Route.() -> Unit): Route =
    createRouteFromPath(path).apply(build)

public object Defaults {
    public fun ready() {
        route("/x") {}
    }
}
`)
	res, err := ParseKotlin(context.Background(), "repo", "Routing.kt", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]types.SymbolKind{
		"ApplicationCall": types.SymbolKindInterface,
		"RoutingNode":     types.SymbolKindClass,
		"createChild":     types.SymbolKindFunction,
		"route":           types.SymbolKindFunction,
		"Defaults":        types.SymbolKindClass,
		"ready":           types.SymbolKindFunction,
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
	}
	for name, kind := range want {
		got, ok := found[name]
		if !ok {
			t.Errorf("missing symbol %q; got %v", name, found)
			continue
		}
		if got != kind {
			t.Errorf("symbol %q kind=%q, want %q", name, got, kind)
		}
	}
	if found["Route"] != "" {
		t.Errorf("should not index extension receiver type as function name, found Route")
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected call edges from Kotlin function bodies")
	}
	var callNames []string
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
			callNames = append(callNames, e.TargetID[i+1:])
		}
	}
	joined := strings.Join(callNames, ",")
	for _, wantCall := range []string{"createRouteFromPath", "apply", "route"} {
		if !strings.Contains(joined, wantCall) {
			t.Errorf("expected call to %q in %v", wantCall, callNames)
		}
	}
}
