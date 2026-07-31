//go:build windows

package green

import (
	"os"
	"syscall"
)

// detachAttr starts the managed server in a new process group so it outlives
// codehelper and doesn't receive codehelper's console Ctrl-C/Ctrl-Break. Windows
// has no Setsid; CREATE_NEW_PROCESS_GROUP is the closest detach primitive.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminatePID kills the managed server. Windows has no signals/process-group
// kill via syscall.Kill, so terminate the process directly; already-dead is fine.
func terminatePID(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
