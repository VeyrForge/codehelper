package verify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunShell_PreservesQuotedArgs(t *testing.T) {
	t.Parallel()

	out, _, err := runCommand(context.Background(), ".", `printf "%s" "hello world"`, ExecShell, nil, BlockPolicy{}, time.Second*5)
	if err != nil {
		t.Fatalf("shell mode returned error: %v", err)
	}
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestArgvMode_NoShellMetacharExpansion(t *testing.T) {
	t.Parallel()

	// A metachar inside a single quoted argument is one literal token: argv mode
	// must not expand it. (Uses a benign payload so the safety policy, which
	// rejects standalone operators and destructive fragments, doesn't fire.)
	out, stat, err := runCommand(context.Background(), ".", `printf "%s" "a; echo b; ls"`, ExecArgv, nil, BlockPolicy{}, time.Second*5)
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

func TestArgvMode_AllowlistRejectsUnlisted(t *testing.T) {
	t.Parallel()

	_, _, err := runCommand(context.Background(), ".", `printf "ok"`, ExecArgv, []string{"go", "make"}, BlockPolicy{}, time.Second*5)
	if !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("expected ErrCommandNotAllowed, got %v", err)
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
