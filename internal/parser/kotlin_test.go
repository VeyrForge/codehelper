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
		"createChild":     types.SymbolKindMethod,
		"route":           types.SymbolKindFunction,
		"Defaults":        types.SymbolKindClass,
		"ready":           types.SymbolKindMethod,
		"application":     types.SymbolKindVariable,
	}
	found := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		found[s.Name] = s
	}
	for name, kind := range want {
		got, ok := found[name]
		if !ok {
			t.Errorf("missing symbol %q; got %v", name, keysOfSym(found))
			continue
		}
		if got.Kind != kind {
			t.Errorf("symbol %q kind=%q, want %q", name, got.Kind, kind)
		}
	}
	if found["Route"].Name != "" {
		t.Errorf("should not index extension receiver type as function name, found Route")
	}
	if found["createChild"].ParentID != "RoutingNode" {
		t.Errorf("createChild ParentID=%q want RoutingNode", found["createChild"].ParentID)
	}
	if found["ready"].ParentID != "Defaults" {
		t.Errorf("ready ParentID=%q want Defaults", found["ready"].ParentID)
	}
	if found["application"].ParentID != "ApplicationCall" {
		t.Errorf("application ParentID=%q want ApplicationCall", found["application"].ParentID)
	}
	if found["route"].ParentID != "" {
		t.Errorf("top-level extension route should have empty ParentID, got %q", found["route"].ParentID)
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

func TestParseKotlinInheritanceAndParentID(t *testing.T) {
	src := []byte(`
package demo
open class Base
interface Auditable
class UserService : Base(), Auditable {
  val repo: UserRepo = UserRepo()
  fun save(u: User): User {
    validate(u)
    return repo.persist(u)
  }
}
`)
	res, err := ParseKotlin(context.Background(), "repo", "UserService.kt", src)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	for _, s := range res.Symbols {
		if s.Name == "UserService" {
			userID = s.ID
		}
		if s.Name == "save" && s.ParentID != "UserService" {
			t.Fatalf("save ParentID=%q want UserService", s.ParentID)
		}
		if s.Name == "repo" && s.ParentID != "UserService" {
			t.Fatalf("repo ParentID=%q want UserService", s.ParentID)
		}
	}
	if userID == "" {
		t.Fatal("missing UserService")
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
	if !sawInherits {
		t.Fatal("expected inherits Base")
	}
	if !sawImplements {
		t.Fatal("expected implements Auditable")
	}
}

func keysOfSym(m map[string]types.Symbol) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
