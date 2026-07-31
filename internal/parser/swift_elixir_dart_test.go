package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseSwiftImportsAndCalls(t *testing.T) {
	src := []byte(`
import Foundation
import MyLib.Helpers

class Greeter {
    func greet(name: String) -> String {
        return format(name)
    }
}

extension Greeter {
    func welcome(name: String) -> String {
        return greet(name: name)
    }
}

func format(_ s: String) -> String {
    return s.uppercased()
}
`)
	res, err := ParseSwift(context.Background(), "repo", "Greeter.swift", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]types.SymbolKind{
		"Greeter": types.SymbolKindClass,
		"greet":   types.SymbolKindFunction,
		"welcome": types.SymbolKindFunction,
		"format":  types.SymbolKindFunction,
	}
	found := map[string]types.SymbolKind{}
	greeterClasses := 0
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "Greeter" && s.Kind == types.SymbolKindClass {
			greeterClasses++
		}
		if s.Name == "greet" && s.ParentID != "Greeter" {
			t.Errorf("greet ParentID=%q, want Greeter", s.ParentID)
		}
		if s.Name == "welcome" && s.ParentID != "Greeter" {
			t.Errorf("welcome ParentID=%q, want Greeter", s.ParentID)
		}
	}
	if greeterClasses != 1 {
		t.Errorf("Greeter class symbols=%d, want 1 (extension must not duplicate)", greeterClasses)
	}
	for name, kind := range want {
		if found[name] != kind {
			t.Errorf("symbol %q kind=%q, want %q (got map %v)", name, found[name], kind, found)
		}
	}
	if len(res.Imports) == 0 {
		t.Fatal("expected Swift import edges")
	}
	joined := strings.Join(res.Imports, ",")
	if !strings.Contains(joined, "Foundation") && !strings.Contains(joined, "MyLib") {
		t.Errorf("imports=%v, want Foundation or MyLib", res.Imports)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected Swift call edges")
	}
}

