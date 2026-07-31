package main

import (
	"log/slog"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

// autoEnsureCodehelperGitignore keeps codehelper-generated local artifacts
// (.codehelper/, CLAUDE.md, .cursor/, .claude/) in the repo-root .gitignore
// when that file already exists. Best-effort and non-blocking for callers.
func autoEnsureCodehelperGitignore(workPath string) {
	go func() {
		added, err := gitutil.EnsureCodehelperGitignored(workPath)
		if err != nil {
			slog.Debug("ensure codehelper gitignore", "path", workPath, "err", err)
			return
		}
		if added {
			slog.Debug("appended codehelper ignore entries to .gitignore", "path", workPath)
		}
	}()
}
