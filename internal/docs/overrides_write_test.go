package docs

import (
	"path/filepath"
	"testing"
)

func TestAddOverrideRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docs-overrides.json")

	if err := AddOverride(path, Override{
		Match:   []string{"Acme-SDK", "acme"},
		DocBase: "https://docs.acme.dev/",
	}); err != nil {
		t.Fatalf("AddOverride: %v", err)
	}

	// Resolution honors the override (names are lowercased) and probes llms.txt.
	got := ResolveWith("acme-sdk", "", readOverrideFile(path))
	if got.Origin != "override" {
		t.Fatalf("origin = %q, want override", got.Origin)
	}
	if got.DocBase != "https://docs.acme.dev" {
		t.Fatalf("docBase = %q, want trailing slash trimmed", got.DocBase)
	}
	if !hasLLMSSource(got.Sources) {
		t.Fatalf("expected an llms.txt source, got %+v", got.Sources)
	}

	// Alias resolves too.
	if a := ResolveWith("acme", "", readOverrideFile(path)); a.Origin != "override" {
		t.Fatalf("alias did not resolve: %+v", a)
	}

	// Re-adding the same primary name replaces rather than duplicates.
	if err := AddOverride(path, Override{Match: []string{"acme-sdk"}, DocBase: "https://acme.dev/docs"}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	entries, err := ReadOverridesFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after replace, got %d", len(entries))
	}

	// Remove by name.
	n, err := RemoveOverride(path, "acme-sdk")
	if err != nil || n != 1 {
		t.Fatalf("remove = %d,%v want 1,nil", n, err)
	}
	if e, _ := ReadOverridesFile(path); len(e) != 0 {
		t.Fatalf("expected empty after remove, got %d", len(e))
	}
}

func TestAddOverrideValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := AddOverride(path, Override{Match: []string{"x"}, DocBase: "ftp://nope"}); err == nil {
		t.Fatal("expected error for non-https doc_base")
	}
	if err := AddOverride(path, Override{Match: nil, DocBase: "https://ok.dev"}); err == nil {
		t.Fatal("expected error for missing match")
	}
}

func TestResolveDirectURL(t *testing.T) {
	r := Resolve("https://api.example.com/openapi", "")
	if r.Origin != "direct-url" {
		t.Fatalf("origin = %q, want direct-url", r.Origin)
	}
	if len(r.Sources) == 0 {
		t.Fatal("expected at least one source for a direct URL")
	}
}

func hasLLMSSource(srcs []Source) bool {
	for _, s := range srcs {
		if s.Kind == "llms.txt" || s.Kind == "llms-full.txt" {
			return true
		}
	}
	return false
}
