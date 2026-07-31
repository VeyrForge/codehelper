package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
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
	if !hasTarget(res, "list_users") {
		t.Fatalf("expected fastapi route->list_users edge, calls=%v", callTargets(res))
	}
	if !hasTarget(res, "home") {
		t.Fatalf("expected django path->home edge, calls=%v", callTargets(res))
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
	if !hasTarget(res, "get") && !hasTarget(res, "Depends") {
		t.Fatalf("expected decorator/Depends call edges, got %#v", callTargets(res))
	}
	if !hasTarget(res, "common_parameters") {
		t.Fatalf("expected Depends->common_parameters edge, got %#v", callTargets(res))
	}
	if !hasTarget(res, "app.get") {
		t.Fatalf("expected app.get alias edge, got %#v", callTargets(res))
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
		t.Fatalf("expected app->include_router edge; edges=%#v", res.Edges)
	}
	if !hasTarget(res, "router") {
		t.Fatalf("expected include_router->router arg edge, calls=%v", callTargets(res))
	}
}

func TestParsePython_FlaskRouteToView(t *testing.T) {
	t.Parallel()
	src := []byte(`
class Flask:
    def route(self, rule, **options):
        def deco(fn):
            return fn
        return deco

app = Flask()

class UserService:
    @staticmethod
    def list_users():
        return []

@app.route("/users")
def list_users():
    return UserService.list_users()
`)
	res, err := ParsePython(context.Background(), "repo", "app.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
		if s.Name == "list_users" && s.Kind == types.SymbolKindMethod && s.ParentID == "UserService" {
			names["UserService.list_users.method"] = true
		}
	}
	if !names["Flask"] || !hasPythonSymbolPrefixed(res, "flask_route_") {
		t.Fatalf("expected Flask + flask_route symbol, got %#v", res.Symbols)
	}
	if !names["UserService.list_users.method"] {
		t.Fatalf("expected UserService.list_users method ParentID, got %#v", res.Symbols)
	}
	if !hasTarget(res, "list_users") || !hasTarget(res, "route") || !hasTarget(res, "app.route") {
		t.Fatalf("expected flask route->list_users edges, got %#v", callTargets(res))
	}
	if !hasTarget(res, "UserService.list_users") {
		t.Fatalf("expected view->service typed call, got %#v", callTargets(res))
	}
}

func TestParsePython_DjangoRESTViewSet(t *testing.T) {
	t.Parallel()
	src := []byte(`
class APIView:
    pass

class ViewSet(APIView):
    pass

class UserViewSet(ViewSet):
    def list(self, request):
        return UserService.list_all()

class UserService:
    @staticmethod
    def list_all():
        return []

router = DefaultRouter()
router.register("users", UserViewSet)
urlpatterns = [path("users/", UserViewSet.as_view({"get": "list"}))]
`)
	res, err := ParsePython(context.Background(), "repo", "app/urls.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var viewSet *types.Symbol
	var listMeth *types.Symbol
	for i := range res.Symbols {
		s := &res.Symbols[i]
		switch {
		case s.Name == "UserViewSet" && s.Kind == types.SymbolKindClass:
			viewSet = s
		case s.Name == "list" && s.ParentID == "UserViewSet":
			listMeth = s
		}
	}
	if viewSet == nil {
		t.Fatalf("missing UserViewSet, symbols=%#v", res.Symbols)
	}
	if listMeth == nil {
		t.Fatalf("missing UserViewSet.list method, symbols=%#v", res.Symbols)
	}
	if !hasTarget(res, "APIView") || !hasTarget(res, "ViewSet") {
		t.Fatalf("expected class base edges, got %#v", callTargets(res))
	}
	if !hasTarget(res, "UserViewSet") {
		t.Fatalf("expected router/path->UserViewSet edges, got %#v", callTargets(res))
	}
	if !hasPythonSymbolPrefixed(res, "drf_register_") {
		t.Fatalf("expected drf_register symbol, got %#v", res.Symbols)
	}
	if !hasTarget(res, "UserService.list_all") {
		t.Fatalf("expected list->UserService.list_all, got %#v", callTargets(res))
	}
}

func TestParsePython_FromImportNameEdges(t *testing.T) {
	t.Parallel()
	src := []byte("from fastapi import Depends, FastAPI\n")
	res, err := ParsePython(context.Background(), "repo", "main.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	foundDepends, foundFastAPI, foundMod := false, false, false
	for _, e := range res.Edges {
		if e.Kind != "imports" {
			continue
		}
		if strings.HasSuffix(e.TargetID, ":Depends") {
			foundDepends = true
		}
		if strings.HasSuffix(e.TargetID, ":FastAPI") {
			foundFastAPI = true
		}
		if strings.Contains(e.TargetID, "mod:") && strings.HasSuffix(e.TargetID, ":fastapi") {
			foundMod = true
		}
	}
	if !foundDepends || !foundFastAPI || !foundMod {
		t.Fatalf("expected from-import edges for Depends/FastAPI/fastapi mod; edges=%#v", res.Edges)
	}
}

func hasPythonSymbolPrefixed(res *ParseResult, prefix string) bool {
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, prefix) {
			return true
		}
	}
	return false
}
