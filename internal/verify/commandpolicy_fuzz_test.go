package verify

import (
	"strings"
	"testing"
)

func TestShellLineBlocked(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		blocked bool
	}{
		{"clean go test", "go test ./...", false},
		{"empty", "", true},
		{"rm rf root", "rm -rf /", true},
		{"command substitution", "echo $(whoami)", true},
		{"backtick", "echo `id`", true},
		{"npm install", "npm install lodash", true},
		{"python -c", "python -c 'print(1)'", true},
		{"env file", "cat .env", true},
		{"clean make", "make build", false},
	}
	for _, c := range cases {
		got, reason := ShellLineBlocked(c.line, BlockPolicy{})
		if got != c.blocked {
			t.Errorf("%s: ShellLineBlocked(%q)=%v want %v (reason=%q)", c.name, c.line, got, c.blocked, reason)
		}
	}
}

func FuzzShellLineBlocked(f *testing.F) {
	seeds := []string{
		"",
		"go test ./...",
		"rm -rf /",
		"echo $(whoami)",
		"echo `id`",
		"npm install x",
		"python -c 'x'",
		"cat .env",
		"make build",
		"go test ./... && rm -rf /",
		"  rm   -rf   /tmp  ",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmdline string) {
		blocked, reason := ShellLineBlocked(cmdline, BlockPolicy{})
		if strings.TrimSpace(cmdline) == "" {
			if !blocked {
				t.Fatalf("empty/whitespace must be blocked (reason=%q)", reason)
			}
			return
		}
		_ = blocked
		_ = reason
	})
}
