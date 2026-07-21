package parser

import (
	"context"
	"strings"
	"testing"
)

func TestExtractTypeScript_VariableFunctionSymbols(t *testing.T) {
	t.Parallel()

	src := []byte(`
import { helper } from "./lib";

const HomePage = () => {
  helper();
  return <main>Hello</main>;
};

const loadData = async function () {
  return 1;
};
`)

	res, err := Extract(context.Background(), "repo", "app/page.tsx", src)
	if err != nil {
		t.Fatalf("extract tsx: %v", err)
	}
	if len(res.Symbols) < 2 {
		t.Fatalf("expected >=2 symbols, got %d", len(res.Symbols))
	}

	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["HomePage"] {
		t.Fatalf("expected HomePage symbol, got %#v", res.Symbols)
	}
	if !names["loadData"] {
		t.Fatalf("expected loadData symbol, got %#v", res.Symbols)
	}
}

func TestExtractTypeScript_FrameworkPatterns(t *testing.T) {
	t.Parallel()
	src := []byte(`
import React, { memo } from "react";
import { registerPlugin } from "@capacitor/core";

const FancyCard = memo(() => <div />);
const DevicePlugin = registerPlugin("Device");
export default () => <main>ok</main>;
export const GET = async () => new Response("ok");
`)
	res, err := Extract(context.Background(), "repo", "app/api/route.tsx", src)
	if err != nil {
		t.Fatalf("extract ts frameworks: %v", err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["FancyCard"] {
		t.Fatalf("expected wrapped component symbol, got %#v", res.Symbols)
	}
	if !names["DevicePlugin"] {
		t.Fatalf("expected capacitor plugin symbol, got %#v", res.Symbols)
	}
	if !names["default_export"] {
		t.Fatalf("expected default export symbol, got %#v", res.Symbols)
	}
	if !names["GET"] {
		t.Fatalf("expected route handler symbol, got %#v", res.Symbols)
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

func TestExtractJavaScript_CJSPrototypeAssigns(t *testing.T) {
	t.Parallel()
	src := []byte(`
'use strict';

exports.Router = Router;

app.use = function use(fn) {
  return this;
};

proto.send = function send(body) {
  return this;
};

res.json = function json(obj) {
  return this;
};

// Must NOT be indexed — arbitrary object mutation.
other.thing = function thing() {};
`)
	res, err := ParseTypeScript(context.Background(), "repo", "lib/application.js", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]string{}
	bySig := map[string]string{}
	for _, s := range res.Symbols {
		byName[s.Name] = string(s.Kind)
		bySig[s.Name] = s.Signature
		if s.Language != "javascript" {
			t.Errorf("%s language=%q want javascript", s.Name, s.Language)
		}
	}
	for _, want := range []string{"exports.Router", "app.use", "proto.send", "res.json"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing CJS symbol %q; got %#v", want, res.Symbols)
		}
	}
	if _, ok := byName["thing"]; ok {
		t.Fatal("arbitrary other.thing assign must not become a symbol")
	}
	if _, ok := byName["use"]; ok {
		t.Fatal("bare name use must not be the indexed name; want app.use")
	}
	if byName["app.use"] != "function" || byName["exports.Router"] != "variable" {
		t.Fatalf("kinds: %#v", byName)
	}
	if !strings.Contains(bySig["app.use"], "bare=use") || !strings.Contains(bySig["app.use"], "alias=app.use") {
		t.Fatalf("app.use signature missing bare/alias: %q", bySig["app.use"])
	}
}

func TestExtractJavaScript_CJSRequireAndTopLevelExpress(t *testing.T) {
	t.Parallel()
	src := []byte(`
'use strict';
var express = require('../../');
var path = require('node:path');
var app = module.exports = express();

app.get('/', function(req, res){
  res.send('Hello World');
});

app.use(function(req, res, next){
  next();
});

if (!module.parent) {
  app.listen(3000);
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "examples/hello-world/index.js", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != "imports" {
			continue
		}
		// mod:repo:<module> — module may itself contain colons (node:path).
		const prefix = "mod:repo:"
		if strings.HasPrefix(e.TargetID, prefix) {
			imports[e.TargetID[len(prefix):]] = true
		}
	}
	for _, want := range []string{"../../", "node:path"} {
		if !imports[want] {
			t.Errorf("missing require import %q; got %#v", want, imports)
		}
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["express_get_7"] && !hasPrefixName(names, "express_get_") {
		t.Fatalf("expected top-level express_get_* entrypoint; got %#v", names)
	}
	if !names["express_use_11"] && !hasPrefixName(names, "express_use_") {
		t.Fatalf("expected top-level express_use_* entrypoint; got %#v", names)
	}
	var sawAliasCall bool
	for _, e := range res.Edges {
		if e.Kind == "calls" && strings.Contains(e.TargetID, "app.get") {
			sawAliasCall = true
		}
	}
	if !sawAliasCall {
		t.Fatal("expected call edge to app.get alias")
	}
}

func hasPrefixName(names map[string]bool, prefix string) bool {
	for n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

func TestExtractTypeScript_InstanceNewFoo(t *testing.T) {
	t.Parallel()
	src := []byte(`
class Foo { bar() {} }
function run() {
  const foo = new Foo();
  foo.bar();
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/foo.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var runID string
	for _, sym := range res.Symbols {
		if sym.Name == "run" {
			runID = sym.ID
		}
	}
	for _, edge := range res.Edges {
		if edge.SourceID == runID && symrefName(edge.TargetID) == "Foo.bar" {
			if edge.Confidence != 0.9 {
				t.Fatalf("Foo.bar confidence=%v want 0.9", edge.Confidence)
			}
			return
		}
	}
	t.Fatalf("missing Foo.bar call: %#v", res.Edges)
}
