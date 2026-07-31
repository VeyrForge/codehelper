package parser

import (
	"context"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseGo_InterfaceKind(t *testing.T) {
	t.Parallel()
	src := []byte(`package fiber

type Ctx interface {
	Get(key string) string
}

type App struct{}

type Router interface {
	Use(args ...any) Router
}
`)
	res, err := ParseGo(context.Background(), "repo", "ctx.go", src)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		kinds[s.Name] = s.Kind
	}
	if kinds["Ctx"] != types.SymbolKindInterface {
		t.Fatalf("Ctx kind=%q want interface", kinds["Ctx"])
	}
	if kinds["Router"] != types.SymbolKindInterface {
		t.Fatalf("Router kind=%q want interface", kinds["Router"])
	}
	if kinds["App"] != types.SymbolKindClass {
		t.Fatalf("App kind=%q want class", kinds["App"])
	}
}
