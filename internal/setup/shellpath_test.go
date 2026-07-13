package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureUserPath_unix_idempotent(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("unix test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", "/usr/bin")

	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	changed, err := EnsureUserPath(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected profile update")
	}
	profile := filepath.Join(home, ".bashrc")
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), pathMarker) || !strings.Contains(string(data), bin) {
		t.Fatalf("profile = %q", data)
	}

	changed2, err := EnsureUserPath(bin)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("expected idempotent second run")
	}
}

func TestEnsureUserPath_skipsWhenAlreadyOnPath(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("unix test")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", bin+":"+"/usr/bin")

	changed, err := EnsureUserPath(bin)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected no profile write when already on PATH")
	}
}
