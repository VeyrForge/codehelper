//go:build !windows

package main

import (
	"os"
	"syscall"
)

func signalWatchDaemon(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
