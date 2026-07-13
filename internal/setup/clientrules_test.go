package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteClientRules_CreatesBothAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := WriteClientRules(dir); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Cursor rule exists with alwaysApply frontmatter.
	cursor, err := os.ReadFile(filepath.Join(dir, ".cursor", "rules", "codehelper.mdc"))
	if err != nil {
		t.Fatalf("cursor rule missing: %v", err)
	}
	if !strings.Contains(string(cursor), "alwaysApply: true") {
		t.Errorf("cursor rule missing alwaysApply frontmatter: %q", cursor)
	}
	if !strings.Contains(string(cursor), "change_kit") {
		t.Errorf("cursor rule should mention the tools")
	}
	if !strings.Contains(string(cursor), "`name`") {
		t.Errorf("cursor rule should mention correct param names")
	}

	// CLAUDE.md created with the managed block.
	claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md missing: %v", err)
	}
	if strings.Count(string(claude), claudeBeginMarker) != 1 {
		t.Errorf("expected exactly one managed block, got %d", strings.Count(string(claude), claudeBeginMarker))
	}

	// Idempotent: second write does not duplicate the block.
	if err := WriteClientRules(dir); err != nil {
		t.Fatal(err)
	}
	claude2, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if strings.Count(string(claude2), claudeBeginMarker) != 1 {
		t.Errorf("block duplicated on re-write: %d blocks", strings.Count(string(claude2), claudeBeginMarker))
	}
}

func TestWriteClientRules_PreservesExistingClaudeMd(t *testing.T) {
	dir := t.TempDir()
	userContent := "# My project\n\nImportant project-specific instructions the user wrote.\n"
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteClientRules(dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(claudePath)
	if !strings.Contains(string(got), "Important project-specific instructions") {
		t.Errorf("user content was clobbered: %q", got)
	}
	if !strings.Contains(string(got), claudeBeginMarker) {
		t.Errorf("managed block not appended")
	}

	// Updating the block must not touch user content.
	if err := WriteClientRules(dir); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(claudePath)
	if !strings.Contains(string(got2), "Important project-specific instructions") {
		t.Errorf("user content lost on update: %q", got2)
	}
	if strings.Count(string(got2), claudeBeginMarker) != 1 {
		t.Errorf("duplicate block after update")
	}
}
