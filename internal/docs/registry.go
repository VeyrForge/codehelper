package docs

import (
	"net/url"
	"strings"
)

// libEntry is a curated documentation source for a well-known library.
type libEntry struct {
	// match are lowercase aliases that resolve to this entry (dep names or
	// doc-friendly short names).
	match []string
	// docBase is the documentation site root (no trailing slash).
	docBase string
	// llmsTxt, when set, overrides the derived <docBase>/llms.txt URL for sites
	// that publish llms.txt at a non-root path.
	llmsTxt string
	// llmsFull, when set, overrides the derived <docBase>/llms-full.txt URL.
	llmsFull string
	// trust is a 0-10 curation confidence, mirroring Context7's trust score.
	trust int
	// ecosystem, when set, overrides the docBase-inferred ecosystem (used by
	// user/project overrides that point at non-standard hosts).
	ecosystem string
	// htmlOnly suppresses derived <docBase>/llms.txt candidates for registry
	// pages (pkg.go.dev, npm, docs.rs) that are known not to publish one.
	htmlOnly bool
}

// curated maps common ecosystem libraries to their official docs. Entries with
// a known-good llms.txt location are preferred; for everything else the engine
// derives <docBase>/llms.txt and /llms-full.txt candidates (the standard
// location) and falls back to HTML extraction.
var curated = []libEntry{
	{match: []string{"next", "next.js", "nextjs", "@vercel/next"}, docBase: "https://nextjs.org/docs", trust: 9},
	{match: []string{"react", "react-dom"}, docBase: "https://react.dev", trust: 9},
	{match: []string{"vue", "vuejs"}, docBase: "https://vuejs.org", trust: 9},
	{match: []string{"svelte", "sveltekit", "@sveltejs/kit"}, docBase: "https://svelte.dev/docs", trust: 9},
	{match: []string{"angular", "@angular/core"}, docBase: "https://angular.dev", trust: 8},
	{match: []string{"express"}, docBase: "https://expressjs.com", trust: 8},
	{match: []string{"tailwind", "tailwindcss"}, docBase: "https://tailwindcss.com/docs", trust: 9},
	{match: []string{"vite"}, docBase: "https://vite.dev", trust: 8},
	{match: []string{"typescript"}, docBase: "https://www.typescriptlang.org/docs", trust: 9},
	{match: []string{"node", "nodejs"}, docBase: "https://nodejs.org/en/docs", trust: 9},
	{match: []string{"prisma", "@prisma/client"}, docBase: "https://www.prisma.io/docs", trust: 8},
	{match: []string{"drizzle", "drizzle-orm"}, docBase: "https://orm.drizzle.team/docs", trust: 7},
	{match: []string{"zod"}, docBase: "https://zod.dev", trust: 8},
	{match: []string{"axios"}, docBase: "https://axios-http.com/docs/intro", trust: 8},
	{match: []string{"lodash"}, docBase: "https://lodash.com/docs", trust: 8},
	{match: []string{"react-query", "tanstack-query", "@tanstack/react-query"}, docBase: "https://tanstack.com/query/latest", trust: 8},
	{match: []string{"zustand"}, docBase: "https://zustand.docs.pmnd.rs", trust: 7},
	{match: []string{"redux"}, docBase: "https://redux.js.org", trust: 8},
	{match: []string{"redux-toolkit", "@reduxjs/toolkit"}, docBase: "https://redux-toolkit.js.org", trust: 8},
	{match: []string{"mongoose"}, docBase: "https://mongoosejs.com/docs", trust: 8},
	{match: []string{"sequelize"}, docBase: "https://sequelize.org/docs/v6", trust: 7},
	{match: []string{"jest"}, docBase: "https://jestjs.io/docs/getting-started", trust: 8},
	{match: []string{"vitest"}, docBase: "https://vitest.dev", trust: 8},
	{match: []string{"playwright", "@playwright/test"}, docBase: "https://playwright.dev/docs/intro", trust: 8},
	{match: []string{"webpack"}, docBase: "https://webpack.js.org/concepts", trust: 8},
	{match: []string{"esbuild"}, docBase: "https://esbuild.github.io", trust: 8},
	{match: []string{"eslint"}, docBase: "https://eslint.org/docs/latest", trust: 8},
	{match: []string{"prettier"}, docBase: "https://prettier.io/docs", trust: 8},
	{match: []string{"storybook"}, docBase: "https://storybook.js.org/docs", trust: 8},
	{match: []string{"nestjs", "@nestjs/core", "nest"}, docBase: "https://docs.nestjs.com", trust: 8},
	{match: []string{"remix", "@remix-run/react"}, docBase: "https://remix.run/docs", trust: 8},
	{match: []string{"astro"}, docBase: "https://docs.astro.build", trust: 8},
	{match: []string{"solid", "solid-js", "solidjs"}, docBase: "https://docs.solidjs.com", trust: 7},
	{match: []string{"qwik", "@builder.io/qwik"}, docBase: "https://qwik.dev/docs", trust: 7},
	{match: []string{"nuxt"}, docBase: "https://nuxt.com/docs", trust: 8},

	// Go ecosystem (pkg.go.dev serves canonical, version-aware docs).
	{match: []string{"cobra", "spf13/cobra"}, docBase: "https://pkg.go.dev/github.com/spf13/cobra", trust: 8},
	{match: []string{"gin", "gin-gonic/gin"}, docBase: "https://pkg.go.dev/github.com/gin-gonic/gin", trust: 7},
	{match: []string{"gorm", "gorm.io/gorm"}, docBase: "https://gorm.io/docs", trust: 8},
	{match: []string{"echo", "labstack/echo"}, docBase: "https://echo.labstack.com/docs", trust: 7},
	{match: []string{"fiber", "gofiber/fiber"}, docBase: "https://docs.gofiber.io", trust: 7},
	{match: []string{"chi", "go-chi/chi"}, docBase: "https://pkg.go.dev/github.com/go-chi/chi/v5", trust: 7},
	{match: []string{"zap", "uber-go/zap", "go.uber.org/zap"}, docBase: "https://pkg.go.dev/go.uber.org/zap", trust: 7},
	{match: []string{"go", "golang"}, docBase: "https://go.dev/doc", trust: 9},

	// Python ecosystem.
	{match: []string{"django"}, docBase: "https://docs.djangoproject.com/en/stable", trust: 9},
	{match: []string{"flask"}, docBase: "https://flask.palletsprojects.com", trust: 8},
	{match: []string{"fastapi"}, docBase: "https://fastapi.tiangolo.com", trust: 8},
	{match: []string{"requests"}, docBase: "https://requests.readthedocs.io/en/latest", trust: 7},
	{match: []string{"pydantic"}, docBase: "https://docs.pydantic.dev/latest", trust: 8},
	{match: []string{"numpy"}, docBase: "https://numpy.org/doc/stable", trust: 8},
	{match: []string{"pandas"}, docBase: "https://pandas.pydata.org/docs", trust: 8},
	{match: []string{"sqlalchemy"}, docBase: "https://docs.sqlalchemy.org/en/20", trust: 8},
	{match: []string{"celery"}, docBase: "https://docs.celeryq.dev/en/stable", trust: 7},
	{match: []string{"pytest"}, docBase: "https://docs.pytest.org/en/stable", trust: 8},
	{match: []string{"poetry"}, docBase: "https://python-poetry.org/docs", trust: 8},
	{match: []string{"python"}, docBase: "https://docs.python.org/3", trust: 9},

	// PHP ecosystem.
	{match: []string{"laravel", "laravel/framework"}, docBase: "https://laravel.com/docs", trust: 9},
	{match: []string{"symfony"}, docBase: "https://symfony.com/doc/current", trust: 8},
	{match: []string{"woocommerce", "woo"}, docBase: "https://developer.woocommerce.com/docs", trust: 7},
	{match: []string{"wordpress", "wp"}, docBase: "https://developer.wordpress.org", trust: 8},
	{match: []string{"php"}, docBase: "https://www.php.net/manual/en", trust: 8},

	// Rust ecosystem (docs.rs is version-aware).
	{match: []string{"tokio"}, docBase: "https://docs.rs/tokio/latest/tokio", trust: 8},
	{match: []string{"serde"}, docBase: "https://docs.rs/serde/latest/serde", trust: 8},
	{match: []string{"actix-web", "actix"}, docBase: "https://docs.rs/actix-web/latest/actix_web", trust: 7},
	{match: []string{"reqwest"}, docBase: "https://docs.rs/reqwest/latest/reqwest", trust: 8},
	{match: []string{"clap"}, docBase: "https://docs.rs/clap/latest/clap", trust: 8},

	// AI / docs platforms known to publish llms.txt.
	{match: []string{"anthropic", "@anthropic-ai/sdk", "claude"}, docBase: "https://docs.claude.com", llmsTxt: "https://docs.claude.com/llms.txt", llmsFull: "https://docs.claude.com/llms-full.txt", trust: 10},
	{match: []string{"openai"}, docBase: "https://platform.openai.com/docs", trust: 8},
	{match: []string{"mintlify"}, docBase: "https://mintlify.com/docs", llmsTxt: "https://mintlify.com/docs/llms.txt", trust: 8},
}

