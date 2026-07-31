//go:build !windows

package green

import "syscall"

// detachAttr puts the managed server in its own session so it outlives codehelper
// and isn't tied to codehelper's controlling terminal.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminatePID signals the whole process group created by Setsid (negative pid),
// then the leader, so child workers die too. Already-dead is not an error.
func terminatePID(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
