//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachAttrs creates a new session so the daemon survives parent shell exit.
func detachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// lowerPriority nices the current process so background reindexing yields CPU to
// interactive work (the editor, other tools) — it keeps converging without ever
// making the machine lag. Best-effort; ignored if the OS refuses.
func lowerPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10)
}
