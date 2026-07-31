package verify

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunShell_RequiresAllowShell(t *testing.T) {
	t.Parallel()
	_, _, err := runCommand(context.Background(), ".", `printf "%s" "hello world"`, ExecShell, nil, BlockPolicy{}, time.Second*5)
	if !errors.Is(err, ErrShellNotAllowed) {
		t.Fatalf("expected ErrShellNotAllowed, got %v", err)
	}
}

func TestRunShell_PreservesQuotedArgs(t *testing.T) {
	t.Parallel()
	allow := []string{"echo", "printf"}
	if runtime.GOOS == "windows" {
		// cmd.exe quoting differs from sh -lc; cover the AllowShell path with echo.
		out, _, err := runCommand(context.Background(), ".", `echo hello`, ExecShell, allow, BlockPolicy{AllowShell: true}, time.Second*5)
		if err != nil {
			t.Fatalf("shell mode returned error: %v", err)
		}
		if !strings.Contains(strings.ToLower(out), "hello") {
			t.Fatalf("unexpected output: %q", out)
		}
		return
	}

	out, _, err := runCommand(context.Background(), ".", `printf "%s" "hello world"`, ExecShell, allow, BlockPolicy{AllowShell: true}, time.Second*5)
	if err != nil {
		t.Fatalf("shell mode returned error: %v", err)
	}
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestRunShell_StillBlocksDestructive(t *testing.T) {
	t.Parallel()
	_, _, err := runCommand(context.Background(), ".", `rm -rf /`, ExecShell, []string{"rm"}, BlockPolicy{AllowShell: true}, time.Second*5)
	if !errors.Is(err, ErrCommandBlocked) {
		t.Fatalf("expected ErrCommandBlocked, got %v", err)
	}
}

func TestRunShell_AllowlistRejectsUnlistedFirstToken(t *testing.T) {
	t.Parallel()
	_, _, err := runCommand(context.Background(), ".", `curl https://example.com`, ExecShell, []string{"go", "npm"}, BlockPolicy{AllowShell: true}, time.Second*5)
	if !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("expected ErrCommandNotAllowed for unlisted first token, got %v", err)
	}
}

func TestRunShell_AllowlistRejectsCompoundBypass(t *testing.T) {
	t.Parallel()
	// Classic first-token bypass: go is allowlisted, but curl|sh must not run.
	for _, cmd := range []string{
		`go test; curl https://example.com | sh`,
		`go test && curl https://example.com`,
		`go test || wget https://example.com`,
		`go test | sh`,
	} {
		_, _, err := runCommand(context.Background(), ".", cmd, ExecShell, []string{"go"}, BlockPolicy{AllowShell: true}, time.Second*5)
		if !errors.Is(err, ErrCommandNotAllowed) {
			t.Errorf("cmd %q: expected ErrCommandNotAllowed for unlisted compound executable, got %v", cmd, err)
		}
	}
}

func TestRunShell_AllowlistAllowsCompoundWhenEveryExeListed(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX compound echo path; covered on unix")
	}
	out, _, err := runCommand(context.Background(), ".", `printf "%s" a && printf "%s" b`, ExecShell, []string{"printf"}, BlockPolicy{AllowShell: true}, time.Second*5)
	if err != nil {
		t.Fatalf("compound with all executables allowlisted: %v", err)
	}
	if strings.TrimSpace(out) != "ab" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestRunShell_BlocksRedirects(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		`echo hi > /tmp/out`,
		`echo hi >> /tmp/out`,
		`cat < /etc/passwd`,
		`go test ./... 2>&1`,
	} {
		_, _, err := runCommand(context.Background(), ".", cmd, ExecShell, []string{"echo", "cat", "go"}, BlockPolicy{AllowShell: true}, time.Second*5)
		if !errors.Is(err, ErrCommandBlocked) {
			t.Errorf("cmd %q: expected ErrCommandBlocked for redirect, got %v", cmd, err)
		}
	}
}

