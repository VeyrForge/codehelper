package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseTypeScript_NuxtRouteMiddlewareDensify(t *testing.T) {
	t.Parallel()
	src := []byte(`
export default defineNuxtRouteMiddleware((to) => {
  if (to.path.startsWith("/admin")) {
    return navigateTo("/");
  }
});
`)
	res, err := ParseTypeScript(context.Background(), "repo", "middleware/auth.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var foundSite, foundNav bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "nuxt_mw_") {
			foundSite = true
			if !strings.Contains(s.Signature, "nuxt") {
				t.Errorf("mw signature=%q want nuxt", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=middleware") {
				t.Errorf("mw signature=%q want role=middleware", s.Signature)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":navigateTo") {
			foundNav = true
		}
	}
	if !foundSite {
		t.Fatalf("expected nuxt_mw_* middleware site; symbols=%#v", res.Symbols)
	}
	if !foundNav {
		t.Fatal("expected defineNuxtRouteMiddleware → navigateTo calls edge")
	}
}

func TestParseTypeScript_NuxtPluginDensify(t *testing.T) {
	t.Parallel()
	src := []byte(`
function provideHello(nuxtApp: unknown) {
  return "hi";
}
export default defineNuxtPlugin((nuxtApp) => {
  provideHello(nuxtApp);
});
`)
	res, err := ParseTypeScript(context.Background(), "repo", "plugins/hello.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var foundSite, foundCall bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "nuxt_plugin_") {
			foundSite = true
			if !strings.Contains(s.Signature, "role=plugin") {
				t.Errorf("plugin signature=%q want role=plugin", s.Signature)
			}
			if !strings.Contains(s.Signature, "nuxt") {
				t.Errorf("plugin signature=%q want nuxt", s.Signature)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":provideHello") {
			foundCall = true
		}
	}
	if !foundSite {
		t.Fatalf("expected nuxt_plugin_* site; symbols=%#v", res.Symbols)
	}
	if !foundCall {
		t.Fatal("expected defineNuxtPlugin → provideHello calls edge")
	}
}

func TestParseTypeScript_NuxtServerRoutesAndMiddleware(t *testing.T) {
	t.Parallel()
	routeSrc := []byte(`
function ping() { return { ok: true }; }
export default defineEventHandler(() => ping());
`)
	route, err := ParseTypeScript(context.Background(), "repo", "server/routes/ping.ts", routeSrc)
	if err != nil {
		t.Fatal(err)
	}
	var foundRoute, foundPing bool
	for _, s := range route.Symbols {
		if strings.HasPrefix(s.Name, "nuxt_event_") {
			foundRoute = true
			if !strings.Contains(s.Signature, "role=server_route") {
				t.Errorf("route signature=%q want role=server_route", s.Signature)
			}
		}
	}
	for _, e := range route.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":ping") {
			foundPing = true
		}
	}
	if !foundRoute {
		t.Fatalf("expected nuxt_event_* server_route; symbols=%#v", route.Symbols)
	}
	if !foundPing {
		t.Fatal("expected defineEventHandler → ping calls edge")
	}

	mwSrc := []byte(`
export default defineEventHandler((event) => {
  setHeader(event, "x-log", "1");
});
`)
	mw, err := ParseTypeScript(context.Background(), "repo", "server/middleware/log.ts", mwSrc)
	if err != nil {
		t.Fatal(err)
	}
	var foundMw, foundHeader bool
	for _, s := range mw.Symbols {
		if strings.HasPrefix(s.Name, "nuxt_event_") {
			foundMw = true
			if !strings.Contains(s.Signature, "role=server_middleware") {
				t.Errorf("server mw signature=%q want role=server_middleware", s.Signature)
			}
		}
	}
	for _, e := range mw.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":setHeader") {
			foundHeader = true
		}
	}
	if !foundMw {
		t.Fatalf("expected nuxt_event_* server_middleware; symbols=%#v", mw.Symbols)
	}
	if !foundHeader {
		t.Fatal("expected defineEventHandler → setHeader calls edge")
	}
}

func TestParseTypeScript_NuxtComposableWiring(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { listUsers } from "../lib/users";

export function useUsers() {
  const users = useState("users", () => listUsers());
  async function refresh() {
    users.value = listUsers();
  }
  return { users, refresh };
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "composables/useUsers.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var foundRole, foundList bool
	for _, s := range res.Symbols {
		if s.Name == "useUsers" {
			foundRole = true
			if !strings.Contains(s.Signature, "role=composable") {
				t.Errorf("useUsers signature=%q want role=composable", s.Signature)
			}
			if !strings.Contains(s.Signature, "nuxt") {
				t.Errorf("useUsers signature=%q want nuxt", s.Signature)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":listUsers") {
			foundList = true
		}
	}
	if !foundRole {
		t.Fatalf("missing useUsers; symbols=%#v", res.Symbols)
	}
	if !foundList {
		t.Fatal("expected useUsers → listUsers calls edge")
	}
}

func TestParseTypeScript_NuxtServerAPIUsersWiring(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { listUsers } from "../../../lib/users";

export default defineEventHandler(async () => {
  return listUsers();
});
`)
	res, err := ParseTypeScript(context.Background(), "repo", "server/api/users/index.get.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var foundSite, foundList bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "nuxt_event_") {
			foundSite = true
			if !strings.Contains(s.Signature, "role=server_api") {
				t.Errorf("api signature=%q want role=server_api", s.Signature)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":listUsers") {
			foundList = true
		}
	}
	if !foundSite {
		t.Fatal("expected nuxt_event_* server_api symbol")
	}
	if !foundList {
		t.Fatal("expected defineEventHandler → listUsers calls edge")
	}
}
