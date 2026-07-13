package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CodexMCP registers codehelper as an MCP server for the OpenAI Codex CLI by
// appending an [mcp_servers.codehelper] block to ~/.codex/config.toml.
//
// Codex has no per-project MCP config and no approval gate, so one global entry
// is enough for it to launch codehelper from any directory; codehelper itself
// scopes its index/state to whatever project Codex is invoked in. The block is
// appended (not rewritten) so the rest of config.toml — including comments and
// other tables — is left untouched, and the write is skipped when the section
// already exists, making it idempotent. binary is the command Codex should
// launch; "codehelper" keeps it portable when on PATH. Returns true if added.
func CodexMCP(binary string) (bool, error) {
	if binary == "" {
		binary = "codehelper"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	dir := filepath.Join(home, ".codex")
	path := filepath.Join(dir, "config.toml")

	existing := ""
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if strings.Contains(existing, "[mcp_servers.codehelper]") {
		return false, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	block := fmt.Sprintf("[mcp_servers.codehelper]\ncommand = %q\nargs = [\"mcp\"]\n", binary)
	prefix := ""
	if existing != "" {
		prefix = "\n"
		if !strings.HasSuffix(existing, "\n") {
			prefix = "\n\n"
		}
	}
	if err := os.WriteFile(path, []byte(existing+prefix+block), 0o600); err != nil {
		return false, err
	}
	return true, nil
}
