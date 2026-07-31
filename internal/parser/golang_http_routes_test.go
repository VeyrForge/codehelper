package parser

import (
	"context"
	"strings"
	"testing"
)

func TestParseGo_HTTPRouteHandlers_EchoChiBeego(t *testing.T) {
	t.Parallel()
	src := []byte(`package main

type Echo struct{}
type Mux struct{}
type UserController struct{}

func HealthHandler() {}
func ListUsers() {}
func AuthFilter() {}
func (c *UserController) Get() {}

func routesEcho(e *Echo) {
	e.GET("/health", HealthHandler)
	e.POST("/users", ListUsers)
}

func routesChi(r *Mux) {
	r.Get("/health", HealthHandler)
	r.Method("GET", "/users/{id}", ListUsers)
}

func routesBeego() {
	InsertFilter("/*", 1, AuthFilter)
	Router("/", &UserController{})
	Get("/health", HealthHandler)
}

func cacheGet(c *Mux) {
	c.Get("user:1")
}
`)
	res, err := ParseGo(context.Background(), "repo", "routes.go", src)
	if err != nil {
		t.Fatal(err)
	}

	callsFrom := func(fn string) map[string]bool {
		out := map[string]bool{}
		for _, e := range res.Edges {
			if e.Kind != "calls" || !strings.Contains(e.SourceID, ":"+fn) {
				continue
			}
			if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
				out[e.TargetID[i+1:]] = true
			}
		}
		return out
	}

	echo := callsFrom("routesEcho")
	if !echo["HealthHandler"] || !echo["ListUsers"] {
		t.Fatalf("echo route→handler missing: %#v", echo)
	}
	chi := callsFrom("routesChi")
	if !chi["HealthHandler"] || !chi["ListUsers"] {
		t.Fatalf("chi route→handler missing: %#v", chi)
	}
	beego := callsFrom("routesBeego")
	if !beego["UserController"] || !beego["HealthHandler"] || !beego["AuthFilter"] {
		t.Fatalf("beego route→handler/filter missing: %#v", beego)
	}
	cache := callsFrom("cacheGet")
	if cache["HealthHandler"] || len(cache) > 2 {
		// May still call Get; must not invent HealthHandler.
		for k := range cache {
			if k == "HealthHandler" || k == "ListUsers" || k == "UserController" || k == "AuthFilter" {
				t.Fatalf("cache.Get must not emit HTTP handlers: %#v", cache)
			}
		}
	}
}

func TestParseGo_HTTPRouteHandlers_GinFiber(t *testing.T) {
	t.Parallel()
	src := []byte(`package main

type Engine struct{}
type App struct{}
type Svc struct{}

func Health() {}
func (s *Svc) List() {}

func setupGin(r *Engine, s *Svc) {
	r.GET("/health", Health)
	r.GET("/items", s.List)
}

func setupFiber(app *App) {
	app.Get("/health", Health)
	app.All("/x", Health)
}
`)
	res, err := ParseGo(context.Background(), "repo", "ginfiber.go", src)
	if err != nil {
		t.Fatal(err)
	}
	var health, list int
	for _, e := range res.Edges {
		if e.Kind != "calls" {
			continue
		}
		if strings.Contains(e.SourceID, ":setupGin") || strings.Contains(e.SourceID, ":setupFiber") {
			if strings.HasSuffix(e.TargetID, ":Health") {
				health++
			}
			if strings.HasSuffix(e.TargetID, ":List") {
				list++
			}
		}
	}
	if health < 2 {
		t.Fatalf("expected ≥2 Health handler edges (gin+fiber), got %d", health)
	}
	if list < 1 {
		t.Fatalf("expected gin selector handler List edge, got %d", list)
	}
}

func TestDetectFrameworkPacks_GoHTTP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path, body, want string
	}{
		{"main.go", "package echo\n\ntype Context struct{}", "echo"},
		{"main.go", "package chi\n\ntype Mux struct{}", "chi"},
		{"main.go", "package beego\n\ntype Controller struct{}", "beego"},
		{"main.go", "import \"github.com/labstack/echo/v4\"\n", "echo"},
		{"main.go", "import \"github.com/go-chi/chi/v5\"\n", "chi"},
		{"main.go", "import \"github.com/beego/beego/v2/server/web\"\n", "beego"},
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
			t.Fatalf("%s: want pack %q in %#v", tc.path+"/"+tc.want, tc.want, got)
		}
	}
}