func TestParseElixirImportsAndParent(t *testing.T) {
	src := []byte(`
defmodule Demo.Greeter do
  alias Demo.Format
  import Demo.Helpers
  use GenServer

  def greet(name) do
    Format.apply(name)
  end
end
`)
	res, err := ParseElixir(context.Background(), "repo", "lib/demo/greeter.ex", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, s := range res.Symbols {
		found[s.Name] = true
		if s.Name == "greet" && s.ParentID != "Demo.Greeter" {
			t.Errorf("greet ParentID=%q, want Demo.Greeter", s.ParentID)
		}
	}
	if !found["Demo.Greeter"] || !found["greet"] {
		t.Fatalf("missing symbols: %v", found)
	}
	if len(res.Imports) < 2 {
		t.Fatalf("expected alias/import/use imports, got %v", res.Imports)
	}
	joined := strings.Join(res.Imports, ",")
	for _, want := range []string{"Demo.Format", "Demo.Helpers", "GenServer"} {
		if !strings.Contains(joined, want) {
			t.Errorf("imports missing %q in %v", want, res.Imports)
		}
	}
	calls := 0
	var sawFormatApply, sawDemoFormatApply, sawBehaviour bool
	for _, e := range res.Edges {
		leaf := e.TargetID
		if i := strings.LastIndex(leaf, ":"); i >= 0 {
			leaf = leaf[i+1:]
		}
		switch e.Kind {
		case types.RefKindCalls:
			calls++
			if leaf == "Format.apply" {
				sawFormatApply = true
			}
			if leaf == "Demo.Format.apply" {
				sawDemoFormatApply = true
			}
		case types.RefKindImplements:
			if leaf == "GenServer" {
				sawBehaviour = true
			}
		}
	}
	if calls == 0 {
		t.Fatal("expected Elixir call edges")
	}
	if !sawFormatApply {
		t.Fatal("expected Format.apply remote call edge")
	}
	if !sawDemoFormatApply {
		t.Fatal("expected alias-resolved Demo.Format.apply call edge")
	}
	if !sawBehaviour {
		t.Fatal("expected Demo.Greeter implements GenServer")
	}
}

func TestParseDartLiteKindsImportsCalls(t *testing.T) {
	src := []byte(`
import 'package:flutter/material.dart';
import 'helpers.dart';
export 'tone.dart';

mixin Auditable {
  String audit(String s) => format(s);
}

class Greeter with Auditable {
  final Tone tone;

  Greeter({this.tone = Tone.casual});

  Greeter.formal() : tone = Tone.formal;

  String greet(String name) {
    return format(name);
  }

  String get label => 'hi';
}

String shout(String name) {
  return format(name);
}
`)
	res, err := parseDartLite(context.Background(), "repo", "lib/greeter.dart", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	parents := map[string]string{}
	var sawGreeterClass, sawCtor bool
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		parents[s.Name] = s.ParentID
		if s.Name == "Greeter" && s.Kind == types.SymbolKindClass {
			sawGreeterClass = true
		}
		if s.Name == "Greeter" && s.Kind == types.SymbolKindMethod && s.ParentID == "Greeter" {
			sawCtor = true
		}
	}
	if !sawGreeterClass {
		t.Errorf("Greeter class missing: %v", found)
	}
	if found["Auditable"] != types.SymbolKindInterface {
		t.Errorf("Auditable kind=%q", found["Auditable"])
	}
	if found["greet"] != types.SymbolKindMethod {
		t.Errorf("greet kind=%q want method: %v", found["greet"], found)
	}
	if found["audit"] != types.SymbolKindMethod || parents["audit"] != "Auditable" {
		t.Errorf("audit kind=%q parent=%q", found["audit"], parents["audit"])
	}
	if found["shout"] != types.SymbolKindFunction {
		t.Errorf("shout kind=%q want function: %v", found["shout"], found)
	}
	if found["label"] != types.SymbolKindMethod {
		t.Errorf("getter label kind=%q: %v", found["label"], found)
	}
	if found["tone"] != types.SymbolKindVariable || parents["tone"] != "Greeter" {
		t.Errorf("field tone kind=%q parent=%q", found["tone"], parents["tone"])
	}
	if found["formal"] != types.SymbolKindMethod || parents["formal"] != "Greeter" {
		t.Errorf("named ctor formal kind=%q parent=%q", found["formal"], parents["formal"])
	}
	if !sawCtor {
		t.Fatal("expected unnamed Greeter constructor as method")
	}
	if parents["greet"] != "Greeter" {
		t.Errorf("greet ParentID=%q, want Greeter", parents["greet"])
	}
	if parents["label"] != "Greeter" {
		t.Errorf("getter label ParentID=%q, want Greeter", parents["label"])
	}
	if parents["shout"] != "" {
		t.Errorf("top-level shout ParentID=%q, want empty (not sticky class)", parents["shout"])
	}
	joined := strings.Join(res.Imports, ",")
	if !strings.Contains(joined, "helpers.dart") || !strings.Contains(joined, "tone.dart") {
		t.Fatalf("imports=%v, want helpers + export tone", res.Imports)
	}
	var sawInherit, sawFormat, sawHelpersTag bool
	calls := 0
	for _, e := range res.Edges {
		leaf := e.TargetID
		if i := strings.LastIndex(leaf, ":"); i >= 0 {
			leaf = leaf[i+1:]
		}
		switch e.Kind {
		case types.RefKindCalls:
			calls++
			if leaf == "format" {
				sawFormat = true
			}
			if leaf == "Helpers.tag" {
				sawHelpersTag = true
			}
		case types.RefKindInherits, types.RefKindImplements:
			if leaf == "Auditable" {
				sawInherit = true
			}
		}
	}
	if calls == 0 || !sawFormat {
		t.Fatal("expected Dart call edges incl format")
	}
	if !sawInherit {
		t.Fatal("expected Greeter with Auditable inherits/implements")
	}
	_ = sawHelpersTag // exercised on helpers.dart bed; greeter fixture has no Helpers.tag
}

func TestParseLuaRequireAndCalls(t *testing.T) {
	src := []byte(`
local helpers = require("helpers")

local function format(s)
  return string.upper(s)
end

function greet(name)
  return format(name)
end
`)
	res, err := ParseLua(context.Background(), "repo", "greeter.lua", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, s := range res.Symbols {
		found[s.Name] = true
	}
	if !found["format"] || !found["greet"] {
		t.Fatalf("missing Lua symbols: %v", found)
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "helpers") {
		t.Fatalf("expected require import, got %v", res.Imports)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected Lua call edges")
	}
}

func TestParseScalaImportsAndCalls(t *testing.T) {
	src := []byte(`
package demo
import scala.util.Try
import demo.helpers.Format

object Greeter {
  def greet(name: String): String = {
    Format.apply(name)
  }

  def format(s: String): String = s.toUpperCase
}

trait Auditable

class BaseGreeter

class LoggedGreeter extends BaseGreeter with Auditable {
  def loud(name: String): String = greet(name)
}
`)
	res, err := ParseScala(context.Background(), "repo", "Greeter.scala", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	parents := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		parents[s.Name] = s.ParentID
	}
	if found["Greeter"] != types.SymbolKindClass {
		t.Errorf("Greeter kind=%q", found["Greeter"])
	}
	if found["greet"] != types.SymbolKindFunction {
		t.Errorf("greet missing: %v", found)
	}
	if found["Auditable"] != types.SymbolKindInterface {
		t.Errorf("Auditable kind=%q", found["Auditable"])
	}
	if parents["greet"] != "Greeter" {
		t.Errorf("greet ParentID=%q, want Greeter", parents["greet"])
	}
	if parents["format"] != "Greeter" {
		t.Errorf("format ParentID=%q, want Greeter", parents["format"])
	}
	if parents["loud"] != "LoggedGreeter" {
		t.Errorf("loud ParentID=%q, want LoggedGreeter", parents["loud"])
	}
	if len(res.Imports) == 0 {
		t.Fatal("expected Scala imports")
	}
	var inherits, implements, calls int
	for _, e := range res.Edges {
		switch e.Kind {
		case types.RefKindCalls:
			calls++
		case types.RefKindInherits:
			inherits++
		case types.RefKindImplements:
			implements++
		}
	}
	if calls == 0 {
		t.Fatal("expected Scala call edges")
	}
	if inherits == 0 {
		t.Fatal("expected LoggedGreeter inherits BaseGreeter")
	}
	if implements == 0 {
		t.Fatal("expected LoggedGreeter implements Auditable")
	}
}
