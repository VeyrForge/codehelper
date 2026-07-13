//go:build windows

package verify

import (
	"context"
	"os/exec"
)

func newShellCmd(ctx context.Context, cmdline string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/C", cmdline)
}
