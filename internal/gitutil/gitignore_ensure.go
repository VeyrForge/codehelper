package gitutil

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
	gitignore "github.com/sabhiram/go-gitignore"
)

const codehelperIgnoreLine = ".codehelper/"

type gitignoreCacheEntry struct {
	modTime time.Time
	ok      bool
}

var gitignoreEnsureCache sync.Map

// EnsureCodehelperGitignored appends ".codehelper/" to the repo-root .gitignore
// when that file exists and does not already ignore the index directory.
// It never creates a new .gitignore. Returns true when the file was updated.
func EnsureCodehelperGitignored(startPath string) (bool, error) {
	// External index mode writes nothing into the repo, so there is nothing to
	// ignore — leave the repo's .gitignore completely untouched (zero footprint).
	if paths.ExternalIndexHome() != "" {
		return false, nil
	}
	gitRoot, err := FindGitRoot(startPath)
	if err != nil {
		return false, nil
	}
	giPath := filepath.Join(gitRoot, ".gitignore")
	st, err := os.Stat(giPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if ce, ok := gitignoreEnsureCache.Load(gitRoot); ok {
		entry := ce.(gitignoreCacheEntry)
		if entry.ok && entry.modTime.Equal(st.ModTime()) {
			return false, nil
		}
	}

	data, err := os.ReadFile(giPath)
	if err != nil {
		return false, err
	}
	if codehelperAlreadyIgnored(string(data)) {
		gitignoreEnsureCache.Store(gitRoot, gitignoreCacheEntry{modTime: st.ModTime(), ok: true})
		return false, nil
	}

	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(codehelperIgnoreLine)
	b.WriteByte('\n')
	if err := os.WriteFile(giPath, []byte(b.String()), st.Mode().Perm()); err != nil {
		return false, err
	}
	if st2, err := os.Stat(giPath); err == nil {
		gitignoreEnsureCache.Store(gitRoot, gitignoreCacheEntry{modTime: st2.ModTime(), ok: true})
	} else {
		gitignoreEnsureCache.Store(gitRoot, gitignoreCacheEntry{modTime: time.Now().UTC(), ok: true})
	}
	return true, nil
}

func codehelperAlreadyIgnored(content string) bool {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return false
	}
	gi := gitignore.CompileIgnoreLines(lines...)
	if gi == nil {
		return false
	}
	return gi.MatchesPath(".codehelper") ||
		gi.MatchesPath(".codehelper/") ||
		gi.MatchesPath(".codehelper/meta.json")
}
