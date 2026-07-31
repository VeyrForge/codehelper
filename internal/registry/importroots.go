package registry

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var cargoNameRe = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)

// DetectImportRoots reads common manifests under root and returns module/package
// paths that ResolveImportOwners can match against import strings.
func DetectImportRoots(root string) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	var out []string
	if m := readGoModule(root); m != "" {
		out = append(out, m)
	}
	if n := readJSONName(filepath.Join(root, "package.json")); n != "" {
		out = append(out, n)
	}
	if n := readCargoPackageName(root); n != "" {
		out = append(out, n)
	}
	if n := readJSONName(filepath.Join(root, "composer.json")); n != "" {
		out = append(out, n)
	}
	if n := readPyProjectName(root); n != "" {
		out = append(out, n)
	}
	return uniqueNonEmpty(out)
}

func readGoModule(root string) string {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func readJSONName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return ""
	}
	return strings.TrimSpace(obj.Name)
}

func readCargoPackageName(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "Cargo.toml"))
	if err != nil {
		return ""
	}
	text := string(b)
	idx := strings.Index(text, "[package]")
	if idx < 0 {
		return ""
	}
	section := text[idx:]
	if end := strings.Index(section[1:], "["); end > 0 {
		section = section[:end+1]
	}
	m := cargoNameRe.FindStringSubmatch(section)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func readPyProjectName(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return ""
	}
	text := string(b)
	for _, section := range []string{"[project]", "[tool.poetry]"} {
		idx := strings.Index(text, section)
		if idx < 0 {
			continue
		}
		chunk := text[idx:]
		if end := strings.Index(chunk[1:], "["); end > 0 {
			chunk = chunk[:end+1]
		}
		m := cargoNameRe.FindStringSubmatch(chunk)
		if len(m) >= 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func uniqueNonEmpty(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}
