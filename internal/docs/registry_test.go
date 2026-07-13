package docs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGoModulePathDerivation(t *testing.T) {
	cases := []struct {
		name    string
		wantURL string
	}{
		{"github.com/spf13/cobra", "https://pkg.go.dev/github.com/spf13/cobra"},
		{"golang.org/x/tools", "https://pkg.go.dev/golang.org/x/tools"},
		{"go.uber.org/multierr", "https://pkg.go.dev/go.uber.org/multierr"},
	}
	for _, tc := range cases {
		r := Resolve(tc.name, "")
		if r.Origin != "derived" {
			t.Errorf("%s: origin=%q want derived", tc.name, r.Origin)
		}
		if r.Ecosystem != "go" {
			t.Errorf("%s: ecosystem=%q want go", tc.name, r.Ecosystem)
		}
		if r.TrustScore != 6 {
			t.Errorf("%s: trust=%d want 6", tc.name, r.TrustScore)
		}
		// pkg.go.dev has no llms.txt: exactly one HTML source, no fabricated
		// llms.txt candidates.
		if len(r.Sources) != 1 || r.Sources[0].Kind != "html" || r.Sources[0].URL != tc.wantURL {
			t.Errorf("%s: sources=%+v want single html %q", tc.name, r.Sources, tc.wantURL)
		}
	}
}

func TestResolveNpmScopedDerivation(t *testing.T) {
	r := Resolve("@scope/widget", "")
	if r.Origin != "derived" || r.Ecosystem != "npm" {
		t.Fatalf("origin=%q ecosystem=%q want derived/npm", r.Origin, r.Ecosystem)
	}
	if len(r.Sources) != 1 || r.Sources[0].Kind != "html" ||
		r.Sources[0].URL != "https://www.npmjs.com/package/@scope/widget" {
		t.Errorf("sources=%+v want single npm html page", r.Sources)
	}
}

func TestResolveCargoCrateDerivation(t *testing.T) {
	// Bare crate names only derive docs.rs when the cargo ecosystem is known.
	r := ResolveFull("some-crate", "", "cargo", "")
	if r.Origin != "derived" || r.Ecosystem != "cargo" {
		t.Fatalf("origin=%q ecosystem=%q want derived/cargo", r.Origin, r.Ecosystem)
	}
	if len(r.Sources) != 1 || r.Sources[0].URL != "https://docs.rs/some-crate/latest/some_crate" {
		t.Errorf("sources=%+v want docs.rs page with underscored module", r.Sources)
	}
	// Without the hint, a bare name falls back to host guesses (not docs.rs).
	g := Resolve("some-crate", "")
	for _, s := range g.Sources {
		if s.URL == "https://docs.rs/some-crate/latest/some_crate" {
			t.Errorf("bare name without cargo hint should not derive docs.rs: %+v", g.Sources)
		}
	}
}

func TestResolveUnknownBareNameStillWorks(t *testing.T) {
	r := Resolve("somerandomlib", "")
	if r.Origin != "derived" {
		t.Fatalf("origin=%q want derived", r.Origin)
	}
	if len(r.Sources) == 0 {
		t.Fatal("unknown bare name should still derive candidate sources")
	}
	var haveLLMS, haveHTML bool
	for _, s := range r.Sources {
		switch s.Kind {
		case "llms.txt":
			haveLLMS = true
		case "html":
			haveHTML = true
		}
	}
	if !haveLLMS || !haveHTML {
		t.Errorf("bare name should derive both llms.txt and html candidates: %+v", r.Sources)
	}
}

func TestLoadOverridesMergeAndPrecedence(t *testing.T) {
	// Reset the package-level cache so this test sees fresh files.
	overrideMu.Lock()
	overrideCache = map[string][]libEntry{}
	overrideMu.Unlock()

	// Point the global registry dir at a temp HOME and the project dir at a
	// temp repo. HOME drives paths.RegistryDir().
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()

	globalDir := filepath.Join(home, ".codehelper")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(repo, ".codehelper")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Global override: adds "acme" and overrides curated "react".
	write(t, globalDir, "docs-registry.json", `[
	  {"match":["acme","acme-sdk"],"doc_base":"https://acme.example/docs","trust":7,"ecosystem":"npm"},
	  {"match":["react"],"doc_base":"https://global-react.example","trust":3}
	]`)
	// Project override: re-overrides "react" (project wins over global).
	write(t, projDir, "docs-overrides.json", `[
	  {"match":["react"],"doc_base":"https://project-react.example","trust":9}
	]`)

	overrides := LoadOverrides(repo)

	// New entry from the global file.
	r := ResolveWith("acme-sdk", "", overrides)
	if r.Origin != "override" || r.DocBase != "https://acme.example/docs" || r.Ecosystem != "npm" {
		t.Errorf("acme-sdk override = %+v", r)
	}

	// Project override beats global override beats curated for react.
	rr := ResolveWith("react", "", overrides)
	if rr.Origin != "override" || rr.DocBase != "https://project-react.example" || rr.TrustScore != 9 {
		t.Errorf("react should resolve to project override, got %+v", rr)
	}

	// A name not in overrides still falls through to curated.
	c := ResolveWith("next", "", overrides)
	if c.Origin != "curated" {
		t.Errorf("next should remain curated, got %q", c.Origin)
	}
}

func TestLoadOverridesBestEffort(t *testing.T) {
	overrideMu.Lock()
	overrideCache = map[string][]libEntry{}
	overrideMu.Unlock()

	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	projDir := filepath.Join(repo, ".codehelper")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Malformed JSON and an entry missing doc_base must both be ignored.
	write(t, projDir, "docs-overrides.json", `not json at all`)
	if got := LoadOverrides(repo); len(got) != 0 {
		t.Errorf("invalid override file should yield no entries, got %+v", got)
	}

	overrideMu.Lock()
	overrideCache = map[string][]libEntry{}
	overrideMu.Unlock()
	write(t, projDir, "docs-overrides.json", `[{"match":["x"]},{"match":[],"doc_base":"https://y"}]`)
	if got := LoadOverrides(repo); len(got) != 0 {
		t.Errorf("entries missing doc_base/match should be dropped, got %+v", got)
	}

	// Missing files (empty repoRoot, no global file) never error.
	overrideMu.Lock()
	overrideCache = map[string][]libEntry{}
	overrideMu.Unlock()
	if got := LoadOverrides(""); len(got) != 0 {
		t.Errorf("no files should yield no entries, got %+v", got)
	}
}
