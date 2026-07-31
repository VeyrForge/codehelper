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

// codehelperIgnoreEntries are repo-local artifacts codehelper creates that must
// not land in git. AGENTS.md and root .mcp.json stay out of this list on purpose:
// they are portable shared setup (PATH command "codehelper") that clones benefit
// from before the first analyze.
var codehelperIgnoreEntries = []struct {
	line     string // exact line to append when missing
	check    string // path to test against an existing matcher
	altCheck string
}{
	{line: ".codehelper/", check: ".codehelper", altCheck: ".codehelper/meta.json"},
	{line: "CLAUDE.md", check: "CLAUDE.md"},
	{line: ".cursor/", check: ".cursor", altCheck: ".cursor/rules/codehelper.mdc"},
	{line: ".claude/", check: ".claude", altCheck: ".claude/settings.local.json"},
}

const codehelperIgnoreBanner = "# codehelper (generated local — do not commit)"

type gitignoreCacheEntry struct {
	modTime time.Time
	ok      bool
}

var gitignoreEnsureCache sync.Map

// EnsureCodehelperGitignored appends ignore lines for codehelper-generated local
// artifacts to the repo-root .gitignore when that file exists and does not
// already cover them. It never creates a new .gitignore. Returns true when the
// file was updated.
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
	content := string(data)
	missing := missingCodehelperIgnores(content)
	if len(missing) == 0 {
		gitignoreEnsureCache.Store(gitRoot, gitignoreCacheEntry{modTime: st.ModTime(), ok: true})
		return false, nil
	}

	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(content, "\n") {
		b.WriteByte('\n')
	}
	if !strings.Contains(content, codehelperIgnoreBanner) {
		if len(data) > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(codehelperIgnoreBanner)
		b.WriteByte('\n')
	}
	for _, line := range missing {
		b.WriteString(line)
		b.WriteByte('\n')
	}
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

func missingCodehelperIgnores(content string) []string {
	gi := compileGitignoreLines(content)
	var missing []string
	for _, e := range codehelperIgnoreEntries {
		if gi != nil && (gi.MatchesPath(e.check) ||
			(e.altCheck != "" && gi.MatchesPath(e.altCheck)) ||
			gi.MatchesPath(strings.TrimSuffix(e.line, "/")) ||
			gi.MatchesPath(e.line)) {
			continue
		}
		missing = append(missing, e.line)
	}
	return missing
}

func compileGitignoreLines(content string) *gitignore.GitIgnore {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	return gitignore.CompileIgnoreLines(lines...)
}

// codehelperAlreadyIgnored reports whether .codehelper/ is covered. Kept for
// tests and callers that only care about the index directory.
func codehelperAlreadyIgnored(content string) bool {
	gi := compileGitignoreLines(content)
	if gi == nil {
		return false
	}
	return gi.MatchesPath(".codehelper") ||
		gi.MatchesPath(".codehelper/") ||
		gi.MatchesPath(".codehelper/meta.json")
}
