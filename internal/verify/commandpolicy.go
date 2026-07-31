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
	// AllowShell permits exec_mode=shell. Default false — only the user CLI may
	// set verify_allow_shell on the project policy; agents cannot enable it.
	AllowShell bool
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
			return true, "shell operator " + strings.TrimSpace(tok) +
				" is not supported in argv mode (no shell). Hint: pass a single argv command (e.g. \"go test ./...\"); for pipes/&& set exec_mode=shell (opt-in), or split into separate lint_cmd/build_cmd/test_cmd calls"
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
	if blocked, reason := interpreterEvalBlocked(argv); blocked {
		return true, reason
	}
	return false, ""
}

// interpreterEvalBlocked rejects inline code execution flags (python -c, node -e,
// php -r, …) that turn an allowlisted interpreter into arbitrary shell-equivalent.
func interpreterEvalBlocked(argv []string) (bool, string) {
	if len(argv) < 2 {
		return false, ""
	}
	bin := commandBasename(argv[0])
	bin = strings.ToLower(strings.TrimSuffix(bin, ".exe"))
	flag := strings.TrimSpace(argv[1])
	switch bin {
	case "python", "python3", "python2":
		if flag == "-c" {
			return true, "interpreter eval flag blocked: python -c"
		}
	case "node", "nodejs":
		if flag == "-e" || flag == "--eval" || flag == "-p" || flag == "--print" {
			return true, "interpreter eval flag blocked: node " + flag
		}
	case "php":
		if flag == "-r" {
			return true, "interpreter eval flag blocked: php -r"
		}
	case "ruby":
		if flag == "-e" {
			return true, "interpreter eval flag blocked: ruby -e"
		}
	case "perl":
		if flag == "-e" || flag == "-E" {
			return true, "interpreter eval flag blocked: perl " + flag
		}
	}
	return false, ""
}

// ShellLineBlocked applies destructive-pattern and injection checks to a raw
// shell command line (exec_mode=shell). Even when the user opts into shell,
// catastrophic patterns remain blocked. Compound/pipeline operators (; && || |)
// are NOT rejected here — runCommand allowlists EVERY segment executable instead
// (first-token-only was a bypass). Redirections and newlines are rejected.
func ShellLineBlocked(cmdline string, pol BlockPolicy) (bool, string) {
	_ = pol
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return true, "empty command"
	}
	if blocked, reason := shellLineStructuralBlocked(cmdline); blocked {
		return true, reason
	}
	lower := strings.ToLower(cmdline)
	collapsed := strings.Join(strings.Fields(lower), " ")
	for _, frag := range destructiveFragments {
		if strings.Contains(collapsed, frag) {
			return true, "blocked destructive command pattern: " + frag
		}
	}
	if strings.Contains(lower, "$(") || strings.Contains(cmdline, "`") {
		return true, "command substitution is not allowed in shell mode"
	}
	if strings.Contains(collapsed, ".env") {
		return true, "environment file edits blocked"
	}
	for _, frag := range []string{"npm install", "npm update", "yarn add", "pnpm add", "go get", "pip install"} {
		if strings.Contains(collapsed, frag) {
			return true, "package install/update blocked by default"
		}
	}
	// Interpreter one-liners remain blocked in shell mode too.
	for _, frag := range []string{"python -c", "python3 -c", "node -e", "node --eval", "php -r", "ruby -e", "perl -e"} {
		if strings.Contains(collapsed, frag) {
			return true, "interpreter eval flag blocked in shell mode: " + frag
		}
	}
	return false, ""
}

// shellLineStructuralBlocked rejects redirects and newlines outside quotes.
// Compound/pipeline operators are left for per-segment allowlisting.
func shellLineStructuralBlocked(cmdline string) (bool, string) {
	state := stateUnquoted
	runes := []rune(cmdline)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch state {
		case stateUnquoted:
			switch {
			case r == '\'':
				state = stateSingle
			case r == '"':
				state = stateDouble
			case r == '\\':
				if i+1 < len(runes) {
					i++
				}
			case r == '\n' || r == '\r':
				return true, "newlines are not allowed in shell mode"
			case r == '>' || r == '<':
				return true, "redirection is not allowed in shell mode (omit > < << >>; verify already captures stdout/stderr)"
			}
		case stateSingle:
			if r == '\'' {
				state = stateUnquoted
			}
		case stateDouble:
			if r == '\\' && i+1 < len(runes) {
				i++
				continue
			}
			if r == '"' {
				state = stateUnquoted
			}
		}
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
