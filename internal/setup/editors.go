package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ProjectMCP wires codehelper into the per-project MCP config of the editors that
// read one from the repo root, so opening the project in Claude Code or Cursor
// exposes every codehelper tool with no extra steps:
//
//   - <repoRoot>/.mcp.json          — Claude Code (and other .mcp.json clients)
//   - <repoRoot>/.cursor/mcp.json   — Cursor project-scoped servers
//
// Each file is merged non-destructively: any other MCP servers already present
// are preserved and only the "codehelper" entry is added or refreshed. binary is
// the command clients should launch (use "codehelper" when it is on PATH so the
// config stays portable/shareable). Returns the paths actually written.
func ProjectMCP(repoRoot, binary string) ([]string, error) {
	if repoRoot == "" {
		return nil, fmt.Errorf("project root is required")
	}
	if binary == "" {
		binary = "codehelper"
	}
	targets := []string{
		filepath.Join(repoRoot, ".mcp.json"),
		filepath.Join(repoRoot, ".cursor", "mcp.json"),
	}
	var written []string
	for _, path := range targets {
		if err := mergeMCPServerFile(path, "codehelper", binary, []string{"mcp"}); err != nil {
			return written, fmt.Errorf("%s: %w", path, err)
		}
		written = append(written, path)
	}
	return written, nil
}

// pruneMCPServerFile removes a single server entry from an MCP config file,
// preserving every other key and server. It is a no-op (no error) when the file
// or the named server is absent, so it is safe to call unconditionally. Returns
// true when an entry was actually removed.
func pruneMCPServerFile(path, name string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if len(b) == 0 {
		return false, nil
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		// Don't clobber a config we can't parse — surface it instead.
		return false, fmt.Errorf("existing config is not valid JSON: %w", err)
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		return false, nil
	}
	if _, ok := servers[name]; !ok {
		return false, nil
	}
	delete(servers, name)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// mergeMCPServerFile upserts a single server entry into an MCP config file under
// the conventional {"mcpServers": {...}} shape, creating the file and parent
// directory if needed and preserving every other key and server already present.
func mergeMCPServerFile(path, name, binary string, args []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var root map[string]any
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		// A malformed existing file must not be silently overwritten — surface it
		// so the user can resolve the conflict rather than losing their config.
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("existing config is not valid JSON: %w", err)
		}
	}
	if root == nil {
		root = map[string]any{}
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	servers[name] = map[string]any{
		"command": binary,
		"args":    args,
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
