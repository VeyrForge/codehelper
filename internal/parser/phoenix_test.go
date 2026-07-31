package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseElixir_PhoenixControllerLiveViewRoutes(t *testing.T) {
	router := []byte(`
defmodule DemoWeb.Router do
  use DemoWeb, :router

  pipeline :browser do
    plug :accepts
    plug :fetch_session
  end

  scope "/", DemoWeb do
    pipe_through :browser
    get "/", PageController, :index
    post "/users", UserController, :create
    live "/dashboard", DashboardLive, :index
    resources "/posts", PostController
  end
end
`)
	res, err := ParseElixir(context.Background(), "p", "lib/demo_web/router.ex", router)
	if err != nil {
		t.Fatal(err)
	}
	var routerMod *types.Symbol
	var sawGet, sawLive, sawResources, sawPlug bool
	for i := range res.Symbols {
		s := &res.Symbols[i]
		switch {
		case s.Name == "DemoWeb.Router":
			routerMod = s
		case strings.HasPrefix(s.Name, "phoenix_get_pagecontroller_index_"):
			sawGet = true
			if !strings.Contains(s.Signature, "frameworks=phoenix") {
				t.Errorf("route site missing phoenix: %q", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("route site missing entrypoint: %q", s.Signature)
			}
		case strings.HasPrefix(s.Name, "phoenix_live_dashboardlive_"):
			sawLive = true
		case strings.HasPrefix(s.Name, "phoenix_resources_postcontroller_"):
			sawResources = true
		case strings.HasPrefix(s.Name, "phoenix_plug_"):
			sawPlug = true
		}
	}
	if routerMod == nil {
		t.Fatalf("missing DemoWeb.Router; got %v", phoenixSymNames(res))
	}
	if !strings.Contains(routerMod.Signature, "frameworks=phoenix") {
		t.Errorf("Router signature=%q", routerMod.Signature)
	}
	if !strings.Contains(routerMod.Signature, "role=router") {
		t.Errorf("Router missing router role: %q", routerMod.Signature)
	}
	if !sawGet || !sawLive || !sawResources {
		t.Errorf("expected route sites get/live/resources; symbols=%v", phoenixSymNames(res))
	}
	if !sawPlug {
		t.Errorf("expected plug filter sites; symbols=%v", phoenixSymNames(res))
	}

	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
			targets[e.TargetID[i+1:]] = true
		}
	}
	for _, want := range []string{"PageController", "index", "DashboardLive", "UserController", "PostController", "accepts", "fetch_session"} {
		if !targets[want] {
			t.Errorf("router missing call to %q; got %#v", want, targets)
		}
	}

	ctrl := []byte(`
defmodule DemoWeb.PageController do
  use DemoWeb, :controller

  def index(conn, _params) do
    render(conn, :index)
  end
end
`)
	cres, err := ParseElixir(context.Background(), "p", "lib/demo_web/controllers/page_controller.ex", ctrl)
	if err != nil {
		t.Fatal(err)
	}
	var pageCtrl, indexFn *types.Symbol
	for i := range cres.Symbols {
		s := &cres.Symbols[i]
		switch {
		case s.Name == "DemoWeb.PageController":
			pageCtrl = s
		case s.Name == "index":
			indexFn = s
		}
	}
	if pageCtrl == nil || !strings.Contains(pageCtrl.Signature, "role=controller") {
		t.Errorf("PageController signature=%v", pageCtrl)
	}
	if !strings.Contains(pageCtrl.Signature, "frameworks=phoenix") {
		t.Errorf("PageController missing phoenix: %v", pageCtrl)
	}
	if indexFn == nil || indexFn.ParentID != "DemoWeb.PageController" {
		t.Errorf("index ParentID=%v", indexFn)
	}
	if indexFn != nil && !strings.Contains(indexFn.Signature, "role=entrypoint") {
		t.Errorf("index missing entrypoint: %q", indexFn.Signature)
	}

	live := []byte(`
defmodule DemoWeb.DashboardLive do
  use DemoWeb, :live_view

  def mount(_params, _session, socket) do
    {:ok, assign(socket, :count, 0)}
  end

  def handle_event("inc", _params, socket) do
    {:noreply, update(socket, :count, &(&1 + 1))}
  end

  def render(assigns) do
    ~H"<div>{@count}</div>"
  end
end
`)
	lres, err := ParseElixir(context.Background(), "p", "lib/demo_web/live/dashboard_live.ex", live)
	if err != nil {
		t.Fatal(err)
	}
	var liveMod, handleEvent *types.Symbol
	for i := range lres.Symbols {
		s := &lres.Symbols[i]
		switch {
		case s.Name == "DemoWeb.DashboardLive":
			liveMod = s
		case s.Name == "handle_event":
			handleEvent = s
		}
	}
	if liveMod == nil || !strings.Contains(liveMod.Signature, "role=live_view") {
		t.Errorf("DashboardLive signature=%v", liveMod)
	}
	if handleEvent == nil || !strings.Contains(handleEvent.Signature, "role=entrypoint") {
		t.Errorf("handle_event signature=%v", handleEvent)
	}
}

func TestDetectFrameworkPacks_Phoenix(t *testing.T) {
	t.Parallel()
	got := DetectFrameworkPacks("lib/demo_web/router.ex", nil, `
defmodule DemoWeb.Router do
  use DemoWeb, :router
  get "/", PageController, :index
end
`)
	found := false
	for _, g := range got {
		if g == "phoenix" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected phoenix, got %v", got)
	}
}

func phoenixSymNames(res *ParseResult) []string {
	out := make([]string, 0, len(res.Symbols))
	for _, s := range res.Symbols {
		out = append(out, s.Name)
	}
	return out
}
