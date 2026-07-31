package setup

import (
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/skills"
)

// CursorGlobal performs the global (machine-wide) Cursor setup: it installs the
// bundled Cursor skills and, crucially, REMOVES any codehelper entry from the
// global ~/.cursor/mcp.json.
//
// codehelper is strictly per-project, and Cursor merges the global config with a
// project's <repo>/.cursor/mcp.json. A global "codehelper" entry therefore shows
// up as a DUPLICATE alongside the per-project one (and launches with cwd=$HOME,
// resolving to no project). Per-project registration via ProjectMCP is the only
// place codehelper should be wired into Cursor, so the global setup actively
// prunes the stray entry — this self-heals duplicates left by older versions on
// the next `setup`/`update`. Returns whether a stray global entry was pruned.
func CursorGlobal() (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	dir := filepath.Join(home, ".cursor")
	pruned, err := pruneMCPServerFile(filepath.Join(dir, "mcp.json"), "codehelper")
	if err != nil {
		return false, err
	}
	if err := skills.Install(filepath.Join(dir, "skills")); err != nil {
		return pruned, err
	}
	return pruned, nil
}
