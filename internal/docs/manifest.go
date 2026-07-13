// Package docs provides a local-first, up-to-date documentation engine.
//
// It resolves the libraries a project actually depends on (with their pinned
// versions, read from manifests) and fetches version-correct official docs,
// preferring the modern llms.txt / llms-full.txt standard before falling back
// to allowlisted HTML doc pages. This is codehelper's answer to Context7, but
// local-first: no API keys, no external index, privacy-gated network access.
//
// All resolution and parsing logic in this package is pure and testable
// offline; only fetch.go touches the network, behind an injectable Fetcher.
package docs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Dependency is a library a project depends on, as read from a manifest.
type Dependency struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`  // normalized, constraint chars stripped where possible
	Raw       string `json:"raw,omitempty"`      // raw version constraint as written
	Ecosystem string `json:"ecosystem"`          // npm|go|pip|composer|cargo|gem
	Dev       bool   `json:"dev,omitempty"`      // dev/test-only dependency
	Manifest  string `json:"manifest,omitempty"` // manifest file the dep came from (relative)
}

// firstVersionToken matches the first concrete version anchor in a constraint
// string, e.g. "^4.18.2" -> "4.18.2", "v1.9.0" -> "1.9.0", ">=1.2,<2" -> "1.2".
var firstVersionToken = regexp.MustCompile(`[0-9]+(?:\.[0-9A-Za-z\-+]+)*`)

// normalizeVersion turns a constraint string into a best-effort concrete-ish
// version (the first version-looking token). Returns "" for wildcards/latest.
func normalizeVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" || strings.EqualFold(raw, "latest") {
		return ""
	}
	return firstVersionToken.FindString(raw)
}

// ListDependencies reads all supported manifests under repoRoot (root level
// only, where manifests live) and returns the declared dependencies.
func ListDependencies(repoRoot string) []Dependency {
	var deps []Dependency
	deps = append(deps, readPackageJSON(repoRoot)...)
	deps = append(deps, readGoMod(repoRoot)...)
	deps = append(deps, readRequirementsTxt(repoRoot)...)
	deps = append(deps, readPyProject(repoRoot)...)
	deps = append(deps, readComposerJSON(repoRoot)...)
	deps = append(deps, readCargoToml(repoRoot)...)
	return dedupeDeps(deps)
}

// ResolveVersion finds the version a project pins for libName (case-insensitive,
// matching either the exact dep name or a doc-friendly short name). Returns the
// version and ecosystem, or empty strings if the project does not depend on it.
func ResolveVersion(repoRoot, libName string) (version, ecosystem string) {
	want := strings.ToLower(strings.TrimSpace(libName))
	if want == "" {
		return "", ""
	}
	for _, d := range ListDependencies(repoRoot) {
		dn := strings.ToLower(d.Name)
		if dn == want || shortName(dn) == want || strings.HasSuffix(dn, "/"+want) {
			return d.Version, d.Ecosystem
		}
	}
	return "", ""
}

// shortName returns the last path/scope segment of a dependency name, e.g.
// "@vercel/next" -> "next", "github.com/spf13/cobra" -> "cobra".
func shortName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "/"); i >= 0 && i < len(name)-1 {
		return name[i+1:]
	}
	return name
}

func readPackageJSON(repoRoot string) []Dependency {
	b, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		PeerDeps        map[string]string `json:"peerDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return nil
	}
	var out []Dependency
	add := func(m map[string]string, dev bool) {
		for name, raw := range m {
			out = append(out, Dependency{
				Name: name, Version: normalizeVersion(raw), Raw: raw,
				Ecosystem: "npm", Dev: dev, Manifest: "package.json",
			})
		}
	}
	add(pkg.Dependencies, false)
	add(pkg.PeerDeps, false)
	add(pkg.DevDependencies, true)
	return out
}

var goRequireLine = regexp.MustCompile(`^\s*([^\s]+/[^\s]+)\s+(v[0-9][0-9A-Za-z.\-+]*)`)

