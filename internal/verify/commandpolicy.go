package verify

import (
	"fmt"
	"strings"
)

var blockedCommandPrefixes = []string{
	"git", "sudo", "rm", "chmod", "chown", "curl", "wget", "ssh", "scp",
}

// BlockPolicy optional per-project overrides for CommandBlocked (goal.md §19:
// blocked commands can be allowed only by explicit user/project policy).
type BlockPolicy struct {
	AllowGit bool
}

// sshExtraBlocked are interpreters, shells, and secret/DB clients that must
// never appear on an SSH host allowlist even when local verify might allow them.
var sshExtraBlocked = []string{
	"bash", "sh", "zsh", "dash", "fish", "ksh", "csh", "tcsh",
	"python", "python3", "perl", "ruby", "node", "php",
	"mysql", "psql", "mongo", "mongosh", "redis-cli", "sqlite3",
	"cat", "tee", "sed", "awk", "vi", "vim", "nano", "emacs", "less", "more",
	"passwd", "useradd", "userdel", "usermod", "chpasswd",
	"apt", "yum", "dnf", "apk", "brew", "snap",
	"nc", "netcat", "nmap", "telnet",
}

// shellOperatorTokens are argv tokens that only make sense to a shell. Because
// codehelper executes in argv mode (no shell), their presence means the model
// intended a pipeline/redirect/chain that will NOT run as expected — so we block
// rather than silently execute a command the model didn't mean. (OWASP OS
// Command Injection: keep command and data separate; never let data become an
// operator.)
var shellOperatorTokens = map[string]bool{
	";": true, "|": true, "||": true, "&": true, "&&": true,
	">": true, ">>": true, "<": true, "<<": true, "|&": true,
}

// destructiveFragments are catastrophic command patterns blocked unconditionally
// as defense-in-depth, even if the binary allow/deny list is later loosened.
var destructiveFragments = []string{
	"rm -rf /", "rm -fr /", "rm -rf ~", "rm -rf /*",
	"mkfs", "dd if=", "> /dev/sd", "of=/dev/sd",
	":(){:|:&};:", "chmod -r 777 /", "chown -r",
}

// CommandBlocked reports whether argv is blocked by default (goal.md §19). It
// gates on three independent signals: a binary deny-list, shell-operator/
// injection tokens (argv mode never runs a shell), and unconditional
// destructive-command patterns.
func CommandBlocked(argv []string) (bool, string) {
	return CommandBlockedWithPolicy(argv, BlockPolicy{})
}

// CommandBlockedWithPolicy is CommandBlocked with optional per-project overrides.
func CommandBlockedWithPolicy(argv []string, pol BlockPolicy) (bool, string) {
	if len(argv) == 0 {
		return true, "empty command"
	}

	// Shell-operator / command-substitution detection across every token.
	for _, tok := range argv {
		if shellOperatorTokens[strings.TrimSpace(tok)] {
			return true, "shell operator " + strings.TrimSpace(tok) + " is not supported in argv mode (no shell); run commands separately"
		}
		if strings.ContainsAny(tok, "`\n") || strings.Contains(tok, "$(") {
			return true, "command substitution / control characters are not allowed"
		}
	}

	bin := strings.ToLower(strings.TrimSpace(argv[0]))
	for _, p := range blockedCommandPrefixes {
		if pol.AllowGit && p == "git" {
			continue
		}
		if bin == p || strings.HasSuffix(bin, "/"+p) {
			return true, "blocked command: " + p
		}
	}

	joined := strings.ToLower(strings.Join(argv, " "))
	// Normalize whitespace so "rm  -rf   /" still matches destructive fragments.
	collapsed := strings.Join(strings.Fields(joined), " ")
	for _, frag := range destructiveFragments {
		if strings.Contains(collapsed, frag) {
			return true, "blocked destructive command pattern: " + frag
		}
	}
	for _, frag := range []string{"npm install", "npm update", "yarn add", "pnpm add", "go get", "pip install"} {
		if strings.Contains(joined, frag) {
			return true, "package install/update blocked by default"
		}
	}
	if strings.Contains(joined, ".env") {
		return true, "environment file edits blocked"
	}
	return false, ""
}

// SSHAllowlistBlocked reports whether a command basename may never be on an SSH
// host allowlist. Uses the global deny-list plus sshExtraBlocked (interpreters,
// secret readers, package managers). Intended for user-configured allowlists only.
func SSHAllowlistBlocked(name string) (bool, string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true, "empty command name"
	}
	if blocked, reason := CommandBlocked([]string{name}); blocked {
		return true, reason
	}
	for _, p := range sshExtraBlocked {
		if name == p {
			return true, "never allowed on SSH allowlist: " + p
		}
	}
	return false, ""
}

// ValidateSSHAllowlist rejects any basename that SSHAllowlistBlocked flags.
func ValidateSSHAllowlist(commands []string) error {
	for _, c := range commands {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if blocked, reason := SSHAllowlistBlocked(c); blocked {
			return fmt.Errorf("allowed_commands: %s", reason)
		}
	}
	return nil
}
