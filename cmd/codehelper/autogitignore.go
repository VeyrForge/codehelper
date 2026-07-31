package main

import (
	"log/slog"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

// autoEnsureCodehelperGitignore keeps codehelper-generated local artifacts
// (.codehelper/, .cursor/, .claude/, .codex/, .mcp.json, AGENTS.md, CLAUDE.md,
// CODEHELPER*.md) in the repo-root .gitignore. Best-effort and non-blocking.
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
