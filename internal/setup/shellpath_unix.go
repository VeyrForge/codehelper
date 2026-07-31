//go:build !windows

package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ensureWindowsUserPath(binDir string) (bool, error) {
	return false, nil
}

func ensurePlatformUserPath(binDir string) (bool, error) {
	return ensureUnixUserPath(binDir)
}

func ensureUnixUserPath(binDir string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	if inPathList(os.Getenv("PATH"), binDir) {
		return false, nil
	}
	if fishPath, ok := fishConfig(home); ok {
		if hasMarker(fishPath) {
			return false, nil
		}
		block := fmt.Sprintf("\n%s\nfish_add_path -g %s\n", pathMarker, shellQuote(binDir))
		if err := appendFile(fishPath, block); err != nil {
			return false, err
		}
		return true, nil
	}
	target := pickUnixProfile(home)
	if target == "" {
		target = filepath.Join(home, ".profile")
	}
	if hasMarker(target) {
		return false, nil
	}
	block := fmt.Sprintf("\n%s\nexport PATH=%s:$PATH\n", pathMarker, shellQuote(binDir))
	if err := appendFile(target, block); err != nil {
		return false, err
	}
	return true, nil
}

func pickUnixProfile(home string) string {
	shell := strings.ToLower(strings.TrimSpace(os.Getenv("SHELL")))
	switch {
	case strings.Contains(shell, "zsh"), fileExists(filepath.Join(home, ".zshrc")):
		return filepath.Join(home, ".zshrc")
	case strings.Contains(shell, "bash"), fileExists(filepath.Join(home, ".bashrc")):
		return filepath.Join(home, ".bashrc")
	case fileExists(filepath.Join(home, ".profile")):
		return filepath.Join(home, ".profile")
	}
	return filepath.Join(home, ".profile")
}
