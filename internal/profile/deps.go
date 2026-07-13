package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxDeps caps how many dependencies are persisted, so a manifest with hundreds
// of transitive-ish entries can't bloat the profile. The most relevant direct
// deps come first (manifests list direct deps), and they are sorted by name.
const maxDeps = 200

// collectDependencies reads every dependency manifest present at the repo root and
// returns the declared direct dependencies with their version constraints, tagged
// by ecosystem. It parses manifests, not lockfiles, so the versions are the ones
// the project asked for (what a human reads as "its dependencies").
func collectDependencies(repoRoot string) []Dependency {
	var out []Dependency
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			return ""
		}
		return string(b)
	}

	if c := read("go.mod"); c != "" {
		out = append(out, goModDeps(c)...)
	}
	if c := read("package.json"); c != "" {
		out = append(out, packageJSONDeps(c)...)
	}
	if c := read("composer.json"); c != "" {
		out = append(out, composerDeps(c)...)
	}
	if c := read("Cargo.toml"); c != "" {
		out = append(out, cargoDeps(c)...)
	}
	if c := read("requirements.txt"); c != "" {
		out = append(out, requirementsDeps(c)...)
	}
	if c := read("pyproject.toml"); c != "" {
		out = append(out, pyprojectDeps(c)...)
	}
	if c := read("Gemfile"); c != "" {
		out = append(out, gemfileDeps(c)...)
	}
	if c := read("pom.xml"); c != "" {
		out = append(out, pomDeps(c)...)
	}

	// Stable order: ecosystem, then name. Dedup by (ecosystem,name).
	seen := map[string]bool{}
	uniq := out[:0]
	for _, d := range out {
		k := d.Ecosystem + "\x00" + d.Name
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, d)
	}
	sort.Slice(uniq, func(i, j int) bool {
		if uniq[i].Ecosystem != uniq[j].Ecosystem {
			return uniq[i].Ecosystem < uniq[j].Ecosystem
		}
		return uniq[i].Name < uniq[j].Name
	})
	if len(uniq) > maxDeps {
		uniq = uniq[:maxDeps]
	}
	return uniq
}

var goRequireLineRe = regexp.MustCompile(`^\s*([^\s/][^\s]*\.[^\s]+/\S+|[^\s]+\.\S+)\s+(v[0-9][^\s]*)`)

// goModDeps parses require entries (both the block and single-line form). Only
// direct requires are kept (lines with a trailing "// indirect" are skipped).
func goModDeps(content string) []Dependency {
	var deps []Dependency
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "require (") {
			inBlock = true
			continue
		}
		if inBlock && t == ")" {
			inBlock = false
			continue
		}
		s := t
		if !inBlock {
			if !strings.HasPrefix(s, "require ") {
				continue
			}
			s = strings.TrimPrefix(s, "require ")
		}
		if strings.Contains(s, "// indirect") {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "v") {
			deps = append(deps, Dependency{Name: fields[0], Version: strings.TrimPrefix(fields[1], "v"), Ecosystem: "go"})
		}
	}
	return deps
}

func packageJSONDeps(content string) []Dependency {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal([]byte(content), &pkg) != nil {
		return nil
	}
	var deps []Dependency
	for n, v := range pkg.Dependencies {
		deps = append(deps, Dependency{Name: n, Version: cleanVer(v), Ecosystem: "npm"})
	}
	for n, v := range pkg.DevDependencies {
		deps = append(deps, Dependency{Name: n, Version: cleanVer(v), Ecosystem: "npm", Dev: true})
	}
	return deps
}

func composerDeps(content string) []Dependency {
	var c struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if json.Unmarshal([]byte(content), &c) != nil {
		return nil
	}
	var deps []Dependency
	add := func(m map[string]string, dev bool) {
		for n, v := range m {
			// Skip platform requirements (php, ext-*, lib-*) — they aren't packages.
			if n == "php" || strings.HasPrefix(n, "ext-") || strings.HasPrefix(n, "lib-") {
				continue
			}
			deps = append(deps, Dependency{Name: n, Version: cleanVer(v), Ecosystem: "composer", Dev: dev})
		}
	}
	add(c.Require, false)
	add(c.RequireDev, true)
	return deps
}

