package verify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CommandLineOutcome is the result of one argv-mode command execution.
type CommandLineOutcome struct {
	Cmdline  string `json:"cmdline"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Error    string `json:"error,omitempty"`
}

// RunCommandsOptions configures sequential command execution.
type RunCommandsOptions struct {
	RepoRoot        string
	ExecMode        ExecMode
	AllowedCommands []string
	BlockPolicy     BlockPolicy
	TimeoutSeconds  int
}

// RunCommandLines executes each non-empty command line using the shared verify
// runner (argv mode by default, allowlist, timeouts).
func RunCommandLines(ctx context.Context, cmdlines []string, opts RunCommandsOptions) []CommandLineOutcome {
	mode := opts.ExecMode
	if mode == "" {
		mode = ExecArgv
	}
	timeout := time.Duration(opts.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	var out []CommandLineOutcome
	for _, cmdline := range cmdlines {
		line := strings.TrimSpace(cmdline)
		if line == "" {
			continue
		}
		combined, stat, err := runCommand(ctx, opts.RepoRoot, line, mode, opts.AllowedCommands, opts.BlockPolicy, timeout)
		o := CommandLineOutcome{
			Cmdline:  line,
			ExitCode: stat.ExitCode,
			Output:   combined,
			TimedOut: stat.TimedOut,
		}
		if err != nil {
			o.Error = err.Error()
			if o.ExitCode == 0 {
				o.ExitCode = 1
			}
		}
		out = append(out, o)
	}
	return out
}

// HasFailures reports whether any command exited non-zero or timed out.
func HasFailures(outcomes []CommandLineOutcome) bool {
	for _, o := range outcomes {
		if o.TimedOut || o.ExitCode != 0 {
			return true
		}
	}
	return false
}

// FailuresText formats failed command outcomes for diagnostics display.
func FailuresText(outcomes []CommandLineOutcome) string {
	var parts []string
	for _, o := range outcomes {
		if !o.TimedOut && o.ExitCode == 0 {
			continue
		}
		tail := strings.TrimSpace(o.Output)
		if len(tail) > 4000 {
			tail = tail[:4000] + "\n…(truncated)"
		}
		parts = append(parts, fmt.Sprintf("$ %s → exit %d\n%s", o.Cmdline, o.ExitCode, tail))
	}
	return strings.Join(parts, "\n\n")
}
