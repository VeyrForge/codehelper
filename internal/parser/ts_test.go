package parser

import (
	"context"
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