// Source describes a single documentation location the engine will consult.
type Source struct {
	URL  string `json:"url"`
	Kind string `json:"kind"` // llms.txt | llms-full.txt | html
}

// Resolved is a library resolved to version + ordered documentation sources.
type Resolved struct {
	Name       string   `json:"name"`
	Version    string   `json:"version,omitempty"`
	Ecosystem  string   `json:"ecosystem,omitempty"`
	DocBase    string   `json:"doc_base,omitempty"`
	Sources    []Source `json:"sources"`
	TrustScore int      `json:"trust_score"`
	Origin     string   `json:"origin"` // override | curated | derived
	Note       string   `json:"note,omitempty"`
}

// Resolve maps a library name (and optional version detected elsewhere) to an
// ordered list of documentation sources to try. Pure: no network and no disk —
// consults only the built-in curated index. Use ResolveWith to also honor
// user/project override files, or ResolveFull to additionally load overrides
// from disk and apply an ecosystem hint.
func Resolve(name, version string) Resolved {
	return resolve(name, version, "", nil)
}

// ResolveWith resolves like Resolve but checks the supplied overrides (loaded
// via LoadOverrides) before the built-in curated list.
func ResolveWith(name, version string, overrides []libEntry) Resolved {
	return resolve(name, version, "", overrides)
}

