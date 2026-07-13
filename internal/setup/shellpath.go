package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const pathMarker = "# codehelper PATH"

// EnsureUserPath prepends binDir to the user shell PATH persistently (idempotent).
// Returns true when a profile file or Windows user PATH was updated.
func EnsureUserPath(binDir string) (bool, error) {
	binDir = filepath.Clean(strings.TrimSpace(binDir))
	if binDir == "" {
		return false, nil
	}
	if runtime.GOOS == "windows" {
		return ensurePlatformUserPath(binDir)
	}
	if inPathList(os.Getenv("PATH"), binDir) {
		return false, nil
	}
	return ensurePlatformUserPath(binDir)
}

func fishConfig(home string) (string, bool) {
	if strings.Contains(strings.ToLower(os.Getenv("SHELL")), "fish") {
		return filepath.Join(home, ".config", "fish", "config.fish"), true
	}
	p := filepath.Join(home, ".config", "fish", "config.fish")
	return p, fileExists(p)
}

func hasMarker(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), pathMarker)
}

func appendFile(path, block string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block)
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func inPathList(pathEnv, dir string) bool {
	dir = filepath.Clean(dir)
	for _, p := range filepath.SplitList(pathEnv) {
		if filepath.Clean(p) == dir {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"'$\\") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
