// Package devsource locates the local codehelper git checkout used by
// `codehelper update` so the binary can be rebuilt from anywhere.
package devsource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/registry"
)

const (
	// EnvSource is an absolute (or expandable) path to the codehelper source tree.
	EnvSource = "CODEHELPER_SOURCE"
	// rememberedFile is stored under ~/.codehelper/ after a successful update.
	rememberedFile = "source_path"
)

// PathFile returns ~/.codehelper/source_path.
func PathFile() (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, rememberedFile), nil
}

// IsSourceTree reports whether dir looks like a codehelper source checkout
// (go.mod module ending in /codehelper plus ./cmd/codehelper).
func IsSourceTree(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	modPath := filepath.Join(abs, "go.mod")
	mod, err := os.ReadFile(modPath)
	if err != nil {
		return false
	}
	if !moduleLooksLikeCodehelper(string(mod)) {
		return false
	}
	mainPkg := filepath.Join(abs, "cmd", "codehelper")
	st, err := os.Stat(mainPkg)
	return err == nil && st.IsDir()
}

func moduleLooksLikeCodehelper(goMod string) bool {
	for _, line := range strings.Split(goMod, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		mod := strings.TrimSpace(strings.TrimPrefix(line, "module "))
		return strings.HasSuffix(mod, "/codehelper") || mod == "codehelper"
	}
	return false
}

// Remember persists dir as the preferred source checkout for future updates.
func Remember(dir string) error {
	abs, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return err
	}
	if !IsSourceTree(abs) {
		return fmt.Errorf("not a codehelper source tree: %s", abs)
	}
	p, err := PathFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(abs+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadRemembered returns the persisted source path, or "" if unset/invalid.
func LoadRemembered() string {
	p, err := PathFile()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(b))
	if !IsSourceTree(dir) {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return abs
}

// Resolve finds the codehelper source tree to rebuild from.
//
// Order:
//  1. explicitPath (CLI arg), after resolving to a git root when applicable
//  2. CODEHELPER_SOURCE
//  3. ~/.codehelper/source_path from a prior successful update
//  4. cwd / startPath when it is already the source tree
//  5. any registered project that looks like the source tree
func Resolve(explicitPath, startPath string) (string, error) {
	try := func(raw, via string) (string, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", nil
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("codehelper source (%s): %w", via, err)
		}
		// Prefer git root when inside a checkout; fall back to abs if not a git repo.
		root := abs
		if gr, err := gitutil.FindGitRoot(abs); err == nil {
			root = gr
		}
		if !IsSourceTree(root) {
			return "", fmt.Errorf("path %q (%s) is not a codehelper source tree (need go.mod module …/codehelper and cmd/codehelper/)", root, via)
		}
		return root, nil
	}

	if explicitPath != "" {
		root, err := try(explicitPath, "argument")
		if err != nil {
			return "", err
		}
		return root, nil
	}
	if env := strings.TrimSpace(os.Getenv(EnvSource)); env != "" {
		root, err := try(env, EnvSource)
		if err != nil {
			return "", err
		}
		return root, nil
	}
	if remembered := LoadRemembered(); remembered != "" {
		return remembered, nil
	}
	if startPath == "" {
		if wd, err := os.Getwd(); err == nil {
			startPath = wd
		}
	}
	if startPath != "" {
		if root, err := try(startPath, "current directory"); err == nil && root != "" {
			return root, nil
		}
		// Not an error yet — fall through to registry scan.
		if gr, err := gitutil.FindGitRoot(startPath); err == nil && IsSourceTree(gr) {
			return gr, nil
		}
	}
	if reg, err := registry.Load(); err == nil {
		for _, e := range reg.List() {
			if IsSourceTree(e.RootPath) {
				abs, aerr := filepath.Abs(e.RootPath)
				if aerr != nil {
					continue
				}
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("could not locate codehelper source tree — pass the checkout path (`codehelper update /path/to/codehelper`), set %s, or run update once from the checkout so it is remembered in ~/.codehelper/%s", EnvSource, rememberedFile)
}
