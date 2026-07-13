package freshness

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// worktreeSkipDirs are never indexed, so changes inside them don't make the
// index stale. Mirrors the indexer/walker ignore set (kept local to avoid an
// import cycle — freshness must not depend on the indexer).
var worktreeSkipDirs = map[string]bool{
	".git": true, ".codehelper": true, "node_modules": true, "vendor": true,
	"target": true, "dist": true, "build": true, "__pycache__": true,
	".venv": true, "venv": true, ".idea": true, ".vscode": true,
}

// worktreeSrcExt is the set of source extensions the indexer parses. Only changes
// to these files can make the symbol index out of date.
var worktreeSrcExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".py": true, ".rs": true, ".java": true,
	".cs": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".cxx": true, ".hpp": true, ".hh": true, ".hxx": true, ".php": true, ".rb": true,
}

// worktreeScanCap bounds the walk so a pathological monorepo can't make a single
// freshness check arbitrarily slow. Early-exit on the first newer file means the
// "something changed" case is fast regardless; the cap only bounds the all-clean
// scan of a very large tree.
const worktreeScanCap = 50000

// WorkingTreeChangedSince reports whether any indexable source file under root
// has a modification time after `since` (the moment the index was built). This is
// the language-agnostic, git-independent signal that catches UNCOMMITTED edits —
// the gap left by purely git-HEAD-based staleness detection. Early-exits on the
// first newer file, so it is cheap when something changed.
func WorkingTreeChangedSince(root string, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	changed := false
	scanned := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && worktreeSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !worktreeSrcExt[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		scanned++
		if scanned > worktreeScanCap {
			return fs.SkipAll
		}
		if info, e := d.Info(); e == nil && info.ModTime().After(since) {
			changed = true
			return fs.SkipAll
		}
		return nil
	})
	return changed
}
