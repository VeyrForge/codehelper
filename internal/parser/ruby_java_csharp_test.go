package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseRubyRequireAndCalls(t *testing.T) {
	src := []byte(`
require 'sinatra/base'
require_relative './helpers'

module Sinatra
  class Base
    def get(path)
      route(path)
    end
  end
end
`)
	res, err := ParseRuby(context.Background(), "r", "lib/app.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		id := e.TargetID
		if i := strings.LastIndex(id, ":"); i >= 0 {
			imports[id[i+1:]] = true
		}
	}
	for _, want := range []string{"sinatra/base", "./helpers"} {
		if !imports[want] {
			t.Errorf("missing require import %q; got %#v", want, imports)
		}
	}
	var sawGet bool
	for _, s := range res.Symbols {
		if s.Name == "get" {
			sawGet = true
			if s.ParentID != "Base" {
				t.Errorf("get ParentID=%q want Base", s.ParentID)
			}
		}
	}
	if !sawGet {
		t.Fatal("expected get method symbol")
	}
	var sawCall bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			sawCall = true
			break
		}
	}
	if !sawCall {
		t.Fatal("expected at least one calls edge from get body")
	}
}

func TestParseRuby_SinatraDSLEntrypoints(t *testing.T) {
	src := []byte(`
require 'sinatra'

get '/' do
  'hi'
end

post '/users' do
  status 201
end
`)
	res, err := ParseRuby(context.Background(), "r", "app.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !hasPrefixName(names, "sinatra_get_") || !hasPrefixName(names, "sinatra_post_") {
		t.Fatalf("expected sinatra DSL entrypoints, got %#v", names)
	}
	var sawGet, sawRoute bool
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		if strings.HasSuffix(e.TargetID, ":get") {
			sawGet = true
		}
		if strings.HasSuffix(e.TargetID, ":route") {
			sawRoute = true
		}
	}
	if !sawGet || !sawRoute {
		t.Fatalf("expected DSL→get and DSL→route calls; get=%v route=%v", sawGet, sawRoute)
	}
}

func TestParseJavaImports(t *testing.T) {
	src := []byte(`
package org.example;
import java.util.List;
import static java.util.Collections.emptyList;
class Demo {
  void run() { List.of(); }
}
`)
	res, err := ParseJava(context.Background(), "j", "Demo.java", src)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		id := e.TargetID
		if i := strings.LastIndex(id, ":"); i >= 0 {
			imports[id[i+1:]] = true
		}
	}
	for _, want := range []string{"java.util.List", "java.util.Collections.emptyList"} {
		if !imports[want] {
			t.Errorf("missing java import %q; got %#v", want, imports)
		}
	}
}

func TestParseCSharpUsings(t *testing.T) {
	src := []byte(`
using System;
using System.Collections.Generic;
using static System.Math;
namespace N {
  class C {
    void M() { Console.WriteLine(1); }
  }
}
`)
	res, err := ParseCSharp(context.Background(), "c", "C.cs", src)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		id := e.TargetID
		if i := strings.LastIndex(id, ":"); i >= 0 {
			imports[id[i+1:]] = true
		}
	}
	for _, want := range []string{"System", "System.Collections.Generic", "System.Math"} {
		if !imports[want] {
			t.Errorf("missing csharp using %q; got %#v", want, imports)
		}
	}
}