// ResolveFull is the engine entry point: it loads user/project override files
// from disk (best-effort) and applies an ecosystem hint (from manifest
// detection) so a bare Rust crate resolves to docs.rs. repoRoot may be empty.
func ResolveFull(name, version, ecosystem, repoRoot string) Resolved {
	return resolve(name, version, ecosystem, LoadOverrides(repoRoot))
}

// resolve is the shared resolution core. It checks overrides before the
// built-in curated list, so users can add or override doc sources. llms.txt /
// llms-full.txt come first (the modern, clean-markdown standard), HTML doc
// pages last. When the library is unknown, returns a derived best-effort entry:
// a registry-page source for namespaced packages (Go modules, npm scopes) or
// the cargo crate page when the ecosystem hint says so, else guessed hosts for
// bare names. Sources is empty only when nothing plausible can be inferred.
func resolve(name, version, ecosystem string, overrides []libEntry) Resolved {
	want := strings.ToLower(strings.TrimSpace(name))
	r := Resolved{Name: name, Version: version}
	if want == "" {
		return r
	}
	// A caller can pass a documentation/API-reference URL directly (e.g. an
	// OpenAPI page, a specific guide, or an internal docs site) instead of a
	// library name. Fetch it as-is — no curation, no registry lookup — which is
	// how "anything with docs", including API references, is supported.
	if looksLikeURL(name) {
		base := strings.TrimRight(strings.TrimSpace(name), "/")
		base = strings.Replace(base, "http://", "https://", 1) // the fetcher is HTTPS-only
		r.DocBase = base
		r.TrustScore = 7
		r.Origin = "direct-url"
		r.Ecosystem = ecosystemFor(base)
		r.Sources = sourcesForDocBase(base)
		return r
	}
	for _, e := range overrides {
		if !matchesEntry(e, want) {
			continue
		}
		r.DocBase = e.docBase
		r.TrustScore = e.trust
		r.Origin = "override"
		r.Ecosystem = entryEcosystem(e)
		r.Sources = sourcesFor(e)
		return r
	}
	for _, e := range curated {
		if !matchesEntry(e, want) {
			continue
		}
		r.DocBase = e.docBase
		r.TrustScore = e.trust
		r.Origin = "curated"
		r.Ecosystem = entryEcosystem(e)
		r.Sources = sourcesFor(e)
		return r
	}
	// Unknown library: try a registry-page derivation first (namespaced
	// packages on a public registry), then fall back to bare-name host guesses.
	r.Origin = "derived"
	r.Note = "library not in curated index; using derived + validated candidates"
	if e, ok := registryEntry(want, ecosystem); ok {
		r.DocBase = e.docBase
		r.TrustScore = e.trust
		r.Ecosystem = entryEcosystem(e)
		r.Sources = sourcesFor(e)
		return r
	}
	r.Sources = derivedSources(want)
	if len(r.Sources) > 0 {
		r.DocBase = candidateBases(want)[0]
	}
	return r
}

