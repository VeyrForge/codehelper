//go:build windows

package main

import "os"

// signalWatchDaemon terminates a watch daemon. Windows does not support
// SIGTERM via os.Process.Signal; Kill matches internal/green's approach.
func signalWatchDaemon(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
