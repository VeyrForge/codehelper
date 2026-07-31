package mcpsvc

import (
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestPreferNonFixtureSymbols(t *testing.T) {
	t.Parallel()
	syms := []types.Symbol{
		{Path: "sample/01-cats/cats.service.ts", Name: "CatsService"},
		{Path: "packages/cats/cats.service.ts", Name: "CatsService"},
		{Path: "integration/e2e/cats.service.ts", Name: "CatsService"},
	}
	got := preferNonFixtureSymbols(syms)
	if len(got) != 1 || got[0].Path != "packages/cats/cats.service.ts" {
		t.Fatalf("got %#v", got)
	}
}

func TestPreferCanonicalSample(t *testing.T) {
	t.Parallel()
	syms := []types.Symbol{
		{Path: "integration/mongoose/cats.service.ts", Name: "CatsService"},
		{Path: "sample/06-mongoose/cats.service.ts", Name: "CatsService"},
		{Path: "sample/01-cats-app/src/cats/cats.service.ts", Name: "CatsService"},
	}
	got := preferCanonicalSample(syms)
	if got == nil || got.Path != "sample/01-cats-app/src/cats/cats.service.ts" {
		t.Fatalf("got %#v", got)
	}
	// Mixed production + sample must not auto-pick.
	if preferCanonicalSample(append(syms, types.Symbol{Path: "lib/cats.service.ts"})) != nil {
		t.Fatal("mixed set should not pick a sample")
	}
	// Nest sample without 01-: prefer …/src/ over flat sample file.
	nestSrc := preferCanonicalSample([]types.Symbol{
		{Path: "sample/07-sequelize/cats.service.ts", Name: "CatsService"},
		{Path: "sample/07-sequelize/src/cats/cats.service.ts", Name: "CatsService"},
	})
	if nestSrc == nil || nestSrc.Path != "sample/07-sequelize/src/cats/cats.service.ts" {
		t.Fatalf("nest src prefer: %#v", nestSrc)
	}
	// Express examples: prefer hello-world spine.
	ex := preferCanonicalSample([]types.Symbol{
		{Path: "examples/auth/index.js", Name: "express_get_1"},
		{Path: "examples/hello-world/index.js", Name: "express_get_1"},
	})
	if ex == nil || ex.Path != "examples/hello-world/index.js" {
		t.Fatalf("express hello prefer: %#v", ex)
	}
}
