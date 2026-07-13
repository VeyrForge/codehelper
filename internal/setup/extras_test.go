package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureGreenConfigSkipsWhenMissingGE(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ge := filepath.Join(bin, "ge")
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
	wrote, err = EnsureGreenConfig(bin)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("expected idempotent skip on second run")
	}
}
