package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseTypeScript_DenoServeDensify(t *testing.T) {
	t.Parallel()
	src := []byte(`
function greet(name: string): string {
  return "hi " + name;
}
function handler(req: Request): Response {
  return new Response(greet("deno"));
}
Deno.serve(handler);
`)
	res, err := ParseTypeScript(context.Background(), "repo", "main.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var foundServe, foundHandlerRole, foundCall bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "deno_serve_") {
			foundServe = true
			if !strings.Contains(s.Signature, "deno") {
				t.Errorf("serve signature=%q want frameworks=deno", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("serve signature=%q want role=entrypoint", s.Signature)
			}
		}
		if s.Name == "handler" && strings.Contains(s.Signature, "role=edge_handler") {
			foundHandlerRole = true
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":handler") {
			foundCall = true
		}
	}
	if !foundServe {
		t.Fatal("expected deno_serve_* entrypoint symbol")
	}
	if !foundHandlerRole {
		t.Fatal("expected handler role=edge_handler")
	}
	if !foundCall {
		t.Fatal("expected Deno.serve → handler calls edge")
	}
}

func TestParseTypeScript_BunServeObjectFetch(t *testing.T) {
	t.Parallel()
	src := []byte(`
function greet(name: string): string { return "hi " + name; }
function fetchHandler(req: Request): Response {
  return new Response(greet("bun"));
}
function errorHandler(err: Error): Response {
  return new Response(String(err), { status: 500 });
}
Bun.serve({ fetch: fetchHandler, error: errorHandler });
`)
	res, err := ParseTypeScript(context.Background(), "repo", "index.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var foundServe, foundFetch, foundError, foundFetchRole bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "bun_serve_") {
			foundServe = true
			if !strings.Contains(s.Signature, "bun") {
				t.Errorf("serve signature=%q want bun", s.Signature)
			}
		}
		if s.Name == "fetchHandler" && strings.Contains(s.Signature, "role=edge_handler") {
			foundFetchRole = true
		}
	}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		if strings.HasSuffix(e.TargetID, ":fetchHandler") {
			foundFetch = true
		}
		if strings.HasSuffix(e.TargetID, ":errorHandler") {
			foundError = true
		}
	}
	if !foundServe {
		t.Fatal("expected bun_serve_* entrypoint symbol")
	}
	if !foundFetch {
		t.Fatal("expected Bun.serve → fetchHandler calls edge")
	}
	if !foundError {
		t.Fatal("expected Bun.serve → errorHandler calls edge")
	}
	if !foundFetchRole {
		t.Fatal("expected fetchHandler role=edge_handler")
	}
}

func TestParseTypeScript_NuxtDefineEventHandler(t *testing.T) {
	t.Parallel()
	src := []byte(`
function healthPayload() { return { ok: true }; }
export default defineEventHandler(() => {
  return healthPayload();
});
`)
	res, err := ParseTypeScript(context.Background(), "repo", "server/api/health.get.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var foundSite, foundCall bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "nuxt_event_") {
			foundSite = true
			if !strings.Contains(s.Signature, "nuxt") {
				t.Errorf("event signature=%q want nuxt", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=server_api") {
				t.Errorf("event signature=%q want role=server_api", s.Signature)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":healthPayload") {
			foundCall = true
		}
	}
	if !foundSite {
		t.Fatal("expected nuxt_event_* server_api symbol")
	}
	if !foundCall {
		t.Fatal("expected defineEventHandler → healthPayload calls edge")
	}
}

func TestParseTypeScript_CloudflareWorkerFetchRole(t *testing.T) {
	t.Parallel()
	src := []byte(`
export default {
  async fetch(req: Request, env: unknown, ctx: unknown): Promise<Response> {
    return new Response("ok");
  }
};
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/index.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range res.Symbols {
		if s.Name != "fetch" {
			continue
		}
		if !strings.Contains(s.Signature, "cloudflare_workers") && !strings.Contains(s.Signature, "edge") {
			t.Errorf("fetch signature=%q want cloudflare_workers/edge", s.Signature)
		}
		if !strings.Contains(s.Signature, "role=edge_handler") {
			t.Errorf("fetch signature=%q want role=edge_handler", s.Signature)
		}
		found = true
	}
	if !found {
		t.Fatal("expected fetch method with edge_handler role")
	}
}

func TestDetectFrameworkPacks_DenoBunEdge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path, body, want string
	}{
		{"deno.json", `{"tasks":{}}`, "deno"},
		{"main.ts", "Deno.serve(() => new Response('ok'));", "deno"},
		{"bun.lockb", "stub", "bun"},
		{"index.ts", "Bun.serve({ port: 3000 });", "bun"},
		{"wrangler.toml", "name='x'", "cloudflare_workers"},
		{"edge.ts", `export const runtime = "edge";`, "edge"},
	}
	for _, tc := range cases {
		got := DetectFrameworkPacks(tc.path, nil, tc.body)
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("path %q expected %q, got %v", tc.path, tc.want, got)
		}
	}
}

func TestDetectFrameworkPacks_DenoMainNoElectronBleed(t *testing.T) {
	t.Parallel()
	got := DetectFrameworkPacks("main.ts", nil, "Deno.serve(() => new Response('ok'));")
	if !containsFramework(got, "deno") {
		t.Fatalf("want deno, got %v", got)
	}
	if containsFramework(got, "electron") {
		t.Fatalf("host bleed: electron on Deno main.ts, got %v", got)
	}
}
