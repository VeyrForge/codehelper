package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseRLite(t *testing.T) {
	src := []byte(`
source("helpers.R")

greet <- function(name) {
  format(name)
}

shout <- function(name) {
  greet(name)
}
`)
	res, err := parseRLite(context.Background(), "repo", "greeter.R", src)
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
	if found["greet"] != types.SymbolKindFunction || found["shout"] != types.SymbolKindFunction {
		t.Errorf("functions missing: %v", found)
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "helpers.R") {
		t.Fatalf("imports=%v", res.Imports)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected R call edges")
	}
}

func TestParsePerlLite(t *testing.T) {
	src := []byte(`
package Greeter;
use strict;
use warnings;
use Helpers;

sub greet {
    my ($name) = @_;
    return format($name);
}

sub shout {
    my ($name) = @_;
    return greet($name);
}

1;
`)
	res, err := parsePerlLite(context.Background(), "repo", "Greeter.pm", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "greet" && s.ParentID != "Greeter" {
			t.Errorf("greet ParentID=%q, want Greeter", s.ParentID)
		}
	}
	if found["Greeter"] != types.SymbolKindNamespace {
		t.Errorf("package kind=%q map=%v", found["Greeter"], found)
	}
	if found["greet"] != types.SymbolKindFunction || found["shout"] != types.SymbolKindFunction {
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
		t.Fatal("expected Perl call edges")
	}
}

func TestParseOCamlLite(t *testing.T) {
	src := []byte(`
open Helpers

module Greeter = struct
  let greet name =
    format(name)

  let shout name =
    greet(name)
end
`)
	res, err := parseOCamlLite(context.Background(), "repo", "greeter.ml", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "greet" && s.ParentID != "Greeter" {
			t.Errorf("greet ParentID=%q, want Greeter", s.ParentID)
		}
	}
	if found["Greeter"] != types.SymbolKindNamespace {
		t.Errorf("module kind=%q map=%v", found["Greeter"], found)
	}
	if found["greet"] != types.SymbolKindFunction || found["shout"] != types.SymbolKindFunction {
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
		t.Fatal("expected OCaml call edges")
	}
}

func TestParseHaskellLite(t *testing.T) {
	src := []byte(`
module Greeter where

import Helpers (format)

greet :: String -> String
greet name = format(name)

shout :: String -> String
shout name = greet(name)
`)
	res, err := parseHaskellLite(context.Background(), "repo", "Greeter.hs", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "greet" && s.ParentID != "Greeter" {
			t.Errorf("greet ParentID=%q, want Greeter", s.ParentID)
		}
	}
	if found["Greeter"] != types.SymbolKindNamespace {
		t.Errorf("module kind=%q map=%v", found["Greeter"], found)
	}
	if found["greet"] != types.SymbolKindFunction || found["shout"] != types.SymbolKindFunction {
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
		t.Fatal("expected Haskell call edges")
	}
}

func TestExtractorForRPerlOCamlHaskell(t *testing.T) {
	for _, ext := range []string{".r", ".R", ".pl", ".pm", ".t", ".ml", ".mli", ".hs", ".lhs"} {
		if ExtractorForExt(ext) == nil {
			t.Fatalf("expected extractor for %s", ext)
		}
	}
}
