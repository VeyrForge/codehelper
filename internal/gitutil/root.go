package gitutil

import (
	"fmt"
	"path/filepath"
)

// FindGitRoot walks up from start until a git work tree is found.
func FindGitRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if IsGitRepo(dir) {
			return filepath.Clean(dir), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no git repository found above %s", start)
		}
		dir = parent
	}
}
