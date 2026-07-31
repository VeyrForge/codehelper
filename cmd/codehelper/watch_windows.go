//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const detachedProcess = 0x00000008

func detachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess,
		HideWindow:    true,
	}
}

// lowerPriority drops the watch daemon below normal so interactive work
// (editor, games, UI) wins the CPU on Windows — matching the Unix nice(5)
// behavior in watch_unix.go.
func lowerPriority() {
	handle, err := windows.GetCurrentProcess()
	if err != nil {
		return
	}
	_ = windows.SetPriorityClass(handle, windows.BELOW_NORMAL_PRIORITY_CLASS)
}
