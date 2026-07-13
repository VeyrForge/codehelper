package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeCodeEnableApprovesAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := "/some/project"

	// A realistic pre-existing config: unrelated top-level keys and another
	// project that must survive untouched.
	pre := `{"numStartups":7,"projects":{"/other":{"enabledMcpjsonServers":["x"]}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := ClaudeCodeEnable(root, "codehelper")
	if err != nil {
		t.Fatalf("ClaudeCodeEnable: %v", err)
	}
	if !changed {
		t.Fatal("expected a change on first enable")
	}

	got := readClaude(t, home)
	if got["numStartups"] != float64(7) {
		t.Fatalf("unrelated key lost: numStartups=%v", got["numStartups"])
	}
	projects := got["projects"].(map[string]any)
	if other := projects["/other"].(map[string]any); !hasServer(other, "enabledMcpjsonServers", "x") {
		t.Fatal("other project's servers were clobbered")
	}
	proj := projects[root].(map[string]any)
	if !hasServer(proj, "enabledMcpjsonServers", "codehelper") {
		t.Fatal("codehelper not enabled for project")
	}

	// Idempotent.
	changed, err = ClaudeCodeEnable(root, "codehelper")
	if err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if changed {
		t.Fatal("expected no change on idempotent re-run")
	}
}

func TestClaudeCodeEnableClearsStaleDisable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := "/p"
	pre := `{"projects":{"/p":{"disabledMcpjsonServers":["codehelper"]}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ClaudeCodeEnable(root, "codehelper"); err != nil {
		t.Fatalf("ClaudeCodeEnable: %v", err)
	}
	proj := readClaude(t, home)["projects"].(map[string]any)[root].(map[string]any)
	if hasServer(proj, "disabledMcpjsonServers", "codehelper") {
		t.Fatal("codehelper should have been removed from the disabled list")
	}
	if !hasServer(proj, "enabledMcpjsonServers", "codehelper") {
		t.Fatal("codehelper should be enabled")
	}
}

func TestClaudeCodeEnableCreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := ClaudeCodeEnable("/p", "codehelper"); err != nil {
		t.Fatalf("ClaudeCodeEnable on missing file: %v", err)
	}
	proj := readClaude(t, home)["projects"].(map[string]any)["/p"].(map[string]any)
	if !hasServer(proj, "enabledMcpjsonServers", "codehelper") {
		t.Fatal("codehelper not enabled in freshly created config")
	}
}

func TestClaudeCodeEnableRejectsMalformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaudeCodeEnable("/p", "codehelper"); err == nil {
		t.Fatal("expected an error rather than overwriting a malformed config")
	}
}

func TestCodexMCPAppendsIdempotently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pre := "model = \"gpt-5.4\"\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n"
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	added, err := CodexMCP("codehelper")
	if err != nil {
		t.Fatalf("CodexMCP: %v", err)
	}
	if !added {
		t.Fatal("expected the block to be added")
	}
	b, _ := os.ReadFile(path)
	out := string(b)
	if !strings.HasPrefix(out, pre) {
		t.Fatal("existing config content was not preserved as a prefix")
	}
	if !strings.Contains(out, "[mcp_servers.codehelper]") ||
		!strings.Contains(out, "command = \"codehelper\"") ||
		!strings.Contains(out, "args = [\"mcp\"]") {
		t.Fatalf("codehelper block missing or malformed:\n%s", out)
	}

	added, err = CodexMCP("codehelper")
	if err != nil {
		t.Fatalf("second CodexMCP: %v", err)
	}
	if added {
		t.Fatal("expected idempotent no-op on re-run")
	}
	b2, _ := os.ReadFile(path)
	if strings.Count(string(b2), "[mcp_servers.codehelper]") != 1 {
		t.Fatal("duplicate codehelper section after re-run")
	}
}

func TestCodexMCPCreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	added, err := CodexMCP("codehelper")
	if err != nil {
		t.Fatalf("CodexMCP on missing file: %v", err)
	}
	if !added {
		t.Fatal("expected the block to be added to a new file")
	}
	b, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	if !strings.Contains(string(b), "[mcp_servers.codehelper]") {
		t.Fatal("codehelper block missing from new config")
	}
}

func readClaude(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read ~/.claude.json: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("~/.claude.json is not valid JSON: %v", err)
	}
	return root
}

func hasServer(proj map[string]any, key, name string) bool {
	for _, v := range toStringSlice(proj[key]) {
		if v == name {
			return true
		}
	}
	return false
}