func readGoMod(repoRoot string) []Dependency {
	f, err := os.Open(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Dependency
	sc := bufio.NewScanner(f)
	inBlock := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "require (") {
			inBlock = true
			continue
		}
		if inBlock && line == ")" {
			inBlock = false
			continue
		}
		probe := line
		if strings.HasPrefix(probe, "require ") {
			probe = strings.TrimPrefix(probe, "require ")
		}
		if !inBlock && !strings.HasPrefix(line, "require ") {
			continue
		}
		if m := goRequireLine.FindStringSubmatch(probe); m != nil {
			out = append(out, Dependency{
				Name: m[1], Version: strings.TrimPrefix(m[2], "v"), Raw: m[2],
				Ecosystem: "go", Dev: strings.Contains(line, "// indirect"),
				Manifest: "go.mod",
			})
		}
	}
	return out
}

var pipReq = regexp.MustCompile(`^([A-Za-z0-9_.\-]+)\s*(?:[=<>!~]=?\s*([0-9][0-9A-Za-z.\-*]*))?`)

func readRequirementsTxt(repoRoot string) []Dependency {
	b, err := os.ReadFile(filepath.Join(repoRoot, "requirements.txt"))
	if err != nil {
		return nil
	}
	var out []Dependency
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if m := pipReq.FindStringSubmatch(line); m != nil && m[1] != "" {
			out = append(out, Dependency{
				Name: m[1], Version: normalizeVersion(m[2]), Raw: m[2],
				Ecosystem: "pip", Manifest: "requirements.txt",
			})
		}
	}
	return out
}

func readPyProject(repoRoot string) []Dependency {
	b, err := os.ReadFile(filepath.Join(repoRoot, "pyproject.toml"))
	if err != nil {
		return nil
	}
	var out []Dependency
	// Lightweight TOML scan for [project] dependencies = ["pkg>=1.2", ...] and
	// [tool.poetry.dependencies] pkg = "^1.2" forms. Avoids a TOML dependency.
	text := string(b)
	for _, m := range regexp.MustCompile(`"([A-Za-z0-9_.\-]+)\s*(?:[=<>!~]=?\s*([0-9][0-9A-Za-z.\-*]*))?"`).FindAllStringSubmatch(text, -1) {
		if m[1] == "" {
			continue
		}
		out = append(out, Dependency{
			Name: m[1], Version: normalizeVersion(m[2]), Raw: m[2],
			Ecosystem: "pip", Manifest: "pyproject.toml",
		})
	}
	return out
}

func readComposerJSON(repoRoot string) []Dependency {
	b, err := os.ReadFile(filepath.Join(repoRoot, "composer.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return nil
	}
	var out []Dependency
	add := func(m map[string]string, dev bool) {
		for name, raw := range m {
			if name == "php" || strings.HasPrefix(name, "ext-") {
				continue
			}
			out = append(out, Dependency{
				Name: name, Version: normalizeVersion(raw), Raw: raw,
				Ecosystem: "composer", Dev: dev, Manifest: "composer.json",
			})
		}
	}
	add(pkg.Require, false)
	add(pkg.RequireDev, true)
	return out
}

func readCargoToml(repoRoot string) []Dependency {
	b, err := os.ReadFile(filepath.Join(repoRoot, "Cargo.toml"))
	if err != nil {
		return nil
	}
	var out []Dependency
	section := ""
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.Trim(t, "[]")
			continue
		}
		if section != "dependencies" && section != "dev-dependencies" {
			continue
		}
		eq := strings.Index(t, "=")
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(t[:eq])
		rest := strings.TrimSpace(t[eq+1:])
		var ver string
		if strings.HasPrefix(rest, "\"") {
			ver = strings.Trim(rest, "\"")
		} else if m := regexp.MustCompile(`version\s*=\s*"([^"]+)"`).FindStringSubmatch(rest); m != nil {
			ver = m[1]
		}
		out = append(out, Dependency{
			Name: name, Version: normalizeVersion(ver), Raw: ver,
			Ecosystem: "cargo", Dev: section == "dev-dependencies",
			Manifest: "Cargo.toml",
		})
	}
	return out
}

func dedupeDeps(in []Dependency) []Dependency {
	seen := map[string]struct{}{}
	var out []Dependency
	for _, d := range in {
		if strings.TrimSpace(d.Name) == "" {
			continue
		}
		key := d.Ecosystem + "|" + strings.ToLower(d.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, d)
	}
	return out
}
