package verify

import "testing"

func TestCommandBlocked(t *testing.T) {
	blocked, _ := CommandBlocked([]string{"git", "status"})
	if !blocked {
		t.Fatal("expected git blocked")
	}
	ok, _ := CommandBlocked([]string{"go", "test", "./..."})
	if ok {
		t.Fatal("expected go test allowed")
	}
}

func TestCommandBlockedShellInjection(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		blocked bool
	}{
		{"pipe operator", []string{"go", "build", "|", "tee", "log"}, true},
		{"semicolon chain", []string{"go", "test", ";", "echo", "done"}, true},
		{"and chain", []string{"make", "&&", "make", "install"}, true},
		{"redirect", []string{"echo", "hi", ">", "/etc/passwd"}, true},
		{"command substitution", []string{"echo", "$(whoami)"}, true},
		{"backtick", []string{"echo", "`id`"}, true},
		{"newline injection", []string{"echo", "a\nrm -rf /"}, true},
		{"destructive rm", []string{"sh", "rm", "-rf", "/"}, true},
		{"fork bomb literal", []string{":(){:|:&};:"}, true},
		{"clean go test", []string{"go", "test", "./..."}, false},
		{"clean make", []string{"make", "build"}, false},
	}
	for _, c := range cases {
		got, reason := CommandBlocked(c.argv)
		if got != c.blocked {
			t.Errorf("%s: CommandBlocked=%v want %v (reason=%q)", c.name, got, c.blocked, reason)
		}
	}
}

func TestCommandBlockedWithPolicy_AllowGit(t *testing.T) {
	blocked, _ := CommandBlocked([]string{"git", "status"})
	if !blocked {
		t.Fatal("git blocked by default")
	}
	ok, _ := CommandBlockedWithPolicy([]string{"git", "status"}, BlockPolicy{AllowGit: true})
	if ok {
		t.Fatal("git should be allowed when AllowGit is set")
	}
}

func TestShellLineBlocked_RedirectsAndNewlines(t *testing.T) {
	for _, cmd := range []string{
		"echo hi > /tmp/x",
		"echo hi >> /tmp/x",
		"cat < /etc/passwd",
		"go test 2>&1",
		"echo a\necho b",
	} {
		blocked, reason := ShellLineBlocked(cmd, BlockPolicy{AllowShell: true})
		if !blocked {
			t.Errorf("%q: expected blocked, reason=%q", cmd, reason)
		}
	}
	// Compound operators are allowlist-checked elsewhere, not blocked here.
	blocked, _ := ShellLineBlocked(`go test; go vet`, BlockPolicy{AllowShell: true})
	if blocked {
		t.Fatal("compound without redirect should not be ShellLineBlocked")
	}
}

func TestSSHAllowlistBlocked(t *testing.T) {
	for _, name := range []string{"bash", "cat", "ssh", "mysql", "journalctl"} {
		blocked, _ := SSHAllowlistBlocked(name)
		want := name != "journalctl"
		if blocked != want {
			t.Errorf("SSHAllowlistBlocked(%q)=%v want %v", name, blocked, want)
		}
	}
}

func TestValidateSSHAllowlist(t *testing.T) {
	if err := ValidateSSHAllowlist([]string{"journalctl", "tail"}); err != nil {
		t.Fatalf("expected ok: %v", err)
	}
	if err := ValidateSSHAllowlist([]string{"cat"}); err == nil {
		t.Fatal("cat must be rejected")
	}
}
