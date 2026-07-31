package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/green"
)

func TestEnsureGreenConfigSkipsWhenMissingGE(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", "")

	wrote, err := EnsureGreenConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("expected no config when ge is absent")
	}
}

func TestEnsureGreenConfigWritesWhenGEPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	geName := "ge"
	if os.PathSeparator == '\\' {
		geName = "ge.exe"
	}
	ge := filepath.Join(bin, geName)
	if err := os.WriteFile(ge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	wrote, err := EnsureGreenConfig(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("expected green.json to be written")
	}

	raw, err := os.ReadFile(filepath.Join(home, ".codehelper", "green.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg green.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "embed" {
		t.Fatalf("expected embed-only config, got %+v", cfg.Servers)
	}
	if cfg.Servers[0].Cmd != ge {
		t.Fatalf("cmd=%q want %q", cfg.Servers[0].Cmd, ge)
	}

	wrote, err = EnsureGreenConfig(bin)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("expected idempotent skip on second run")
	}
}
