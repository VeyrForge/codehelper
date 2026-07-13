package mcpsvc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/projcfg"
)

func TestResolveVerifyWorkspaceSubprojectRust(t *testing.T) {
	root := t.TempDir()
	rustDir := filepath.Join(root, "rust")
	if err := os.MkdirAll(rustDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rustDir, "Cargo.toml"), []byte("[package]\nname=\"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := resolveVerifyWorkspace(root)
	if len(ws.Cmds) == 0 {
		t.Fatal("expected cargo check commands from rust/ subproject")
	}
	if ws.Cwd != rustDir {
		t.Fatalf("cwd=%q want %q", ws.Cwd, rustDir)
	}
	if ws.Cmds[0] != "cargo check --quiet" {
		t.Fatalf("cmds=%v", ws.Cmds)
	}
}

func TestResolveVerifyWorkspaceProjcfgCwd(t *testing.T) {
	root := t.TempDir()
	rustDir := filepath.Join(root, "rust")
	if err := os.MkdirAll(rustDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rustDir, "Cargo.toml"), []byte("[package]\nname=\"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := projcfg.Save(root, projcfg.Config{
		ToolsEnabled: true,
		Track:        projcfg.TrackSummary,
		VerifyCwd:    "rust",
		VerifyBuild:  "cargo check --quiet",
	}); err != nil {
		t.Fatal(err)
	}
	ws := resolveVerifyWorkspace(root)
	if ws.Cwd != rustDir {
		t.Fatalf("cwd=%q", ws.Cwd)
	}
	if len(ws.Cmds) != 1 || ws.Cmds[0] != "cargo check --quiet" {
		t.Fatalf("cmds=%v", ws.Cmds)
	}
}