// registryEntry derives a documentation entry for a namespaced package on a
// public registry, turning "every package on pkg.go.dev / npm / docs.rs" into a
// resolvable source without per-library curation. The ecosystem hint (when
// known from manifest detection) lets a bare crate name resolve to docs.rs.
// Returns ok=false for simple bare names (handled by the host-guess path) and
// anything unrecognizable.
func registryEntry(name, ecosystem string) (libEntry, bool) {
	switch {
	case isGoModulePath(name):
		// pkg.go.dev serves canonical, version-aware Go docs but publishes no
		// llms.txt, so we point only at the package page as HTML.
		return libEntry{
			docBase:   "https://pkg.go.dev/" + name,
			trust:     6,
			ecosystem: "go",
			htmlOnly:  true,
		}, true
	case strings.HasPrefix(name, "@") && strings.Count(name, "/") == 1:
		// npm scoped package: the registry project page is a reliable HTML
		// landing that links onward to the real docs.
		return libEntry{
			docBase:   "https://www.npmjs.com/package/" + name,
			trust:     5,
			ecosystem: "npm",
			htmlOnly:  true,
		}, true
	case ecosystem == "cargo" && !strings.ContainsAny(name, "/@ "):
		// Rust crate: docs.rs builds version-aware API docs for every crate.
		return libEntry{
			docBase:   "https://docs.rs/" + name + "/latest/" + crateModule(name),
			trust:     6,
			ecosystem: "cargo",
			htmlOnly:  true,
		}, true
	default:
		return libEntry{}, false
	}
}

// crateModule maps a crate name to its root module path on docs.rs, where
// hyphens become underscores (e.g. "actix-web" -> "actix_web").
func crateModule(crate string) string {
	return strings.ReplaceAll(crate, "-", "_")
}

// isGoModulePath reports whether name looks like a Go import path: it contains a
// slash and its first segment contains a dot (a host), e.g. "github.com/spf13/
// cobra" or "golang.org/x/tools". This excludes npm-scoped ("@scope/name") and
// other ecosystems whose first segment has no host dot.
func isGoModulePath(name string) bool {
	i := strings.Index(name, "/")
	if i <= 0 {
		return false
	}
	first := name[:i]
	return strings.Contains(first, ".") && !strings.ContainsAny(name, "@ ")
}

// entryEcosystem returns an entry's explicit ecosystem when set, else infers it
// from the docBase host.
func entryEcosystem(e libEntry) string {
	if e.ecosystem != "" {
		return e.ecosystem
	}
	return ecosystemFor(e.docBase)
}

// candidateBases returns conservative doc-host guesses for an unknown bare
// package, ordered by how likely each is to publish llms.txt. Only bare names
// (no scope/slash/space) are guessed, to avoid fabricating URLs.
func candidateBases(name string) []string {
	if name == "" || strings.ContainsAny(name, "/@ ") {
		return nil
	}
	return []string{
		"https://" + name + ".readthedocs.io/en/latest", // most common community-docs host
		"https://" + name + ".dev",
		"https://" + name + ".io",
		"https://" + name + ".org",
		"https://" + name + ".com",
	}
}

// derivedSources builds llms.txt/llms-full.txt candidates for every guessed host
// plus the package-registry project pages as HTML fallbacks. The engine tries
// them best-first and skips any that 404, and the link validator drops dead
// links — so broad-but-validated discovery never returns a broken URL.
func derivedSources(name string) []Source {
	var out []Source
	for _, base := range candidateBases(name) {
		out = append(out,
			Source{URL: base + "/llms.txt", Kind: "llms.txt"},
			Source{URL: base + "/llms-full.txt", Kind: "llms-full.txt"},
		)
	}
	if name != "" && !strings.ContainsAny(name, "/@ ") {
		// Registry project pages: a reliable HTML fallback that almost always
		// resolves and links onward to the real docs.
		out = append(out,
			Source{URL: "https://pkg.go.dev/" + name, Kind: "html"},
			Source{URL: "https://pypi.org/project/" + name + "/", Kind: "html"},
			Source{URL: "https://www.npmjs.com/package/" + name, Kind: "html"},
		)
	}
	return dedupeSources(out)
}

