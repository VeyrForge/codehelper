package setup

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// A connected MCP server is invisible to the model unless the project's
// instruction file tells the agent to use it. Each client reads a DIFFERENT
// file: Claude Code → CLAUDE.md, Cursor → .cursor/rules/*.mdc, Codex → AGENTS.md
// (written separately by internal/agents). codehelper writes AGENTS.md, so Codex
// works — but Claude Code and Cursor would otherwise see no directive and never
// call the tools. WriteClientRules closes that gap.
//
// To avoid showing the SAME long directive three times (Cursor auto-imports
// CLAUDE.md and AGENTS.md on top of its native .cursor/rules), the full tool
// contract lives ONLY in AGENTS.md. The Cursor rule and the CLAUDE.md block carry
// a short, distinct pointer — enough to make the agent reach for the tools first,
// without duplicating the catalog that AGENTS.md already owns.

const clientRulesBody = "This project is indexed by **codehelper**, and its MCP tools are the DEFAULT toolchain for reading, " +
	"searching, and reasoning about this code — for **every agent and subagent**, not just the main session. " +
	"Use them FIRST; fall back to the built-in Read/Grep/Glob/Bash tools ONLY when a codehelper tool errors, " +
	"returns stale/empty data, or the task is genuinely out of its scope — never as the first reflex (and say so when you do). " +
	"Call `project_context` once per session (default `verbosity=short`); then `query`/`scout` to find code — do not re-bootstrap when you only need a symbol or file. " +
	"`context`/`trace` for callers/callees; `change_kit` before editing; `impact`/`test_impact` for blast radius; `diagnostics` after. " +
	"Before `context`, `impact`, or `change_kit`: read that tool's schema — `context`/`impact` take `name` (not `symbol`); `change_kit` takes `target`. Fix a wrong param once; don't retry blind. " +
	"Tools scope to THIS project only. **Full tool contract: AGENTS.md.**"

const cursorRuleFile = `---
description: codehelper's MCP tools are the default toolchain — use them first, built-ins only as fallback
alwaysApply: true
---

# codehelper MCP tools are the default toolchain

` + clientRulesBody + "\n"

const (
	claudeBeginMarker = "<!-- codehelper:begin (managed — edits between these markers are overwritten) -->"
	claudeEndMarker   = "<!-- codehelper:end -->"
)

// WriteClientRules writes the per-client tool-first directive so Cursor and
// Claude Code actually invoke codehelper's MCP tools. The Cursor rule is a
// dedicated codehelper-owned file (overwritten); the Claude Code block is upserted
// into CLAUDE.md between managed markers so the user's own content is preserved.
func WriteClientRules(repoRoot string) error {
	// Zero-footprint (external index) mode writes nothing into the repo.
	if paths.ExternalIndexHome() != "" {
		return nil
	}
	cursorDir := filepath.Join(repoRoot, ".cursor", "rules")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "codehelper.mdc"), []byte(cursorRuleFile), 0o644); err != nil {
		return err
	}
	return upsertClaudeBlock(filepath.Join(repoRoot, "CLAUDE.md"))
}

// upsertClaudeBlock creates or updates the managed codehelper section in CLAUDE.md
// without disturbing anything outside the markers.
func upsertClaudeBlock(path string) error {
	block := claudeBeginMarker + "\n## codehelper MCP tools are the default toolchain\n\n" + clientRulesBody + "\n" + claudeEndMarker + "\n"
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(path, []byte("# Project guidance\n\n"+block), 0o644)
		}
		return err
	}
	s := string(existing)
	begin := strings.Index(s, claudeBeginMarker)
	if begin == -1 {
		// Append a fresh managed block, preserving the user's file.
		sep := "\n\n"
		if strings.HasSuffix(s, "\n") {
			sep = "\n"
		}
		return os.WriteFile(path, []byte(s+sep+block), 0o644)
	}
	end := strings.Index(s, claudeEndMarker)
	if end == -1 || end < begin {
		// Corrupted markers: replace from begin to EOF.
		return os.WriteFile(path, []byte(s[:begin]+block), 0o644)
	}
	end += len(claudeEndMarker)
	updated := s[:begin] + strings.TrimSuffix(block, "\n") + s[end:]
	return os.WriteFile(path, []byte(updated), 0o644)
}