// cargoDeps parses [dependencies] / [dev-dependencies] tables. It handles the two
// common forms: `name = "1.2"` and `name = { version = "1.2", ... }`.
func cargoDeps(content string) []Dependency {
	var deps []Dependency
	section := ""
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.Trim(t, "[]")
			continue
		}
		dev := section == "dev-dependencies"
		if section != "dependencies" && !dev {
			continue
		}
		eq := strings.Index(t, "=")
		if eq < 0 || strings.HasPrefix(t, "#") {
			continue
		}
		name := strings.TrimSpace(t[:eq])
		rest := strings.TrimSpace(t[eq+1:])
		ver := ""
		if strings.HasPrefix(rest, "{") {
			if m := cargoInlineVerRe.FindStringSubmatch(rest); m != nil {
				ver = m[1]
			}
		} else {
			ver = strings.Trim(rest, `"`)
		}
		if name != "" {
			deps = append(deps, Dependency{Name: name, Version: cleanVer(ver), Ecosystem: "cargo", Dev: dev})
		}
	}
	return deps
}

var cargoInlineVerRe = regexp.MustCompile(`version\s*=\s*"([^"]+)"`)

var reqLineRe = regexp.MustCompile(`^([A-Za-z0-9._-]+)\s*(?:\[[^\]]*\])?\s*([<>=!~]=?[^;#]+)?`)

func requirementsDeps(content string) []Dependency {
	var deps []Dependency
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "-") {
			continue
		}
		if m := reqLineRe.FindStringSubmatch(t); m != nil && m[1] != "" {
			deps = append(deps, Dependency{Name: m[1], Version: cleanVer(m[2]), Ecosystem: "pip"})
		}
	}
	return deps
}

// pyprojectDeps parses PEP 621 [project] dependencies = ["pkg>=1", ...] and the
// Poetry [tool.poetry.dependencies] table.
func pyprojectDeps(content string) []Dependency {
	var deps []Dependency
	section := ""
	inList := false
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.Trim(t, "[]")
			inList = false
			continue
		}
		if section == "project" && strings.HasPrefix(t, "dependencies") && strings.Contains(t, "[") {
			inList = true
			t = t[strings.Index(t, "[")+1:]
		}
		if inList {
			for _, item := range strings.Split(t, ",") {
				s := strings.Trim(strings.TrimSpace(item), `"'[]`)
				if s == "" {
					continue
				}
				if m := reqLineRe.FindStringSubmatch(s); m != nil && m[1] != "" {
					deps = append(deps, Dependency{Name: m[1], Version: cleanVer(m[2]), Ecosystem: "pip"})
				}
			}
			if strings.Contains(t, "]") {
				inList = false
			}
			continue
		}
		if section == "tool.poetry.dependencies" {
			eq := strings.Index(t, "=")
			if eq < 0 || strings.HasPrefix(t, "#") {
				continue
			}
			name := strings.TrimSpace(t[:eq])
			if name == "" || strings.EqualFold(name, "python") {
				continue
			}
			ver := strings.Trim(strings.TrimSpace(t[eq+1:]), `"'`)
			deps = append(deps, Dependency{Name: name, Version: cleanVer(ver), Ecosystem: "pip"})
		}
	}
	return deps
}

var gemfileRe = regexp.MustCompile(`^\s*gem\s+['"]([^'"]+)['"]\s*(?:,\s*['"]([^'"]+)['"])?`)

func gemfileDeps(content string) []Dependency {
	var deps []Dependency
	for _, line := range strings.Split(content, "\n") {
		if m := gemfileRe.FindStringSubmatch(line); m != nil {
			deps = append(deps, Dependency{Name: m[1], Version: cleanVer(m[2]), Ecosystem: "rubygems"})
		}
	}
	return deps
}

var (
	pomDepRe     = regexp.MustCompile(`(?s)<dependency>(.*?)</dependency>`)
	pomArtifact  = regexp.MustCompile(`<artifactId>([^<]+)</artifactId>`)
	pomGroup     = regexp.MustCompile(`<groupId>([^<]+)</groupId>`)
	pomVersionRe = regexp.MustCompile(`<version>([^<]+)</version>`)
)

func pomDeps(content string) []Dependency {
	var deps []Dependency
	for _, m := range pomDepRe.FindAllStringSubmatch(content, -1) {
		block := m[1]
		art := pomArtifact.FindStringSubmatch(block)
		if art == nil {
			continue
		}
		name := art[1]
		if g := pomGroup.FindStringSubmatch(block); g != nil {
			name = g[1] + ":" + name
		}
		ver := ""
		if v := pomVersionRe.FindStringSubmatch(block); v != nil {
			ver = strings.TrimSpace(v[1])
		}
		deps = append(deps, Dependency{Name: name, Version: cleanVer(ver), Ecosystem: "maven"})
	}
	return deps
}
