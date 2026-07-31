package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseClojureLite(t *testing.T) {
	src := []byte(`
(ns demo.greeter
  (:require [demo.helpers :as h]))

(defn greet [name]
  (format name))

(defprotocol Auditable)
`)
	res, err := parseClojureLite(context.Background(), "repo", "src/demo/greeter.clj", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "greet" && s.ParentID != "demo.greeter" {
			t.Errorf("greet ParentID=%q, want demo.greeter", s.ParentID)
		}
	}
	if found["demo.greeter"] != types.SymbolKindNamespace {
		t.Errorf("ns kind=%q map=%v", found["demo.greeter"], found)
	}
	if found["greet"] != types.SymbolKindFunction {
		t.Errorf("greet missing: %v", found)
	}
	if found["Auditable"] != types.SymbolKindInterface {
		t.Errorf("Auditable kind=%q", found["Auditable"])
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "demo.helpers") {
		t.Fatalf("imports=%v", res.Imports)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected Clojure call edges")
	}
}

func TestParseErlangLite(t *testing.T) {
	src := []byte(`
-module(greeter).
-export([greet/1]).
-include("helpers.hrl").

greet(Name) ->
    format(Name).

format(S) ->
    string:uppercase(S).
`)
	res, err := parseErlangLite(context.Background(), "repo", "src/greeter.erl", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "greet" && s.ParentID != "greeter" {
			t.Errorf("greet ParentID=%q, want greeter", s.ParentID)
		}
	}
	if found["greeter"] != types.SymbolKindNamespace {
		t.Errorf("module kind=%q map=%v", found["greeter"], found)
	}
	if found["greet"] != types.SymbolKindFunction || found["format"] != types.SymbolKindFunction {
		t.Errorf("functions missing: %v", found)
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "helpers.hrl") {
		t.Fatalf("imports=%v", res.Imports)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected Erlang call edges")
	}
}

func TestParseFSharpLite(t *testing.T) {
	src := []byte(`
module Greeter

open Helpers

type Greeter() =
    member _.Greet(name: string) =
        format(name)

let format (s: string) =
    s.ToUpper()
`)
	res, err := parseFSharpLite(context.Background(), "repo", "Greeter.fs", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "Greet" && s.ParentID != "Greeter" {
			t.Errorf("Greet ParentID=%q, want Greeter", s.ParentID)
		}
	}
	if found["Greeter"] != types.SymbolKindNamespace && found["Greeter"] != types.SymbolKindClass {
		// module Greeter + type Greeter — either/both OK; at least one must exist
		t.Errorf("Greeter missing: %v", found)
	}
	if found["Greet"] != types.SymbolKindFunction || found["format"] != types.SymbolKindFunction {
		t.Errorf("functions missing: %v", found)
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "Helpers") {
		t.Fatalf("imports=%v", res.Imports)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected F# call edges")
	}
}

func TestExtractorForClojureErlangFSharp(t *testing.T) {
	for _, ext := range []string{".clj", ".cljs", ".cljc", ".erl", ".hrl", ".fs", ".fsi", ".fsx"} {
		if ExtractorForExt(ext) == nil {
			t.Fatalf("expected extractor for %s", ext)
		}
	}
}