// looksLikeURL reports whether the input is an http(s) URL the caller wants
// fetched directly rather than a library name to resolve.
func looksLikeURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func matchesEntry(e libEntry, want string) bool {
	for _, m := range e.match {
		if m == want || shortName(m) == want {
			return true
		}
	}
	return false
}

func sourcesFor(e libEntry) []Source {
	var out []Source
	base := strings.TrimRight(e.docBase, "/")
	// Registry pages (pkg.go.dev, npm, docs.rs) publish no llms.txt, so when an
	// explicit one isn't given we emit only the HTML page to avoid dead probes.
	if e.htmlOnly && e.llmsTxt == "" && e.llmsFull == "" {
		return []Source{{URL: base, Kind: "html"}}
	}
	// Prefer the site root for llms.txt (the standard location) when docBase is
	// a deeper docs path, then also try docBase-relative.
	llms := e.llmsTxt
	full := e.llmsFull
	if llms == "" {
		if root := siteRoot(base); root != "" {
			out = append(out, Source{URL: root + "/llms.txt", Kind: "llms.txt"})
		}
		out = append(out, Source{URL: base + "/llms.txt", Kind: "llms.txt"})
	} else {
		out = append(out, Source{URL: llms, Kind: "llms.txt"})
	}
	if full == "" {
		if root := siteRoot(base); root != "" {
			out = append(out, Source{URL: root + "/llms-full.txt", Kind: "llms-full.txt"})
		}
	} else {
		out = append(out, Source{URL: full, Kind: "llms-full.txt"})
	}
	out = append(out, Source{URL: base, Kind: "html"})
	return dedupeSources(out)
}

// sourcesForDocBase builds the ordered doc sources for a docBase discovered at
// runtime (e.g. from registry metadata), mirroring sourcesFor for curated
// entries. Registry landing pages and code hosts (pkg.go.dev, docs.rs, npm,
// PyPI, GitHub/GitLab) publish no llms.txt, so those are emitted as HTML-only to
// avoid dead llms.txt probes; real documentation domains get the llms.txt-first
// treatment.
func sourcesForDocBase(docBase string) []Source {
	base := strings.TrimRight(strings.TrimSpace(docBase), "/")
	if base == "" {
		return nil
	}
	return sourcesFor(libEntry{
		docBase:   base,
		ecosystem: ecosystemFor(base),
		htmlOnly:  isCodeOrRegistryHost(base),
	})
}

// isCodeOrRegistryHost reports whether a URL points at a package registry or a
// source-code host rather than a documentation site — these never publish an
// llms.txt, so probing for one only wastes a request.
func isCodeOrRegistryHost(raw string) bool {
	host := hostOf(raw)
	for _, h := range []string{
		"github.com", "gitlab.com", "bitbucket.org",
		"pkg.go.dev", "docs.rs", "npmjs.com", "pypi.org", "crates.io",
	} {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// siteRoot returns scheme://host for a URL, or "" on parse failure.
func siteRoot(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func dedupeSources(in []Source) []Source {
	seen := map[string]struct{}{}
	var out []Source
	for _, s := range in {
		if _, ok := seen[s.URL]; ok {
			continue
		}
		seen[s.URL] = struct{}{}
		out = append(out, s)
	}
	return out
}

func ecosystemFor(docBase string) string {
	switch {
	case strings.Contains(docBase, "pkg.go.dev"), strings.Contains(docBase, "go.dev"):
		return "go"
	case strings.Contains(docBase, "docs.rs"):
		return "cargo"
	case strings.Contains(docBase, "python.org"), strings.Contains(docBase, "readthedocs"), strings.Contains(docBase, "pydantic"), strings.Contains(docBase, "djangoproject"):
		return "pip"
	case strings.Contains(docBase, "php.net"), strings.Contains(docBase, "laravel.com"), strings.Contains(docBase, "symfony.com"):
		return "composer"
	default:
		return "npm"
	}
}
