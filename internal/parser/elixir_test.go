package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseElixirAliasAndDefs(t *testing.T) {
	src := []byte(`
defmodule Phoenix.Router do
  defmodule NoRouteError do
    def exception(opts) do
      %__MODULE__{}
    end
  end

  defp prelude(opts) do
    quote do
      unquote(opts)
    end
  end

  def call(conn, opts) do
    prelude(opts)
    conn
  end
end
`)
	res, err := ParseElixir(context.Background(), "repo", "lib/phoenix/router.ex", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Phoenix.Router", "NoRouteError", "exception", "prelude", "call"}
	found := map[string]bool{}
	for _, s := range res.Symbols {
		found[s.Name] = true
	}
	for _, name := range want {
		if !found[name] {
			t.Errorf("missing Elixir symbol %q; got %v", name, found)
		}
	}
	var router *types.Symbol
	for i := range res.Symbols {
		if res.Symbols[i].Name == "Phoenix.Router" {
			router = &res.Symbols[i]
			break
		}
	}
	if router == nil || router.Kind != types.SymbolKindNamespace {
		t.Fatalf("Phoenix.Router should be namespace, got %+v", router)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
			if !strings.Contains(e.TargetID, "prelude") && !strings.Contains(e.TargetID, "quote") && !strings.Contains(e.TargetID, "unquote") {
				// other calls ok
			}
		}
	}
	if calls == 0 {
		t.Fatal("expected call edges from Elixir def bodies")
	}
}
