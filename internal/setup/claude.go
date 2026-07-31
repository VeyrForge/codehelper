package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ClaudeCodeEnable approves a project-scoped MCP server for Claude Code.
//
// Claude Code never starts a server discovered in a repo's .mcp.json until the
// user approves it; that approval is stored as the server name in
// projects[<repoRoot>].enabledMcpjsonServers inside ~/.claude.json. Writing
// .mcp.json is therefore necessary but not sufficient — without this entry the
// tools silently never appear. This records the exact same state a manual
// "Approve" click would, so the server loads on the next Claude Code launch.
//
// The rest of ~/.claude.json (a large, Claude-managed file) is preserved
// byte-for-byte except the one project's enable/disable lists, and the file is
// re-serialized compact to match Claude Code's own formatting. Returns true if a
// change was written. A restart of Claude Code is still required for it to take
// effect, since MCP servers are only launched at startup.
func ClaudeCodeEnable(repoRoot, serverName string) (bool, error) {
	if repoRoot == "" || serverName == "" {
		return false, fmt.Errorf("repoRoot and serverName are required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	path := filepath.Join(home, ".claude.json")

	var root map[string]any
	if b, err := os.ReadFile(path); err == nil {
		if len(b) > 0 {
			// Never clobber a config we can't parse — surface it instead.
			if err := json.Unmarshal(b, &root); err != nil {
				return false, fmt.Errorf("%s is not valid JSON: %w", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if root == nil {
		root = map[string]any{}
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		root["projects"] = projects
	}
	proj, _ := projects[repoRoot].(map[string]any)
	if proj == nil {
		proj = map[string]any{}
		projects[repoRoot] = proj
	}

	enabled := toStringSlice(proj["enabledMcpjsonServers"])
	disabled := toStringSlice(proj["disabledMcpjsonServers"])

	changed := false
	if !containsString(enabled, serverName) {
		enabled = append(enabled, serverName)
		changed = true
	}
	// An explicit disable would override the enable, so clear any stale one.
	if i := indexOfString(disabled, serverName); i >= 0 {
		disabled = append(disabled[:i], disabled[i+1:]...)
		changed = true
	}
	if !changed {
		return false, nil
	}
	proj["enabledMcpjsonServers"] = enabled
	proj["disabledMcpjsonServers"] = disabled

	// Compact, matching Claude Code's own single-line style, to keep the diff to
	// this file minimal. ~/.claude.json holds tokens, so preserve 0600.
	out, err := json.Marshal(root)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(ss []string, want string) bool {
	return indexOfString(ss, want) >= 0
}

func indexOfString(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}
