package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/verify"
)

// AliasRunResult is output from run_alias.
type AliasRunResult struct {
	Alias    string `json:"alias"`
	Kind     string `json:"kind"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// RunAlias executes a configured alias locally (argv) or via remote_exec.
func RunAlias(ctx context.Context, repoRoot, aliasName string, params map[string]string, approved bool) (*AliasRunResult, error) {
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	a := cfg.FindAlias(aliasName)
	if a == nil {
		return nil, fmt.Errorf("alias %q not configured", aliasName)
	}
	if a.RequiresApproval && !approved {
		return nil, fmt.Errorf("alias %q requires user approval — pass approved=true after confirming", aliasName)
	}
	if a.RemoteHost != "" && a.RemoteRecipe != "" {
		res, err := ExecRecipe(ctx, repoRoot, a.RemoteHost, a.RemoteRecipe, params, 60*time.Second)
		if res == nil {
			return nil, err
		}
		ar := &AliasRunResult{Alias: a.Name, Kind: "remote", Output: res.Output, ExitCode: res.ExitCode}
		return ar, err
	}
	if len(a.Argv) == 0 {
		return nil, fmt.Errorf("alias %q has no argv", aliasName)
	}
	allow := connections.ResolveVerifyAllowlist(repoRoot, nil)
	block := connections.VerifyBlockPolicy(repoRoot)
	cwd := repoRoot
	if strings.TrimSpace(a.Cwd) != "" {
		cwd = a.Cwd
	}
	cmdline := strings.Join(a.Argv, " ")
	outcomes := verify.RunCommandLines(ctx, []string{cmdline}, verify.RunCommandsOptions{
		RepoRoot:        cwd,
		ExecMode:        verify.ExecArgv,
		AllowedCommands: allow,
		BlockPolicy:     block,
		TimeoutSeconds:  300,
	})
	if len(outcomes) == 0 {
		return nil, fmt.Errorf("alias produced no output")
	}
	o := outcomes[0]
	ar := &AliasRunResult{Alias: a.Name, Kind: "local", Output: o.Output, ExitCode: o.ExitCode}
	if o.Error != "" {
		return ar, fmt.Errorf("%s", o.Error)
	}
	return ar, nil
}
