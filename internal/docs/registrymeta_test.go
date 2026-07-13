package docs

import (
	"context"
	"testing"
)

func TestResolveFromRegistry_PyPI(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://pypi.org/pypi/fastapi/json": {StatusCode: 200, Body: `{
			"info": {"version": "0.118.0", "home_page": null, "docs_url": null,
			"project_urls": {"Documentation": "https://fastapi.tiangolo.com/", "Repository": "https://github.com/fastapi/fastapi"}}}`},
	}}
	m, ok := resolveFromRegistry(context.Background(), ff, "fastapi", "pip")
	if !ok {
		t.Fatal("expected resolution")
	}
	if m.DocBase != "https://fastapi.tiangolo.com" {
		t.Fatalf("docBase=%q", m.DocBase)
	}
	if m.Version != "0.118.0" || m.Source != "pypi" {
		t.Fatalf("version=%q source=%q", m.Version, m.Source)
	}
}

func TestResolveFromRegistry_NPM_RepoFallback(t *testing.T) {
	// No homepage -> fall back to the repository object URL, stripping git+/.git.
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://registry.npmjs.org/zod": {StatusCode: 200, Body: `{
			"dist-tags": {"latest": "3.23.8"},
			"repository": {"type": "git", "url": "git+https://github.com/colinhacks/zod.git"}}`},
	}}
	m, ok := resolveFromRegistry(context.Background(), ff, "zod", "npm")
	if !ok {
		t.Fatal("expected resolution")
	}
	if m.DocBase != "https://github.com/colinhacks/zod" {
		t.Fatalf("docBase=%q", m.DocBase)
	}
	if m.Version != "3.23.8" {
		t.Fatalf("version=%q", m.Version)
	}
}

func TestResolveFromRegistry_NPMScopedEncodesSlash(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://registry.npmjs.org/@scope%2fpkg": {StatusCode: 200, Body: `{"homepage": "https://scope.example/docs"}`},
	}}
	m, ok := resolveFromRegistry(context.Background(), ff, "@scope/pkg", "npm")
	if !ok || m.DocBase != "https://scope.example/docs" {
		t.Fatalf("ok=%v docBase=%q", ok, m.DocBase)
	}
}

func TestResolveFromRegistry_CratesPrefersDocumentation(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://crates.io/api/v1/crates/serde": {StatusCode: 200, Body: `{
			"crate": {"documentation": "https://docs.rs/serde", "homepage": "https://serde.rs",
			"repository": "https://github.com/serde-rs/serde", "max_stable_version": "1.0.210"}}`},
	}}
	m, ok := resolveFromRegistry(context.Background(), ff, "serde", "cargo")
	if !ok {
		t.Fatal("expected resolution")
	}
	if m.DocBase != "https://docs.rs/serde" || m.Version != "1.0.210" {
		t.Fatalf("docBase=%q version=%q", m.DocBase, m.Version)
	}
}

func TestResolveFromRegistry_UnknownEcosystemProbesNpmFirst(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://registry.npmjs.org/leftpad": {StatusCode: 200, Body: `{"homepage": "https://left.pad/docs"}`},
	}}
	m, ok := resolveFromRegistry(context.Background(), ff, "leftpad", "")
	if !ok || m.Source != "npmjs" || m.DocBase != "https://left.pad/docs" {
		t.Fatalf("ok=%v source=%q docBase=%q", ok, m.Source, m.DocBase)
	}
}

func TestResolveFromRegistry_MissReturnsFalse(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{}} // all 404
	if _, ok := resolveFromRegistry(context.Background(), ff, "nonesuch", "cargo"); ok {
		t.Fatal("expected miss")
	}
}

// cleanDocURL must reject non-http(s) declarations (ssh remotes, UNKNOWN).
func TestCleanDocURL(t *testing.T) {
	cases := map[string]string{
		"git+https://github.com/a/b.git": "https://github.com/a/b",
		"https://x.dev/docs/#readme":     "https://x.dev/docs",
		"http://x.dev":                   "https://x.dev",
		"git@github.com:a/b.git":         "",
		"UNKNOWN":                        "",
		"":                               "",
	}
	for in, want := range cases {
		if got := cleanDocURL(in); got != want {
			t.Errorf("cleanDocURL(%q)=%q want %q", in, got, want)
		}
	}
}

// Engine integration: a library missing from the curated index resolves via
// registry metadata, then fetches the discovered llms.txt.
func TestEngineLookup_UsesRegistryMetadataOnCuratedMiss(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://pypi.org/pypi/obscurelib/json": {StatusCode: 200, Body: `{
			"info": {"version": "2.1.0", "project_urls": {"Documentation": "https://obscurelib.dev/docs"}}}`},
		"https://obscurelib.dev/llms.txt": {StatusCode: 200, Body: "# Obscurelib\n\n- [Guide](https://obscurelib.dev/docs/guide): start here"},
	}}
	eng := &Engine{Fetcher: ff}
	res, err := eng.Lookup(context.Background(), LookupOptions{Library: "obscurelib", Network: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolved.Origin != "registry:pypi" {
		t.Fatalf("origin=%q want registry:pypi", res.Resolved.Origin)
	}
	if res.Version != "2.1.0" {
		t.Fatalf("version=%q want 2.1.0 (from registry)", res.Version)
	}
	if res.Resolved.DocBase != "https://obscurelib.dev/docs" {
		t.Fatalf("docBase=%q", res.Resolved.DocBase)
	}
}
