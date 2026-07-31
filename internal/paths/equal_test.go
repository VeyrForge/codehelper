package paths

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestEqual_SlashNormalized(t *testing.T) {
	a := filepath.FromSlash("F:/Projects/codehelper")
	b := filepath.FromSlash("F:/Projects/codehelper/")
	if !Equal(a, b) {
		t.Fatalf("trailing slash should not matter: %q vs %q", a, b)
	}
	// Mixed separators (common when JSON/MCP paths meet Windows Abs).
	mixed := "F:/Projects\\codehelper"
	if !Equal(a, mixed) {
		t.Fatalf("mixed separators should match: %q vs %q (canon %q / %q)",
			a, mixed, Canonical(a), Canonical(mixed))
	}
}

func TestEqual_WindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path case only")
	}
	a := `F:\Projects\codehelper`
	b := `f:\Projects\Codehelper`
	if !Equal(a, b) {
		t.Fatalf("Windows case variants should match: %q vs %q", a, b)
	}
	if !EqualIndexRoot(a, b) {
		t.Fatalf("EqualIndexRoot should match case variants")
	}
}

func TestEqual_DistinctRoots(t *testing.T) {
	if Equal(`F:\a`, `F:\b`) {
		t.Fatal("distinct roots must not Equal")
	}
}
