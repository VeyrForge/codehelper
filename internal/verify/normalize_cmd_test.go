package verify

import (
	"errors"
	"testing"
)

func TestJoinArgv_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"go", "test", "./..."},
		{"echo", "hello world"},
		{"cmd", "/c", "echo", "lint-ok"},
		{"printf", "%s", `a; echo b`},
	}
	for _, argv := range cases {
		joined := JoinArgv(argv)
		got, err := splitArgv(joined)
		if err != nil {
			t.Fatalf("splitArgv(%q): %v", joined, err)
		}
		if len(got) != len(argv) {
			t.Fatalf("round-trip len %v → %q → %v", argv, joined, got)
		}
		for i := range argv {
			if got[i] != argv[i] {
				t.Fatalf("round-trip[%d] %q → %q → %q want %q", i, argv, joined, got[i], argv[i])
			}
		}
	}
}

func TestNormalizeCommandLine_JSONArrayString(t *testing.T) {
	t.Parallel()
	got, recovered, err := NormalizeCommandLine(`["echo","ok"]`)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("expected recovered=true for JSON array string")
	}
	argv, err := splitArgv(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 || argv[0] != "echo" || argv[1] != "ok" {
		t.Fatalf("got argv=%v from %q", argv, got)
	}
}

func TestNormalizeCommandLine_GoSprintMangled(t *testing.T) {
	t.Parallel()
	// Exact form produced by fmt.Sprint([]string{"cmd","/c","echo","lint-ok"})
	// and by argString's default branch — the Windows MCP footgun from HONEST review.
	raw := "[cmd /c echo lint-ok]"
	got, recovered, err := NormalizeCommandLine(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("expected recovered=true for Go Sprint mangled slice")
	}
	argv, err := splitArgv(got)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd", "/c", "echo", "lint-ok"}
	if len(argv) != len(want) {
		t.Fatalf("argv=%v want %v (from %q)", argv, want, got)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q", i, argv[i], want[i])
		}
	}
}

func TestNormalizeCommandLine_PlainPassthrough(t *testing.T) {
	t.Parallel()
	got, recovered, err := NormalizeCommandLine("go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	if recovered {
		t.Fatal("plain string should not be marked recovered")
	}
	if got != "go test ./..." {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeCommandLine_UnrecoverableBracket(t *testing.T) {
	t.Parallel()
	// Bracketed, non-JSON, with quotes → cannot safely unmangle Sprint form.
	_, _, err := NormalizeCommandLine(`[token with"quote inside]`)
	if err == nil {
		t.Fatal("expected error for unrecoverable bracket form")
	}
	if !errors.Is(err, ErrMangledArgv) {
		t.Fatalf("want ErrMangledArgv, got %v", err)
	}
}
