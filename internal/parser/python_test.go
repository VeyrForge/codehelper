package parser

import (
	"context"
	"strings"
	"testing"
)

func TestParsePython_FrameworkPatterns(t *testing.T) {
	t.Parallel()
	src := []byte(`
from fastapi import FastAPI
app = FastAPI()

@app.get("/users")
def list_users():
    return []

urlpatterns = [path("home/", views.home)]
`)
	res, err := ParsePython(context.Background(), "repo", "api/urls.py", src)
	if err != nil {
		t.Fatalf("parse python: %v", err)
	}
	if len(res.Symbols) == 0 {
		t.Fatal("expected symbols")
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["fastapi_get_5"] {
		t.Fatalf("expected FastAPI decorator symbol, got %#v", res.Symbols)
	}
	if !names["django_path_views_home_9"] {
		t.Fatalf("expected Django path symbol, got %#v", res.Symbols)
	}
	readEdges := 0
	for _, e := range res.Edges {
		if e.Kind == "reads" {
			readEdges++
		}
	}
	if readEdges == 0 {
		t.Fatalf("expected reads edges, got %#v", res.Edges)
	}
}

func TestParsePython_DecoratorCallEdges(t *testing.T) {
	t.Parallel()
	src := []byte(`
from fastapi import Depends, FastAPI
app = FastAPI()

def common_parameters():
    return {}

@app.get("/items")
def read_items(commons=Depends(common_parameters)):
    return commons
`)
	res, err := ParsePython(context.Background(), "repo", "main.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != "calls" {
			continue
		}
		id := e.TargetID
		if i := lastColon(id); i >= 0 {
			calls[id[i+1:]] = true
		}
	}
	// Decorator @app.get should emit a call edge (get), and Depends(...) in the
	// signature/body is a call — at least one of get/Depends should be present
	// from the decorator walk.
	if !calls["get"] && !calls["Depends"] {
		t.Fatalf("expected decorator/Depends call edges, got %#v", calls)
	}
}

func TestParsePython_IncludeRouterModuleEdge(t *testing.T) {
	t.Parallel()
	src := []byte(`
from fastapi import FastAPI, APIRouter
app = FastAPI()
router = APIRouter()

@router.get("/items")
def read_items():
    return []

app.include_router(router)
`)
	res, err := ParsePython(context.Background(), "repo", "main.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var appID string
	for _, s := range res.Symbols {
		if s.Name == "app" {
			appID = s.ID
		}
	}
	if appID == "" {
		t.Fatal("missing app symbol")
	}
	found := false
	for _, e := range res.Edges {
		if e.Kind == "calls" && e.SourceID == appID && strings.HasSuffix(e.TargetID, ":include_router") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected app→include_router edge; edges=%#v", res.Edges)
	}
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