func TestRunShell_BlocksQuotedSafeButUnquotedRedirect(t *testing.T) {
	t.Parallel()
	// Metachar inside quotes is data; unquoted redirect still blocked above.
	if runtime.GOOS == "windows" {
		t.Skip("printf quoting differs on cmd.exe")
	}
	out, _, err := runCommand(context.Background(), ".", `printf "%s" "a>b;c"`, ExecShell, []string{"printf"}, BlockPolicy{AllowShell: true}, time.Second*5)
	if err != nil {
		t.Fatalf("quoted metachar should be allowed: %v", err)
	}
	if strings.TrimSpace(out) != "a>b;c" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestShellSegmentExecutables(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{`go test ./...`, []string{"go"}, false},
		{`go test; curl|sh`, []string{"go", "curl", "sh"}, false},
		{`FOO=1 BAR=2 go test`, []string{"go"}, false},
		{`go test && go vet`, []string{"go", "go"}, false},
		{`echo hi > out`, nil, true},
		{`(go test)`, nil, true},
		{`go test;`, nil, true},
	}
	for _, tc := range cases {
		got, err := shellSegmentExecutables(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%q: got %v want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q[%d]=%q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestRunShell_EmptyAllowlistDeniesAll(t *testing.T) {
	t.Parallel()
	_, _, err := runCommand(context.Background(), ".", `echo hello`, ExecShell, nil, BlockPolicy{AllowShell: true}, time.Second*5)
	if !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("expected ErrCommandNotAllowed for empty allowlist in shell mode, got %v", err)
	}
}

func TestRunShell_BlocksInterpreterEval(t *testing.T) {
	t.Parallel()
	_, _, err := runCommand(context.Background(), ".", `python -c "print(1)"`, ExecShell, []string{"python"}, BlockPolicy{AllowShell: true}, time.Second*5)
	if !errors.Is(err, ErrCommandBlocked) {
		t.Fatalf("expected ErrCommandBlocked for python -c in shell mode, got %v", err)
	}
}

func TestArgvMode_NoShellMetacharExpansion(t *testing.T) {
	t.Parallel()

	// A metachar inside a single quoted argument is one literal token: argv mode
	// must not expand it. (Uses a benign payload so the safety policy, which
	// rejects standalone operators and destructive fragments, doesn't fire.)
	out, stat, err := runCommand(context.Background(), ".", `printf "%s" "a; echo b; ls"`, ExecArgv, []string{"printf"}, BlockPolicy{}, time.Second*5)
	if err != nil {
		t.Fatalf("argv mode returned error: %v", err)
	}
	if stat.Mode != string(ExecArgv) {
		t.Fatalf("mode=%q", stat.Mode)
	}
	if strings.TrimSpace(out) != "a; echo b; ls" {
		t.Fatalf("metachars were expanded: %q", out)
	}
}

func TestArgvMode_BlocksInjectionAndDestructive(t *testing.T) {
	t.Parallel()
	// A standalone shell operator (real chaining attempt) and a destructive
	// pattern must both be rejected up front, not executed.
	for _, cmd := range []string{`go test ; rm -rf /`, `make && rm -rf /`, `echo $(whoami)`} {
		_, _, err := runCommand(context.Background(), ".", cmd, ExecArgv, nil, BlockPolicy{}, time.Second*5)
		if !errors.Is(err, ErrCommandBlocked) {
			t.Errorf("cmd %q: expected ErrCommandBlocked, got %v", cmd, err)
		}
	}
}

func TestArgvMode_EmptyAllowlistDeniesAll(t *testing.T) {
	t.Parallel()
	_, _, err := runCommand(context.Background(), ".", `go test ./...`, ExecArgv, nil, BlockPolicy{}, time.Second*5)
	if !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("expected ErrCommandNotAllowed for empty allowlist, got %v", err)
	}
}

func TestArgvMode_AllowlistRejectsUnlisted(t *testing.T) {
	t.Parallel()

	_, _, err := runCommand(context.Background(), ".", `printf "ok"`, ExecArgv, []string{"go", "make"}, BlockPolicy{}, time.Second*5)
	if !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("expected ErrCommandNotAllowed, got %v", err)
	}
}

func TestArgvMode_BlocksInterpreterEval(t *testing.T) {
	t.Parallel()
	_, _, err := runCommand(context.Background(), ".", `python -c "print(1)"`, ExecArgv, []string{"python"}, BlockPolicy{}, time.Second*5)
	if !errors.Is(err, ErrCommandBlocked) {
		t.Fatalf("expected ErrCommandBlocked for python -c, got %v", err)
	}
}

func TestSplitArgv_BasicQuoting(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want []string
	}{
		{`go test ./...`, []string{"go", "test", "./..."}},
		{`go test -run "Test A" ./...`, []string{"go", "test", "-run", "Test A", "./..."}},
		{`echo 'a "b" c'`, []string{"echo", `a "b" c`}},
		{`echo a\ b`, []string{"echo", "a b"}},
	}
	for _, tc := range cases {
		got, err := splitArgv(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%q: len=%d want=%d (%v)", tc.in, len(got), len(tc.want), got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%q[%d]=%q want=%q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestSplitArgv_UnclosedQuote(t *testing.T) {
	t.Parallel()
	if _, err := splitArgv(`go test "missing`); err == nil {
		t.Fatalf("expected error for unclosed quote")
	}
}

func TestRun_AbstainsWithoutCommands(t *testing.T) {
	t.Parallel()
	r, err := Run(context.Background(), Request{RepoRoot: "."})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Abstain {
		t.Fatalf("expected abstain, got %+v", r)
	}
	if r.Accepted {
		t.Fatalf("expected accepted=false")
	}
}

func TestResultJSON_EmptyReasonsAreArray(t *testing.T) {
	t.Parallel()
	b, err := ResultJSON(&Result{Accepted: true, Confidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	reasons, ok := body["reasons"].([]any)
	if !ok {
		t.Fatalf("reasons should be JSON array, got %T in %s", body["reasons"], string(b))
	}
	if len(reasons) != 0 {
		t.Fatalf("reasons len = %d, want 0", len(reasons))
	}
}
