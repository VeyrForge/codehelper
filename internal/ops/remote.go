package ops

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/verify"
)

// RemoteExecResult is bounded SSH recipe output.
type RemoteExecResult struct {
	Host     string   `json:"host"`
	Recipe   string   `json:"recipe"`
	Argv     []string `json:"argv"`
	ExitCode int      `json:"exit_code"`
	Output   string   `json:"output"`
	TimedOut bool     `json:"timed_out,omitempty"`
}

// ExecRecipe runs a named recipe on a configured SSH host via argv-mode ssh.
func ExecRecipe(ctx context.Context, repoRoot, hostName, recipeName string, params map[string]string, timeout time.Duration) (*RemoteExecResult, error) {
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	h, r := cfg.FindRecipe(hostName, recipeName)
	if h == nil {
		return nil, fmt.Errorf("ssh host %q not configured", hostName)
	}
	if r == nil {
		return nil, fmt.Errorf("recipe %q not found on host %q", recipeName, hostName)
	}
	if !r.ReadOnly {
		return nil, fmt.Errorf("recipe %q is not marked read-only — only read-only recipes may run via MCP", recipeName)
	}
	if h.Disabled {
		return nil, fmt.Errorf("ssh host %q is disabled", hostName)
	}
	argv, err := connections.ExpandRecipe(*r, params)
	if err != nil {
		return nil, err
	}
	if err := allowRemoteArgv(*h, argv); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sshArgv := buildSSHArgv(cfg, *h, argv)
	cmd := exec.CommandContext(cctx, sshArgv[0], sshArgv[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	out := buf.String()
	if len(out) > maxLogBytes {
		out = out[len(out)-maxLogBytes:]
	}
	res := &RemoteExecResult{Host: h.Name, Recipe: r.Name, Argv: argv, Output: out}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if cctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if runErr != nil && res.ExitCode == 0 {
		res.ExitCode = 1
	}
	return res, runErr
}

func allowRemoteArgv(h connections.SSHHost, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty remote argv")
	}
	base := strings.ToLower(strings.TrimSpace(argv[0]))
	if len(h.AllowedCommands) == 0 {
		return fmt.Errorf("host %q has no allowed_commands — add basenames via CLI first", h.Name)
	}
	allowed := false
	for _, a := range h.AllowedCommands {
		if strings.EqualFold(strings.TrimSpace(a), base) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("remote command %q not in host allowlist", base)
	}
	if blocked, reason := verify.SSHAllowlistBlocked(base); blocked {
		return fmt.Errorf("%s", reason)
	}
	if blocked, reason := verify.CommandBlocked(argv); blocked {
		return fmt.Errorf("%s", reason)
	}
	return nil
}

func buildSSHArgv(cfg connections.Config, h connections.SSHHost, remoteArgv []string) []string {
	target := strings.TrimSpace(h.Hostname)
	if u := strings.TrimSpace(h.User); u != "" {
		target = u + "@" + target
	}
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=accept-new"}
	if h.Port > 0 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	if id := strings.TrimSpace(h.IdentityFile); id != "" {
		args = append(args, "-i", id)
	}
	if jh := strings.TrimSpace(h.JumpHost); jh != "" {
		if jump := cfg.FindSSH(jh); jump != nil {
			jumpTarget := strings.TrimSpace(jump.Hostname)
			if u := strings.TrimSpace(jump.User); u != "" {
				jumpTarget = u + "@" + jumpTarget
			}
			if jump.Port > 0 {
				jumpTarget = jumpTarget + ":" + strconv.Itoa(jump.Port)
			}
			args = append(args, "-J", jumpTarget)
		}
	}
	args = append(args, target, "--")
	args = append(args, remoteArgv...)
	return args
}

// DefaultTailRecipe returns a safe tail recipe for log paths.
func DefaultTailRecipe(name string, linesParam string) connections.Recipe {
	if linesParam == "" {
		linesParam = "lines"
	}
	return connections.Recipe{
		Name: name, ReadOnly: true,
		Argv:   []string{"tail", "-n", "{" + linesParam + "}", "{path}"},
		Params: []string{linesParam, "path"},
	}
}
