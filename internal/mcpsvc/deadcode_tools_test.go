package mcpsvc

import (
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestLooksSyntheticOrNoise(t *testing.T) {
	cases := []struct {
		sym  types.Symbol
		want bool
	}{
		{types.Symbol{Name: "route_get_5", Path: "routes/web.php"}, true},
		{types.Symbol{Name: "fastapi_get_12", Path: "docs_src/x.py"}, true},
		{types.Symbol{Name: "express_post_1", Path: "app.js"}, true},
		{types.Symbol{Name: "@keyframes fade", Path: "styles.css", Language: "css"}, true},
		{types.Symbol{Name: ".btn-primary", Path: "app.css"}, true},
		{types.Symbol{Name: "unusedHelper", Path: "internal/util.go", Language: "go"}, false},
		{types.Symbol{Name: "formatDate", Path: "src/lib/dates.ts", Language: "typescript"}, false},
	}
	for _, tc := range cases {
		if got := looksSyntheticOrNoise(tc.sym); got != tc.want {
			t.Errorf("looksSyntheticOrNoise(%q @ %q)=%v want %v", tc.sym.Name, tc.sym.Path, got, tc.want)
		}
	}
}

func TestLooksRuntimeInvoked(t *testing.T) {
	for _, name := range []string{"main", "init", "ServeHTTP", "TestFoo", "test_login", "index", "store", "__construct", "onModuleInit", "ngOnInit"} {
		if !looksRuntimeInvoked(types.Symbol{Name: name}) {
			t.Errorf("expected %q to look runtime-invoked", name)
		}
	}
	if looksRuntimeInvoked(types.Symbol{Name: "computeTotal"}) {
		t.Error("computeTotal should not look runtime-invoked")
	}
}

func TestClassifyDeadCandidateShape(t *testing.T) {
	sym := types.Symbol{
		Name: "orphanHelper", Kind: types.SymbolKindFunction,
		Path: "internal/util.go", LineStart: 42, Language: "go",
	}
	c := classifyDeadCandidate(sym, false)
	if c.Symbol != "orphanHelper" || c.Path != "internal/util.go" || c.Line != 42 {
		t.Fatalf("shape wrong: %+v", c)
	}
	if c.Confidence != "high" {
		t.Fatalf("confidence=%s want high", c.Confidence)
	}
	if c.Reason == "" || c.Loc != "internal/util.go:42" {
		t.Fatalf("reason/loc missing: %+v", c)
	}
	method := types.Symbol{Name: "doWork", Kind: types.SymbolKindMethod, Path: "svc.go", LineStart: 1, Language: "go"}
	m := classifyDeadCandidate(method, false)
	if m.Confidence != "medium" {
		t.Fatalf("method confidence=%s want medium", m.Confidence)
	}
}
