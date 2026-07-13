package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstall_WritesVersionStamp(t *testing.T) {
	dest := t.TempDir()
	if err := Install(dest); err != nil {
		t.Fatalf("install skills: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, ".codehelper-skills-version"))
	if err != nil {
		t.Fatalf("read version stamp: %v", err)
	}
	if strings.TrimSpace(string(b)) != "1.0.0" {
		t.Fatalf("unexpected version stamp: %q", string(b))
	}
}

func TestInstall_SkipsWhenVersionUnchanged(t *testing.T) {
	dest := t.TempDir()
	if err := Install(dest); err != nil {
		t.Fatalf("first install: %v", err)
	}
	target := filepath.Join(dest, "codehelper-guide", "SKILL.md")
	if err := os.WriteFile(target, []byte("custom\n"), 0o644); err != nil {
		t.Fatalf("write custom skill content: %v", err)
	}

	if err := Install(dest); err != nil {
		t.Fatalf("second install: %v", err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}
	if string(b) != "custom\n" {
		t.Fatalf("expected install skip to keep local changes, got %q", string(b))
	}
}
