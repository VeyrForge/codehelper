package docs

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// RegistryMeta is the author-declared documentation location for a package,
// resolved live from the ecosystem's public registry API (npm, PyPI, crates.io).
//
// This is the cure for the curation treadmill: instead of hand-maintaining a
// docBase for every library, we read the URL the package author already
// published in their registry metadata. Those URLs are factual data, not
// copyrightable content, and the lookup stores nothing — so it both scales to
// every package in an ecosystem and keeps the copyright surface at zero.
type RegistryMeta struct {
	DocBase string // best author-declared docs/homepage URL
	Version string // latest published version when the registry reports one
	Source  string // which registry answered: npmjs | pypi | crates
	Trust   int    // curation confidence, mirroring libEntry.trust
}

// resolveFromRegistry queries the public registry for the package's
// author-declared docs URL. It uses the engine's gated Fetcher (HTTPS-only, no
// private hosts), so it honors the same privacy boundary as documentation
// fetches. ok=false when the ecosystem/package is unknown or declares no URL.
//
// When the ecosystem is unknown (no manifest match) it probes npm then PyPI then
// crates and takes the first hit — a bare name is most often an npm package.
func resolveFromRegistry(ctx context.Context, f Fetcher, name, ecosystem string) (RegistryMeta, bool) {
	name = strings.TrimSpace(name)
	if name == "" || f == nil {
		return RegistryMeta{}, false
	}
	switch ecosystem {
	case "npm":
		return resolveNpm(ctx, f, name)
	case "pip":
		return resolvePyPI(ctx, f, name)
	case "cargo":
		return resolveCrates(ctx, f, name)
	case "":
		for _, try := range []func(context.Context, Fetcher, string) (RegistryMeta, bool){
			resolveNpm, resolvePyPI, resolveCrates,
		} {
			if m, ok := try(ctx, f, name); ok {
				return m, true
			}
		}
	}
	return RegistryMeta{}, false
}

// resolveNpm reads the packument from registry.npmjs.org and prefers the
// declared homepage, falling back to the repository URL.
func resolveNpm(ctx context.Context, f Fetcher, name string) (RegistryMeta, bool) {
	// Scoped names (@scope/pkg) must encode the slash for the packument endpoint.
	path := name
	if strings.HasPrefix(name, "@") {
		path = strings.Replace(name, "/", "%2f", 1)
	}
	var doc struct {
		Homepage   string            `json:"homepage"`
		Repository json.RawMessage   `json:"repository"`
		DistTags   map[string]string `json:"dist-tags"`
	}
	if !fetchJSON(ctx, f, "https://registry.npmjs.org/"+path, &doc) {
		return RegistryMeta{}, false
	}
	docBase := cleanDocURL(doc.Homepage)
	if docBase == "" {
		docBase = cleanDocURL(repoURLFromNPM(doc.Repository))
	}
	if docBase == "" {
		return RegistryMeta{}, false
	}
	return RegistryMeta{DocBase: docBase, Version: doc.DistTags["latest"], Source: "npmjs", Trust: 6}, true
}

// resolvePyPI reads the PyPI JSON API and prefers an explicit Documentation
// project URL, then any "doc"-ish project URL, then Homepage / docs_url.
func resolvePyPI(ctx context.Context, f Fetcher, name string) (RegistryMeta, bool) {
	var doc struct {
		Info struct {
			Version     string            `json:"version"`
			HomePage    string            `json:"home_page"`
			DocsURL     string            `json:"docs_url"`
			ProjectURLs map[string]string `json:"project_urls"`
		} `json:"info"`
	}
	if !fetchJSON(ctx, f, "https://pypi.org/pypi/"+url.PathEscape(name)+"/json", &doc) {
		return RegistryMeta{}, false
	}
	docBase := cleanDocURL(pickProjectURL(doc.Info.ProjectURLs))
	for _, fallback := range []string{doc.Info.DocsURL, doc.Info.HomePage} {
		if docBase != "" {
			break
		}
		docBase = cleanDocURL(fallback)
	}
	if docBase == "" {
		return RegistryMeta{}, false
	}
	return RegistryMeta{DocBase: docBase, Version: doc.Info.Version, Source: "pypi", Trust: 6}, true
}

// resolveCrates reads the crates.io API and prefers the declared documentation
// URL (usually docs.rs), then homepage, then repository.
func resolveCrates(ctx context.Context, f Fetcher, name string) (RegistryMeta, bool) {
	var doc struct {
		Crate struct {
			Documentation    string `json:"documentation"`
			Homepage         string `json:"homepage"`
			Repository       string `json:"repository"`
			MaxStableVersion string `json:"max_stable_version"`
			NewestVersion    string `json:"newest_version"`
		} `json:"crate"`
	}
	if !fetchJSON(ctx, f, "https://crates.io/api/v1/crates/"+url.PathEscape(name), &doc) {
		return RegistryMeta{}, false
	}
	var docBase string
	for _, cand := range []string{doc.Crate.Documentation, doc.Crate.Homepage, doc.Crate.Repository} {
		if docBase = cleanDocURL(cand); docBase != "" {
			break
		}
	}
	if docBase == "" {
		return RegistryMeta{}, false
	}
	ver := doc.Crate.MaxStableVersion
	if ver == "" {
		ver = doc.Crate.NewestVersion
	}
	return RegistryMeta{DocBase: docBase, Version: ver, Source: "crates", Trust: 6}, true
}

// pickProjectURL chooses the most documentation-like entry from PyPI's
// project_urls map: an explicit "Documentation" key first, then any key that
// mentions docs, then a Homepage.
func pickProjectURL(urls map[string]string) string {
	if len(urls) == 0 {
		return ""
	}
	if v := urls["Documentation"]; v != "" {
		return v
	}
	for k, v := range urls {
		if v != "" && strings.Contains(strings.ToLower(k), "doc") {
			return v
		}
	}
	return urls["Homepage"]
}

// repoURLFromNPM extracts a repository URL from npm's repository field, which is
// either a string or an object {type,url}.
func repoURLFromNPM(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.URL
	}
	return ""
}

// cleanDocURL normalizes a declared URL into an https doc base: it strips VCS
// prefixes (git+…), a trailing .git, and a #readme fragment, and rejects
// anything that isn't an http(s) URL (e.g. "UNKNOWN", git@ ssh remotes).
func cleanDocURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "git+")
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimRight(s, "/")
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "https":
		return s
	case "http":
		u.Scheme = "https"
		return u.String()
	default:
		return ""
	}
}

// fetchJSON fetches rawURL through the gated Fetcher and decodes the body into v.
// Returns false on any network error, non-2xx status, empty body, or JSON that
// does not decode.
func fetchJSON(ctx context.Context, f Fetcher, rawURL string, v any) bool {
	fr := f.Fetch(ctx, rawURL)
	if fr.Err != nil || fr.StatusCode >= 400 || strings.TrimSpace(fr.Body) == "" {
		return false
	}
	return json.Unmarshal([]byte(fr.Body), v) == nil
}
