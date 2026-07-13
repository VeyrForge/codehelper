package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

// ResolveIndexPaths returns git work tree root and directory to index (may equal git root).
func ResolveIndexPaths(pathArg, indexSubdir string) (gitRoot string, indexRoot string, err error) {
	pathArg, err = filepath.Abs(pathArg)
	if err != nil {
		return "", "", err
	}
	gitRoot, err = gitutil.FindGitRoot(pathArg)
	if err != nil {
		return "", "", err
	}
	indexRoot = gitRoot
	sub := strings.TrimSpace(indexSubdir)
	if sub != "" && sub != "." {
		sub = filepath.Clean(sub)
		if sub == ".." || strings.HasPrefix(sub, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("invalid index subdirectory %q", indexSubdir)
		}
		indexRoot = filepath.Join(gitRoot, sub)
	}
	if fi, err := os.Stat(indexRoot); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("index root not a directory: %s", indexRoot)
	}
	return gitRoot, indexRoot, nil
}
