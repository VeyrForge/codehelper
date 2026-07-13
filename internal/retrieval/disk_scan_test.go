package retrieval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiskGrepIdentifier_FindsDefinitionFirst(t *testing.T) {
	dir := t.TempDir()
	// A reference in one file, the definition in another.
	mustWrite(t, filepath.Join(dir, "use.go"), "package x\nfunc caller() { ZanzibarWidget() }\n")
	mustWrite(t, filepath.Join(dir, "def.go"), "package x\n\nfunc ZanzibarWidget() int { return 1 }\n")

	matches := DiskGrepIdentifier(dir, "ZanzibarWidget", 5)
	if len(matches) == 0 {
		t.Fatal("expected disk matches for ZanzibarWidget")
	}
	// The definition should sort ahead of the bare reference.
	if !matches[0].IsDef {
		t.Fatalf("expected the definition line first, got %#v", matches[0])
	}
	if filepath.Base(matches[0].Path) != "def.go" {
		t.Fatalf("expected def.go first, got %q", matches[0].Path)
	}
}

func TestDiskGrepIdentifier_WordBoundary(t *testing.T) {
	dir := t.TempDir()
	// "Widget" must NOT match inside "ZanzibarWidget" or "WidgetFactory".
	mustWrite(t, filepath.Join(dir, "a.go"), "package x\nfunc ZanzibarWidgetFactory() {}\nfunc Widget() {}\n")
	matches := DiskGrepIdentifier(dir, "Widget", 5)
	for _, m := range matches {
		if m.Line == 2 {
			t.Fatalf("word-boundary leak: matched substring on line 2: %#v", m)
		}
	}
	found := false
	for _, m := range matches {
		if m.Line == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected to match the standalone Widget definition on line 3")
	}
}

func TestDiskGrepIdentifier_SkipsVendorAndNonSource(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "vendor", "v.go"), "package v\nfunc ZanzibarWidget() {}\n")
	mustWrite(t, filepath.Join(dir, "notes.txt"), "ZanzibarWidget here too\n")
	if got := DiskGrepIdentifier(dir, "ZanzibarWidget", 5); len(got) != 0 {
		t.Fatalf("expected no matches (vendor + non-source skipped), got %#v", got)
	}
}

func TestDiskGrepIdentifier_RejectsNonIdentifier(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package x\nfunc Foo() {}\n")
	if got := DiskGrepIdentifier(dir, "how does it work", 5); got != nil {
		t.Fatalf("multi-word phrase should not be disk-grepped, got %#v", got)
	}
	if got := DiskGrepIdentifier(dir, "", 5); got != nil {
		t.Fatalf("empty name should return nil, got %#v", got)
	}
}

func TestIsIdentifierLike(t *testing.T) {
	cases := map[string]bool{
		"Foo":            true,
		"snake_case":     true,
		"camelCase42":    true,
		"sym:r:p:1:Name": true,
		"two words":      false,
		"a.b":            false,
		"":               false,
	}
	for in, want := range cases {
		if got := isIdentifierLike(in); got != want {
			t.Errorf("isIdentifierLike(%q)=%v, want %v", in, got, want)
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
