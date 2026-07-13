package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
)

// EnvContext is toolchain and alias metadata for a project.
type EnvContext struct {
	RepoRoot    string            `json:"repo_root"`
	Toolchain   map[string]string `json:"toolchain,omitempty"`
	Scripts     []string          `json:"scripts,omitempty"`
	MakeTargets []string          `json:"make_targets,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	LogSources  []string          `json:"log_sources,omitempty"`
	DockerHint  bool              `json:"docker_compose,omitempty"`
}

// DetectEnv scans project files for toolchain versions and configured aliases.
func DetectEnv(repoRoot string) (*EnvContext, error) {
	repoRoot = filepath.Clean(repoRoot)
	out := &EnvContext{RepoRoot: repoRoot, Toolchain: map[string]string{}}
	readVersionFile := func(path, key string) {
		b, err := os.ReadFile(filepath.Join(repoRoot, path))
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				out.Toolchain[key+" "+parts[0]] = parts[1]
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(repoRoot, "go.mod")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "go ") {
				out.Toolchain["go"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "go "))
				break
			}
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "package.json")); err == nil {
		out.Toolchain["node"] = "package.json present"
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if b, err := os.ReadFile(filepath.Join(repoRoot, "package.json")); err == nil {
			if json.Unmarshal(b, &pkg) == nil {
				for k := range pkg.Scripts {
					out.Scripts = append(out.Scripts, k)
				}
			}
		}
	}
	readVersionFile(".tool-versions", "asdf")
	if _, err := os.Stat(filepath.Join(repoRoot, "mise.toml")); err == nil {
		out.Toolchain["mise"] = "mise.toml"
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "docker-compose.yml")); err == nil {
		out.DockerHint = true
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "docker-compose.yaml")); err == nil {
		out.DockerHint = true
	}
	if b, err := os.ReadFile(filepath.Join(repoRoot, "Makefile")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasSuffix(line, ":") || strings.HasPrefix(line, ".") || strings.HasPrefix(line, "#") {
				continue
			}
			name := strings.TrimSuffix(line, ":")
			if idx := strings.Index(name, ":"); idx >= 0 {
				name = name[:idx]
			}
			if name != "" && !strings.Contains(name, " ") {
				out.MakeTargets = append(out.MakeTargets, name)
			}
			if len(out.MakeTargets) >= 20 {
				break
			}
		}
	}
	cfg, _ := connections.Load(repoRoot)
	for _, a := range cfg.Aliases {
		out.Aliases = append(out.Aliases, a.Name)
	}
	for _, l := range cfg.LogSources {
		if !l.Disabled {
			out.LogSources = append(out.LogSources, l.Name+" ("+l.Kind+")")
		}
	}
	return out, nil
}
