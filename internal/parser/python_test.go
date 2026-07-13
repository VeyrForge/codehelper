package parser

import (
	"context"
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
