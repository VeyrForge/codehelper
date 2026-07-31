package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseTypeScript_ExpressRouterMountAndMiddlewareChain(t *testing.T) {
	t.Parallel()
	src := []byte(`
import express from "express";

function requireAuth(req, res, next) { next(); }
function validateUser(req, res, next) { next(); }
function listUsers(req, res) { res.json([]); }

const usersRouter = express.Router();
usersRouter.get("/", requireAuth, validateUser, listUsers);

function mountAPI(app) {
  app.use("/api/users", usersRouter);
  app.get("/health", requireAuth, function(req, res) {
    res.send("ok");
  });
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app.ts", src)
	if err != nil {
		t.Fatal(err)
	}

	var sawRouterFactory, sawRouterGet, sawUseMount, sawNestedGet bool
	for _, s := range res.Symbols {
		switch {
		case strings.HasPrefix(s.Name, "express_router_"):
			sawRouterFactory = true
			if !strings.Contains(s.Signature, "frameworks=express") {
				t.Errorf("router factory missing pack: %q", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=router") {
				t.Errorf("router factory missing role=router: %q", s.Signature)
			}
		case strings.HasPrefix(s.Name, "express_get_"):
			if strings.Contains(s.Signature, "role=entrypoint") {
				sawRouterGet = true
			}
		case strings.HasPrefix(s.Name, "express_use_"):
			sawUseMount = true
		}
	}
	// Nested app.get inside mountAPI must densify too.
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "express_get_") {
			sawNestedGet = true
		}
	}

	var (
		toRouterHub, toExpressRouter                          bool
		toRequireAuth, toValidate, toListUsers, toUsersRouter bool
		toResSend                                             bool
	)
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		switch symrefName(e.TargetID) {
		case "Router":
			toRouterHub = true
		case "express.Router":
			toExpressRouter = true
		case "requireAuth":
			toRequireAuth = true
		case "validateUser":
			toValidate = true
		case "listUsers":
			toListUsers = true
		case "usersRouter":
			toUsersRouter = true
		case "res.send":
			toResSend = true
		}
	}

	if !sawRouterFactory {
		t.Fatal("expected express_router_* factory symbol")
	}
	if !sawRouterGet {
		t.Fatal("expected express_get_* entrypoint on usersRouter")
	}
	if !sawUseMount {
		t.Fatal("expected express_use_* mount site")
	}
	if !sawNestedGet {
		t.Fatal("expected nested express_get_* inside mountAPI")
	}
	if !toRouterHub || !toExpressRouter {
		t.Errorf("expected Router hub + express.Router edges; hub=%v alias=%v", toRouterHub, toExpressRouter)
	}
	if !toRequireAuth || !toValidate || !toListUsers {
		t.Errorf("expected middleware chain edges; auth=%v validate=%v list=%v",
			toRequireAuth, toValidate, toListUsers)
	}
	if !toUsersRouter {
		t.Error("expected app.use → usersRouter mount edge")
	}
	if !toResSend {
		t.Error("expected inline health handler → res.send")
	}
}

func TestParseTypeScript_ExpressBareRouterFactory(t *testing.T) {
	t.Parallel()
	src := []byte(`
const { Router } = require("express");
const api = Router();
api.post("/items", createItem);
function createItem(req, res) { res.end(); }
`)
	res, err := ParseTypeScript(context.Background(), "repo", "routes/api.js", src)
	if err != nil {
		t.Fatal(err)
	}
	var sawFactory, sawPost, toCreate bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "express_router_") {
			sawFactory = true
		}
		if strings.HasPrefix(s.Name, "express_post_") {
			sawPost = true
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && symrefName(e.TargetID) == "createItem" {
			toCreate = true
		}
	}
	if !sawFactory {
		t.Fatal("expected express_router_* for bare Router()")
	}
	if !sawPost {
		t.Fatal("expected express_post_* on api receiver from Router()")
	}
	if !toCreate {
		t.Fatal("expected api.post → createItem edge")
	}
}

func TestParseTypeScript_ExpressSkipsReactRouter(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Router } from "react-router-dom";
const r = Router();
r.get("/x", handler);
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/rr.tsx", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "express_") {
			t.Fatalf("react-router must not mint express densify sites: %s", s.Name)
		}
	}
}
