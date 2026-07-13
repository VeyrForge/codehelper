package mcpsvc

import "testing"

// TestParseDiagnostics_Python covers the two Python output shapes diagnostics
// now understands: ruff/flake8/mypy "path:line:col: message" and the compileall
// "File ..., line N" + "SyntaxError: ..." pair.
func TestParseDiagnostics_Python(t *testing.T) {
	ruff := "app/handlers.py:12:5: F401 'os' imported but unused\n"
	got := parseDiagnostics(ruff)
	if len(got) != 1 || got[0].File != "app/handlers.py" || got[0].Line != 12 || got[0].Col != 5 {
		t.Fatalf("ruff parse failed: %+v", got)
	}

	compileall := `*** Error compiling './app/broken.py'...
  File "./app/broken.py", line 3
    def f(:
          ^
SyntaxError: invalid syntax
`
	got = parseDiagnostics(compileall)
	if len(got) != 1 {
		t.Fatalf("compileall: expected 1 diagnostic, got %d (%+v)", len(got), got)
	}
	if got[0].File != "app/broken.py" || got[0].Line != 3 {
		t.Errorf("compileall location wrong: %+v", got[0])
	}
	if got[0].Message == "" || got[0].Severity != "error" {
		t.Errorf("compileall message/severity wrong: %+v", got[0])
	}
}

// TestParseDiagnostics_PHP covers phpstan --error-format=raw output.
func TestParseDiagnostics_PHP(t *testing.T) {
	out := parseDiagnostics("app/Models/User.php:25:Method App\\Models\\User::scope() has no return type specified.\n")
	if len(out) != 1 || out[0].File != "app/Models/User.php" || out[0].Line != 25 || out[0].Message == "" {
		t.Fatalf("php parse failed: %+v", out)
	}
}

// TestOrderedToolchains_CoversCommonLanguages guards that the universality
// expansion stays wired (Go/Rust/TS/Python/JVM all detectable).
func TestOrderedToolchains_CoversCommonLanguages(t *testing.T) {
	want := map[string]bool{"go": false, "rust": false, "typescript": false, "python": false, "java-maven": false, "java-gradle": false, "php": false}
	for _, tc := range orderedToolchains {
		if _, ok := want[tc.name]; ok {
			want[tc.name] = true
		}
		if len(tc.cmds) == 0 || len(tc.allowed) == 0 {
			t.Errorf("toolchain %q has empty cmds/allowed", tc.name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("toolchain %q not registered", name)
		}
	}
}
