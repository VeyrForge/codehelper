package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDetectFrameworkPacks_SymfonyFastifyHono(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		body string
		want string
	}{
		{
			"src/Controller/UserController.php",
			"<?php\nuse Symfony\\Bundle\\FrameworkBundle\\Controller\\AbstractController;\n#[Route('/x')]\nclass UserController extends AbstractController {}\n",
			"symfony",
		},
		{
			"src/app.ts",
			"import Fastify from \"fastify\";\nconst app = Fastify();\napp.get('/', async () => ({}));\n",
			"fastify",
		},
		{
			"src/index.ts",
			"import { Hono } from \"hono\";\nconst app = new Hono();\napp.get('/', (c) => c.text('ok'));\n",
			"hono",
		},
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
			t.Fatalf("path %q expected framework %q, got %v", tc.path, tc.want, got)
		}
	}
	// Express markers must not become Fastify/Hono.
	ex := DetectFrameworkPacks("index.js", nil, "const express = require('express');\nconst app = express();\napp.get('/', fn);\n")
	for _, g := range ex {
		if g == "fastify" || g == "hono" {
			t.Fatalf("express file must not tag %s: %v", g, ex)
		}
	}
}

func TestParsePHP_SymfonyRouteAndCtorDI(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "symfony")
	src, err := os.ReadFile(filepath.Join(root, "src", "Controller", "UserController.php"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParsePHP(context.Background(), "s", "src/Controller/UserController.php", src)
	if err != nil {
		t.Fatal(err)
	}

	var ctrl, show *types.Symbol
	var routeSite bool
	for i := range res.Symbols {
		s := &res.Symbols[i]
		switch {
		case s.Name == "UserController" && s.Kind == types.SymbolKindClass:
			ctrl = s
		case s.Name == "show" && s.Kind == types.SymbolKindMethod:
			show = s
		case strings.HasPrefix(s.Name, "symfony_route_"):
			routeSite = true
			if !strings.Contains(s.Signature, "frameworks=symfony") {
				t.Errorf("route site missing symfony pack: %q", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("route site missing entrypoint: %q", s.Signature)
			}
		}
	}
	if ctrl == nil {
		t.Fatalf("missing UserController; symbols=%v", symbolNames(res))
	}
	if !strings.Contains(ctrl.Signature, "frameworks=symfony") {
		t.Errorf("UserController missing symfony: %q", ctrl.Signature)
	}
	if !strings.Contains(ctrl.Signature, "role=controller") {
		t.Errorf("UserController missing controller role: %q", ctrl.Signature)
	}
	if show == nil || !strings.Contains(show.Signature, "role=entrypoint") {
		t.Errorf("show missing entrypoint role: %#v", show)
	}
	if !routeSite {
		t.Errorf("expected symfony_route_* site; got %v", symbolNames(res))
	}

	callsFrom := map[string][]string{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		callsFrom[e.SourceID] = append(callsFrom[e.SourceID], symrefName(e.TargetID))
	}
	ctrlCalls := strings.Join(callsFrom[ctrl.ID], ",")
	if !strings.Contains(ctrlCalls, "UserService") {
		t.Errorf("UserController ctor DI missing UserService; got %v", callsFrom[ctrl.ID])
	}
	var sawRouteShow bool
	for id, tgts := range callsFrom {
		_ = id
		if strings.Contains(strings.Join(tgts, ","), "show") {
			for _, s := range res.Symbols {
				if s.ID == id && strings.HasPrefix(s.Name, "symfony_route_") {
					sawRouteShow = true
				}
			}
		}
	}
	if !sawRouteShow {
		t.Error("expected symfony_route_* → show call edge")
	}
}

func TestParseTypeScript_FastifyRouteDensify(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "fastify")
	src, err := os.ReadFile(filepath.Join(root, "src", "app.ts"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseTypeScript(context.Background(), "f", "src/app.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var getSite, listUsersEdge, getUserEdge bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "fastify_get_") {
			getSite = true
			if !strings.Contains(s.Signature, "frameworks=fastify") {
				t.Errorf("fastify site missing pack: %q", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("fastify site missing entrypoint: %q", s.Signature)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		switch symrefName(e.TargetID) {
		case "listUsers":
			listUsersEdge = true
		case "getUser":
			getUserEdge = true
		}
	}
	if !getSite {
		t.Fatalf("expected fastify_get_* sites; symbols=%v", symbolNames(res))
	}
	if !listUsersEdge {
		t.Error("expected app.get → listUsers call edge")
	}
	if !getUserEdge {
		t.Error("expected inline handler → getUser call edge")
	}
	got := DetectFrameworkPacks("src/app.ts", nil, string(src))
	found := false
	for _, g := range got {
		if g == "fastify" {
			found = true
		}
		if g == "express" {
			t.Fatalf("fastify bed must not tag express: %v", got)
		}
	}
	if !found {
		t.Fatalf("DetectFrameworkPacks missing fastify: %v", got)
	}
}

func TestParseTypeScript_HonoRouteDensify(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "hono")
	src, err := os.ReadFile(filepath.Join(root, "src", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseTypeScript(context.Background(), "h", "src/index.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var getSite, listUsersEdge, greetEdge bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "hono_get_") {
			getSite = true
			if !strings.Contains(s.Signature, "frameworks=hono") {
				t.Errorf("hono site missing pack: %q", s.Signature)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		switch symrefName(e.TargetID) {
		case "listUsers":
			listUsersEdge = true
		case "greet":
			greetEdge = true
		}
	}
	if !getSite {
		t.Fatalf("expected hono_get_* sites; symbols=%v", symbolNames(res))
	}
	if !listUsersEdge || !greetEdge {
		t.Errorf("expected listUsers+greet edges; listUsers=%v greet=%v", listUsersEdge, greetEdge)
	}
	got := DetectFrameworkPacks("src/index.ts", nil, string(src))
	found := false
	for _, g := range got {
		if g == "hono" {
			found = true
		}
		if g == "express" {
			t.Fatalf("hono bed must not tag express: %v", got)
		}
	}
	if !found {
		t.Fatalf("DetectFrameworkPacks missing hono: %v", got)
	}
}

func TestParseTypeScript_FastifySkipsLifecycleAsEntrypoint(t *testing.T) {
	t.Parallel()
	src := []byte(`
import Fastify from "fastify";
const app = Fastify();
app.use(mw);
app.listen(3000);
app.register(plugin);
app.on("ready", fn);
app.get("/", handler);
`)
	res, err := ParseTypeScript(context.Background(), "f", "src/life.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	var getOK bool
	for _, s := range res.Symbols {
		if !strings.HasPrefix(s.Name, "fastify_") {
			continue
		}
		switch {
		case strings.HasPrefix(s.Name, "fastify_get_"):
			getOK = true
		case strings.HasPrefix(s.Name, "fastify_use_"),
			strings.HasPrefix(s.Name, "fastify_listen_"),
			strings.HasPrefix(s.Name, "fastify_register_"),
			strings.HasPrefix(s.Name, "fastify_on_"):
			t.Errorf("lifecycle call must not be entrypoint site: %s", s.Name)
		}
	}
	if !getOK {
		t.Fatalf("expected fastify_get_* site; symbols=%v", symbolNames(res))
	}
}

func TestParsePHP_SymfonyAutowireCtorDI(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Controller;
use App\Service\UserService;
use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\DependencyInjection\Attribute\Autowire;
use Symfony\Component\Routing\Attribute\Route;

class UserController extends AbstractController
{
    public function __construct(
        #[Autowire("%app.secret%")]
        private UserService $users
    ) {
    }

    #[Route("/users/{id}")]
    public function show(int $id): array
    {
        return $this->users->find($id);
    }
}
`)
	res, err := ParsePHP(context.Background(), "s", "src/Controller/UserController.php", src)
	if err != nil {
		t.Fatal(err)
	}
	var ctrl *types.Symbol
	for i := range res.Symbols {
		s := &res.Symbols[i]
		if s.Name == "UserController" && s.Kind == types.SymbolKindClass {
			ctrl = s
			break
		}
	}
	if ctrl == nil {
		t.Fatalf("missing UserController; symbols=%v", symbolNames(res))
	}
	found := false
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != ctrl.ID {
			continue
		}
		if symrefName(e.TargetID) == "UserService" {
			found = true
		}
	}
	if !found {
		t.Error("UserController ctor DI missing UserService when #[Autowire(...)] nests parens")
	}
}

func TestParsePHP_SymfonyClassLevelRouteNotEntrypoint(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\Routing\Attribute\Route;

#[Route("/api")]
class ApiController extends AbstractController
{
    #[Route("/users")]
    public function list(): array
    {
        return [];
    }
}
`)
	res, err := ParsePHP(context.Background(), "s", "src/Controller/ApiController.php", src)
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "symfony_route_") {
			sites = append(sites, s.Name)
		}
	}
	if len(sites) != 1 {
		t.Fatalf("expected only method-level route site, got %v", sites)
	}
	if !strings.HasPrefix(sites[0], "symfony_route_list_") {
		t.Fatalf("expected symfony_route_list_*, got %v", sites)
	}
}
