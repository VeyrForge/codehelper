package selfdoc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestIsPublic(t *testing.T) {
	cases := []struct {
		name, lang string
		want       bool
	}{
		{"ExportedFunc", "go", true},
		{"unexportedFunc", "go", false},
		{"Store.AddFact", "go", true}, // qualified: judged on last component
		{"store.addFact", "go", false},
		{"publicThing", "typescript", true},
		{"_privateThing", "typescript", false},
		{"public_helper", "python", true},
		{"_internal", "python", false},
		{"", "go", false},
	}
	for _, c := range cases {
		if got := isPublic(c.name, c.lang); got != c.want {
			t.Errorf("isPublic(%q,%q)=%v want %v", c.name, c.lang, got, c.want)
		}
	}
}

func TestPackageOfAndSlug(t *testing.T) {
	cases := []struct{ path, pkg, slug string }{
		{"internal/foo/bar.go", "internal/foo", "internal__foo"},
		{"main.go", "root", "root"},
		{"cmd/app/main.go", "cmd/app", "cmd__app"},
	}
	for _, c := range cases {
		if got := packageOf(c.path); got != c.pkg {
			t.Errorf("packageOf(%q)=%q want %q", c.path, got, c.pkg)
		}
		if got := slug(c.pkg); got != c.slug {
			t.Errorf("slug(%q)=%q want %q", c.pkg, got, c.slug)
		}
	}
}

func TestIsTestPath(t *testing.T) {
	tests := map[string]bool{
		"internal/foo/foo_test.go": true,
		"src/foo.test.ts":          true,
		"src/foo.spec.js":          true,
		"src/__tests__/foo.ts":     true,
		"internal/foo/foo.go":      false,
		"src/component.tsx":        false,
	}
	for path, want := range tests {
		if got := isTestPath(path); got != want {
			t.Errorf("isTestPath(%q)=%v want %v", path, got, want)
		}
	}
}

func TestSignatureLine(t *testing.T) {
	lines := []string{
		"package foo",
		"func ExportedFunc(a int) error {",
		"\tlongbody",
	}
	if got := signatureLine(lines, 2); got != "func ExportedFunc(a int) error" {
		t.Errorf("signature=%q", got)
	}
	if got := signatureLine(lines, 0); got != "" {
		t.Errorf("out-of-range lineStart should be empty, got %q", got)
	}
	if got := signatureLine(lines, 99); got != "" {
		t.Errorf("beyond EOF should be empty, got %q", got)
	}
}

func TestRenderPackageDeterministicAndPublicShape(t *testing.T) {
	pd := &pkgDoc{
		name:  "internal/foo",
		files: map[string]struct{}{"internal/foo/a.go": {}, "internal/foo/b.go": {}},
		functions: []symEntry{
			{sym: types.Symbol{Name: "Beta", Kind: types.SymbolKindFunction, Path: "internal/foo/a.go", LineStart: 10, Language: "go"}, signature: "func Beta()"},
			{sym: types.Symbol{Name: "Alpha", Kind: types.SymbolKindFunction, Path: "internal/foo/a.go", LineStart: 5, Language: "go"}, signature: "func Alpha()", callers: []string{"Caller2", "Caller1"}, moreCall: 3},
		},
		types: []symEntry{
			{sym: types.Symbol{Name: "Widget", Kind: types.SymbolKindClass, Path: "internal/foo/b.go", LineStart: 3, Language: "go"}, signature: "type Widget struct"},
		},
	}
	sortEntries(pd.functions)
	sortEntries(pd.types)
	md := renderPackage(pd)

	// Deterministic: regenerating yields identical bytes.
	if md != renderPackage(pd) {
		t.Fatal("renderPackage is not deterministic")
	}
	// Alpha must precede Beta after sorting.
	if strings.Index(md, "### Alpha") > strings.Index(md, "### Beta") {
		t.Error("functions not sorted (Alpha should precede Beta)")
	}
	// Callers are sorted and capped overflow is annotated.
	if !strings.Contains(md, "Used by: Caller1, Caller2 (+3 more)") {
		t.Errorf("caller line missing/unsorted:\n%s", md)
	}
	// Sections and signature fences present.
	for _, want := range []string{"# package internal/foo", "## Functions", "## Types & Interfaces", "```go\nfunc Alpha()\n```", "## Files"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in output", want)
		}
	}
}
