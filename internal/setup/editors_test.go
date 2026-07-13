package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectMCPWritesBothEditors(t *testing.T) {
	root := t.TempDir()

	// A pre-existing Claude Code config with another server must be preserved.
	pre := `{"mcpServers":{"other":{"command":"foo","args":["bar"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}

	written, err := ProjectMCP(root, "codehelper")
	if err != nil {
		t.Fatalf("ProjectMCP: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("expected 2 files written, got %d: %v", len(written), written)
	}

	claude := readServers(t, filepath.Join(root, ".mcp.json"))
	if _, ok := claude["other"]; !ok {
		t.Fatal("existing 'other' server was clobbered")
	}
	assertCodehelperServer(t, claude)

	cursor := readServers(t, filepath.Join(root, ".cursor", "mcp.json"))
	assertCodehelperServer(t, cursor)

	// Idempotent: a second run keeps exactly one codehelper entry and still has 'other'.
	if _, err := ProjectMCP(root, "codehelper"); err != nil {
		t.Fatalf("second ProjectMCP: %v", err)
	}
	again := readServers(t, filepath.Join(root, ".mcp.json"))
	if len(again) != 2 {
		t.Fatalf("expected 2 servers after idempotent re-run, got %d", len(again))
	}
}

func TestProjectMCPRejectsMalformedConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectMCP(root, "codehelper"); err == nil {
		t.Fatal("expected an error rather than overwriting a malformed config")
	}
}

func TestPruneMCPServerFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	// Absent file: no-op, no error.
	if removed, err := pruneMCPServerFile(path, "codehelper"); err != nil || removed {
		t.Fatalf("absent file: removed=%v err=%v, want false/nil", removed, err)
	}

	// Removing the named server preserves the others.
	pre := `{"mcpServers":{"codehelper":{"command":"x","args":["mcp"]},"other":{"command":"foo"}}}`
	if err := os.WriteFile(path, []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := pruneMCPServerFile(path, "codehelper")
	if err != nil || !removed {
		t.Fatalf("prune: removed=%v err=%v, want true/nil", removed, err)
	}
	servers := readServers(t, path)
	if _, ok := servers["codehelper"]; ok {
		t.Fatal("codehelper entry was not removed")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatal("unrelated 'other' server was clobbered")
	}

	// Second run is idempotent: already gone → no removal.
	if removed, err := pruneMCPServerFile(path, "codehelper"); err != nil || removed {
		t.Fatalf("idempotent prune: removed=%v err=%v, want false/nil", removed, err)
	}
}

func readServers(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		t.Fatalf("%s has no mcpServers object", path)
	}
	return servers
}

func assertCodehelperServer(t *testing.T, servers map[string]any) {
	t.Helper()
	ch, ok := servers["codehelper"].(map[string]any)
	if !ok {
		t.Fatal("codehelper server entry missing")
	}
	if ch["command"] != "codehelper" {
		t.Fatalf("command = %v, want codehelper", ch["command"])
	}
}
