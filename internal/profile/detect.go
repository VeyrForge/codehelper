package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file centralises project-type and version detection. The goal is that
// project_context never reports a bare "unknown" when a recognizable manifest or
// engine marker is present, and that it reports the concrete version of whatever
// defines the project — game engine (Godot/Unity/Unreal), language runtime
// (Go/Rust/Python/Node), or web framework (Laravel/React/Vue).

// --- version extractors (file-format specific) -----------------------------

var (
	goVerRe     = regexp.MustCompile(`(?m)^go[ \t]+([0-9][0-9.]*)`)
	unityVerRe  = regexp.MustCompile(`m_EditorVersion:\s*([^\s]+)`)
	godotFeatRe = regexp.MustCompile(`config/features\s*=\s*PackedStringArray\(\s*"([^"]+)"`)
	cargoEdRe   = regexp.MustCompile(`(?m)^\s*edition\s*=\s*"([^"]+)"`)
	cargoRustRe = regexp.MustCompile(`(?m)^\s*rust-version\s*=\s*"([^"]+)"`)
	pyReqRe     = regexp.MustCompile(`requires-python\s*=\s*"([^"]+)"`)
)

// cleanVer trims constraint noise (^ ~ >= leading v, surrounding space/quotes)
// so a dependency spec like "^12.0" or ">=8.2" reads as a version, while keeping
// the meaningful digits. Also strips trailing quote/comma leftovers from TOML
// list parsing (e.g. `django>=4.2",` → `4.2`).
func cleanVer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`+"`")
	s = strings.TrimRight(s, ",]")
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "^~>=<! vV")
	s = strings.Trim(s, `"'`+"`")
	return strings.TrimSpace(s)
}

func goModVersion(content string) string {
	if m := goVerRe.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return ""
}

func unityEditorVersion(content string) string {
	if m := unityVerRe.FindStringSubmatch(content); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// godotVersion returns the engine version from project.godot's feature list,
// e.g. config/features=PackedStringArray("4.7", "Mobile") -> "4.7".
func godotVersion(content string) string {
	if m := godotFeatRe.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return ""
}

func cargoVersion(content string) string {
	if m := cargoRustRe.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	if m := cargoEdRe.FindStringSubmatch(content); m != nil {
		return "edition " + m[1]
	}
	return ""
}

func pythonRequires(content string) string {
	if m := pyReqRe.FindStringSubmatch(content); m != nil {
		return cleanVer(m[1])
	}
	return ""
}

var mixElixirRe = regexp.MustCompile(`elixir:\s*"([^"]+)"`)

// mixElixirVersion reads the elixir: requirement from mix.exs (project/ or @elixir_requirement).
func mixElixirVersion(content string) string {
	if m := regexp.MustCompile(`@elixir_requirement\s+"([^"]+)"`).FindStringSubmatch(content); m != nil {
		return cleanVer(m[1])
	}
	if m := mixElixirRe.FindStringSubmatch(content); m != nil {
		return cleanVer(m[1])
	}
	return ""
}

// jsonDep returns the version spec of a dependency from a package.json's
// dependencies or devDependencies.
func jsonDep(content, name string) string {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Engines         map[string]string `json:"engines"`
	}
	if json.Unmarshal([]byte(content), &pkg) != nil {
		return ""
	}
	if name == "node" {
		return cleanVer(pkg.Engines["node"])
	}
	if v, ok := pkg.Dependencies[name]; ok {
		return cleanVer(v)
	}
	if v, ok := pkg.DevDependencies[name]; ok {
		return cleanVer(v)
	}
	return ""
}

// composerRequire returns the version spec of a composer require entry (e.g.
// "php" or "laravel/framework").
func composerRequire(content, name string) string {
	var c struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if json.Unmarshal([]byte(content), &c) != nil {
		return ""
	}
	if v, ok := c.Require[name]; ok {
		return cleanVer(v)
	}
	if v, ok := c.RequireDev[name]; ok {
		return cleanVer(v)
	}
	return ""
}

// --- game-engine detection (returns matched, version) ----------------------

// detectUnreal looks for a .uproject (Unreal) in the repo root and reads its
// EngineAssociation. Returns ("", false) when none.
func detectUnreal(repoRoot string) (string, bool) {
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".uproject") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(repoRoot, e.Name()))
		if rerr != nil {
			return "", true
		}
		var up struct {
			EngineAssociation string `json:"EngineAssociation"`
		}
		_ = json.Unmarshal(b, &up)
		return strings.TrimSpace(up.EngineAssociation), true
	}
	return "", false
}

// detectUnity reads ProjectSettings/ProjectVersion.txt (m_EditorVersion).
func detectUnity(repoRoot string) (string, bool) {
	p := filepath.Join(repoRoot, "ProjectSettings", "ProjectVersion.txt")
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return unityEditorVersion(string(b)), true
}

// detectGodot reads project.godot (config/features version).
func detectGodot(repoRoot string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "project.godot"))
	if err != nil {
		return "", false
	}
	return godotVersion(string(b)), true
}

// primaryVersionFor picks the version that best characterizes a resolved
// ProjectType from the collected per-tech versions.
func primaryVersionFor(projectType string, versions map[string]string) string {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := versions[k]; v != "" {
				return v
			}
		}
		return ""
	}
	switch projectType {
	case "go":
		return pick("go")
	case "rust":
		return pick("rust")
	case "python":
		return pick("python")
	case "laravel":
		return pick("laravel", "php")
	case "php_composer", "wordpress_woocommerce":
		return pick("php")
	case "node":
		return pick("react", "vue", "next", "node")
	}
	return ""
}
