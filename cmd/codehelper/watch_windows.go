//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008

func detachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess,
		HideWindow:    true,
	}
}

// lowerPriority is a no-op on Windows (no POSIX nice); the daemon already runs
// detached. Kept for cross-platform parity with watch_unix.go.
func lowerPriority() {}
