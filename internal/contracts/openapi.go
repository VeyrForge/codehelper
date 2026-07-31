package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// OpenAPIContract is a lightweight discovery record for OpenAPI/Swagger specs.
// It is a hook for cross-repo API edges — not a full schema validator.
type OpenAPIContract struct {
	Path     string   `json:"path"`
	Format   string   `json:"format"` // json | yaml
	Title    string   `json:"title,omitempty"`
	Version  string   `json:"version,omitempty"`
	APIPaths []string `json:"api_paths,omitempty"`
	RepoHint string   `json:"repo_hint,omitempty"`
}

var (
	yamlTitleRe   = regexp.MustCompile(`(?m)^\s*title:\s*["']?([^"'\n#]+)`)
	yamlVersionRe = regexp.MustCompile(`(?m)^\s*version:\s*["']?([^"'\n#]+)`)
	yamlPathKeyRe = regexp.MustCompile(`(?m)^(\s*)(/[A-Za-z0-9_{}/.\-]+):\s*(?:#.*)?$`)
)

// DiscoverOpenAPI finds OpenAPI/Swagger files under root (common paths + shallow dirs).
// It does not recurse the whole tree and does not fetch runtime-generated specs
// (e.g. FastAPI /openapi.json served only at request time).
func DiscoverOpenAPI(root string) []OpenAPIContract {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	candidates := []string{
		"openapi.json", "openapi.yaml", "openapi.yml",
		"swagger.json", "swagger.yaml", "swagger.yml",
		"api/openapi.json", "api/openapi.yaml", "api/openapi.yml",
		"api/swagger.json", "api/swagger.yaml", "api/swagger.yml",
		"docs/openapi.json", "docs/openapi.yaml", "docs/openapi.yml",
		"docs/swagger.json", "docs/swagger.yaml", "docs/swagger.yml",
		"spec/openapi.json", "spec/openapi.yaml", "spec/openapi.yml",
		"spec/swagger.json", "spec/swagger.yaml",
		"openapi/openapi.json", "openapi/openapi.yaml", "openapi/openapi.yml",
		"swagger/swagger.json", "swagger/swagger.yaml", "swagger/swagger.yml",
		"schemas/openapi.json", "schemas/openapi.yaml", "schemas/openapi.yml",
	}
	seen := map[string]struct{}{}
	var out []OpenAPIContract
	add := func(abs string) {
		c, ok := parseOpenAPIFile(abs)
		if !ok {
			return
		}
		key := filepath.ToSlash(c.Path)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	for _, rel := range candidates {
		add(filepath.Join(root, filepath.FromSlash(rel)))
	}
	for _, dir := range []string{".", "openapi", "swagger", "api", "docs", "spec", "schemas", "src"} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		if dir == "." {
			base = root
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if !looksLikeOpenAPIName(name) {
				continue
			}
			switch {
			case strings.HasSuffix(name, ".json"),
				strings.HasSuffix(name, ".yaml"),
				strings.HasSuffix(name, ".yml"):
				add(filepath.Join(base, e.Name()))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func looksLikeOpenAPIName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "openapi") || strings.Contains(name, "swagger")
}

func parseOpenAPIFile(abs string) (OpenAPIContract, bool) {
	b, err := os.ReadFile(abs)
	if err != nil || len(b) == 0 {
		return OpenAPIContract{}, false
	}
	ext := strings.ToLower(filepath.Ext(abs))
	switch ext {
	case ".json":
		c, ok := parseOpenAPIJSON(abs, b)
		return c, ok
	case ".yaml", ".yml":
		c, ok := parseOpenAPIYAMLLite(abs, b)
		return c, ok
	default:
		return OpenAPIContract{}, false
	}
}

func parseOpenAPIJSON(abs string, b []byte) (OpenAPIContract, bool) {
	var doc struct {
		OpenAPI string `json:"openapi"`
		Swagger string `json:"swagger"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return OpenAPIContract{}, false
	}
	if doc.OpenAPI == "" && doc.Swagger == "" && len(doc.Paths) == 0 {
		return OpenAPIContract{}, false
	}
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return OpenAPIContract{
		Path:     abs,
		Format:   "json",
		Title:    strings.TrimSpace(doc.Info.Title),
		Version:  strings.TrimSpace(doc.Info.Version),
		APIPaths: paths,
	}, true
}

// parseOpenAPIYAMLLite extracts title/version/path keys without a YAML dependency.
func parseOpenAPIYAMLLite(abs string, b []byte) (OpenAPIContract, bool) {
	text := string(b)
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "openapi:") && !strings.Contains(lower, "swagger:") && !strings.Contains(lower, "\npaths:") {
		return OpenAPIContract{}, false
	}
	c := OpenAPIContract{Path: abs, Format: "yaml"}
	if m := yamlTitleRe.FindStringSubmatch(text); len(m) >= 2 {
		c.Title = strings.TrimSpace(m[1])
	}
	if m := yamlVersionRe.FindStringSubmatch(text); len(m) >= 2 {
		c.Version = strings.TrimSpace(m[1])
	}
	inPaths := false
	pathsIndent := -1
	lines := strings.Split(text, "\n")
	var paths []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		indent := countLeadingSpaces(line)
		if strings.HasPrefix(trim, "paths:") {
			inPaths = true
			pathsIndent = indent
			continue
		}
		if !inPaths {
			continue
		}
		if indent <= pathsIndent && !strings.HasPrefix(trim, "/") {
			inPaths = false
			continue
		}
		if m := yamlPathKeyRe.FindStringSubmatch(line); len(m) >= 3 {
			paths = append(paths, strings.TrimSpace(m[2]))
		}
	}
	sort.Strings(paths)
	c.APIPaths = uniqueStrings(paths)
	return c, true
}

func countLeadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
			continue
		}
		if r == '\t' {
			n += 2
			continue
		}
		break
	}
	return n
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
