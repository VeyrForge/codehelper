package mcpsvc

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/projcfg"
)

// verifyWorkspace holds cwd + commands for diagnostics/verify in a repo.
type verifyWorkspace struct {
	Cwd       string
	Toolchain string
	Cmds      []string
	Allowed   []string
	Source    string // projcfg | root | subproject
}

// resolveVerifyWorkspace picks the working directory and static-check commands.
func resolveVerifyWorkspace(repoRoot string) verifyWorkspace {
	repoRoot = filepath.Clean(repoRoot)
	cfg, _ := projcfg.Load(repoRoot)

	cwd := repoRoot
	if sp := strings.TrimSpace(cfg.VerifyCwd); sp != "" {
		cwd = filepath.Join(repoRoot, filepath.FromSlash(sp))
	}

	if b, t, l := strings.TrimSpace(cfg.VerifyBuild), strings.TrimSpace(cfg.VerifyTest), strings.TrimSpace(cfg.VerifyLint); b != "" || t != "" || l != "" {
		var cmds []string
		if l != "" {
			cmds = append(cmds, l)
		}
		if b != "" {
			cmds = append(cmds, b)
		}
		if t != "" {
			cmds = append(cmds, t)
		}
		return verifyWorkspace{
			Cwd: cwd, Toolchain: "project_config", Cmds: cmds,
			Allowed: allowedForCommands(cmds), Source: "projcfg",
		}
	}

	if tc, cmds, allowed, ok := toolchainAt(cwd); ok {
		src := "root"
		if cwd != repoRoot {
			rel, _ := filepath.Rel(repoRoot, cwd)
			src = "subproject:" + filepath.ToSlash(rel)
		}
		return verifyWorkspace{Cwd: cwd, Toolchain: tc, Cmds: cmds, Allowed: allowed, Source: src}
	}

	if cwd == repoRoot {
		if pr, err := profile.ReadOrGenerate(repoRoot); err == nil && pr != nil {
			for _, sp := range pr.SubProjects {
				subRoot := filepath.Join(repoRoot, filepath.FromSlash(sp.Path))
				if tc, cmds, allowed, ok := toolchainAt(subRoot); ok {
					return verifyWorkspace{
						Cwd: subRoot, Toolchain: tc + "@" + sp.Path, Cmds: cmds,
						Allowed: allowed, Source: "subproject:" + sp.Path,
					}
				}
			}
		}
	}

	return verifyWorkspace{Cwd: cwd}
}

func toolchainAt(dir string) (name string, cmds, allowed []string, ok bool) {
	for _, tc := range orderedToolchains {
		if fileExists(filepath.Join(dir, tc.marker)) {
			return tc.name, append([]string(nil), tc.cmds...), append([]string(nil), tc.allowed...), true
		}
	}
	return "", nil, nil, false
}

func allowedForCommands(cmds []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, cmdline := range cmds {
		fields := strings.Fields(cmdline)
		if len(fields) == 0 {
			continue
		}
		base := filepath.Base(fields[0])
		if base != "" && !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}
	return out
}

func verifyWorkspaceNote(ws verifyWorkspace) string {
	if len(ws.Cmds) == 0 {
		return "no toolchain auto-detected (looked for go.mod, Cargo.toml, tsconfig.json, phpstan.neon, pyproject.toml/setup.py/requirements.txt, pom.xml, build.gradle, and nested sub-projects). Pass an explicit `command` to diagnostics or set verify_cwd / verify_build via `codehelper config project`."
	}
	if ws.Source != "" && ws.Source != "root" {
		return "using verify workspace " + ws.Source + " (cwd=" + ws.Cwd + ")"
	}
	return ""
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
